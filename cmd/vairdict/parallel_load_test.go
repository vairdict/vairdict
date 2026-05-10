//go:build integration

package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	codephase "github.com/vairdict/vairdict/internal/phases/code"
	planphase "github.com/vairdict/vairdict/internal/phases/plan"
	qualityphase "github.com/vairdict/vairdict/internal/phases/quality"
	"github.com/vairdict/vairdict/internal/state"
	"github.com/vairdict/vairdict/internal/workspace"
)

// Issue #82 — definition-of-done gate for M5 (Parallelism).
//
// TestLoadFiveConcurrentTasks spawns five tasks concurrently against a
// throwaway git repo with mocked phase runners + GitHub and a real
// SQLite store + real isolated git worktrees. It verifies the four
// invariants the parallel-runner work has to hold under load:
//
//	1. all tasks complete (no cross-task interference)
//	2. no goroutine leaks
//	3. no worktree leaks (every worktree is removed on task completion)
//	4. SQLite state is consistent — every task lands in StateDone with
//	   the expected attempt count
//	5. wall time scales sub-linearly (parallel run is materially faster
//	   than running the same tasks serially)
//
// Build-tagged `integration` so it stays out of the default `go test
// ./...`. Run with `make test-integration` or `go test -tags=integration
// ./cmd/vairdict/...`.

const (
	loadConcurrent      = 5
	loadPerPhaseLatency = 50 * time.Millisecond
	// Each task does 3 phases × 50ms ≈ 150ms of synthetic work.
	// Serial baseline of one task is ~150ms; running 5 in parallel
	// should also land near 150ms. Bound the assertion at 4× the
	// serial baseline so we still catch a regression to outright
	// linear scaling (which would land near 5×) without flaking on
	// CI variance.
	loadScalingBound = 4.0
)

func TestLoadFiveConcurrentTasks(t *testing.T) {
	// goleak's defer fires LAST (LIFO), after the store's Close defer
	// below, so the database/sql connectionOpener goroutine has time to
	// exit before the leak check runs.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	repoRoot := setupTestGitRepo(t)
	store := newIntegrationStore(t)
	defer func() {
		_ = store.Close()
		// Brief settle so database/sql's connection opener can drop out
		// of its select before goleak inspects live goroutines.
		time.Sleep(50 * time.Millisecond)
	}()

	// --- 1. Serial baseline. One task end-to-end gives us the per-
	//        task wall time we'll compare the concurrent run against.
	baselineStart := time.Now()
	if err := runMockedTask(t, repoRoot, store, "baseline"); err != nil {
		t.Fatalf("baseline serial run: %v", err)
	}
	serialDuration := time.Since(baselineStart)

	// --- 2. Concurrent run. Five tasks fired into the orchestration
	//        loop simultaneously. Each gets its own task ID, its own
	//        workspace, its own mock bundle. Same store.
	concurrentStart := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, loadConcurrent)
	for i := 0; i < loadConcurrent; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = runMockedTask(t, repoRoot, store, fmt.Sprintf("concurrent-%d", idx))
		}(i)
	}
	wg.Wait()
	parallelDuration := time.Since(concurrentStart)

	// --- per-task success ---
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent task %d: %v", i, err)
		}
	}

	// --- SQLite consistency ---
	allTasks, err := store.ListTasks("")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	wantTasks := loadConcurrent + 1 // +1 for the serial baseline
	if len(allTasks) != wantTasks {
		t.Errorf("store: expected %d tasks, got %d", wantTasks, len(allTasks))
	}
	for _, task := range allTasks {
		if task.State != state.StateDone {
			t.Errorf("task %s: expected StateDone, got %s", task.ID, task.State)
		}
		// Plan + Code + Quality = 3 attempts on a clean happy path.
		if got := len(task.Attempts); got != 3 {
			t.Errorf("task %s: expected 3 attempts, got %d", task.ID, got)
		}
	}

	// --- worktree leak ---
	if extras := countExtraWorktrees(t, repoRoot); extras != 0 {
		t.Errorf("worktree leak: %d worktree(s) left after run", extras)
	}

	// --- sub-linear scaling ---
	bound := time.Duration(float64(serialDuration) * loadScalingBound)
	if parallelDuration > bound {
		t.Errorf(
			"parallel scaling regressed: serial=%v, parallel(%d)=%v, bound=%v (%.1fx serial)",
			serialDuration, loadConcurrent, parallelDuration, bound, loadScalingBound,
		)
	}
	t.Logf(
		"scaling: serial=%v parallel(%d)=%v ratio=%.2fx",
		serialDuration, loadConcurrent, parallelDuration,
		float64(parallelDuration)/float64(serialDuration),
	)
}

// runMockedTask runs one task end-to-end through runOrchestration with
// mocked phase runners + GitHub, a real workspace (real git worktree),
// and the shared SQLite store.
func runMockedTask(t *testing.T, repoRoot string, store *state.Store, intent string) error {
	t.Helper()
	ctx := context.Background()

	taskID := uniqueTaskID(intent)
	task := state.NewTask(taskID, intent)
	if err := store.CreateTask(task); err != nil {
		return fmt.Errorf("create task: %w", err)
	}

	wsMgr := workspace.New(repoRoot, "", &workspace.ExecRunner{})
	ws, err := wsMgr.Create(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}
	defer func() { _ = ws.Cleanup(ctx) }()

	bundle := newOrchBundle()
	deps := bundle.deps()
	deps.workDir = ws.Path
	deps.persistTask = func(t *state.Task) error {
		t.UpdatedAt = time.Now()
		return store.UpdateTask(t)
	}
	deps.plan = &slowPlanRunner{inner: bundle.plan, sleep: loadPerPhaseLatency}
	deps.code = &slowCodeRunner{inner: bundle.code, sleep: loadPerPhaseLatency}
	deps.quality = &slowQualityRunner{inner: bundle.quality, sleep: loadPerPhaseLatency}

	if orchErr := runOrchestration(ctx, deps, task, &fakeRenderer{}); orchErr != nil {
		return fmt.Errorf("orchestration: %w", orchErr)
	}

	// Persist the final state so the SQLite consistency check sees
	// StateDone. runOrchestration only persists once after plan, and
	// the fakes mutate state in memory; without this the store would
	// still hold the post-plan StateCoding row.
	if err := store.UpdateTask(task); err != nil {
		return fmt.Errorf("persist final state: %w", err)
	}
	return nil
}

// uniqueTaskID returns a unique task ID derived from intent + an
// atomic counter. state.Store keys on Task.ID, so duplicates from
// concurrent goroutines would silently lose data.
var loadTaskCounter int64

func uniqueTaskID(intent string) string {
	n := atomic.AddInt64(&loadTaskCounter, 1)
	return fmt.Sprintf("%s-%d", intent, n)
}

// setupTestGitRepo initialises a git repo in t.TempDir() with one
// empty commit on `main` — the minimum needed for `git worktree add`.
func setupTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	runGit := func(args ...string) {
		full := append([]string{"-C", dir}, args...)
		out, err := exec.Command("git", full...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// `git init -b main` is git ≥ 2.28; symbolic-ref works back to ≥ 2.5.
	runGit("symbolic-ref", "HEAD", "refs/heads/main")
	runGit("config", "user.name", "Load Test")
	runGit("config", "user.email", "loadtest@example.invalid")
	runGit("commit", "--allow-empty", "-m", "initial commit")
	return dir
}

// newIntegrationStore opens a fresh SQLite store under t.TempDir().
func newIntegrationStore(t *testing.T) *state.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "integration.db")
	store, err := state.NewStore(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// countExtraWorktrees returns the number of worktrees beyond the main
// repository working tree. `git worktree list --porcelain` emits one
// "worktree <path>" line per registered worktree; the first is always
// the main repo.
func countExtraWorktrees(t *testing.T, repoRoot string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", repoRoot, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			count++
		}
	}
	if count <= 1 {
		return 0
	}
	return count - 1
}

// --- Slow wrappers around the existing orchestration fakes ---
//
// Wrap the production-test fakes from run_test.go with a context-aware
// sleep so the load test has a deterministic synthetic work duration
// per phase. Per-bundle (per-goroutine), so no shared mutable state
// across the concurrent runs.

type slowPlanRunner struct {
	inner *fakePlanRunner
	sleep time.Duration
}

func (s *slowPlanRunner) Run(ctx context.Context, task *state.Task) (*planphase.PhaseResult, error) {
	if err := sleepCtx(ctx, s.sleep); err != nil {
		return nil, err
	}
	return s.inner.Run(ctx, task)
}

type slowCodeRunner struct {
	inner *fakeCodeRunner
	sleep time.Duration
}

func (s *slowCodeRunner) Run(ctx context.Context, task *state.Task, plan string) (*codephase.PhaseResult, error) {
	if err := sleepCtx(ctx, s.sleep); err != nil {
		return nil, err
	}
	return s.inner.Run(ctx, task, plan)
}

type slowQualityRunner struct {
	inner *fakeQualityRunner
	sleep time.Duration
}

func (s *slowQualityRunner) Run(ctx context.Context, task *state.Task, plan, codeFacts string) (*qualityphase.PhaseResult, error) {
	if err := sleepCtx(ctx, s.sleep); err != nil {
		return nil, err
	}
	return s.inner.Run(ctx, task, plan, codeFacts)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

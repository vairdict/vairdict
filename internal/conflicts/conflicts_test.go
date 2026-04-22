package conflicts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- Unit tests with a fake runner ---

// fakeRunner records commands and returns scripted outputs.
type fakeRunner struct {
	calls   []fakeCall
	results map[string]fakeResult
}

type fakeCall struct {
	Dir  string
	Name string
	Args []string
}

type fakeResult struct {
	out []byte
	err error
}

func (f *fakeRunner) Run(_ context.Context, dir, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, fakeCall{Dir: dir, Name: name, Args: args})
	key := name + " " + strings.Join(args, " ")
	if r, ok := f.results[key]; ok {
		return r.out, r.err
	}
	return nil, nil
}

func TestDetectAndResolve_NoDivergence(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		results: map[string]fakeResult{
			"git fetch origin main":                  {},
			"git rev-list --count HEAD..origin/main": {out: []byte("0\n")},
		},
	}

	d := New(runner)
	result, err := d.DetectAndResolve(context.Background(), "/work", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Diverged {
		t.Error("expected Diverged=false")
	}
	if result.HasConflicts {
		t.Error("expected HasConflicts=false")
	}
	if result.Rebased {
		t.Error("expected Rebased=false")
	}
}

func TestDetectAndResolve_DivergedRebaseSucceeds(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		results: map[string]fakeResult{
			"git fetch origin main":                  {},
			"git rev-list --count HEAD..origin/main": {out: []byte("3\n")},
			"git rebase origin/main":                 {},
		},
	}

	d := New(runner)
	result, err := d.DetectAndResolve(context.Background(), "/work", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Diverged {
		t.Error("expected Diverged=true")
	}
	if !result.Rebased {
		t.Error("expected Rebased=true")
	}
	if result.HasConflicts {
		t.Error("expected HasConflicts=false after successful rebase")
	}
	if !result.Clean() {
		t.Error("expected Clean()=true")
	}
}

func TestDetectAndResolve_DivergedRebaseFails(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		results: map[string]fakeResult{
			"git fetch origin main":                  {},
			"git rev-list --count HEAD..origin/main": {out: []byte("2\n")},
			"git rebase origin/main":                 {err: fmt.Errorf("rebase failed: conflict")},
			"git diff --name-only --diff-filter=U":   {out: []byte("file1.go\nfile2.go\n")},
			"git rebase --abort":                     {},
		},
	}

	d := New(runner)
	result, err := d.DetectAndResolve(context.Background(), "/work", "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Diverged {
		t.Error("expected Diverged=true")
	}
	if !result.HasConflicts {
		t.Error("expected HasConflicts=true")
	}
	if result.Rebased {
		t.Error("expected Rebased=false")
	}
	if result.Clean() {
		t.Error("expected Clean()=false")
	}
	if len(result.ConflictFiles) != 2 {
		t.Fatalf("expected 2 conflict files, got %d: %v", len(result.ConflictFiles), result.ConflictFiles)
	}
	if result.ConflictFiles[0] != "file1.go" || result.ConflictFiles[1] != "file2.go" {
		t.Errorf("unexpected conflict files: %v", result.ConflictFiles)
	}
}

func TestDetectAndResolve_FetchFails(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		results: map[string]fakeResult{
			"git fetch origin main": {err: fmt.Errorf("network error")},
		},
	}

	d := New(runner)
	_, err := d.DetectAndResolve(context.Background(), "/work", "main")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fetching origin/main") {
		t.Errorf("error should mention fetch: %v", err)
	}
}

func TestDetectAndResolve_RebaseAbortFails(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		results: map[string]fakeResult{
			"git fetch origin main":                  {},
			"git rev-list --count HEAD..origin/main": {out: []byte("1\n")},
			"git rebase origin/main":                 {err: fmt.Errorf("conflict")},
			"git diff --name-only --diff-filter=U":   {out: []byte("a.go\n")},
			"git rebase --abort":                     {err: fmt.Errorf("abort failed")},
		},
	}

	d := New(runner)
	_, err := d.DetectAndResolve(context.Background(), "/work", "main")
	if err == nil {
		t.Fatal("expected error when rebase abort fails")
	}
	if !strings.Contains(err.Error(), "aborting rebase") {
		t.Errorf("error should mention abort: %v", err)
	}
}

func TestDetectAndResolve_CustomBaseBranch(t *testing.T) {
	t.Parallel()
	runner := &fakeRunner{
		results: map[string]fakeResult{
			"git fetch origin develop":                  {},
			"git rev-list --count HEAD..origin/develop": {out: []byte("0\n")},
		},
	}

	d := New(runner)
	result, err := d.DetectAndResolve(context.Background(), "/work", "develop")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Diverged {
		t.Error("expected no divergence")
	}
}

func TestClean_TrueWhenNoConflicts(t *testing.T) {
	t.Parallel()
	r := &Result{Diverged: true, Rebased: true}
	if !r.Clean() {
		t.Error("Clean() should be true when no conflicts")
	}
}

func TestClean_FalseWhenConflicts(t *testing.T) {
	t.Parallel()
	r := &Result{Diverged: true, HasConflicts: true, ConflictFiles: []string{"a.go"}}
	if r.Clean() {
		t.Error("Clean() should be false when conflicts exist")
	}
}

// --- Integration tests with real git ---

// initTestRepo creates a temporary git repo with an initial commit on "main".
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "test")

	// Create initial commit.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-m", "init")

	return dir
}

// execRunner is the real runner for integration tests.
type execRunner struct{}

func (e *execRunner) Run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func TestIntegration_CleanMerge(t *testing.T) {
	t.Parallel()

	// Set up a "remote" repo and a clone.
	remote := initTestRepo(t)
	clone := t.TempDir()

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	// Clone from remote.
	cmd := exec.Command("git", "clone", remote, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	run(clone, "config", "user.email", "test@test.com")
	run(clone, "config", "user.name", "test")

	// Create a task branch with a non-conflicting change.
	run(clone, "checkout", "-b", "vairdict/task-1")
	if err := os.WriteFile(filepath.Join(clone, "new-file.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(clone, "add", "-A")
	run(clone, "commit", "-m", "task change")

	// No divergence because main hasn't moved.
	d := New(&execRunner{})
	result, err := d.DetectAndResolve(context.Background(), clone, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Diverged {
		t.Error("expected no divergence")
	}
	if result.HasConflicts {
		t.Error("expected no conflicts")
	}
}

func TestIntegration_RebaseSuccess(t *testing.T) {
	t.Parallel()

	remote := initTestRepo(t)
	clone := t.TempDir()

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	cmd := exec.Command("git", "clone", remote, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	run(clone, "config", "user.email", "test@test.com")
	run(clone, "config", "user.name", "test")

	// Create task branch with change to new file.
	run(clone, "checkout", "-b", "vairdict/task-1")
	if err := os.WriteFile(filepath.Join(clone, "task.go"), []byte("package task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(clone, "add", "-A")
	run(clone, "commit", "-m", "task work")

	// Push a non-conflicting change to main on the remote.
	if err := os.WriteFile(filepath.Join(remote, "other.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(remote, "add", "-A")
	run(remote, "commit", "-m", "main moved forward")

	// Now detect and resolve — should auto-rebase.
	d := New(&execRunner{})
	result, err := d.DetectAndResolve(context.Background(), clone, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Diverged {
		t.Error("expected Diverged=true")
	}
	if !result.Rebased {
		t.Error("expected Rebased=true")
	}
	if result.HasConflicts {
		t.Error("expected no conflicts after rebase")
	}
}

func TestIntegration_ConflictEscalation(t *testing.T) {
	t.Parallel()

	remote := initTestRepo(t)
	clone := t.TempDir()

	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	cmd := exec.Command("git", "clone", remote, clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	run(clone, "config", "user.email", "test@test.com")
	run(clone, "config", "user.name", "test")

	// Create task branch that modifies README.md.
	run(clone, "checkout", "-b", "vairdict/task-1")
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("# task version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(clone, "add", "-A")
	run(clone, "commit", "-m", "task modifies README")

	// Push a conflicting change to README.md on the remote.
	if err := os.WriteFile(filepath.Join(remote, "README.md"), []byte("# remote version\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(remote, "add", "-A")
	run(remote, "commit", "-m", "remote modifies README")

	// Detect and resolve — should fail rebase and report conflict.
	d := New(&execRunner{})
	result, err := d.DetectAndResolve(context.Background(), clone, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Diverged {
		t.Error("expected Diverged=true")
	}
	if !result.HasConflicts {
		t.Error("expected HasConflicts=true")
	}
	if result.Rebased {
		t.Error("expected Rebased=false")
	}
	if len(result.ConflictFiles) == 0 {
		t.Error("expected at least one conflict file")
	}

	// Verify README.md is in the conflict list.
	found := false
	for _, f := range result.ConflictFiles {
		if f == "README.md" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected README.md in conflict files, got %v", result.ConflictFiles)
	}

	// Verify worktree is clean after abort (still on our branch).
	out, err := exec.Command("git", "-C", clone, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Errorf("worktree should be clean after rebase abort, got: %s", out)
	}
}

func TestIntegration_ConcurrentTaskOverlap(t *testing.T) {
	t.Parallel()

	// Use a bare repo as remote so we can push to it.
	seed := initTestRepo(t)
	bare := t.TempDir()
	cmd := exec.Command("git", "clone", "--bare", seed, bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("bare clone: %v\n%s", err, out)
	}

	runIn := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}

	cloneAndBranch := func(name, file, content string) string {
		t.Helper()
		dir := t.TempDir()
		c := exec.Command("git", "clone", bare, dir)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("clone: %v\n%s", err, out)
		}
		runIn(dir, "config", "user.email", "test@test.com")
		runIn(dir, "config", "user.name", "test")
		runIn(dir, "checkout", "-b", "vairdict/"+name)
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		runIn(dir, "add", "-A")
		runIn(dir, "commit", "-m", name+" changes")
		return dir
	}

	// Both tasks clone at the same time (same base), before either merges.
	taskA := cloneAndBranch("task-a", "README.md", "# task A version\n")
	taskB := cloneAndBranch("task-b", "README.md", "# task B version\n")

	// Task A finishes first: merge to main and push.
	runIn(taskA, "checkout", "main")
	runIn(taskA, "merge", "vairdict/task-a")
	runIn(taskA, "push", "origin", "main")

	// Task B detects conflict.
	d := New(&execRunner{})
	result, err := d.DetectAndResolve(context.Background(), taskB, "main")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasConflicts {
		t.Error("expected conflict between concurrent tasks modifying the same file")
	}
	if len(result.ConflictFiles) == 0 {
		t.Error("expected at least one conflict file")
	}
}

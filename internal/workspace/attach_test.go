package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// fakeRunner records every command and replays canned responses keyed
// by the full command line. Anything not in the map succeeds with "".
type fakeRunner struct {
	replies map[string]fakeReply
	calls   []string
}

type fakeReply struct {
	out []byte
	err error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{replies: map[string]fakeReply{}}
}

func (f *fakeRunner) on(cmdline string, out string, err error) {
	f.replies[cmdline] = fakeReply{out: []byte(out), err: err}
}

func (f *fakeRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	line := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, line)
	if r, ok := f.replies[line]; ok {
		return r.out, r.err
	}
	return nil, nil
}

func TestAttach_BranchMissing(t *testing.T) {
	// When neither the worktree nor the branch exists, Attach must
	// refuse — there is no committed code to restore and resume can't
	// continue.
	runner := newFakeRunner()
	runner.on("git worktree list --porcelain", "", nil)
	runner.on("git rev-parse --verify vairdict/t-missing", "", errors.New("unknown revision"))

	mgr := New(t.TempDir(), "worktrees", runner)
	if _, err := mgr.Attach(context.Background(), "t-missing"); err == nil {
		t.Fatal("Attach should fail when branch does not exist")
	} else if !strings.Contains(err.Error(), "branch vairdict/t-missing not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAttach_ReAttachesExistingBranch(t *testing.T) {
	// Branch exists, worktree directory does not — Attach should run
	// `git worktree add <path> <branch>` to restore the worktree at the
	// deterministic path and reuse the existing branch.
	runner := newFakeRunner()
	runner.on("git worktree list --porcelain", "", nil) // no active worktrees
	runner.on("git rev-parse --verify vairdict/t-recover", "abc123\n", nil)

	repoRoot := t.TempDir()
	mgr := New(repoRoot, "worktrees", runner)

	ws, err := mgr.Attach(context.Background(), "t-recover")
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if ws.Branch != "vairdict/t-recover" {
		t.Errorf("branch = %q, want vairdict/t-recover", ws.Branch)
	}
	// Must have invoked `git worktree add <path> <branch>` — i.e. no
	// -b flag, because we are attaching to an existing branch, not
	// creating one.
	foundAdd := false
	for _, c := range runner.calls {
		if strings.HasPrefix(c, "git worktree add ") && strings.Contains(c, "vairdict/t-recover") && !strings.Contains(c, "-b") {
			foundAdd = true
			break
		}
	}
	if !foundAdd {
		t.Errorf("expected `git worktree add <path> vairdict/t-recover` (no -b); got calls: %v", runner.calls)
	}
}

func TestAttach_BranchExistsButWorktreeAddFails(t *testing.T) {
	// Surface the underlying git error rather than returning a generic
	// "cannot resume" — the operator needs the real reason.
	runner := newFakeRunner()
	runner.on("git worktree list --porcelain", "", nil)
	runner.on("git rev-parse --verify vairdict/t-broken", "abc123\n", nil)
	// Any call to `worktree add` fails. Match on prefix via injection
	// using a sentinel key that will never match and a fallback:
	// simpler to just wire the exact call we expect.
	repoRoot := t.TempDir()
	mgr := New(repoRoot, "worktrees", runner)

	wantPath := fmt.Sprintf("%s/worktrees/t-broken", repoRoot)
	runner.on(fmt.Sprintf("git worktree add %s vairdict/t-broken", wantPath), "", errors.New("boom"))

	if _, err := mgr.Attach(context.Background(), "t-broken"); err == nil {
		t.Fatal("Attach should propagate worktree add errors")
	} else if !strings.Contains(err.Error(), "re-attaching worktree") {
		t.Errorf("unexpected error: %v", err)
	}
}

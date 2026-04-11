package workspace

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// These tests use real git commands against a temporary repository.
// They exercise the full worktree lifecycle without mocks.

func setupTestRepo(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()

	run := func(args ...string) {
		t.Helper()
		runner := &ExecRunner{}
		_, err := runner.Run(context.Background(), dir, "git", args...)
		if err != nil {
			t.Fatalf("git %v failed: %v", args, err)
		}
	}

	run("init", "-b", "main")
	run("config", "user.email", "test@test.com")
	run("config", "user.name", "test")

	// Create an initial commit so HEAD exists.
	initial := filepath.Join(dir, "README.md")
	if err := os.WriteFile(initial, []byte("# test"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "initial")

	return dir
}

func TestCreate_And_Cleanup(t *testing.T) {
	repo := setupTestRepo(t)
	mgr := New(repo, "", &ExecRunner{})

	ctx := context.Background()
	ws, err := mgr.Create(ctx, "task-1")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Worktree directory should exist.
	if _, err := os.Stat(ws.Path); os.IsNotExist(err) {
		t.Fatal("worktree directory does not exist")
	}

	// Branch should match convention.
	if ws.Branch != "vairdict/task-1" {
		t.Errorf("branch = %q, want %q", ws.Branch, "vairdict/task-1")
	}

	// Worktree should contain the same files as the main repo.
	readme := filepath.Join(ws.Path, "README.md")
	if _, err := os.Stat(readme); os.IsNotExist(err) {
		t.Error("worktree missing README.md from main branch")
	}

	// Create a file in the worktree — should not appear in main repo.
	testFile := filepath.Join(ws.Path, "task-file.txt")
	if err := os.WriteFile(testFile, []byte("task output"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainTestFile := filepath.Join(repo, "task-file.txt")
	if _, err := os.Stat(mainTestFile); !os.IsNotExist(err) {
		t.Error("file created in worktree should not appear in main repo")
	}

	// Cleanup.
	if err := ws.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup failed: %v", err)
	}

	// Worktree directory should be gone.
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Error("worktree directory should not exist after cleanup")
	}
}

func TestCreate_ConcurrentTasks(t *testing.T) {
	repo := setupTestRepo(t)
	mgr := New(repo, "", &ExecRunner{})
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 3)
	workspaces := make(chan *Workspace, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			taskID := "concurrent-" + string(rune('a'+id))
			ws, err := mgr.Create(ctx, taskID)
			if err != nil {
				errs <- err
				return
			}
			workspaces <- ws

			// Write a unique file in each worktree.
			f := filepath.Join(ws.Path, "output.txt")
			_ = os.WriteFile(f, []byte(taskID), 0o644)
		}(i)
	}

	wg.Wait()
	close(errs)
	close(workspaces)

	for err := range errs {
		t.Fatalf("concurrent create failed: %v", err)
	}

	// Verify each worktree has its own unique file.
	var allWs []*Workspace
	for ws := range workspaces {
		allWs = append(allWs, ws)
		data, err := os.ReadFile(filepath.Join(ws.Path, "output.txt"))
		if err != nil {
			t.Errorf("failed to read output in %s: %v", ws.Path, err)
			continue
		}
		// Content should match the task ID that created it.
		if string(data) != ws.taskID {
			t.Errorf("worktree %s has content %q, want %q", ws.Path, string(data), ws.taskID)
		}
	}

	// Cleanup all.
	for _, ws := range allWs {
		if err := ws.Cleanup(ctx); err != nil {
			t.Errorf("cleanup failed for %s: %v", ws.taskID, err)
		}
	}
}

func TestCleanup_Idempotent(t *testing.T) {
	repo := setupTestRepo(t)
	mgr := New(repo, "", &ExecRunner{})
	ctx := context.Background()

	ws, err := mgr.Create(ctx, "idem-task")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// First cleanup.
	if err := ws.Cleanup(ctx); err != nil {
		t.Fatalf("first Cleanup failed: %v", err)
	}

	// Second cleanup should not error.
	if err := ws.Cleanup(ctx); err != nil {
		t.Fatalf("second Cleanup should be idempotent, got: %v", err)
	}
}

func TestCleanup_AfterFailure(t *testing.T) {
	// Simulate a task that creates files but "crashes" — cleanup should
	// still remove the worktree.
	repo := setupTestRepo(t)
	mgr := New(repo, "", &ExecRunner{})
	ctx := context.Background()

	ws, err := mgr.Create(ctx, "crash-task")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Simulate work: create and modify files.
	_ = os.WriteFile(filepath.Join(ws.Path, "partial.txt"), []byte("in progress"), 0o644)
	runner := &ExecRunner{}
	_, _ = runner.Run(ctx, ws.Path, "git", "add", ".")
	_, _ = runner.Run(ctx, ws.Path, "git", "commit", "-m", "partial work")

	// "Crash" — cleanup should still work.
	if err := ws.Cleanup(ctx); err != nil {
		t.Fatalf("Cleanup after crash failed: %v", err)
	}

	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Error("worktree should be removed after cleanup")
	}
}

func TestPruneStale_NoWorktreesDir(t *testing.T) {
	repo := setupTestRepo(t)
	mgr := New(repo, "", &ExecRunner{})

	// PruneStale should not error when no worktrees directory exists.
	if err := mgr.PruneStale(context.Background()); err != nil {
		t.Fatalf("PruneStale with no worktrees dir should not error: %v", err)
	}
}

func TestNew_CustomBaseDir(t *testing.T) {
	mgr := New("/tmp/repo", "custom/worktrees", &ExecRunner{})
	if mgr.baseDir != "custom/worktrees" {
		t.Errorf("baseDir = %q, want %q", mgr.baseDir, "custom/worktrees")
	}
}

func TestNew_DefaultBaseDir(t *testing.T) {
	mgr := New("/tmp/repo", "", &ExecRunner{})
	if mgr.baseDir != DefaultBaseDir {
		t.Errorf("baseDir = %q, want %q", mgr.baseDir, DefaultBaseDir)
	}
}

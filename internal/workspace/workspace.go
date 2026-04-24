// Package workspace manages isolated git worktrees for concurrent task execution.
// Each task gets its own worktree so multiple tasks can run in parallel without
// interfering with each other's file changes.
package workspace

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultBaseDir is the default directory under the repo root where worktrees
// are created. Each task gets a subdirectory named after its task ID.
const DefaultBaseDir = ".vairdict/worktrees"

// Manager creates and removes git worktrees for task isolation.
type Manager struct {
	// repoRoot is the absolute path to the main repository.
	repoRoot string
	// baseDir is the directory under repoRoot where worktrees are created.
	baseDir string
	// runner executes git commands. Injected for testing.
	runner CommandRunner
}

// CommandRunner executes shell commands. Injected for testing.
type CommandRunner interface {
	Run(ctx context.Context, dir string, name string, args ...string) ([]byte, error)
}

// ExecRunner is the real implementation using os/exec.
type ExecRunner struct{}

// Run executes a command in the given directory.
func (e *ExecRunner) Run(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// New creates a Manager rooted at the given repository path. The baseDir
// is relative to repoRoot; pass "" to use DefaultBaseDir.
func New(repoRoot string, baseDir string, runner CommandRunner) *Manager {
	if baseDir == "" {
		baseDir = DefaultBaseDir
	}
	return &Manager{
		repoRoot: repoRoot,
		baseDir:  baseDir,
		runner:   runner,
	}
}

// Workspace represents an active git worktree for a task.
type Workspace struct {
	// Path is the absolute path to the worktree directory.
	Path string
	// Branch is the git branch created for this worktree.
	Branch string

	mgr    *Manager
	taskID string
}

// Attach returns a Workspace for an existing task, reusing the on-disk
// worktree if it is still registered with git, or re-attaching the
// deterministic `vairdict/<taskID>` branch to a fresh worktree
// directory if the previous one was cleaned up (crash, prune, laptop
// close). Used by `vairdict resume` so a run can pick up exactly where
// it was interrupted without regenerating the plan or losing the code
// the coder already committed.
//
// If neither the worktree nor the branch exists, Attach returns an
// error — resume cannot recover from a missing branch because the code
// the task produced is lost.
func (m *Manager) Attach(ctx context.Context, taskID string) (*Workspace, error) {
	branch := "vairdict/" + taskID
	worktreePath := filepath.Join(m.repoRoot, m.baseDir, taskID)

	// Case 1: worktree is still registered with git and the directory
	// exists — just reuse it.
	if _, err := os.Stat(worktreePath); err == nil {
		active := m.listActiveWorktrees(ctx)
		if active[worktreePath] {
			slog.Info("workspace reattached (existing worktree)", "task", taskID, "path", worktreePath, "branch", branch)
			return &Workspace{Path: worktreePath, Branch: branch, mgr: m, taskID: taskID}, nil
		}
		// Directory exists but git doesn't know about it (partially
		// cleaned up). Remove it so `git worktree add` below succeeds.
		_ = os.RemoveAll(worktreePath)
	}

	// Case 2: branch exists but the worktree is gone. Recreate the
	// worktree pointed at the existing branch so the committed code is
	// restored at worktreePath.
	if _, err := m.runner.Run(ctx, m.repoRoot, "git", "rev-parse", "--verify", branch); err != nil {
		return nil, fmt.Errorf("branch %s not found — cannot resume: %w", branch, err)
	}

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return nil, fmt.Errorf("creating worktree base dir: %w", err)
	}
	if _, err := m.runner.Run(ctx, m.repoRoot, "git", "worktree", "add", worktreePath, branch); err != nil {
		return nil, fmt.Errorf("re-attaching worktree for branch %s: %w", branch, err)
	}

	slog.Info("workspace reattached (recreated worktree)", "task", taskID, "path", worktreePath, "branch", branch)
	return &Workspace{Path: worktreePath, Branch: branch, mgr: m, taskID: taskID}, nil
}

// Create creates a new git worktree for the given task. The worktree is
// based on the current HEAD of the main branch. Returns a Workspace that
// must be cleaned up with Cleanup() when the task is done.
func (m *Manager) Create(ctx context.Context, taskID string) (*Workspace, error) {
	branch := "vairdict/" + taskID
	worktreePath := filepath.Join(m.repoRoot, m.baseDir, taskID)

	// Ensure the base directory exists.
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return nil, fmt.Errorf("creating worktree base dir: %w", err)
	}

	// Resolve the main branch name (main or master).
	baseBranch, err := m.resolveMainBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving main branch: %w", err)
	}

	// Create the worktree on a new branch based on the main branch.
	_, err = m.runner.Run(ctx, m.repoRoot, "git", "worktree", "add",
		"-b", branch, worktreePath, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("creating worktree: %w", err)
	}

	slog.Info("workspace created", "task", taskID, "path", worktreePath, "branch", branch)

	return &Workspace{
		Path:   worktreePath,
		Branch: branch,
		mgr:    m,
		taskID: taskID,
	}, nil
}

// Cleanup removes the worktree directory and deletes its branch.
// Safe to call multiple times — subsequent calls are no-ops.
func (w *Workspace) Cleanup(ctx context.Context) error {
	// Remove the worktree.
	_, err := w.mgr.runner.Run(ctx, w.mgr.repoRoot, "git", "worktree", "remove", "--force", w.Path)
	if err != nil {
		slog.Warn("failed to remove worktree", "path", w.Path, "error", err)
		// Try removing the directory manually as a fallback.
		_ = os.RemoveAll(w.Path)
	}

	// Prune stale worktree entries.
	_, _ = w.mgr.runner.Run(ctx, w.mgr.repoRoot, "git", "worktree", "prune")

	// Delete the branch.
	_, err = w.mgr.runner.Run(ctx, w.mgr.repoRoot, "git", "branch", "-D", w.Branch)
	if err != nil {
		slog.Debug("failed to delete worktree branch", "branch", w.Branch, "error", err)
	}

	slog.Info("workspace cleaned up", "task", w.taskID, "path", w.Path)
	return nil
}

// PruneStale removes any leftover worktrees that were not cleaned up
// (e.g. from a crashed process). It asks git to prune its internal
// worktree metadata, then scans the base directory and removes any
// directories that are not registered as active worktrees.
func (m *Manager) PruneStale(ctx context.Context) error {
	// Let git clean up its own stale metadata first.
	_, _ = m.runner.Run(ctx, m.repoRoot, "git", "worktree", "prune")

	baseDir := filepath.Join(m.repoRoot, m.baseDir)
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No worktrees directory — nothing to prune.
		}
		return fmt.Errorf("reading worktree base dir: %w", err)
	}

	// Build a set of active worktree paths from git.
	activeWorktrees := m.listActiveWorktrees(ctx)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		worktreePath := filepath.Join(baseDir, entry.Name())

		// Skip directories that are still active worktrees.
		if activeWorktrees[worktreePath] {
			continue
		}

		// Not an active worktree — remove the orphaned directory.
		slog.Info("pruning stale worktree", "path", worktreePath)
		_ = os.RemoveAll(worktreePath)
	}

	return nil
}

// listActiveWorktrees returns a set of absolute paths for all currently
// registered git worktrees.
func (m *Manager) listActiveWorktrees(ctx context.Context) map[string]bool {
	out, err := m.runner.Run(ctx, m.repoRoot, "git", "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}

	active := make(map[string]bool)
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			path := strings.TrimPrefix(line, "worktree ")
			active[path] = true
		}
	}
	return active
}

// resolveMainBranch returns the name of the main branch (main or master).
func (m *Manager) resolveMainBranch(ctx context.Context) (string, error) {
	// Try "main" first.
	_, err := m.runner.Run(ctx, m.repoRoot, "git", "rev-parse", "--verify", "main")
	if err == nil {
		return "main", nil
	}

	// Fall back to "master".
	_, err = m.runner.Run(ctx, m.repoRoot, "git", "rev-parse", "--verify", "master")
	if err == nil {
		return "master", nil
	}

	return "", fmt.Errorf("neither 'main' nor 'master' branch found")
}

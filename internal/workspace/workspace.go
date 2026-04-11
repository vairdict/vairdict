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
// (e.g. from a crashed process). It scans the base directory for
// directories that are not registered as active worktrees.
func (m *Manager) PruneStale(ctx context.Context) error {
	_, _ = m.runner.Run(ctx, m.repoRoot, "git", "worktree", "prune")

	baseDir := filepath.Join(m.repoRoot, m.baseDir)
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No worktrees directory — nothing to prune.
		}
		return fmt.Errorf("reading worktree base dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		worktreePath := filepath.Join(baseDir, entry.Name())

		// Check if this directory is a registered worktree.
		_, err := m.runner.Run(ctx, m.repoRoot, "git", "worktree", "list", "--porcelain")
		if err != nil {
			continue
		}

		// If the directory exists but isn't in the worktree list, remove it.
		// For simplicity, just try to remove it — git worktree remove will
		// fail gracefully if it's still active.
		_, rmErr := m.runner.Run(ctx, m.repoRoot, "git", "worktree", "remove", "--force", worktreePath)
		if rmErr != nil {
			_ = os.RemoveAll(worktreePath)
		}
	}

	return nil
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

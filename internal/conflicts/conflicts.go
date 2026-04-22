// Package conflicts detects and resolves merge conflicts between a task
// branch and its base branch. Before opening a PR, the orchestrator calls
// DetectAndResolve to fetch the latest base, check for divergence, and
// attempt an automatic rebase. If the rebase fails (true conflict), it
// returns the list of conflicting files so the caller can escalate.
package conflicts

import (
	"context"
	"fmt"
	"strings"
)

// CommandRunner executes shell commands in a given directory.
type CommandRunner interface {
	Run(ctx context.Context, dir string, name string, args ...string) ([]byte, error)
}

// Result describes the outcome of a conflict check.
type Result struct {
	// Diverged is true when the base branch has commits not in HEAD.
	Diverged bool
	// Rebased is true when an automatic rebase succeeded.
	Rebased bool
	// HasConflicts is true when a rebase was attempted but failed.
	HasConflicts bool
	// ConflictFiles lists the files that have merge conflicts.
	ConflictFiles []string
}

// Clean returns true when there are no conflicts — either the base has
// not diverged or a rebase resolved the divergence.
func (r *Result) Clean() bool {
	return !r.HasConflicts
}

// Detector checks for and resolves merge conflicts.
type Detector struct {
	runner CommandRunner
}

// New creates a Detector that uses the given runner for git commands.
func New(runner CommandRunner) *Detector {
	return &Detector{runner: runner}
}

// DetectAndResolve is the main entry point. It fetches the latest base
// branch, checks whether the base has diverged from HEAD, and if so
// attempts an automatic rebase. Returns a Result describing the outcome.
//
// workDir is the worktree path where the task branch is checked out.
// baseBranch is the branch to merge into (typically "main").
func (d *Detector) DetectAndResolve(ctx context.Context, workDir, baseBranch string) (*Result, error) {
	// Fetch latest base branch from origin.
	if _, err := d.runner.Run(ctx, workDir, "git", "fetch", "origin", baseBranch); err != nil {
		return nil, fmt.Errorf("fetching origin/%s: %w", baseBranch, err)
	}

	// Check how many commits the base has ahead of us.
	diverged, err := d.hasDiverged(ctx, workDir, baseBranch)
	if err != nil {
		return nil, fmt.Errorf("checking divergence: %w", err)
	}

	if !diverged {
		return &Result{Diverged: false}, nil
	}

	// Base has new commits — attempt rebase.
	result, err := d.rebase(ctx, workDir, baseBranch)
	if err != nil {
		return nil, err
	}
	result.Diverged = true
	return result, nil
}

// hasDiverged returns true if origin/<baseBranch> has commits not in HEAD.
func (d *Detector) hasDiverged(ctx context.Context, workDir, baseBranch string) (bool, error) {
	ref := "origin/" + baseBranch
	out, err := d.runner.Run(ctx, workDir, "git", "rev-list", "--count", "HEAD.."+ref)
	if err != nil {
		return false, fmt.Errorf("rev-list: %w", err)
	}
	count := strings.TrimSpace(string(out))
	return count != "0", nil
}

// rebase attempts git rebase origin/<baseBranch>. On success it returns a
// clean result with Rebased=true. On failure it extracts the conflicting
// file list from git status, aborts the rebase, and returns HasConflicts=true.
func (d *Detector) rebase(ctx context.Context, workDir, baseBranch string) (*Result, error) {
	ref := "origin/" + baseBranch

	_, err := d.runner.Run(ctx, workDir, "git", "rebase", ref)
	if err == nil {
		// Rebase succeeded — divergence resolved.
		return &Result{Rebased: true}, nil
	}

	// Rebase failed — extract conflicting files.
	files, extractErr := d.conflictFiles(ctx, workDir)
	if extractErr != nil {
		// Abort anyway, then return the extract error.
		_, _ = d.runner.Run(ctx, workDir, "git", "rebase", "--abort")
		return nil, fmt.Errorf("extracting conflict files: %w", extractErr)
	}

	// Abort the in-progress rebase to restore the worktree.
	if _, abortErr := d.runner.Run(ctx, workDir, "git", "rebase", "--abort"); abortErr != nil {
		return nil, fmt.Errorf("aborting rebase: %w", abortErr)
	}

	return &Result{
		HasConflicts:  true,
		ConflictFiles: files,
	}, nil
}

// conflictFiles returns the list of files with unmerged entries (UU in
// git status --porcelain). Called while a conflicted rebase is in progress.
func (d *Detector) conflictFiles(ctx context.Context, workDir string) ([]string, error) {
	out, err := d.runner.Run(ctx, workDir, "git", "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, fmt.Errorf("diff --name-only: %w", err)
	}

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

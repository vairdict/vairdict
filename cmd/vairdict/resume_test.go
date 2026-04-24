package main

import (
	"context"
	"testing"

	"github.com/vairdict/vairdict/internal/state"
)

// TestRunOrchestrationWithResume_SkipsPlanWhenPlanProvided verifies the
// resume-from-code path: when a resumeState carries the plan text, the
// plan phase must be skipped and the pre-existing plan threaded into
// the code and quality phases so they work from the same plan the
// branch was originally built against.
func TestRunOrchestrationWithResume_SkipsPlanWhenPlanProvided(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	task := state.NewTask("t-resume-code", "pick up where we left off")
	r := &fakeRenderer{}

	resume := resumeState{
		plan:      "original plan text",
		branch:    "vairdict/t-resume-code",
		fromPhase: state.PhaseCode,
	}

	err := runOrchestrationWithResume(context.Background(), b.deps(), task, r, resume)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.plan.called {
		t.Error("plan runner should be skipped when resume.plan is set")
	}
	if b.gh.branchCalled {
		t.Error("CreateBranch should be skipped when resume.branch is set")
	}
	if !b.code.called {
		t.Error("code runner should run on a code-phase resume")
	}
	if b.code.plan != "original plan text" {
		t.Errorf("code runner got plan %q, want original", b.code.plan)
	}
	if !b.quality.called {
		t.Error("quality runner should still run after code")
	}
	if b.quality.plan != "original plan text" {
		t.Errorf("quality runner got plan %q, want original", b.quality.plan)
	}
}

// TestRunOrchestrationWithResume_SkipsCodeForQualityPhase verifies that
// resuming from the quality phase skips the coder entirely — the code
// is already committed to the branch and re-running would overwrite it.
func TestRunOrchestrationWithResume_SkipsCodeForQualityPhase(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	task := state.NewTask("t-resume-quality", "re-run the judge")
	r := &fakeRenderer{}

	resume := resumeState{
		plan:      "the plan",
		branch:    "vairdict/t-resume-quality",
		fromPhase: state.PhaseQuality,
	}

	err := runOrchestrationWithResume(context.Background(), b.deps(), task, r, resume)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.plan.called {
		t.Error("plan runner should be skipped")
	}
	if b.code.called {
		t.Error("code runner must NOT run on a quality-phase resume (code already committed)")
	}
	if b.commitCalled {
		t.Error("commit must NOT run on a quality-phase resume")
	}
	if !b.quality.called {
		t.Error("quality runner should run")
	}
	if b.quality.plan != "the plan" {
		t.Errorf("quality runner got plan %q, want original", b.quality.plan)
	}
}

// TestRunOrchestrationWithResume_EmptyResumeIsNoop verifies that a
// zero-value resumeState is equivalent to a fresh run — the existing
// runOrchestration wrapper relies on this.
func TestRunOrchestrationWithResume_EmptyResumeIsNoop(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	task := state.NewTask("t-fresh", "fresh run")
	r := &fakeRenderer{}

	err := runOrchestrationWithResume(context.Background(), b.deps(), task, r, resumeState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !b.plan.called {
		t.Error("plan runner should run on a fresh run")
	}
	if !b.gh.branchCalled {
		t.Error("CreateBranch should run on a fresh run")
	}
	if !b.code.called || !b.quality.called {
		t.Error("both code and quality should run on a fresh run")
	}
}

// TestRunOrchestrationWithResume_PersistsPlanOutputOnFreshPlan verifies
// that when the plan phase runs (no resume.plan), its output is
// captured on the task so a later `vairdict resume` can reuse it.
func TestRunOrchestrationWithResume_PersistsPlanOutputOnFreshPlan(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	task := state.NewTask("t-fresh-persist", "persist the plan")
	r := &fakeRenderer{}

	var persisted *state.Task
	deps := b.deps()
	deps.persistTask = func(tk *state.Task) error {
		persisted = tk
		return nil
	}

	err := runOrchestrationWithResume(context.Background(), deps, task, r, resumeState{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if persisted == nil {
		t.Fatal("persistTask should have been called after plan phase")
	}
	if persisted.PlanOutput != "the plan" {
		t.Errorf("persisted PlanOutput = %q, want %q", persisted.PlanOutput, "the plan")
	}
}

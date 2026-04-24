package state

import (
	"testing"
	"time"
)

func TestIsResumable(t *testing.T) {
	cases := []struct {
		state TaskState
		want  bool
	}{
		{StatePending, false},
		{StatePlanning, true},
		{StatePlanReview, true},
		{StateCoding, true},
		{StateCodeReview, true},
		{StateQuality, true},
		{StateQualityReview, true},
		{StateDone, false},
		{StateEscalated, false},
		{StateBlocked, false},
	}
	for _, tc := range cases {
		t.Run(string(tc.state), func(t *testing.T) {
			task := &Task{State: tc.state}
			if got := task.IsResumable(); got != tc.want {
				t.Errorf("IsResumable() for %s = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

func TestPlanOutputAndPID_RoundTrip(t *testing.T) {
	// #90: plan_output + pid columns are persisted so `vairdict resume`
	// reuses the exact plan the worktree was built from, and `vairdict
	// status` can show a RUNNING indicator.
	store := newTestStore(t)
	task := NewTask("task-resume", "build a resumable thing")
	task.PlanOutput = "## Requirements\n...\n## Plan\n1. do it\n2. test it"
	task.PID = 4242

	if err := store.CreateTask(task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	got, err := store.GetTask("task-resume")
	if err != nil {
		t.Fatalf("getting task: %v", err)
	}
	if got.PlanOutput != task.PlanOutput {
		t.Errorf("PlanOutput not round-tripped:\nwant: %q\ngot:  %q", task.PlanOutput, got.PlanOutput)
	}
	if got.PID != task.PID {
		t.Errorf("PID = %d, want %d", got.PID, task.PID)
	}

	// Mutate and UpdateTask — confirm the update path also persists.
	got.PID = 0
	got.PlanOutput = got.PlanOutput + "\n3. resume from here"
	got.UpdatedAt = time.Now()
	if err := store.UpdateTask(got); err != nil {
		t.Fatalf("updating task: %v", err)
	}

	after, err := store.GetTask("task-resume")
	if err != nil {
		t.Fatalf("getting task after update: %v", err)
	}
	if after.PID != 0 {
		t.Errorf("PID after clear = %d, want 0", after.PID)
	}
	if after.PlanOutput != got.PlanOutput {
		t.Errorf("PlanOutput after update not persisted")
	}
}

func TestListResumable(t *testing.T) {
	store := newTestStore(t)

	// Mix of resumable and non-resumable tasks.
	tasks := []struct {
		id    string
		state TaskState
	}{
		{"a-done", StateDone},
		{"b-planning", StatePlanning},
		{"c-escalated", StateEscalated},
		{"d-coding", StateCoding},
		{"e-blocked", StateBlocked},
		{"f-quality-review", StateQualityReview},
	}
	for i, tc := range tasks {
		tk := NewTask(tc.id, "intent "+tc.id)
		tk.State = tc.state
		// Spread UpdatedAt so the sort can be verified deterministically:
		// later entries update later, so they should appear first.
		tk.UpdatedAt = time.Now().Add(time.Duration(i) * time.Second)
		if err := store.CreateTask(tk); err != nil {
			t.Fatalf("creating %s: %v", tc.id, err)
		}
	}

	got, err := store.ListResumable()
	if err != nil {
		t.Fatalf("ListResumable: %v", err)
	}

	wantIDs := []string{"f-quality-review", "d-coding", "b-planning"}
	if len(got) != len(wantIDs) {
		t.Fatalf("got %d resumable tasks, want %d: %+v", len(got), len(wantIDs), got)
	}
	for i, id := range wantIDs {
		if got[i].ID != id {
			t.Errorf("position %d: got %s, want %s (full order: %v)", i, got[i].ID, id, taskIDs(got))
		}
	}
}

func taskIDs(ts []*Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

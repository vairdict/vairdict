package state

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("creating test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestCreateAndGetTask(t *testing.T) {
	store := newTestStore(t)
	task := NewTask("task-1", "implement feature X")

	if err := store.CreateTask(task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	got, err := store.GetTask("task-1")
	if err != nil {
		t.Fatalf("getting task: %v", err)
	}

	if got.ID != task.ID {
		t.Errorf("expected ID %s, got %s", task.ID, got.ID)
	}
	if got.Intent != task.Intent {
		t.Errorf("expected intent %s, got %s", task.Intent, got.Intent)
	}
	if got.State != StatePending {
		t.Errorf("expected state pending, got %s", got.State)
	}
	if got.LoopCount == nil {
		t.Fatal("expected LoopCount to be initialized")
	}
}

func TestCreateAndGetTask_HardConstraintsRoundTrip(t *testing.T) {
	// #87: hard_constraints column is persisted and hydrated so quality-
	// driven plan rewinds survive a process restart.
	store := newTestStore(t)
	task := NewTask("task-hc", "build it right this time")
	task.HardConstraints = []string{
		"[quality judge, P0] /admin/users is not protected",
		"[quality judge, P1] rate-limit bypass on /admin/logs",
	}

	if err := store.CreateTask(task); err != nil {
		t.Fatalf("creating task: %v", err)
	}

	got, err := store.GetTask("task-hc")
	if err != nil {
		t.Fatalf("getting task: %v", err)
	}
	if len(got.HardConstraints) != 2 {
		t.Fatalf("expected 2 hard constraints, got %d: %v", len(got.HardConstraints), got.HardConstraints)
	}
	for i, want := range task.HardConstraints {
		if got.HardConstraints[i] != want {
			t.Errorf("hard_constraints[%d] = %q, want %q", i, got.HardConstraints[i], want)
		}
	}

	// Mutate and update — confirm the update path also round-trips.
	got.HardConstraints = append(got.HardConstraints, "[quality judge, P0] third constraint")
	got.UpdatedAt = time.Now()
	if err := store.UpdateTask(got); err != nil {
		t.Fatalf("updating task: %v", err)
	}
	refetched, err := store.GetTask("task-hc")
	if err != nil {
		t.Fatalf("refetching: %v", err)
	}
	if len(refetched.HardConstraints) != 3 {
		t.Errorf("expected 3 constraints after update, got %d", len(refetched.HardConstraints))
	}
}

func TestGetTaskNotFound(t *testing.T) {
	store := newTestStore(t)

	_, err := store.GetTask("nonexistent")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got: %v", err)
	}
}

func TestCreateDuplicateTask(t *testing.T) {
	store := newTestStore(t)
	task := NewTask("task-dup", "test")

	if err := store.CreateTask(task); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := store.CreateTask(task); err == nil {
		t.Error("expected error on duplicate create")
	}
}

func TestUpdateTask(t *testing.T) {
	store := newTestStore(t)
	task := NewTask("task-upd", "test update")
	_ = store.CreateTask(task)

	_ = task.Transition(StatePlanning)
	task.Assumptions = []Assumption{
		{Description: "API is stable", Severity: SeverityP2, Phase: PhasePlan},
	}
	task.Attempts = []Attempt{
		{Phase: PhasePlan, Loop: 0, CreatedAt: time.Now()},
	}

	if err := store.UpdateTask(task); err != nil {
		t.Fatalf("updating task: %v", err)
	}

	got, err := store.GetTask("task-upd")
	if err != nil {
		t.Fatalf("getting updated task: %v", err)
	}

	if got.State != StatePlanning {
		t.Errorf("expected state planning, got %s", got.State)
	}
	if got.Phase != PhasePlan {
		t.Errorf("expected phase plan, got %s", got.Phase)
	}
	if len(got.Assumptions) != 1 {
		t.Fatalf("expected 1 assumption, got %d", len(got.Assumptions))
	}
	if got.Assumptions[0].Description != "API is stable" {
		t.Errorf("unexpected assumption: %s", got.Assumptions[0].Description)
	}
	if len(got.Attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(got.Attempts))
	}
}

func TestUpdateTaskNotFound(t *testing.T) {
	store := newTestStore(t)
	task := NewTask("ghost", "not in db")

	err := store.UpdateTask(task)
	if err == nil {
		t.Error("expected error updating nonexistent task")
	}
}

func TestListTasksAll(t *testing.T) {
	store := newTestStore(t)
	_ = store.CreateTask(NewTask("t-1", "first"))
	_ = store.CreateTask(NewTask("t-2", "second"))
	_ = store.CreateTask(NewTask("t-3", "third"))

	tasks, err := store.ListTasks("")
	if err != nil {
		t.Fatalf("listing tasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("expected 3 tasks, got %d", len(tasks))
	}
}

func TestListTasksFilterByState(t *testing.T) {
	store := newTestStore(t)

	t1 := NewTask("t-1", "pending task")
	_ = store.CreateTask(t1)

	t2 := NewTask("t-2", "planning task")
	_ = t2.Transition(StatePlanning)
	_ = store.CreateTask(t2)

	t3 := NewTask("t-3", "another pending")
	_ = store.CreateTask(t3)

	pending, err := store.ListTasks(StatePending)
	if err != nil {
		t.Fatalf("listing pending: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending tasks, got %d", len(pending))
	}

	planning, err := store.ListTasks(StatePlanning)
	if err != nil {
		t.Fatalf("listing planning: %v", err)
	}
	if len(planning) != 1 {
		t.Errorf("expected 1 planning task, got %d", len(planning))
	}
}

func TestListTasksEmpty(t *testing.T) {
	store := newTestStore(t)

	tasks, err := store.ListTasks("")
	if err != nil {
		t.Fatalf("listing empty: %v", err)
	}
	if len(tasks) != 0 {
		t.Errorf("expected 0 tasks, got %d", len(tasks))
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Create and populate.
	store1, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	task := NewTask("persist-1", "survive restart")
	_ = task.Transition(StatePlanning)
	task.LoopCount[PhasePlan] = 2
	task.Assumptions = []Assumption{
		{Description: "test assumption", Severity: SeverityP1, Phase: PhasePlan},
	}
	task.Attempts = []Attempt{
		{
			Phase: PhasePlan, Loop: 1,
			Verdict:   &Verdict{Score: 0.8, Pass: true, Gaps: []Gap{{Severity: SeverityP2, Description: "minor gap", Blocking: false}}},
			CreatedAt: time.Now(),
		},
	}
	_ = store1.CreateTask(task)
	_ = store1.Close()

	// Reopen and verify.
	store2, err := NewStore(dbPath)
	if err != nil {
		t.Fatalf("reopening store: %v", err)
	}
	defer func() { _ = store2.Close() }()

	got, err := store2.GetTask("persist-1")
	if err != nil {
		t.Fatalf("getting persisted task: %v", err)
	}

	if got.State != StatePlanning {
		t.Errorf("expected state planning, got %s", got.State)
	}
	if got.LoopCount[PhasePlan] != 2 {
		t.Errorf("expected loop count 2, got %d", got.LoopCount[PhasePlan])
	}
	if len(got.Assumptions) != 1 {
		t.Fatalf("expected 1 assumption, got %d", len(got.Assumptions))
	}
	if got.Assumptions[0].Severity != SeverityP1 {
		t.Errorf("expected severity P1, got %s", got.Assumptions[0].Severity)
	}
	if len(got.Attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(got.Attempts))
	}
	if got.Attempts[0].Verdict == nil {
		t.Fatal("expected verdict to be persisted")
	}
	if got.Attempts[0].Verdict.Score != 0.8 {
		t.Errorf("expected verdict score 0.8, got %f", got.Attempts[0].Verdict.Score)
	}
	if len(got.Attempts[0].Verdict.Gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(got.Attempts[0].Verdict.Gaps))
	}
}

func TestUpdateWithLoopCountAndAttempts(t *testing.T) {
	store := newTestStore(t)
	task := NewTask("t-full", "full workflow")
	_ = store.CreateTask(task)

	// Simulate a full requeue cycle.
	_ = task.Transition(StatePlanning)
	_ = task.Transition(StatePlanReview)
	_ = task.Requeue(3)
	task.Attempts = append(task.Attempts, Attempt{
		Phase: PhasePlan, Loop: 1, CreatedAt: time.Now(),
	})
	_ = store.UpdateTask(task)

	got, err := store.GetTask("t-full")
	if err != nil {
		t.Fatalf("getting task: %v", err)
	}
	if got.LoopCount[PhasePlan] != 1 {
		t.Errorf("expected loop count 1, got %d", got.LoopCount[PhasePlan])
	}
	if got.State != StatePlanning {
		t.Errorf("expected state planning, got %s", got.State)
	}
}

func TestDefaultDBPath(t *testing.T) {
	path, err := DefaultDBPath()
	if err != nil {
		t.Fatalf("getting default db path: %v", err)
	}

	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".vairdict", "state.db")
	if path != expected {
		t.Errorf("expected %s, got %s", expected, path)
	}
}

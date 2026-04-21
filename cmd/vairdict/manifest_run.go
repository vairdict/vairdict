package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"

	"github.com/google/uuid"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/deps"
	"github.com/vairdict/vairdict/internal/state"
	"github.com/vairdict/vairdict/internal/ui"
)

// runManifest executes a multi-task manifest with inter-task dependencies.
// It builds a deps.Graph keyed by task ID, validates (cycle + missing-dep
// check), and schedules ready tasks into the same semaphore-bounded
// concurrency pool as runTasks. A failing task cascades blocked state to
// every transitive downstream in the store before the scheduler settles.
func runManifest(manifest *Manifest, mode ui.Mode, colors ui.ColorScheme, ascii bool) error {
	overlayPath, err := config.ResolveOverlayPath(envFlag, config.IsCI(), ".", fileExistsFunc)
	if err != nil {
		return fmt.Errorf("resolving env: %w", err)
	}

	cfg, err := config.LoadConfigWithOverlay("vairdict.yaml", overlayPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	dbPath, err := state.DefaultDBPath()
	if err != nil {
		return fmt.Errorf("resolving database path: %w", err)
	}

	store, err := state.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("opening state store: %w", err)
	}
	defer func() { _ = store.Close() }()

	client, _, err := resolveCompleter(cfg)
	if err != nil {
		return err
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Assign an ID per manifest task and remember the name→ID mapping so
	// DependsOn (by name) can be rewritten to task IDs.
	nameToID := make(map[string]string, len(manifest.Tasks))
	idToName := make(map[string]string, len(manifest.Tasks))
	for _, t := range manifest.Tasks {
		id := uuid.New().String()[:8]
		nameToID[t.Name] = id
		idToName[id] = t.Name
	}

	// Persist every task up front in StatePending. DependsOn is rewritten
	// from names to IDs so the store is self-describing and vairdict
	// status can render the graph after the invocation ends.
	tasks := make(map[string]*state.Task, len(manifest.Tasks))
	issueByID := make(map[string]int, len(manifest.Tasks))
	for _, mt := range manifest.Tasks {
		t := state.NewTask(nameToID[mt.Name], mt.Intent)
		t.DependsOn = mapNames(mt.DependsOn, nameToID)
		if err := store.CreateTask(t); err != nil {
			return fmt.Errorf("creating task %q: %w", mt.Name, err)
		}
		tasks[t.ID] = t
		issueByID[t.ID] = mt.Issue
	}

	// Build and validate the graph.
	g := deps.New()
	for _, t := range tasks {
		if err := g.Add(t.ID, t.DependsOn); err != nil {
			return fmt.Errorf("adding %q to graph: %w", idToName[t.ID], err)
		}
	}
	if err := g.Validate(); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}

	fmt.Fprintf(os.Stdout, "Running %d tasks from manifest (max %d concurrent)\n\n",
		len(manifest.Tasks), cfg.Parallel.MaxTasks)

	results := make(map[string]taskResult, len(tasks))
	var resultsMu sync.Mutex
	sem := make(chan struct{}, cfg.Parallel.MaxTasks)
	var wg sync.WaitGroup

	// Scheduler loop: dispatch every ready node, then wait for one to
	// settle before polling again. This keeps lock scope tight without
	// needing a condition variable.
	settled := make(chan struct{}, len(tasks))
	for {
		ready := g.Ready()
		for _, id := range ready {
			if err := g.MarkRunning(id); err != nil {
				slog.Warn("failed to mark running", "id", id, "error", err)
				continue
			}
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				t := tasks[id]
				res := runSingleTask(ctx, cfg, client, store, repoRoot, t.Intent, issueByID[id])
				// runSingleTask generated its own task ID for the run; we
				// want the manifest ID to be the authoritative record
				// surfaced to the user. The per-goroutine subtask ID is
				// only used internally for workspace isolation.
				res.TaskID = id

				resultsMu.Lock()
				results[id] = res
				resultsMu.Unlock()

				if res.Err != nil {
					blocked, err := g.MarkFailed(id)
					if err != nil {
						slog.Warn("mark failed: graph update", "id", id, "error", err)
					}
					_, _ = fmt.Fprintf(os.Stdout, "[%s] %s → FAIL: %v\n", idToName[id], truncate(t.Intent, 60), res.Err)
					// Cascade: mark every newly-blocked task in the store
					// so vairdict status reflects the truth.
					for _, b := range blocked {
						bt := tasks[b]
						if err := bt.Transition(state.StateBlocked); err == nil {
							_ = store.UpdateTask(bt)
						}
						_, _ = fmt.Fprintf(os.Stdout, "[%s] blocked (upstream %q failed)\n", idToName[b], idToName[id])
					}
				} else {
					if err := g.MarkDone(id); err != nil {
						slog.Warn("mark done: graph update", "id", id, "error", err)
					}
					_, _ = fmt.Fprintf(os.Stdout, "[%s] %s → pass\n", idToName[id], truncate(t.Intent, 60))
				}
				settled <- struct{}{}
			}(id)
		}

		if g.AllSettled() {
			break
		}

		// Wait for at least one in-flight task to finish before the next
		// Ready() poll. If nothing was ready and nothing in flight, the
		// graph is structurally stuck — AllSettled will be true next
		// iteration, so this branch only blocks when work is outstanding.
		<-settled
	}

	wg.Wait()

	// Print summary table: manifest name, state, verdict.
	fmt.Fprintln(os.Stdout, "\n--- Summary ---")
	var failures []string
	for _, mt := range manifest.Tasks {
		id := nameToID[mt.Name]
		res, ran := results[id]
		statusStr := string(tasks[id].State)
		detail := ""
		if ran {
			if res.Err != nil {
				statusStr = "failed"
				detail = ": " + res.Err.Error()
				failures = append(failures, fmt.Sprintf("%s (%s)", mt.Name, id))
			} else {
				statusStr = "done"
			}
		}
		_, _ = fmt.Fprintf(os.Stdout, "  [%s] %-8s %-8s %s%s\n", id, mt.Name, statusStr, truncate(mt.Intent, 50), detail)
	}

	if len(failures) > 0 {
		return fmt.Errorf("%d of %d tasks failed: %v", len(failures), len(manifest.Tasks), failures)
	}
	return nil
}

// mapNames resolves a slice of manifest names into their generated IDs,
// dropping any names that don't map (validateManifest already guarantees
// they do, but we're defensive).
func mapNames(names []string, lookup map[string]string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if id, ok := lookup[n]; ok {
			out = append(out, id)
		}
	}
	return out
}

// maybeBlockOnDeps inspects the store for each declared dependency. If
// any dep is not StateDone the task is put into StateBlocked (returns
// true). The single-task runTask entry point calls this so `--depends-on`
// has well-defined semantics without cross-process coordination.
func maybeBlockOnDeps(store *state.Store, t *state.Task, depIDs []string) (bool, error) {
	for _, id := range depIDs {
		dep, err := store.GetTask(id)
		if err != nil {
			return false, fmt.Errorf("dependency %q not found in store: %w", id, err)
		}
		if dep.State != state.StateDone {
			// Straight to blocked — don't walk the pipeline at all.
			if err := t.Transition(state.StateBlocked); err != nil {
				return false, fmt.Errorf("transitioning to blocked: %w", err)
			}
			return true, nil
		}
	}
	return false, nil
}


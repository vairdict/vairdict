// Package deps implements the task dependency graph VAIrdict uses to
// schedule multi-task runs in the correct order.
//
// A graph is built at submission time from user-declared dependencies
// (either a manifest file or the --depends-on CLI flag). It rejects
// circular dependencies, exposes the set of ready-to-run tasks (nodes
// whose dependencies are all complete), cascades blocked status when a
// dependency fails, and never blocks unrelated branches — a failed
// dep only poisons its downstream nodes.
//
// The graph is kept in memory for a single vairdict run invocation;
// dependency state is not persisted across invocations. The persisted
// Task.DependsOn field is for display (vairdict status) and for the
// single-task --depends-on path which looks up already-completed tasks
// in the store.
package deps

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// NodeStatus is the lifecycle state of a single node in the dependency
// graph, tracked independently from Task.State so the graph can reason
// about dependencies without coupling to the full task state machine.
type NodeStatus int

const (
	// StatusPending means not yet scheduled and not ready (deps incomplete
	// or status not yet decided).
	StatusPending NodeStatus = iota
	// StatusRunning means the node has been handed to the runner.
	StatusRunning
	// StatusDone means the node completed successfully. Dependents become
	// eligible to run.
	StatusDone
	// StatusFailed means the node's run returned an error. Dependents
	// cascade to StatusBlocked.
	StatusFailed
	// StatusBlocked means an upstream dependency failed. The node will
	// never run in this invocation.
	StatusBlocked
)

// Priority controls the order in which ready nodes are dispatched. Higher
// values go first. Defaults to PriorityNormal when unset.
type Priority int

const (
	PriorityLow    Priority = 10
	PriorityNormal Priority = 50
	PriorityHigh   Priority = 100
)

// ParsePriority maps user-facing strings (what the CLI flag and YAML
// manifest accept) to the internal Priority values. An empty string maps
// to PriorityNormal so "no priority specified" is always well-defined.
func ParsePriority(s string) (Priority, error) {
	switch s {
	case "", "normal":
		return PriorityNormal, nil
	case "high":
		return PriorityHigh, nil
	case "low":
		return PriorityLow, nil
	default:
		return 0, fmt.Errorf("unknown priority %q (want high|normal|low)", s)
	}
}

// String returns the canonical lowercase label for a Priority.
func (p Priority) String() string {
	switch {
	case p >= PriorityHigh:
		return "high"
	case p <= PriorityLow:
		return "low"
	default:
		return "normal"
	}
}

func (s NodeStatus) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusRunning:
		return "running"
	case StatusDone:
		return "done"
	case StatusFailed:
		return "failed"
	case StatusBlocked:
		return "blocked"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// ErrCycle is returned by Validate when the graph contains a circular
// dependency. The error message includes the participating node IDs.
var ErrCycle = errors.New("dependency cycle detected")

// ErrUnknownDep is returned when a node's DependsOn list references an
// ID that was never added to the graph.
var ErrUnknownDep = errors.New("unknown dependency")

// Graph is a DAG of task nodes keyed by ID. It is safe for concurrent
// use: the runner goroutines call MarkRunning / MarkDone / MarkFailed
// from different goroutines while the scheduler loop polls Ready.
type Graph struct {
	mu    sync.Mutex
	nodes map[string]*node
}

type node struct {
	id       string
	deps     []string // upstream: this node depends on these
	children []string // downstream: these nodes depend on this
	status   NodeStatus
	priority Priority
	// seq is the zero-based insertion order. Used as a stable tiebreak
	// so FIFO holds within a priority bucket and starvation is bounded
	// (an older low-priority node eventually gets picked over a newer
	// equal-priority one).
	seq int
}

// New creates an empty Graph.
func New() *Graph {
	return &Graph{nodes: make(map[string]*node)}
}

// Add registers a node with PriorityNormal. Dependencies must reference
// IDs that are also added (order does not matter; Validate catches
// missing deps). Calling Add with an ID that already exists returns an
// error to prevent silent overwrites — the graph is built once per
// invocation.
func (g *Graph) Add(id string, depsOn []string) error {
	return g.AddWithPriority(id, depsOn, PriorityNormal)
}

// AddWithPriority registers a node with a caller-supplied priority. The
// scheduler dispatches higher priorities first when the Ready set has
// more entries than available concurrency slots. Insertion order is
// preserved within a priority bucket to prevent starvation of older
// low-priority tasks.
func (g *Graph) AddWithPriority(id string, depsOn []string, priority Priority) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, exists := g.nodes[id]; exists {
		return fmt.Errorf("node %q already in graph", id)
	}
	if id == "" {
		return errors.New("node ID cannot be empty")
	}

	g.nodes[id] = &node{
		id:       id,
		deps:     append([]string(nil), depsOn...),
		status:   StatusPending,
		priority: priority,
		seq:      len(g.nodes),
	}
	return nil
}

// Validate resolves child edges and checks that every dependency exists
// and that the graph is acyclic. Must be called once after all nodes
// are added and before scheduling begins.
func (g *Graph) Validate() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	for _, n := range g.nodes {
		for _, depID := range n.deps {
			dep, ok := g.nodes[depID]
			if !ok {
				return fmt.Errorf("%w: %q depends on %q", ErrUnknownDep, n.id, depID)
			}
			dep.children = appendUnique(dep.children, n.id)
		}
	}

	if cycle := findCycle(g.nodes); cycle != nil {
		return fmt.Errorf("%w: %v", ErrCycle, cycle)
	}
	return nil
}

// Ready returns all nodes currently eligible to run: StatusPending with
// every dep in StatusDone. The list is sorted primarily by Priority
// DESC so high-priority nodes are dispatched first, with insertion
// order as a stable tiebreaker — older entries in the same bucket win,
// which bounds starvation to "finite concurrent high-priority burst"
// rather than "unbounded if new high-priority keeps arriving".
func (g *Graph) Ready() []string {
	g.mu.Lock()
	defer g.mu.Unlock()

	var ready []*node
	for _, n := range g.nodes {
		if n.status != StatusPending {
			continue
		}
		allDepsDone := true
		for _, depID := range n.deps {
			if g.nodes[depID].status != StatusDone {
				allDepsDone = false
				break
			}
		}
		if allDepsDone {
			ready = append(ready, n)
		}
	}
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].priority != ready[j].priority {
			return ready[i].priority > ready[j].priority
		}
		return ready[i].seq < ready[j].seq
	})
	out := make([]string, len(ready))
	for i, n := range ready {
		out[i] = n.id
	}
	return out
}

// MarkRunning flips a node from pending to running. The scheduler calls
// this when it hands a ready node to a goroutine, which prevents the
// next Ready() poll from returning the same node twice.
func (g *Graph) MarkRunning(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	n, ok := g.nodes[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownDep, id)
	}
	if n.status != StatusPending {
		return fmt.Errorf("node %q: cannot mark running from %s", id, n.status)
	}
	n.status = StatusRunning
	return nil
}

// MarkDone transitions a running node to done, unblocking downstream
// nodes on the next Ready() call.
func (g *Graph) MarkDone(id string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	n, ok := g.nodes[id]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownDep, id)
	}
	n.status = StatusDone
	return nil
}

// MarkFailed marks a running node as failed and cascades StatusBlocked
// through every transitive child. Returns the list of node IDs that
// were newly blocked so the caller can log or surface them.
func (g *Graph) MarkFailed(id string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n, ok := g.nodes[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownDep, id)
	}
	n.status = StatusFailed

	var blocked []string
	var walk func(childID string)
	walk = func(childID string) {
		c, ok := g.nodes[childID]
		if !ok {
			return
		}
		if c.status != StatusPending && c.status != StatusRunning {
			return
		}
		c.status = StatusBlocked
		blocked = append(blocked, childID)
		for _, grand := range c.children {
			walk(grand)
		}
	}
	for _, child := range n.children {
		walk(child)
	}
	sort.Strings(blocked)
	return blocked, nil
}

// Status returns the current status of a node.
func (g *Graph) Status(id string) (NodeStatus, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n, ok := g.nodes[id]
	if !ok {
		return 0, fmt.Errorf("%w: %q", ErrUnknownDep, id)
	}
	return n.status, nil
}

// AllSettled reports whether every node has reached a terminal status
// (done, failed, or blocked). The scheduler loop uses this as its exit
// condition.
func (g *Graph) AllSettled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, n := range g.nodes {
		if n.status == StatusPending || n.status == StatusRunning {
			return false
		}
	}
	return true
}

// Snapshot returns a copy of the graph state suitable for rendering in
// vairdict status. Returned as a slice sorted by ID.
func (g *Graph) Snapshot() []NodeView {
	g.mu.Lock()
	defer g.mu.Unlock()

	out := make([]NodeView, 0, len(g.nodes))
	for _, n := range g.nodes {
		out = append(out, NodeView{
			ID:        n.id,
			DependsOn: append([]string(nil), n.deps...),
			Status:    n.status,
			Priority:  n.priority,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// NodeView is a read-only projection of a graph node for display.
type NodeView struct {
	ID        string
	DependsOn []string
	Status    NodeStatus
	Priority  Priority
}

// findCycle walks the graph with DFS and returns the IDs of a cycle if
// one exists, or nil. Not thread-safe; called under g.mu.
func findCycle(nodes map[string]*node) []string {
	// 0=unvisited, 1=on stack, 2=fully explored.
	color := make(map[string]int, len(nodes))
	var stack []string

	var visit func(id string) []string
	visit = func(id string) []string {
		color[id] = 1
		stack = append(stack, id)
		for _, depID := range nodes[id].deps {
			switch color[depID] {
			case 0:
				if c := visit(depID); c != nil {
					return c
				}
			case 1:
				// Found a back-edge: extract the cycle slice.
				start := 0
				for i, s := range stack {
					if s == depID {
						start = i
						break
					}
				}
				cycle := append([]string(nil), stack[start:]...)
				cycle = append(cycle, depID) // close the loop
				return cycle
			}
		}
		color[id] = 2
		stack = stack[:len(stack)-1]
		return nil
	}

	// Iterate in sorted order for deterministic error messages.
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if color[id] == 0 {
			if cycle := visit(id); cycle != nil {
				return cycle
			}
		}
	}
	return nil
}

func appendUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

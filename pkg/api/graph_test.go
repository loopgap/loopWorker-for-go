package api

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"loopworker/internal/core/scheduler"
)

// TestB4SelfDependencyIsRefusedAtTheHTTPBoundary is the SPEC 10-B4 acceptance
// test: posting a self-loop to the HTTP layer must produce a 4xx with a code and
// leave the process alive (the old scheduler recursed in getDepth until the
// stack died).
func TestB4SelfDependencyIsRefusedAtTheHTTPBoundary(t *testing.T) {
	env := newTestEnv(t)

	id := env.createTask(roleOperator, "loop", "")
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+id+"/dependencies",
		`{"dependency_id":"`+id+`"}`)

	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("a self-dependency must be a 4xx, got %d: %s", w.Code, w.Body.String())
	}
	env.expectCode(w, http.StatusConflict, CodeDependencyCycle)

	// The process must still be answering.
	env.expectOK(env.call(roleOperator, http.MethodGet, "/api/v1/tasks/"+id, ""), http.StatusOK)
}

// TestB4TwoNodeCycleIsRefused closes the loop in the other direction.
func TestB4TwoNodeCycleIsRefused(t *testing.T) {
	env := newTestEnv(t)

	a := env.createTask(roleOperator, "node-a", "")
	b := env.createTask(roleOperator, "node-b", "")
	env.expectOK(env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+b+"/dependencies",
		`{"dependency_id":"`+a+`"}`), http.StatusOK)

	// b already depends on a, so a depending on b closes the cycle.
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+a+"/dependencies",
		`{"dependency_id":"`+b+`"}`)
	env.expectCode(w, http.StatusConflict, CodeDependencyCycle)

	envW := decodeEnvelope(t, w)
	if !strings.Contains(envW.Error.Message, "Fix:") {
		t.Errorf("a cycle rejection must carry a remedy: %q", envW.Error.Message)
	}

	// Still serving.
	env.expectOK(env.call(roleOperator, http.MethodGet, "/api/v1/workflow/graph", ""), http.StatusOK)
}

// TestB4LongCycleIsRefused covers a chain long enough that a recursive
// implementation would be doing real work before it found the cycle.
func TestB4LongCycleIsRefused(t *testing.T) {
	env := newTestEnv(t)

	const chain = 40
	ids := make([]string, 0, chain)
	for i := 0; i < chain; i++ {
		ids = append(ids, env.createTask(roleOperator, "chain", ""))
	}
	// Build a chain ids[0] -> ids[1] -> ... in dependency order.
	for i := 0; i < chain-1; i++ {
		w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+ids[i+1]+"/dependencies",
			`{"dependency_id":"`+ids[i]+`"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("building the chain at %d: want 200, got %d: %s", i, w.Code, w.Body.String())
		}
	}
	// Closing the loop must be refused.
	w := env.call(roleOperator, http.MethodPost, "/api/v1/tasks/"+ids[0]+"/dependencies",
		`{"dependency_id":"`+ids[chain-1]+`"}`)
	env.expectCode(w, http.StatusConflict, CodeDependencyCycle)
}

// TestB4CyclicGraphInStorageStillRenders proves the graph endpoint tolerates a
// cycle that predates this validation (or came from another writer): it must
// report the cycle instead of recursing until the stack dies.
func TestB4CyclicGraphInStorageStillRenders(t *testing.T) {
	env := newTestEnv(t)

	a := env.createTask(roleOperator, "legacy-a", "")
	b := env.createTask(roleOperator, "legacy-b", "")

	// Write a cycle straight into storage, bypassing the HTTP validation.
	for _, pair := range [][2]string{{a, b}, {b, a}} {
		task, found := env.sched.GetTask(pair[0])
		if !found {
			t.Fatalf("fixture %s vanished", pair[0])
		}
		task.Dependencies = append(task.Dependencies, pair[1])
		task.DependsOn[pair[1]] = true
		if err := env.sched.SaveTask(task); err != nil {
			t.Fatalf("seed cycle: %v", err)
		}
	}

	w := env.call(roleOperator, http.MethodGet, "/api/v1/workflow/graph", "")
	env.expectOK(w, http.StatusOK)

	data := env.data(w)
	meta, _ := data["meta"].(map[string]any)
	if meta == nil {
		t.Fatalf("graph response carries no meta: %s", w.Body.String())
	}
	cycles, _ := meta["cyclic_nodes"].([]any)
	if len(cycles) == 0 {
		t.Errorf("a stored cycle must be reported, not hidden: %s", w.Body.String())
	}
}

// TestGraphSnapshotIsIterative exercises the traversal directly: no input may
// exhaust the stack. The dependency depth here is far beyond what a recursive
// implementation survives comfortably.
func TestGraphSnapshotIsIterative(t *testing.T) {
	const nodes = 20000
	snapshot := newGraphSnapshot(nodes)
	for i := 0; i < nodes; i++ {
		task := &scheduler.Task{ID: idFor(i), Type: "chain"}
		if i > 0 {
			task.Dependencies = []string{idFor(i - 1)}
		}
		if !snapshot.add(task) {
			t.Fatalf("node budget exhausted at %d", i)
		}
	}

	depths, cyclic := snapshot.depths()
	if len(cyclic) != 0 {
		t.Fatalf("an acyclic chain must report no cycle, got %v", cyclic)
	}
	if got := depths[idFor(nodes-1)]; got != nodes-1 {
		t.Errorf("deepest node depth: want %d, got %d", nodes-1, got)
	}

	// Reachability across the same chain must terminate too.
	if _, found := snapshot.reachableFrom(idFor(nodes-1), idFor(0)); !found {
		t.Error("the first node should be reachable from the last")
	}
	if _, found := snapshot.reachableFrom(idFor(0), idFor(nodes-1)); found {
		t.Error("reachability must follow the dependency direction")
	}
}

// TestGraphSnapshotSurvivesSelfAndCycles covers the hostile shapes directly.
func TestGraphSnapshotSurvivesSelfAndCycles(t *testing.T) {
	snapshot := newGraphSnapshot(10)
	// A self edge, which old rows may still carry.
	snapshot.add(&scheduler.Task{ID: "self", Type: "t", Dependencies: []string{"self"}})
	// A tight two-node cycle.
	snapshot.add(&scheduler.Task{ID: "x", Type: "t", Dependencies: []string{"y"}})
	snapshot.add(&scheduler.Task{ID: "y", Type: "t", Dependencies: []string{"x"}})
	// An edge pointing at a task that is not in the snapshot.
	snapshot.add(&scheduler.Task{ID: "dangling", Type: "t", Dependencies: []string{"ghost"}})

	depths, cyclic := snapshot.depths()
	if depths["self"] != 0 {
		t.Errorf("a self edge must not create depth, got %d", depths["self"])
	}
	if len(cyclic) != 2 {
		t.Errorf("the two-node cycle should be reported, got %v", cyclic)
	}

	// A snapshot self-reachability report must terminate.
	if _, found := snapshot.reachableFrom("self", "self"); !found {
		t.Error("a node is trivially reachable from itself")
	}
}

// TestGraphSnapshotRespectsItsNodeBudget proves a huge deployment degrades
// visibly (meta.truncated) instead of loading everything.
func TestGraphSnapshotRespectsItsNodeBudget(t *testing.T) {
	snapshot := newGraphSnapshot(5)
	for i := 0; i < 50; i++ {
		snapshot.add(&scheduler.Task{ID: idFor(i), Type: "t"})
	}
	if !snapshot.truncated {
		t.Error("the snapshot should report that it stopped early")
	}
	if len(snapshot.nodes) != 5 {
		t.Errorf("want at most 5 nodes, got %d", len(snapshot.nodes))
	}
}

func idFor(i int) string { return "task-" + strconv.Itoa(i) }

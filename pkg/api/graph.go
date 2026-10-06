package api

import (
	"sort"

	"loopworker/internal/core/scheduler"
)

// graphSnapshot is an owner-filtered, size-bounded adjacency view of the task
// dependency graph. All traversals on it are iterative: no input can make the
// process run out of stack.
type graphSnapshot struct {
	nodes []string
	deps  map[string][]string
	// reverse index: dependency -> nodes that depend on it
	dependents map[string][]string
	byID       map[string]*scheduler.Task
	maxNode    int
	// truncated reports that the snapshot stopped at maxNode.
	truncated  bool
	considered int
}

func newGraphSnapshot(maxNodes int) *graphSnapshot {
	if maxNodes <= 0 {
		maxNodes = DefaultGraphMaxNodes
	}
	return &graphSnapshot{
		deps:       make(map[string][]string),
		dependents: make(map[string][]string),
		byID:       make(map[string]*scheduler.Task),
		maxNode:    maxNodes,
	}
}

// add indexes one task; it reports false once the node budget is exhausted.
func (g *graphSnapshot) add(task *scheduler.Task) bool {
	if task == nil {
		return false
	}
	if _, seen := g.byID[task.ID]; seen {
		return true
	}
	if len(g.nodes) >= g.maxNode {
		g.truncated = true
		return false
	}
	g.byID[task.ID] = task
	edges := make([]string, 0, len(task.Dependencies))
	for _, dep := range task.Dependencies {
		if dep == "" || dep == task.ID {
			// Self edges are rejected at the boundary but old rows may hold one.
			continue
		}
		edges = append(edges, dep)
	}
	g.deps[task.ID] = edges
	for _, dep := range edges {
		g.dependents[dep] = append(g.dependents[dep], task.ID)
	}
	g.nodes = append(g.nodes, task.ID)
	g.considered++
	return true
}

// reachableFrom walks the dependency chain iteratively and reports whether
// target is reachable from start. The visited set makes cycles harmless.
func (g *graphSnapshot) reachableFrom(start, target string) (path []string, found bool) {
	if start == target {
		return []string{start, target}, true
	}
	if _, ok := g.byID[start]; !ok {
		return nil, false
	}

	type frame struct {
		id   string
		from string
	}
	visited := map[string]bool{start: true}
	queue := []frame{{id: start}}
	budget := g.maxNode + 1

	for len(queue) > 0 && budget > 0 {
		budget--
		current := queue[0]
		queue = queue[1:]

		for _, dep := range g.deps[current.id] {
			if visited[dep] {
				continue
			}
			visited[dep] = true
			if dep == target {
				return reconstructPath(visited, g.deps, start, target), true
			}
			if _, ok := g.byID[dep]; ok {
				queue = append(queue, frame{id: dep, from: current.id})
			}
		}
	}
	return nil, false
}

// reconstructPath returns a short human-readable cycle hint when available.
func reconstructPath(visited map[string]bool, deps map[string][]string, start, target string) []string {
	// Breadth-first parents are not tracked to keep the snapshot cheap; the
	// visited set is enough to prove the cycle, so report the endpoints only.
	return []string{start, target}
}

// depths computes the longest-path depth of every node with Kahn's algorithm.
// Nodes left over after the peel are members of a cycle; depths never recurses.
func (g *graphSnapshot) depths() (depths map[string]int, cyclic []string) {
	depths = make(map[string]int, len(g.nodes))
	indeg := make(map[string]int, len(g.nodes))

	// Only edges between indexed nodes can participate in a cycle inside this
	// snapshot; edges to unknown ids are ignored by the peel.
	for _, id := range g.nodes {
		for _, dep := range g.deps[id] {
			if _, ok := g.byID[dep]; ok {
				indeg[id]++
			}
		}
	}

	queue := make([]string, 0, len(g.nodes))
	for _, id := range g.nodes {
		if indeg[id] == 0 {
			depths[id] = 0
			queue = append(queue, id)
		}
	}

	peeled := make(map[string]bool, len(g.nodes))
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		peeled[id] = true

		for _, dependent := range g.dependentsOf(id) {
			if candidate := depths[id] + 1; candidate > depths[dependent] {
				depths[dependent] = candidate
			}
			indeg[dependent]--
			if indeg[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	if len(peeled) < len(g.nodes) {
		for _, id := range g.nodes {
			if !peeled[id] {
				cyclic = append(cyclic, id)
			}
		}
		sort.Strings(cyclic)
	}
	return depths, cyclic
}

// dependentsOf lists nodes that directly depend on id (reverse index, computed
// on demand because snapshots are read once per request).
func (g *graphSnapshot) dependentsOf(id string) []string {
	return g.dependents[id]
}

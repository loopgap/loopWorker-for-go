package api

import (
	"net/http"
	"sort"

	"loopworker/internal/core/scheduler"
	"loopworker/pkg/security"
)

// graphNode is one vertex of the canvas payload.
type graphNode struct {
	ID       string        `json:"id"`
	Type     string        `json:"type"`
	Position graphPosition `json:"position"`
	Data     graphNodeData `json:"data"`
}

type graphPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type graphNodeData struct {
	Label string    `json:"label"`
	Task  *TaskView `json:"task"`
	Depth int       `json:"depth"`
	Cycle bool      `json:"in_cycle"`
}

// graphEdge connects a dependency to the task waiting for it.
type graphEdge struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Animated bool   `json:"animated"`
}

// graphMeta tells the client when the view was cut short, so a big deployment
// degrades visibly instead of looking empty.
type graphMeta struct {
	Nodes       int      `json:"nodes"`
	Edges       int      `json:"edges"`
	MaxNodes    int      `json:"max_nodes"`
	Truncated   bool     `json:"truncated"`
	Cycles      []string `json:"cyclic_nodes,omitempty"`
	Scope       string   `json:"scope"`
	GeneratedAt string   `json:"generated_at"`
}

// graphSnapshot builds the owner-filtered adjacency used by both the graph route
// and dependency validation.
func (s *APIServer) graphSnapshot(r *http.Request) *graphSnapshot {
	snapshot := newGraphSnapshot(s.cfg.GraphMaxNodes)
	if s.lister == nil {
		return snapshot
	}

	principal, _ := security.PrincipalFromContext(r.Context())
	includeAll := principal != nil && principal.HasPermission(security.PermAdmin)

	for _, task := range s.lister.ListTasks(scheduler.TaskFilter{Limit: s.cfg.GraphMaxNodes}) {
		if task == nil {
			continue
		}
		if !includeAll && !canSee(principal, ownerOf(task)) {
			continue
		}
		if !snapshot.add(task) {
			break
		}
	}
	return snapshot
}

// getWorkflowGraph answers GET /api/v1/workflow/graph. Depth is computed with an
// iterative Kahn peel over a visited set, so cycles and long chains can no
// longer exhaust the stack and take the process down.
func (s *APIServer) getWorkflowGraph(w http.ResponseWriter, r *http.Request) {
	snapshot := s.graphSnapshot(r)
	depths, cyclic := snapshot.depths()

	cycleSet := make(map[string]bool, len(cyclic))
	for _, id := range cyclic {
		cycleSet[id] = true
	}

	columns := make(map[int][]string)
	maxDepth := 0
	for _, id := range snapshot.nodes {
		depth := depths[id]
		columns[depth] = append(columns[depth], id)
		if depth > maxDepth {
			maxDepth = depth
		}
	}
	for depth := range columns {
		sort.Strings(columns[depth])
	}

	nodes := make([]graphNode, 0, len(snapshot.nodes))
	edges := make([]graphEdge, 0, snapshot.considered)
	for depth := 0; depth <= maxDepth; depth++ {
		ids := columns[depth]
		columnX := float64(100 + depth*300)
		for index, id := range ids {
			task := snapshot.byID[id]
			if task == nil {
				continue
			}
			rowY := float64(100 + index*180)
			if len(ids) > 1 {
				rowY = float64(100 + index*(500/len(ids)))
			}
			nodeType := "wasmNode"
			if task.IsAgent {
				nodeType = "agentNode"
			}
			nodes = append(nodes, graphNode{
				ID:       task.ID,
				Type:     nodeType,
				Position: graphPosition{X: columnX, Y: rowY},
				Data: graphNodeData{
					Label: task.Type,
					Task:  newTaskView(task),
					Depth: depth,
					Cycle: cycleSet[task.ID],
				},
			})
			for _, depID := range snapshot.deps[task.ID] {
				animated := task.State == scheduler.StateRunning || task.State == scheduler.StateQueued
				edges = append(edges, graphEdge{
					ID:       "e-" + depID + "-" + task.ID,
					Source:   depID,
					Target:   task.ID,
					Animated: animated,
				})
			}
		}
	}

	principal, _ := security.PrincipalFromContext(r.Context())
	scope := "own_tasks"
	if principal != nil && principal.HasPermission(security.PermAdmin) {
		scope = "all"
	}

	sendSuccess(w, r, map[string]any{
		"nodes": nodes,
		"edges": edges,
		"meta": graphMeta{
			Nodes:       len(nodes),
			Edges:       len(edges),
			MaxNodes:    s.cfg.GraphMaxNodes,
			Truncated:   snapshot.truncated,
			Cycles:      cyclic,
			Scope:       scope,
			GeneratedAt: nowRFC3339(),
		},
	}, http.StatusOK)
}

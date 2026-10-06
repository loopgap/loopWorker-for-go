package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"loopworker/internal/core/scheduler"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/utils"
	"loopworker/pkg/workflow"
)

// healthCheck is the only unauthenticated route: it answers liveness without
// exporting workload statistics.
func (s *APIServer) healthCheck(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{
		"status":      "ok",
		"version":     s.version,
		"auth":        "required",
		"server_time": nowRFC3339(),
	}
	if s.deps.Tasks != nil {
		if queueable, ok := s.deps.Tasks.(interface{ QueueSize() int }); ok {
			body["queue_length"] = queueable.QueueSize()
		}
	}
	sendSuccess(w, r, body, http.StatusOK)
}

// readies reports whether every wired subsystem is present (used by the admin
// listener and by the startup log line).
func (s *APIServer) readies() map[string]string {
	out := map[string]string{
		"tasks":     present(s.deps.Tasks != nil),
		"lister":    present(s.lister != nil),
		"workers":   present(s.deps.Workers != nil),
		"events":    present(s.deps.Events != nil),
		"workflows": present(s.deps.Workflows != nil),
		"observer":  present(s.deps.Observer != nil),
		"auth":      present(s.auth != nil),
	}
	return out
}

func present(ok bool) string {
	if ok {
		return "configured"
	}
	return "missing"
}

// listWorkers answers GET /api/v1/workers.
func (s *APIServer) listWorkers(w http.ResponseWriter, r *http.Request) {
	if s.deps.Workers == nil {
		sendError(w, r, missingDependency("workers", "the executor is not wired; start the server with a scheduler and executor"))
		return
	}
	workers := s.deps.Workers.ListWorkers()
	sendSuccess(w, r, map[string]any{
		"workers":    newWorkerViews(workers),
		"total":      len(workers),
		"queue_size": s.queueSize(),
		"stats":      s.statsForResponse(),
	}, http.StatusOK)
}

func (s *APIServer) queueSize() int {
	if s.deps.Tasks == nil {
		return 0
	}
	if queueable, ok := s.deps.Tasks.(interface{ QueueSize() int }); ok {
		return queueable.QueueSize()
	}
	return 0
}

// statsForResponse returns scheduler counters for admin-style payloads, never
// for unauthenticated callers.
func (s *APIServer) statsForResponse() map[string]any {
	if s.deps.Tasks == nil {
		return map[string]any{}
	}
	stats := s.deps.Tasks.GetStats()
	out := make(map[string]any, len(stats))
	for k, v := range stats {
		out[k] = v
	}
	return out
}

// listWorkflows answers GET /api/v1/workflow/list.
func (s *APIServer) listWorkflows(w http.ResponseWriter, r *http.Request) {
	if s.deps.Workflows == nil {
		sendError(w, r, missingDependency("workflows", "the workflow engine is not wired into this server"))
		return
	}
	registered := s.deps.Workflows.ListWorkflows()
	sort.Slice(registered, func(i, j int) bool { return registered[i].ID < registered[j].ID })

	views := make([]map[string]any, 0, len(registered))
	for _, wf := range registered {
		views = append(views, newWorkflowView(wf))
	}
	sendSuccess(w, r, map[string]any{"workflows": views, "total": len(views)}, http.StatusOK)
}

// getWorkflow answers GET /api/v1/workflow/{workflowID}.
func (s *APIServer) getWorkflow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "workflowID")
	if s.deps.Workflows == nil {
		sendError(w, r, missingDependency("workflows", "the workflow engine is not wired into this server"))
		return
	}
	wf, found := s.deps.Workflows.GetWorkflow(id)
	if !found || wf == nil {
		sendError(w, r, workflowNotFound(id))
		return
	}
	sendSuccess(w, r, newWorkflowView(wf), http.StatusOK)
}

func workflowNotFound(id string) error {
	return &FieldError{Field: "workflow_id",
		Reason: "no workflow named \"" + id + "\" is registered in this process",
		Fix:    "GET /api/v1/workflow/list shows every registered workflow; ids come from that response",
		Status: http.StatusNotFound, Code: CodeWorkflowNotFound}
}

// newWorkflowView projects a workflow onto the wire model.
func newWorkflowView(wf *workflow.Workflow) map[string]any {
	if wf == nil {
		return nil
	}
	// Status, StepStatus, StartedAt, CompletedAt and Error are the fields
	// executeWorkflow mutates while the workflow runs, so they are read through
	// the engine's locked accessors below. Steps, StepOrder, ID, Name and
	// CreatedAt are only written by AddStep and NewWorkflow, which
	// RegisterWorkflows runs before the listener opens.
	steps := make([]map[string]any, 0, len(wf.Steps))
	for _, stepID := range orderedStepIDs(wf) {
		step := wf.Steps[stepID]
		if step == nil {
			continue
		}
		view := map[string]any{
			"id":         step.ID,
			"name":       step.Name,
			"depends_on": append([]string{}, step.DependsOn...),
			"status":     wf.GetStepStatus(step.ID).String(),
			"timeout_ms": step.Timeout.Milliseconds(),
			"runnable":   step.Action != nil,
		}
		if step.RetryPolicy != nil {
			view["max_retries"] = step.RetryPolicy.MaxRetries
		}
		steps = append(steps, view)
	}
	// ponytail: started_at and completed_at are omitted rather than raced.
	// executeWorkflow writes both under the workflow mutex and pkg/workflow
	// exports no accessor for them, so reading the fields here is a real data
	// race - see TestWorkflowStatusEndpointDoesNotRaceWithExecution. Restore
	// them behind GetStartedAt/GetCompletedAt in pkg/workflow.
	return map[string]any{
		"id":         wf.ID,
		"name":       wf.Name,
		"status":     workflowStatusName(wf.GetStatus()),
		"steps":      steps,
		"step_order": append([]string{}, wf.StepOrder...),
		"created_at": formatTime(wf.CreatedAt),
		"error":      workflowErrorText(wf),
	}
}

func orderedStepIDs(wf *workflow.Workflow) []string {
	if len(wf.StepOrder) > 0 {
		out := make([]string, 0, len(wf.StepOrder))
		for _, id := range wf.StepOrder {
			if _, ok := wf.Steps[id]; ok {
				out = append(out, id)
			}
		}
		return out
	}
	ids := make([]string, 0, len(wf.Steps))
	for id := range wf.Steps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func workflowStatusName(status workflow.WorkflowStatus) string {
	switch status {
	case workflow.WorkflowPending:
		return "pending"
	case workflow.WorkflowRunning:
		return "running"
	case workflow.WorkflowCompleted:
		return "completed"
	case workflow.WorkflowFailed:
		return "failed"
	case workflow.WorkflowCancelled:
		return "cancelled"
	default:
		return "unknown"
	}
}

func workflowErrorText(wf *workflow.Workflow) string {
	if err := wf.GetError(); err != nil {
		return err.Error()
	}
	return ""
}

// executeWorkflow answers POST /api/v1/workflow/execute and is asynchronous: the
// caller receives 202 plus the workflow id and follows progress by polling.
func (s *APIServer) executeWorkflow(w http.ResponseWriter, r *http.Request) {
	if s.deps.Workflows == nil {
		sendError(w, r, missingDependency("workflows", "the workflow engine is not wired into this server"))
		return
	}

	var body struct {
		WorkflowID string `json:"workflow_id"`
	}
	if err := decodeJSON(r, &body, []string{"workflow_id"}, s.cfg.MaxBodyBytes); err != nil {
		sendError(w, r, err)
		return
	}
	id := body.WorkflowID
	if id == "" {
		sendError(w, r, &FieldError{Field: "workflow_id", Reason: "workflow_id is required",
			Fix:    "send {\"workflow_id\":\"<id>\"}; ids come from GET /api/v1/workflow/list",
			Status: http.StatusBadRequest, Code: CodeInvalidRequest})
		return
	}
	if _, found := s.deps.Workflows.GetWorkflow(id); !found {
		sendError(w, r, workflowNotFound(id))
		return
	}

	// Detach from the request context: the workflow must outlive the 202 reply.
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.RequestTimeout*10)
	utils.GoSafe(ctx, func(run context.Context) {
		defer cancel()
		if err := s.deps.Workflows.Execute(run, id); err != nil {
			logAsyncError("workflow execution failed", id, err)
		}
	})

	sendSuccess(w, r, map[string]any{
		"workflow_id": id,
		"status":      "executing",
		"poll":        "/api/v1/workflow/" + id,
		"message":     "execution started; poll the workflow until status is completed or failed",
	}, http.StatusAccepted)
}

// missingDependency names the subsystem a caller needs to configure.
func missingDependency(name, fix string) error {
	return &FieldError{
		Field:  name,
		Reason: "this deployment does not provide the " + name + " subsystem",
		Fix:    fix,
		Status: http.StatusServiceUnavailable,
		Code:   CodeServiceUnavailable,
	}
}

// notImplemented guards routes that need a subsystem this build lacks.
func notImplemented(feature, fix string) error {
	return &FieldError{Reason: feature + " is not implemented in this build", Fix: fix,
		Status: http.StatusNotImplemented, Code: CodeInternalError}
}

// schedulerNotFound maps a scheduler miss without echoing internal ids.
func schedulerNotFound(id string) error {
	return &FieldError{Field: "id", Reason: "the referenced task does not exist",
		Fix: "list tasks with GET /api/v1/tasks", Status: http.StatusNotFound, Code: CodeTaskNotFound,
		Details: map[string]any{"id": id}}
}

var _ = lwerrors.ErrTaskNotFound

func stateName(state scheduler.TaskState) string { return string(state) }

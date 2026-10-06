package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"loopworker/internal/core/scheduler"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/security"
)

// authHint is the standard remedy text for a missing caller.
const authHint = "send \"Authorization: Bearer <token>\" or the API-key header; POST /api/v1/auth/token exchanges an API key for a token"

// createTask answers POST /api/v1/tasks.
func (s *APIServer) createTask(w http.ResponseWriter, r *http.Request) {
	var req CreateTaskRequest
	if err := decodeJSON(r, &req, CreateTaskFields, s.cfg.MaxBodyBytes); err != nil {
		sendError(w, r, err)
		return
	}
	if err := req.validate(s.cfg); err != nil {
		sendError(w, r, err)
		return
	}
	input, err := req.resolveInput(s.cfg)
	if err != nil {
		sendError(w, r, err)
		return
	}

	principal, ok := security.PrincipalFromContext(r.Context())
	if !ok {
		sendError(w, r, &FieldError{Reason: "no authenticated caller", Fix: authHint,
			Status: http.StatusUnauthorized, Code: CodeUnauthorized})
		return
	}

	config := publicConfig(req.Config)
	if config == nil {
		config = req.Config
	}

	var task *scheduler.Task
	if req.Priority != nil {
		task, err = s.deps.Tasks.CreateTaskWithPriority(r.Context(), strings.TrimSpace(req.Type),
			scheduler.TaskPriority(*req.Priority), config, input)
	} else {
		task, err = s.deps.Tasks.CreateTask(r.Context(), strings.TrimSpace(req.Type), config, input)
	}
	if err != nil {
		sendError(w, r, err)
		return
	}

	if len(req.Metadata) > 0 {
		if task.Metadata == nil {
			task.Metadata = make(map[string]string, len(req.Metadata)+1)
		}
		for key, value := range req.Metadata {
			if key == OwnerMetadataKey {
				continue
			}
			task.Metadata[key] = value
		}
	}
	if req.IsAgent {
		task.IsAgent = true
		task.AgentConfig = req.AgentConfig
	}
	s.claimOwnership(task, principal)

	if req.Priority != nil {
		task.Priority = scheduler.TaskPriority(*req.Priority)
		if err := s.deps.Tasks.SaveTask(task); err != nil {
			logAsyncError("task priority was not persisted", task.ID, err)
		}
	}

	if err := s.deps.Tasks.QueueTask(r.Context(), task.ID); err != nil {
		// The row exists but could not be queued (unmet dependencies are the
		// common case), so report the state rather than a create failure.
		sendError(w, r, queueError(task.ID, err))
		return
	}

	created, _ := s.deps.Tasks.GetTask(task.ID)
	if created == nil {
		created = task
	}
	w.Header().Set("Location", "/api/v1/tasks/"+created.ID)
	sendSuccess(w, r, newTaskView(created), http.StatusCreated)
}

// queueError explains a failed enqueue without leaking internal identifiers.
func queueError(taskID string, err error) error {
	if errors.Is(err, lwerrors.ErrTaskNotFound) || errors.Is(err, lwerrors.ErrTaskInvalid) {
		return err
	}
	return &FieldError{
		Field:  "id",
		Reason: "the task was stored but could not be queued: " + err.Error(),
		Fix:    "the task stays in \"pending\" until every task it depends on reports state \"completed\"; check dependencies with GET /api/v1/tasks/" + taskID,
		Status: http.StatusConflict,
		Code:   CodeTaskStateConflict,
		Cause:  err,
	}
}

// getTask answers GET /api/v1/tasks/{taskID}.
func (s *APIServer) getTask(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "taskID")
	task, found := s.deps.Tasks.GetTask(id)
	if !found || task == nil {
		sendError(w, r, notFoundTask(id))
		return
	}
	if !s.authorizeTask(w, r, task, security.PermRead) {
		return
	}
	sendSuccess(w, r, newTaskView(task), http.StatusOK)
}

// notFoundTask keeps the id out of the message body except as a quoted echo,
// which is safe because the caller supplied it.
func notFoundTask(id string) error {
	return &FieldError{
		Field:  "taskID",
		Reason: "no task with id \"" + id + "\" is visible to this credential",
		Fix:    "list visible tasks with GET /api/v1/tasks; a 404 is also returned for tasks owned by someone else",
		Status: http.StatusNotFound,
		Code:   CodeTaskNotFound,
	}
}

// cancelTask answers POST /api/v1/tasks/{taskID}/cancel and DELETE shares it.
func (s *APIServer) cancelTask(w http.ResponseWriter, r *http.Request) {
	s.cancelOrDelete(w, r, "cancelled")
}

// deleteTask answers DELETE /api/v1/tasks/{taskID}.
func (s *APIServer) deleteTask(w http.ResponseWriter, r *http.Request) {
	s.cancelOrDelete(w, r, "cancelled")
}

func (s *APIServer) cancelOrDelete(w http.ResponseWriter, r *http.Request, result string) {
	id := chi.URLParam(r, "taskID")
	task, found := s.deps.Tasks.GetTask(id)
	if !found || task == nil {
		sendError(w, r, notFoundTask(id))
		return
	}
	if !s.authorizeTask(w, r, task, security.PermWrite) {
		return
	}

	if isTerminal(task.State) {
		sendError(w, r, &FieldError{
			Field:  "state",
			Reason: "task \"" + id + "\" already reached the terminal state \"" + string(task.State) + "\"",
			Fix:    "terminal tasks cannot be cancelled twice; read the stored result with GET /api/v1/tasks/" + id + " or create a replacement task",
			Status: http.StatusConflict,
			Code:   CodeTaskStateConflict,
		})
		return
	}

	if err := s.deps.Tasks.CancelTask(r.Context(), id); err != nil {
		sendError(w, r, err)
		return
	}

	updated, _ := s.deps.Tasks.GetTask(id)
	state := string(scheduler.StateCancelled)
	if updated != nil {
		state = string(updated.State)
	}
	sendSuccess(w, r, map[string]any{
		"id":     id,
		"state":  state,
		"action": result,
	}, http.StatusOK)
}

func isTerminal(state scheduler.TaskState) bool {
	switch state {
	case scheduler.StateCompleted, scheduler.StateFailed, scheduler.StateCancelled, scheduler.StateDeadLetter:
		return true
	default:
		return false
	}
}

// addTaskDependency answers POST /api/v1/tasks/{taskID}/dependencies. It rejects
// self-edges and cycles before the scheduler stores anything, which is what kept
// the graph endpoint from recursing until the process died.
func (s *APIServer) addTaskDependency(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskID")

	var body struct {
		DependencyID string `json:"dependency_id"`
	}
	fields := []string{"dependency_id"}
	if err := decodeJSON(r, &body, fields, s.cfg.MaxBodyBytes); err != nil {
		sendError(w, r, err)
		return
	}
	depID := strings.TrimSpace(body.DependencyID)
	if depID == "" {
		sendError(w, r, &FieldError{Field: "dependency_id",
			Reason: "dependency_id is required",
			Fix:    "send {\"dependency_id\":\"<task id>\"}; ids come from GET /api/v1/tasks",
			Status: http.StatusBadRequest, Code: CodeInvalidRequest})
		return
	}

	task, found := s.deps.Tasks.GetTask(taskID)
	if !found || task == nil {
		sendError(w, r, notFoundTask(taskID))
		return
	}
	if !s.authorizeTask(w, r, task, security.PermWrite) {
		return
	}

	dependency, found := s.deps.Tasks.GetTask(depID)
	if !found || dependency == nil {
		sendError(w, r, &FieldError{Field: "dependency_id",
			Reason: "no task with id \"" + depID + "\" exists on this server",
			Fix:    "create the dependency first, or copy the id from GET /api/v1/tasks",
			Status: http.StatusNotFound, Code: CodeTaskNotFound})
		return
	}
	if !canSee(principal(r), ownerOf(dependency)) {
		// Existence is confirmed, but cross-tenant edges are refused.
		s.writeFailure(w, r, http.StatusForbidden, string(CodeForbidden),
			"Task \""+taskID+"\" may not depend on a task owned by another caller. Fix: only link tasks you own, or ask an admin to create the edge.",
			map[string]any{"dependency_id": depID})
		return
	}

	if taskID == depID {
		sendError(w, r, &FieldError{Field: "dependency_id",
			Reason: "task \"" + taskID + "\" cannot depend on itself",
			Fix:    "a task must wait for a different task; remove the self reference",
			Status: http.StatusConflict, Code: CodeDependencyCycle})
		return
	}
	if isTerminal(dependency.State) && dependency.State != scheduler.StateCompleted {
		sendError(w, r, &FieldError{Field: "dependency_id",
			Reason: "dependency \"" + depID + "\" is in terminal state \"" + string(dependency.State) + "\" and will never complete",
			Fix:    "depend on a task that can reach \"completed\", or recreate the dependency task",
			Status: http.StatusUnprocessableEntity, Code: CodeDependencyInvalid})
		return
	}

	snapshot := s.graphSnapshot(r)
	if reachable, found := snapshot.reachableFrom(depID, taskID); found {
		sendError(w, r, &FieldError{Field: "dependency_id",
			Reason: "adding this edge would close a cycle: \"" + depID + "\" already transitively depends on \"" + taskID + "\"",
			Fix:    "break the loop first - list the chain with GET /api/v1/workflow/graph and remove one edge; cyclic graphs deadlock every task in them",
			Status: http.StatusConflict, Code: CodeDependencyCycle,
			Details: map[string]any{"cycle_endpoints": reachable}})
		return
	}

	if err := s.deps.Tasks.AddDependency(r.Context(), taskID, depID); err != nil {
		sendError(w, r, err)
		return
	}

	updated, _ := s.deps.Tasks.GetTask(taskID)
	deps := []string{}
	if updated != nil {
		deps = append(deps, updated.Dependencies...)
	}
	sendSuccess(w, r, map[string]any{
		"id":           taskID,
		"dependencies": deps,
		"action":       "dependency_added",
	}, http.StatusOK)
}

// principal returns the caller attached to the request.
func principal(r *http.Request) *security.Principal {
	p, _ := security.PrincipalFromContext(r.Context())
	return p
}

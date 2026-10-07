package api

import (
	"net/http"

	"loopworker/internal/core/scheduler"
	"loopworker/pkg/security"
)

// ownerOf reports the principal id that owns a task. Tasks created before
// ownership was enforced (or by other writers) return an empty owner and are
// therefore reachable only by an admin.
func ownerOf(t *scheduler.Task) string {
	if t == nil || t.Metadata == nil {
		return ""
	}
	return t.Metadata[OwnerMetadataKey]
}

// claimOwnership stamps the authenticated caller onto a freshly created task.
func (s *APIServer) claimOwnership(task *scheduler.Task, principal *security.Principal) {
	if task == nil || principal == nil {
		return
	}
	if task.Metadata == nil {
		task.Metadata = make(map[string]string, 1)
	}
	if task.Metadata[OwnerMetadataKey] != "" {
		return
	}
	task.Metadata[OwnerMetadataKey] = principal.Subject
	if err := s.deps.Tasks.SaveTask(task); err != nil {
		logAsyncError("ownership claim was not persisted", task.ID, err)
	}
}

// canSee reports whether a principal may read a task. Admins see everything;
// other roles only see tasks they own.
func canSee(principal *security.Principal, owner string) bool {
	if principal == nil {
		return false
	}
	if principal.HasPermission(security.PermAdmin) {
		return true
	}
	return owner != "" && owner == principal.Subject
}

// authorizeTask writes the rejection response when access is denied and reports
// whether the handler may continue.
func (s *APIServer) authorizeTask(w http.ResponseWriter, r *http.Request, task *scheduler.Task, perm security.Permission) bool {
	principal, _ := security.PrincipalFromContext(r.Context())
	if principal == nil {
		sendError(w, r, &FieldError{
			Reason: "no authenticated caller on this request",
			Fix:    "send a credential; see GET /api/v1/openapi.json securitySchemes",
			Status: http.StatusUnauthorized,
			Code:   CodeUnauthorized,
		})
		return false
	}

	owner := ownerOf(task)
	if !canSee(principal, owner) {
		message := "Task " + task.ID + " belongs to another caller."
		fix := "Only the creating credential (or an admin key) may touch this task. List your own tasks with GET /api/v1/tasks, or ask an administrator to act on it."
		if owner == "" {
			message = "Task " + task.ID + " has no recorded owner (it predates ownership tracking)."
			fix = "An admin key may read or cancel unowned tasks; other roles cannot claim them."
		}
		details := map[string]any{
			"your_role":           principal.Role,
			"your_subject":        principal.Subject,
			"required_permission": string(perm),
		}
		s.writeFailure(w, r, http.StatusForbidden, string(CodeForbidden), message+" Fix: "+fix, details)
		return false
	}

	if !principal.HasPermission(perm) {
		s.writeFailure(w, r, http.StatusForbidden, string(CodeForbidden),
			"Role \""+principal.Role+"\" cannot perform this operation; it requires permission \""+string(perm)+
				"\". Fix: use a key with role \"operator\" or \"admin\", or call a read-only endpoint.",
			map[string]any{"required_permission": string(perm), "your_role": principal.Role})
		return false
	}
	return true
}

// principalSubject returns the authenticated subject or "anonymous".
func principalSubject(r *http.Request) string {
	if principal, ok := security.PrincipalFromContext(r.Context()); ok {
		return principal.Subject
	}
	return "anonymous"
}

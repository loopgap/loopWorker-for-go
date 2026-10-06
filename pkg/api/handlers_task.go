package api

import (
	"net/http"
	"strconv"
	"time"

	"loopworker/internal/core/scheduler"
	"loopworker/pkg/security"
)

// pagination is the resolved, bounded page request.
type pagination struct {
	limit  int
	offset int
	err    *FieldError
}

func (s *APIServer) parsePagination(r *http.Request) pagination {
	query := r.URL.Query()
	page := pagination{limit: s.cfg.DefaultLimit, offset: 0}

	if raw := query.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			page.err = &FieldError{Field: "limit", Reason: "\"" + raw + "\" is not an integer",
				Fix: "use a whole number between 1 and " + itoa(s.cfg.MaxLimit), Status: http.StatusBadRequest, Code: CodeInvalidRequest}
			return page
		}
		if n < 1 {
			n = 1
		}
		if n > s.cfg.MaxLimit {
			page.err = &FieldError{Field: "limit",
				Reason: itoa(n) + " exceeds the maximum page size of " + itoa(s.cfg.MaxLimit),
				Fix:    "request at most " + itoa(s.cfg.MaxLimit) + " rows and page with offset",
				Status: http.StatusBadRequest, Code: CodeInvalidRequest,
				Details: map[string]any{"max_limit": s.cfg.MaxLimit}}
			return page
		}
		page.limit = n
	}

	if raw := query.Get("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			page.err = &FieldError{Field: "offset", Reason: "\"" + raw + "\" is not an integer",
				Fix: "use a whole number >= 0", Status: http.StatusBadRequest, Code: CodeInvalidRequest}
			return page
		}
		if n < 0 {
			n = 0
		}
		if n > s.cfg.MaxOffset {
			page.err = &FieldError{Field: "offset",
				Reason: itoa(n) + " exceeds the maximum offset of " + itoa(s.cfg.MaxOffset),
				Fix:    "filter with state= and type= instead of scanning deep offsets",
				Status: http.StatusBadRequest, Code: CodeInvalidRequest,
				Details: map[string]any{"max_offset": s.cfg.MaxOffset}}
			return page
		}
		page.offset = n
	}
	return page
}

// parseTaskFilter converts the query string into a scheduler filter, rejecting
// unknown states rather than silently returning nothing.
func (s *APIServer) parseTaskFilter(r *http.Request) (scheduler.TaskFilter, error) {
	query := r.URL.Query()
	filter := scheduler.TaskFilter{}

	if raw := query.Get("state"); raw != "" {
		state := scheduler.TaskState(raw)
		if !knownState(state) {
			return filter, &FieldError{Field: "state",
				Reason: "\"" + raw + "\" is not a task state",
				Fix:    "use one of: " + knownStates(),
				Status: http.StatusBadRequest, Code: CodeInvalidRequest,
				Details: map[string]any{"allowed": knownStateList()}}
		}
		filter.States = []scheduler.TaskState{state}
	}
	if raw := query.Get("type"); raw != "" {
		filter.Types = []string{raw}
	}
	if raw := query.Get("priority"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 3 {
			return filter, &FieldError{Field: "priority",
				Reason: "\"" + raw + "\" is not a task priority",
				Fix:    "use 0 (low), 1 (normal), 2 (high) or 3 (critical)",
				Status: http.StatusBadRequest, Code: CodeInvalidRequest}
		}
		priority := scheduler.TaskPriority(n)
		filter.Priority = &priority
	}
	return filter, nil
}

func knownState(state scheduler.TaskState) bool {
	for _, candidate := range knownStateList() {
		if candidate == state {
			return true
		}
	}
	return false
}

func knownStateList() []scheduler.TaskState {
	return []scheduler.TaskState{
		scheduler.StatePending, scheduler.StateQueued, scheduler.StateRunning,
		scheduler.StateCompleted, scheduler.StateFailed, scheduler.StateCancelled,
		scheduler.StateRetrying, scheduler.StateDeadLetter,
	}
}

func knownStates() string {
	states := knownStateList()
	out := make([]string, 0, len(states))
	for _, state := range states {
		out = append(out, string(state))
	}
	return joinStrings(out, ", ")
}

// listTasks answers GET /api/v1/tasks with owner scoping and push-down paging.
func (s *APIServer) listTasks(w http.ResponseWriter, r *http.Request) {
	page := s.parsePagination(r)
	if page.err != nil {
		sendError(w, r, page.err)
		return
	}
	filter, err := s.parseTaskFilter(r)
	if err != nil {
		sendError(w, r, err)
		return
	}
	if s.lister == nil {
		sendError(w, r, &FieldError{Reason: "the task lister is not wired into this server",
			Fix: "start the server with a scheduler (see docs/USAGE.md)", Status: http.StatusServiceUnavailable, Code: CodeServiceUnavailable})
		return
	}

	principal, _ := security.PrincipalFromContext(r.Context())
	admin := principal != nil && principal.HasPermission(security.PermAdmin)

	// Owner scoping: pushed into the store when it supports it, else applied to
	// the page we received (which is why the response carries both page and
	// visible counts).
	if !admin {
		s.listTasksScoped(w, r, principal, filter, page)
		return
	}

	rows, total, hasMore := s.fetchPage(filter, page, "")
	s.writeTaskPage(w, r, rows, total, hasMore, page, false)
}

// listTasksScoped serves non-admin callers.
func (s *APIServer) listTasksScoped(w http.ResponseWriter, r *http.Request, principal *security.Principal, filter scheduler.TaskFilter, page pagination) {
	owner := principal.Subject
	if scoped, ok := s.lister.(TaskOwnershipLister); ok {
		rows, total, err := scoped.ListTasksForOwner(owner, filter, page.offset, page.limit)
		if err == nil {
			s.writeTaskPage(w, r, rows, total, int64(page.offset+len(rows)) < total, page, false)
			return
		}
		sendError(w, r, err)
		return
	}

	rows, total, hasMore := s.fetchPage(filter, page, owner)
	filtered := make([]*scheduler.Task, 0, len(rows))
	for _, task := range rows {
		if taskOwner := ownerOf(task); taskOwner == owner {
			filtered = append(filtered, task)
		}
	}
	s.writeTaskPage(w, r, filtered, total, hasMore, page, !admin(principal))
}

func admin(principal *security.Principal) bool {
	return principal != nil && principal.HasPermission(security.PermAdmin)
}

// fetchPage pulls one page, using push-down pagination when the store offers it.
func (s *APIServer) fetchPage(filter scheduler.TaskFilter, page pagination, owner string) (rows []*scheduler.Task, total int64, hasMore bool) {
	total = -1
	if paged, ok := s.lister.(PagedTaskLister); ok {
		rows, total, err := paged.ListTasksPaged(filter, page.offset, page.limit)
		if err == nil {
			if total == 0 {
				total = int64(len(rows))
			}
			return rows, total, int64(page.offset+len(rows)) < total
		}
	}

	// Degraded path: ask the store for exactly limit+offset rows plus one probe
	// row, so the whole table is never loaded and "has_more" is still knowable.
	requested := page.offset + page.limit + 1
	cloned := filter
	cloned.Limit = requested
	rows = s.lister.ListTasks(cloned)

	if counter, ok := s.lister.(TaskCounter); ok {
		if count, err := counter.CountTasks(filter); err == nil {
			total = count
		}
	}

	if page.offset >= len(rows) {
		return []*scheduler.Task{}, total, false
	}
	end := page.offset + page.limit
	hasMore = end < len(rows)
	if end > len(rows) {
		end = len(rows)
	}
	window := rows[page.offset:end]
	return window, total, hasMore
}

// taskPage is the list response body.
type taskPage struct {
	Tasks       []*TaskView `json:"tasks"`
	Limit       int         `json:"limit"`
	Offset      int         `json:"offset"`
	Total       *int64      `json:"total"`
	TotalKnown  bool        `json:"total_available"`
	HasMore     bool        `json:"has_more"`
	NextOffset  *int        `json:"next_offset"`
	Scope       string      `json:"scope"`
	GeneratedAt string      `json:"generated_at"`
}

func (s *APIServer) writeTaskPage(w http.ResponseWriter, r *http.Request, rows []*scheduler.Task, total int64, hasMore bool, page pagination, scoped bool) {
	body := taskPage{
		Tasks:       newTaskViews(rows),
		Limit:       page.limit,
		Offset:      page.offset,
		HasMore:     hasMore,
		Scope:       "all",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if scoped {
		body.Scope = "own_tasks"
	}
	if total >= 0 {
		known := total
		body.Total = &known
		body.TotalKnown = true
	}
	if hasMore {
		next := page.offset + page.limit
		body.NextOffset = &next
	}
	sendSuccess(w, r, body, http.StatusOK)
}

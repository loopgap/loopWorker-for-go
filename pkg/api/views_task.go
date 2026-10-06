package api

import (
	"time"

	"loopworker/internal/core/executor"
	"loopworker/internal/core/scheduler"
)

// TaskView is the stable wire representation of a task. Field names are
// snake_case and explicit, so Go struct refactors cannot change the contract.
type TaskView struct {
	ID             string                 `json:"id"`
	Type           string                 `json:"type"`
	State          string                 `json:"state"`
	Priority       int                    `json:"priority"`
	PriorityName   string                 `json:"priority_name"`
	Owner          string                 `json:"owner,omitempty"`
	CreatedAt      string                 `json:"created_at"`
	StartedAt      string                 `json:"started_at,omitempty"`
	EndedAt        string                 `json:"ended_at,omitempty"`
	Input          string                 `json:"input,omitempty"`
	InputEncoding  string                 `json:"input_encoding,omitempty"`
	Result         string                 `json:"result,omitempty"`
	ResultEncoding string                 `json:"result_encoding,omitempty"`
	Error          string                 `json:"error,omitempty"`
	Retry          int                    `json:"retry"`
	MaxRetry       int                    `json:"max_retry"`
	Dependencies   []string               `json:"dependencies"`
	Config         map[string]any         `json:"config,omitempty"`
	Metadata       map[string]string      `json:"metadata,omitempty"`
	IsAgent        bool                   `json:"is_agent"`
	AgentConfig    *scheduler.AgentConfig `json:"agent_config,omitempty"`
}

// newTaskView projects a scheduler task onto the wire model.
func newTaskView(t *scheduler.Task) *TaskView {
	if t == nil {
		return nil
	}
	view := &TaskView{
		ID:           t.ID,
		Type:         t.Type,
		State:        string(t.State),
		Priority:     int(t.Priority),
		PriorityName: t.Priority.String(),
		Owner:        t.Metadata[OwnerMetadataKey],
		CreatedAt:    formatTime(t.CreatedAt),
		Retry:        t.Retry,
		MaxRetry:     t.MaxRetry,
		Dependencies: append([]string{}, t.Dependencies...),
		Config:       publicConfig(t.Config),
		Metadata:     publicMetadata(t.Metadata),
		IsAgent:      t.IsAgent,
		AgentConfig:  t.AgentConfig,
	}
	if t.StartedAt != nil {
		view.StartedAt = formatTime(*t.StartedAt)
	}
	if t.EndedAt != nil {
		view.EndedAt = formatTime(*t.EndedAt)
	}
	view.Error = t.Error
	view.Input, view.InputEncoding = encodeBytes(t.Input)
	view.Result, view.ResultEncoding = encodeBytes(t.Result)
	return view
}

func newTaskViews(tasks []*scheduler.Task) []*TaskView {
	out := make([]*TaskView, 0, len(tasks))
	for _, t := range tasks {
		if v := newTaskView(t); v != nil {
			out = append(out, v)
		}
	}
	return out
}

// encodeBytes renders a byte payload as text when it is valid UTF-8, otherwise
// as base64 declared by the accompanying *_encoding field.
func encodeBytes(raw []byte) (value, encoding string) {
	if len(raw) == 0 {
		return "", ""
	}
	if isUTF8(raw) {
		return string(raw), "text"
	}
	return base64Std(raw), "base64"
}

// WorkerView is the wire representation of an executor worker.
type WorkerView struct {
	ID          string `json:"id"`
	PluginID    string `json:"plugin_id,omitempty"`
	State       string `json:"state"`
	TasksRun    int    `json:"tasks_run"`
	TasksFailed int    `json:"tasks_failed"`
	LastActive  string `json:"last_active,omitempty"`
}

func newWorkerViews(workers []*executor.Worker) []*WorkerView {
	out := make([]*WorkerView, 0, len(workers))
	for _, w := range workers {
		if w == nil {
			continue
		}
		state := "idle"
		if w.IsBusy() {
			state = "busy"
		}
		view := &WorkerView{
			ID:          w.ID,
			PluginID:    w.PluginID,
			State:       state,
			TasksRun:    w.TasksRun(),
			TasksFailed: w.TasksFailed(),
		}
		if last := w.LastActive(); !last.IsZero() {
			view.LastActive = last.UTC().Format(time.RFC3339Nano)
		}
		out = append(out, view)
	}
	return out
}

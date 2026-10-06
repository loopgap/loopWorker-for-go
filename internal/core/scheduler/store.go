package scheduler

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	lwerrors "loopworker/pkg/errors"
)

// ---- storage errors ----

var (
	// ErrStorageUnavailable means the task database could not be opened or written.
	ErrStorageUnavailable = errors.New("task storage unavailable")
	// ErrInstanceRunning means another process already holds this database.
	ErrInstanceRunning = errors.New("another LoopWorker instance is already using this task database")
	// ErrSchemaTooNew means the database was created by a newer LoopWorker.
	ErrSchemaTooNew = errors.New("task database schema is newer than this build")
	// ErrStorageInvalid means the operator asked for something impossible: a missing
	// argument, a destination that already exists, or one inside the data directory.
	// It is separate from ErrStorageUnavailable because the fix belongs to the caller,
	// not to the volume.
	ErrStorageInvalid = errors.New("invalid task storage request")
	// ErrSchedulerClosed is returned by every call after Close.
	ErrSchedulerClosed = errors.New("scheduler is closed")
)

// InstanceRunningError names the process holding the database so a second start attempt
// is actionable instead of corrupting state.
type InstanceRunningError struct {
	DBPath   string
	LockPath string
	PID      int
	Host     string
	Holder   string
	Seen     time.Time
}

func (e *InstanceRunningError) Error() string {
	return fmt.Sprintf("%s: %s is locked by %s (pid %d on %s, last seen %s).\n"+
		"  One database must be served by one process: stop that instance first, or point\n"+
		"  data_dir (or LOOPWORKER_DATA_DIR) at a different directory.\n"+
		"  If that pid is no longer running, delete the stale lock file: %s",
		ErrInstanceRunning, e.DBPath, e.Holder, e.PID, e.Host, e.Seen.Format(time.RFC3339), e.LockPath)
}

func (e *InstanceRunningError) Unwrap() error { return ErrInstanceRunning }

// RetentionConfig bounds how much task history and task output stays on disk.
// Zero means "use DefaultRetention()"; a negative value disables that limit.
type RetentionConfig struct {
	// MaxTasks is how many terminal tasks (completed/failed/cancelled/dead_letter) are
	// kept; the oldest are deleted first. Queued and running tasks are never removed.
	MaxTasks int
	// MaxAge deletes terminal tasks whose end time is older than this.
	MaxAge time.Duration
	// MaxResultAge clears the stored result blob of terminal tasks older than this while
	// keeping the task row: this is what stops task output from growing the DB forever.
	MaxResultAge time.Duration
	// Interval is how often the background pruner runs.
	Interval time.Duration
}

func DefaultRetention() RetentionConfig {
	return RetentionConfig{
		MaxTasks:     50000,
		MaxAge:       0,
		MaxResultAge: 72 * time.Hour,
		Interval:     10 * time.Minute,
	}
}

// UnlimitedRetention keeps every task row and result forever (not recommended).
func UnlimitedRetention() RetentionConfig {
	return RetentionConfig{MaxTasks: -1, MaxAge: -1, MaxResultAge: -1, Interval: time.Minute}
}

func (c RetentionConfig) withDefaults() RetentionConfig {
	d := DefaultRetention()
	if c.MaxTasks != 0 {
		d.MaxTasks = c.MaxTasks
	}
	if c.MaxAge != 0 {
		d.MaxAge = c.MaxAge
	}
	if c.MaxResultAge != 0 {
		d.MaxResultAge = c.MaxResultAge
	}
	if c.Interval > 0 {
		d.Interval = c.Interval
	}
	if d.Interval <= 0 {
		d.Interval = DefaultRetention().Interval
	}
	return d
}

func (c RetentionConfig) enabled() bool {
	return c.MaxTasks > 0 || c.MaxAge > 0 || c.MaxResultAge > 0
}

// StorageConfig is the injected storage location: nothing in this package guesses where
// the database lives any more.
type StorageConfig struct {
	// Path is the SQLite file, e.g. filepath.Join(cfg.DataDir, "tasks.db"). Required.
	Path string
	// BusyTimeout is how long SQLite waits for a competing writer before failing.
	BusyTimeout time.Duration
	// MaxOpenConns caps the database connection pool.
	MaxOpenConns int
	// AllowEphemeral is the explicit, loudly logged opt-in to start without persistence
	// when the database cannot be opened. Leave it false so startup fails instead of
	// silently losing the customer's tasks on restart.
	AllowEphemeral bool
	Retention      RetentionConfig
}

func (c StorageConfig) withDefaults() StorageConfig {
	if c.BusyTimeout <= 0 {
		c.BusyTimeout = 5 * time.Second
	}
	if c.MaxOpenConns <= 0 {
		c.MaxOpenConns = 4
	}
	c.Retention = c.Retention.withDefaults()
	return c
}

// StorageConfigForDataDir points the scheduler at <dataDir>/tasks.db.
func StorageConfigForDataDir(dataDir string) StorageConfig {
	return StorageConfig{Path: filepath.Join(dataDir, tasksDBName)}
}

// schemaVersion is the head of the compiled-in migration set.
var schemaVersion = len(sqliteMigrations)

const tasksDBName = "tasks.db"

type PruneResult struct {
	DeletedTasks   int
	ResultsCleared int
	FreedPages     int
}

// StorageInfo answers "where is my data and how big is it" from the API.
type StorageInfo struct {
	Persistent     bool      `json:"persistent"`
	Path           string    `json:"path"`
	SchemaVersion  int       `json:"schema_version"`
	Rows           int64     `json:"rows"`
	SizeBytes      int64     `json:"size_bytes"`
	JournalMode    string    `json:"journal_mode"`
	TasksDeleted   int64     `json:"tasks_deleted_by_retention"`
	ResultsCleared int64     `json:"results_cleared_by_retention"`
	StorageErrors  int64     `json:"storage_errors"`
	LastPrune      time.Time `json:"last_prune"`
	LastError      string    `json:"last_error,omitempty"`
}

// taskRecord is one row of the tasks table.
type taskRecord struct {
	ID              string
	Type            string
	State           string
	Priority        int
	ConfigJSON      []byte
	Input           []byte
	Result          []byte
	Error           string
	Retry           int
	MaxRetry        int
	CreatedAt       int64
	StartedAt       *int64
	EndedAt         *int64
	MetadataJSON    []byte
	Dependencies    []byte
	IsAgent         bool
	AgentConfigJSON []byte
}

// taskStore is the persistence port: a SQLite implementation for production and an
// in-memory one for the explicitly ephemeral constructor.
type taskStore interface {
	save(*Task) error
	get(string) (*Task, error)
	list(TaskFilter) ([]*Task, error)
	counts() (map[string]int, error)
	prune(RetentionConfig) (PruneResult, error)
	info() StorageInfo
	backup(string) error
	close() error
}

// ---- in-memory store (ephemeral mode) ----

type memStore struct {
	mu    sync.RWMutex
	tasks map[string]*Task
	order []string
	path  string
}

func newMemStore() *memStore {
	return &memStore{tasks: make(map[string]*Task), path: "(in-memory)"}
}

func (m *memStore) save(t *Task) error {
	if t == nil {
		return fmt.Errorf("scheduler: save task: nil task: %w", lwerrors.ErrTaskInvalid)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[t.ID]; !ok {
		m.order = append(m.order, t.ID)
	}
	m.tasks[t.ID] = cloneTask(t)
	return nil
}

func (m *memStore) get(id string) (*Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, fmt.Errorf("%w: get task %s from memory", lwerrors.ErrTaskNotFound, id)
	}
	return cloneTask(t), nil
}

func (m *memStore) list(filter TaskFilter) ([]*Task, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []*Task
	for _, id := range m.order {
		t := m.tasks[id]
		if t == nil || !matchFilter(t, filter) {
			continue
		}
		out = append(out, cloneTask(t))
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (m *memStore) counts() (map[string]int, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]int, len(m.tasks))
	for _, t := range m.tasks {
		out[string(t.State)]++
	}
	return out, nil
}

func (m *memStore) prune(cfg RetentionConfig) (PruneResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	cutoff := cfg.MaxAge > 0 && !now.Add(-cfg.MaxAge).IsZero()
	terminal := make([]string, 0, len(m.order))
	for _, id := range m.order {
		t := m.tasks[id]
		if t == nil || !isTerminal(t.State) {
			continue
		}
		if cutoff && t.EndedAt != nil && t.EndedAt.Before(now.Add(-cfg.MaxAge)) {
			m.deleteLocked(id)
			continue
		}
		terminal = append(terminal, id)
	}
	if cfg.MaxTasks > 0 && len(terminal) > cfg.MaxTasks {
		sort.SliceStable(terminal, func(i, j int) bool {
			return m.tasks[terminal[i]].CreatedAt.After(m.tasks[terminal[j]].CreatedAt)
		})
		var res PruneResult
		for _, id := range terminal[cfg.MaxTasks:] {
			m.deleteLocked(id)
			res.DeletedTasks++
		}
		return res, nil
	}
	return PruneResult{}, nil
}

func (m *memStore) deleteLocked(id string) {
	delete(m.tasks, id)
	for i, x := range m.order {
		if x == id {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

func (m *memStore) info() StorageInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return StorageInfo{Persistent: false, Path: m.path, Rows: int64(len(m.tasks)), SchemaVersion: schemaVersion}
}

func (m *memStore) backup(string) error {
	return fmt.Errorf("scheduler: backup: %s holds no database to copy; tasks live only in this process", m.path)
}

func (m *memStore) close() error { return nil }

func isTerminal(s TaskState) bool {
	switch s {
	case StateCompleted, StateFailed, StateCancelled, StateDeadLetter:
		return true
	}
	return false
}

func matchFilter(t *Task, filter TaskFilter) bool {
	if len(filter.States) > 0 {
		found := false
		for _, s := range filter.States {
			if t.State == s {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(filter.Types) > 0 {
		found := false
		for _, ty := range filter.Types {
			if t.Type == ty {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Priority != nil && t.Priority != *filter.Priority {
		return false
	}
	return true
}

func cloneTask(t *Task) *Task {
	c := *t
	c.Input = cloneBytes(t.Input)
	c.Result = cloneBytes(t.Result)
	if t.Config != nil {
		c.Config = make(map[string]interface{}, len(t.Config))
		for k, v := range t.Config {
			c.Config[k] = v
		}
	}
	if t.Metadata != nil {
		c.Metadata = make(map[string]string, len(t.Metadata))
		for k, v := range t.Metadata {
			c.Metadata[k] = v
		}
	}
	if t.Dependencies != nil {
		c.Dependencies = append([]string(nil), t.Dependencies...)
	}
	if t.DependsOn != nil {
		c.DependsOn = make(map[string]bool, len(t.DependsOn))
		for k, v := range t.DependsOn {
			c.DependsOn[k] = v
		}
	}
	if t.StartedAt != nil {
		v := *t.StartedAt
		c.StartedAt = &v
	}
	if t.EndedAt != nil {
		v := *t.EndedAt
		c.EndedAt = &v
	}
	if t.AgentConfig != nil {
		v := *t.AgentConfig
		c.AgentConfig = &v
	}
	return &c
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

var _ taskStore = (*memStore)(nil)

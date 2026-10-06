package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/event"
)

// failingStore records how many writes were attempted and always fails, so the
// scheduler's error plumbing can be asserted without breaking the real database.
type failingStore struct {
	calls  int
	lastID string
	err    error
}

func (f *failingStore) save(t *Task) error {
	f.calls++
	f.lastID = t.ID
	return f.err
}

func (f *failingStore) get(id string) (*Task, error) {
	if strings.Contains(f.err.Error(), "not found") {
		return nil, fmt.Errorf("%w: %s", lwerrors.ErrTaskNotFound, id)
	}
	return &Task{ID: id, State: StatePending, DependsOn: map[string]bool{}}, f.err
}

func (f *failingStore) list(TaskFilter) ([]*Task, error) { return nil, f.err }
func (f *failingStore) counts() (map[string]int, error)  { return nil, f.err }

func (f *failingStore) prune(RetentionConfig) (PruneResult, error) { return PruneResult{}, f.err }

func (f *failingStore) info() StorageInfo {
	return StorageInfo{Persistent: true, Path: "failing", LastError: f.err.Error()}
}

func (f *failingStore) backup(string) error { return f.err }
func (f *failingStore) close() error        { return nil }

func openTestScheduler(t *testing.T, dir string, mutate func(*StorageConfig)) *Scheduler {
	t.Helper()
	cfg := StorageConfigForDataDir(dir)
	cfg.Retention = UnlimitedRetention()
	if mutate != nil {
		mutate(&cfg)
	}
	s, err := NewSchedulerWithStorage(nil, cfg)
	if err != nil {
		t.Fatalf("NewSchedulerWithStorage(%s): %v", dir, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// DoD (b): tasks must still be there after a full process restart.
func TestRestartRecoversQueuedTasks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	s1 := openTestScheduler(t, dir, nil)
	task, err := s1.CreateTask(ctx, "report", map[string]interface{}{"format": "csv"}, []byte("payload"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s1.QueueTask(ctx, task.ID); err != nil {
		t.Fatalf("queue: %v", err)
	}
	running, _ := s1.CreateTask(ctx, "running", nil, []byte("in-flight"))
	if err := s1.QueueTask(ctx, running.ID); err != nil {
		t.Fatalf("queue running: %v", err)
	}
	if err := s1.StartTask(ctx, running.ID, "w-1"); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2 := openTestScheduler(t, dir, nil)
	if got := s2.QueueSize(); got != 2 {
		t.Fatalf("queue size after restart = %d, want 2 (queued + crashed-running)", got)
	}
	recovered, ok := s2.GetTask(task.ID)
	if !ok {
		t.Fatal("queued task vanished after restart")
	}
	if recovered.State != StateQueued {
		t.Errorf("state = %s, want queued", recovered.State)
	}
	if string(recovered.Input) != "payload" {
		t.Errorf("input = %q, want payload", recovered.Input)
	}
	if recovered.Config["format"] != "csv" {
		t.Errorf("config not recovered: %v", recovered.Config)
	}
	wasRunning, ok := s2.GetTask(running.ID)
	if !ok {
		t.Fatal("crashed-running task vanished after restart")
	}
	if wasRunning.State != StateQueued {
		t.Errorf("crashed running task state = %s, want queued", wasRunning.State)
	}

	// and the recovered task must still be executable
	if err := s2.QueueTask(ctx, recovered.ID); err == nil {
		t.Error("requeued already-queued task should be rejected")
	}
	if err := s2.StartTask(ctx, recovered.ID, "w-2"); err != nil {
		t.Fatalf("start recovered task: %v", err)
	}
	if err := s2.CompleteTask(ctx, recovered.ID, "w-2", []byte("done")); err != nil {
		t.Fatalf("complete recovered task: %v", err)
	}
	if got := s2.ListTasks(TaskFilter{States: []TaskState{StateCompleted}}); len(got) != 1 {
		t.Errorf("completed tasks = %d, want 1", len(got))
	}
}

func TestResultRoundTripsThroughStorage(t *testing.T) {
	ctx := context.Background()
	s := openTestScheduler(t, t.TempDir(), nil)

	task, _ := s.CreateTask(ctx, "agent", map[string]interface{}{"k": "v"}, []byte("in"))
	task.IsAgent = true
	task.AgentConfig = &AgentConfig{SystemPrompt: "hi", Model: "m", ResponseSchema: "{}"}
	task.Metadata = map[string]string{"owner": "ops"}
	if err := s.SaveTask(task); err != nil {
		t.Fatalf("save: %v", err)
	}
	_ = s.QueueTask(ctx, task.ID)
	_ = s.StartTask(ctx, task.ID, "w")
	if err := s.CompleteTask(ctx, task.ID, "w", []byte("big result")); err != nil {
		t.Fatalf("complete: %v", err)
	}

	got, ok := s.GetTask(task.ID)
	if !ok {
		t.Fatal("task not found")
	}
	if string(got.Result) != "big result" {
		t.Errorf("result = %q", got.Result)
	}
	if got.AgentConfig == nil || got.AgentConfig.Model != "m" {
		t.Errorf("agent config not persisted: %+v", got.AgentConfig)
	}
	if got.Metadata["owner"] != "ops" {
		t.Errorf("metadata not persisted: %v", got.Metadata)
	}
	if got.EndedAt == nil {
		t.Error("ended_at not persisted")
	}
}

func TestEphemeralSchedulerWritesNothing(t *testing.T) {
	before, _ := filepath.Abs(".")
	s := NewScheduler(nil)
	info := s.StorageInfo()
	if info.Persistent {
		t.Error("NewScheduler must not report persistent storage")
	}
	if info.Path != "(in-memory)" {
		t.Errorf("path = %s, want (in-memory)", info.Path)
	}
	if _, err := os.Stat(filepath.Join(before, tasksDBName)); err == nil {
		t.Error("ephemeral scheduler must not create a database in the working directory")
	}
	if _, err := os.Stat(filepath.Join(before, lockFileName)); err == nil {
		t.Error("ephemeral scheduler must not create a lock file in the working directory")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestStorageFailureIsFatalByDefault(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := StorageConfig{Path: filepath.Join(blocker, "tasks.db")}

	s, err := NewSchedulerWithStorage(nil, cfg)
	if err == nil {
		t.Fatal("expected startup to fail when the task database cannot be opened")
	}
	if s != nil {
		t.Error("no scheduler should be returned")
	}
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Errorf("error does not identify storage failure: %v", err)
	}
	for _, want := range []string{"allow_ephemeral_tasks", "refuses to start"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error is not actionable, missing %q:\n%s", want, err)
		}
	}
}

func TestAllowEphemeralIsAnExplicitOptIn(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := StorageConfig{Path: filepath.Join(blocker, "tasks.db"), AllowEphemeral: true}

	s, err := NewSchedulerWithStorage(nil, cfg)
	if err != nil {
		t.Fatalf("opt-in ephemeral should start: %v", err)
	}
	defer s.Close()

	info := s.StorageInfo()
	if info.Persistent {
		t.Error("degraded scheduler must report Persistent=false")
	}
	if info.LastError == "" {
		t.Error("degraded scheduler must record why it is running without storage")
	}
	stats := s.GetStats()
	if stats["persistent"] != false {
		t.Errorf("stats must expose persistence, got %v", stats["persistent"])
	}
}

func TestEmptyStoragePathRejected(t *testing.T) {
	_, err := NewSchedulerWithStorage(nil, StorageConfig{})
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("expected storage error, got %v", err)
	}
	if !strings.Contains(err.Error(), "StorageConfigForDataDir") {
		t.Errorf("error should name the fix, got %v", err)
	}
}

// DoD (e): a second scheduler on the same directory must refuse to start.
func TestSecondInstanceRefusesSameDatabase(t *testing.T) {
	dir := t.TempDir()
	first := openTestScheduler(t, dir, nil)

	_, err := NewSchedulerWithStorage(nil, StorageConfigForDataDir(dir))
	if err == nil {
		t.Fatal("second instance must refuse to start")
	}
	var locked *InstanceRunningError
	if !errors.As(err, &locked) {
		t.Fatalf("expected *InstanceRunningError, got %T: %v", err, err)
	}
	if locked.PID != os.Getpid() {
		t.Errorf("lock should name the holding pid %d, got %d", os.Getpid(), locked.PID)
	}
	for _, want := range []string{"another LoopWorker instance", "pid", "delete the stale lock file", locked.LockPath} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error not actionable, missing %q:\n%s", want, err)
		}
	}

	// A different directory is a different instance and must start fine.
	other := openTestScheduler(t, t.TempDir(), nil)
	if other.StorageInfo().Path == first.StorageInfo().Path {
		t.Error("two data dirs must not resolve to the same database")
	}

	// Releasing the lock must free the directory for the next start.
	if err := first.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); err == nil {
		t.Error("lock file should be removed on close")
	}
	third := openTestScheduler(t, dir, nil)
	if third.QueueSize() != 0 {
		t.Error("unexpected queue")
	}
}

// DoD (e) across processes: a real second OS process must get the actionable error.
func TestSecondProcessRefusesSameDatabase(t *testing.T) {
	if os.Getenv("LOOPWORKER_LOCK_HELPER") == "1" {
		dir := os.Getenv("LOOPWORKER_LOCK_DIR")
		s, err := NewSchedulerWithStorage(nil, StorageConfigForDataDir(dir))
		if err != nil {
			fmt.Printf("SECOND_START_ERROR: %v\n", err)
			return
		}
		_ = s.Close()
		fmt.Println("SECOND_START_SUCCEEDED")
		return
	}

	dir := t.TempDir()
	holder := openTestScheduler(t, dir, nil)

	cmd := exec.Command(os.Args[0], "-test.run=TestSecondProcessRefusesSameDatabase")
	cmd.Env = append(os.Environ(),
		"LOOPWORKER_LOCK_HELPER=1",
		"LOOPWORKER_LOCK_DIR="+dir,
	)

	out, err := cmd.CombinedOutput()
	text := string(out)
	if err == nil && strings.Contains(text, "SECOND_START_SUCCEEDED") {
		t.Fatalf("second process started while the lock was held:\n%s", text)
	}
	if !strings.Contains(text, "SECOND_START_ERROR") {
		t.Logf("helper output:\n%s", text)
		t.Skip("helper subprocess did not run in this environment")
	}
	for _, want := range []string{"another LoopWorker instance", holder.StorageInfo().Path} {
		if !strings.Contains(text, want) {
			t.Errorf("second-process error not actionable, missing %q:\n%s", want, text)
		}
	}
}

func TestStaleLockIsTakenOver(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, tasksDBName)
	lockPath := filepath.Join(dir, lockFileName)
	stale := fmt.Sprintf("pid=%d\nhost=elsewhere\nstarted=%s\ndb=%s\n",
		os.Getpid()+999999, time.Now().Add(-time.Hour).Format(time.RFC3339Nano), dbPath)
	if err := os.WriteFile(lockPath, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}
	// An old mtime is the liveness signal: the holder stopped refreshing, so it is dead.
	old := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}

	s := openTestScheduler(t, dir, nil)
	if _, err := s.CreateTask(context.Background(), "after-takeover", nil, nil); err != nil {
		t.Fatalf("create after taking over a stale lock: %v", err)
	}
}

func TestWALAndConnectionPoolAreApplied(t *testing.T) {
	s := openTestScheduler(t, t.TempDir(), func(c *StorageConfig) { c.MaxOpenConns = 2 })
	info := s.StorageInfo()
	if strings.ToLower(info.JournalMode) != "wal" {
		t.Errorf("journal_mode = %q, want wal", info.JournalMode)
	}
	if info.SchemaVersion != schemaVersion {
		t.Errorf("schema_version = %d, want %d", info.SchemaVersion, schemaVersion)
	}
	if !info.Persistent {
		t.Error("persistent scheduler must report Persistent=true")
	}
}

func TestSchemaFromTheFutureIsRefused(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	path := s.StorageInfo().Path
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open(driverName, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT OR REPLACE INTO schema_version(version, name, applied_at) VALUES (9999, 'future', 0)"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	_, err = NewSchedulerWithStorage(nil, StorageConfigForDataDir(dir))
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("expected ErrSchemaTooNew, got %v", err)
	}
	for _, want := range []string{"newer", "copy it somewhere safe"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error should be actionable, missing %q:\n%s", want, err)
		}
	}
}

// Migration path: the pre-pure-Go build stored tasks in a GORM-style table.
func TestLegacyTaskModelsTableIsMigrated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, tasksDBName)

	db, err := sql.Open(driverName, "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE task_models (
		id TEXT PRIMARY KEY, type TEXT, state TEXT, priority INTEGER,
		config_json BLOB, input BLOB, result BLOB, error TEXT,
		retry INTEGER, max_retry INTEGER, created_at INTEGER,
		started_at INTEGER, ended_at INTEGER, metadata_json BLOB,
		dependencies BLOB, is_agent INTEGER, agent_config_json BLOB)`); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UnixNano()
	if _, err := db.Exec(`INSERT INTO task_models (id, type, state, priority, created_at) VALUES (?,?,?,?,?)`,
		"legacy-1", "echo", string(StateQueued), int(PriorityHigh), created); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s := openTestScheduler(t, dir, nil)
	if s.QueueSize() != 1 {
		t.Fatalf("legacy queued task not recovered, queue size = %d", s.QueueSize())
	}
	got, ok := s.GetTask("legacy-1")
	if !ok {
		t.Fatal("legacy task missing")
	}
	if got.Priority != PriorityHigh {
		t.Errorf("priority = %d", got.Priority)
	}
	if info := s.StorageInfo(); info.SchemaVersion != schemaVersion {
		t.Errorf("migrated schema = %d", info.SchemaVersion)
	}
}

func TestRetentionPrunesTerminalTasksAndResults(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := openTestScheduler(t, dir, func(c *StorageConfig) {
		c.Retention = RetentionConfig{MaxTasks: 2, MaxResultAge: time.Hour}
	})

	old := time.Now().Add(-48 * time.Hour)
	for i := 0; i < 5; i++ {
		task, err := s.CreateTask(ctx, "old", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		ended := old.Add(time.Duration(i) * time.Minute)
		task.State = StateCompleted
		task.EndedAt = &ended
		task.Result = []byte(strings.Repeat("x", 512))
		if err := s.SaveTask(task); err != nil {
			t.Fatal(err)
		}
	}
	keep, _ := s.CreateTask(ctx, "fresh", nil, nil)
	_ = s.QueueTask(ctx, keep.ID)

	res, err := s.PruneNow()
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if res.DeletedTasks != 3 {
		t.Errorf("tasks deleted = %d, want 3 (5 terminal, keep 2)", res.DeletedTasks)
	}
	if res.ResultsCleared == 0 {
		t.Error("stale results must be cleared so task output stops growing")
	}

	remaining := s.ListTasks(TaskFilter{States: []TaskState{StateCompleted}})
	if len(remaining) != 2 {
		t.Errorf("terminal tasks left = %d, want 2", len(remaining))
	}
	if _, ok := s.GetTask(keep.ID); !ok {
		t.Error("queued task must never be pruned")
	}
	info := s.StorageInfo()
	if info.TasksDeleted == 0 || info.ResultsCleared == 0 || info.LastPrune.IsZero() {
		t.Errorf("retention counters must be reported: %+v", info)
	}
}

func TestRetentionLoopRunsInBackground(t *testing.T) {
	ctx := context.Background()
	s := openTestScheduler(t, t.TempDir(), func(c *StorageConfig) {
		c.Retention = RetentionConfig{MaxTasks: 1, Interval: 20 * time.Millisecond}
	})
	old := time.Now().Add(-time.Hour)
	for i := 0; i < 4; i++ {
		task, _ := s.CreateTask(ctx, "t", nil, nil)
		ended := old.Add(time.Duration(i) * time.Second)
		task.State = StateCompleted
		task.EndedAt = &ended
		if err := s.SaveTask(task); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.ListTasks(TaskFilter{States: []TaskState{StateCompleted}})) == 1 {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Error("background retention never pruned")
}

func TestBackupToCreatesUsableCopy(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	task, _ := s.CreateTask(ctx, "keepme", nil, []byte("data"))
	_ = s.QueueTask(ctx, task.ID)

	dest := filepath.Join(t.TempDir(), "backup", "tasks-copy.db")
	if err := s.BackupTo(dest); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	if err := s.BackupTo(dest); err == nil {
		t.Error("backup must not silently overwrite an existing file")
	}

	// The copy must be a real database containing the task.
	db, err := sql.Open(driverName, "file:"+dest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var id string
	if err := db.QueryRow("SELECT id FROM tasks WHERE id = ?", task.ID).Scan(&id); err != nil {
		t.Fatalf("backup does not contain the task: %v", err)
	}

	// Documented safe reset: back up first, then start from a clean file.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := openTestScheduler(t, dir, nil)
	if fresh.QueueSize() != 1 {
		t.Errorf("original store should still hold the queued task, got %d", fresh.QueueSize())
	}
}

func TestEphemeralBackupExplainsItself(t *testing.T) {
	s := NewScheduler(nil)
	err := s.BackupTo(filepath.Join(t.TempDir(), "x.db"))
	if err == nil {
		t.Fatal("ephemeral scheduler has nothing to back up")
	}
	if !strings.Contains(err.Error(), "in-memory") {
		t.Errorf("error should explain why: %v", err)
	}
}

func TestWriteErrorsIdentifyTaskAndOperation(t *testing.T) {
	opErr := errors.New("disk on fire")
	store := &failingStore{err: opErr}
	s := newScheduler(event.NewEventBus(nil), store, nil, StorageConfig{Retention: UnlimitedRetention()})

	task, err := s.CreateTask(context.Background(), "t", nil, nil)
	if err == nil {
		t.Fatal("create must fail when the store cannot write")
	}
	if task != nil {
		t.Error("no task may be handed back when the write failed")
	}
	if !strings.Contains(err.Error(), store.lastID) || !strings.Contains(err.Error(), "create task") {
		t.Errorf("error must name the operation and the task id: %v", err)
	}
	if !errors.Is(err, opErr) {
		t.Errorf("error must wrap the storage cause: %v", err)
	}
	if store.calls == 0 {
		t.Error("store.save was never attempted")
	}

	if err := s.QueueTask(context.Background(), "task-42"); err == nil {
		t.Error("queue must fail too")
	} else if !strings.Contains(err.Error(), "task-42") || !strings.Contains(err.Error(), "queue task") {
		t.Errorf("queue error must name task and operation: %v", err)
	}
}

func TestNotFoundIsDistinctFromStorageError(t *testing.T) {
	missing := &failingStore{err: errors.New("not found")}
	s := newScheduler(nil, missing, nil, StorageConfig{})
	if _, ok := s.GetTask("nope"); ok {
		t.Error("missing task must report not found")
	}
	err := s.QueueTask(context.Background(), "nope")
	if !errors.Is(err, lwerrors.ErrTaskNotFound) {
		t.Errorf("expected ErrTaskNotFound, got %v", err)
	}

	breakdown := &failingStore{err: errors.New("sql: connection lost")}
	s2 := newScheduler(nil, breakdown, nil, StorageConfig{})
	err = s2.QueueTask(context.Background(), "task-1")
	if errors.Is(err, lwerrors.ErrTaskNotFound) {
		t.Errorf("a broken store must not be reported as 'task not found': %v", err)
	}
	if !strings.Contains(err.Error(), "task-1") {
		t.Errorf("error must name the task: %v", err)
	}
}

func TestCloseIsIdempotentAndRejectsLaterWrites(t *testing.T) {
	s := openTestScheduler(t, t.TempDir(), nil)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second close must be clean: %v", err)
	}
	if _, err := s.CreateTask(context.Background(), "t", nil, nil); !errors.Is(err, ErrSchedulerClosed) {
		t.Errorf("expected ErrSchedulerClosed, got %v", err)
	}
	if err := s.SaveTask(&Task{ID: "x"}); !errors.Is(err, ErrSchedulerClosed) {
		t.Errorf("SaveTask after close: %v", err)
	}
}

func TestStorageInfoReportsSizeAndRows(t *testing.T) {
	ctx := context.Background()
	s := openTestScheduler(t, t.TempDir(), nil)
	for i := 0; i < 20; i++ {
		if _, err := s.CreateTask(ctx, "bulk", nil, make([]byte, 128)); err != nil {
			t.Fatal(err)
		}
	}
	info := s.StorageInfo()
	if info.Rows != 20 {
		t.Errorf("rows = %d, want 20", info.Rows)
	}
	if info.SizeBytes <= 0 {
		t.Errorf("size_bytes = %d, want > 0", info.SizeBytes)
	}
	stats := s.GetStats()
	if stats["storage"] == nil {
		t.Error("GetStats must include storage info")
	}
	if stats["persistent"] != true {
		t.Errorf("stats persistent = %v", stats["persistent"])
	}
}

// Close must hand back every file the data directory is made of: the database, its WAL
// and its index, and the instance lock. Windows refuses to unlink an open file, so a
// Close that misses one leaves the data directory undeletable and unusable for a
// clean reinstall.
func TestSchedulerCloseReleasesEveryDataFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "schedclose-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	ctx := context.Background()

	cfg := StorageConfigForDataDir(dir)
	cfg.Retention = UnlimitedRetention()
	s, err := NewSchedulerWithStorage(nil, cfg)
	if err != nil {
		t.Fatalf("NewSchedulerWithStorage: %v", err)
	}
	// A real write, so the WAL and the schema are actually on disk.
	if err := s.QueueTask(ctx, mustCreateTask(t, s, ctx, "t1").ID); err != nil {
		t.Fatalf("queue: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("data directory is not removable after Close: %v", err)
	}
}

func mustCreateTask(t *testing.T, s *Scheduler, ctx context.Context, id string) *Task {
	t.Helper()
	task, err := s.CreateTask(ctx, "report", nil, []byte("payload"))
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

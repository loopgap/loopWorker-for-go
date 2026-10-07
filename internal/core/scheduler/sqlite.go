package scheduler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, so one binary really runs everywhere

	lwerrors "loopworker/pkg/errors"
)

const driverName = "sqlite"

var terminalStates = []string{string(StateCompleted), string(StateFailed), string(StateCancelled), string(StateDeadLetter)}

// migration is one step of the ordered, compiled-in schema history.
type migration struct {
	Version int
	Name    string
	Apply   func(context.Context, *sql.Tx) error
}

var sqliteMigrations = []migration{
	{Version: 1, Name: "tasks table", Apply: func(ctx context.Context, tx *sql.Tx) error {
		// v0.x of this product used GORM, which pluralised the model name. Rename the
		// legacy table instead of silently starting from an empty task list.
		if legacy, err := tableExists(ctx, tx, "task_models"); err != nil {
			return err
		} else if legacy {
			if _, err := tx.ExecContext(ctx, "ALTER TABLE task_models RENAME TO tasks"); err != nil {
				return fmt.Errorf("migrate legacy task_models table: %w", err)
			}
		}
		_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tasks (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL DEFAULT '',
			state TEXT NOT NULL DEFAULT '',
			priority INTEGER NOT NULL DEFAULT 1,
			config_json BLOB,
			input BLOB,
			result BLOB,
			error TEXT NOT NULL DEFAULT '',
			retry INTEGER NOT NULL DEFAULT 0,
			max_retry INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER,
			ended_at INTEGER,
			metadata_json BLOB,
			dependencies BLOB,
			is_agent INTEGER NOT NULL DEFAULT 0,
			agent_config_json BLOB
		)`)
		return err
	}},
	{Version: 2, Name: "lookup indexes", Apply: func(ctx context.Context, tx *sql.Tx) error {
		stmts := []string{
			"CREATE INDEX IF NOT EXISTS idx_tasks_state ON tasks(state)",
			"CREATE INDEX IF NOT EXISTS idx_tasks_type ON tasks(type)",
			"CREATE INDEX IF NOT EXISTS idx_tasks_priority ON tasks(priority)",
			"CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks(created_at)",
			"CREATE INDEX IF NOT EXISTS idx_tasks_state_ended ON tasks(state, ended_at)",
		}
		for _, s := range stmts {
			if _, err := tx.ExecContext(ctx, s); err != nil {
				return err
			}
		}
		return nil
	}},
}

const createSchemaVersionTable = `CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at INTEGER NOT NULL
)`

// sqliteStore is the persistent task store.
type sqliteStore struct {
	db   *sql.DB
	path string
	cfg  StorageConfig

	journalMode    atomic.Value // string
	tasksDeleted   atomic.Int64
	resultsCleared atomic.Int64
	storageErrors  atomic.Int64
	lastPrune      atomic.Value // time.Time
	lastError      atomic.Value // string
}

// openSQLite creates the directory, applies pragmas, and runs pending migrations.
func openSQLite(ctx context.Context, cfg StorageConfig) (*sqliteStore, error) {
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		return nil, fmt.Errorf("scheduler: storage path must not be empty (%w)", ErrStorageUnavailable)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("scheduler: resolve storage path %q: %w", path, ErrStorageUnavailable)
	}
	if dir := filepath.Dir(abs); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("scheduler: create data directory %s: %w (is it writable? is data_dir pointing at a read-only volume?)", dir, ErrStorageUnavailable)
		}
	}

	busy := cfg.BusyTimeout
	if busy <= 0 {
		busy = 5 * time.Second
	}
	maxConns := cfg.MaxOpenConns
	if maxConns <= 0 {
		maxConns = 4
	}

	// auto_vacuum must be set before the tables exist, and pragmas are applied per
	// connection at connect time, so they belong in the DSN.
	pragmas := []string{
		fmt.Sprintf("busy_timeout(%d)", int(busy/time.Millisecond)),
		"auto_vacuum(INCREMENTAL)",
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"foreign_keys(1)",
		"temp_store(MEMORY)",
	}
	dsn := "file:" + escapeDSN(abs) + "?_pragma=" + url.QueryEscape(pragmas[0])
	for _, p := range pragmas[1:] {
		dsn += "&_pragma=" + url.QueryEscape(p)
	}

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("scheduler: open task database %s: %w", abs, ErrStorageUnavailable)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxIdleTime(5 * time.Minute)

	s := &sqliteStore{db: db, path: abs, cfg: cfg.withDefaults()}
	s.journalMode.Store("unknown")

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("scheduler: open task database %s: %w (the file is locked or corrupt; "+
			"run a backup copy of it elsewhere, then restore it or start with a fresh data_dir)", abs, ErrStorageUnavailable)
	}
	if mode, err := queryJournalMode(ctx, db); err == nil {
		s.journalMode.Store(mode)
	}
	if err := applyMigrations(ctx, db, abs); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func queryJournalMode(ctx context.Context, db *sql.DB) (string, error) {
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		return "", err
	}
	return mode, nil
}

// applyMigrations upgrades the schema and refuses to open a newer one.
func applyMigrations(ctx context.Context, db *sql.DB, path string) error {
	if _, err := db.ExecContext(ctx, createSchemaVersionTable); err != nil {
		return fmt.Errorf("scheduler: create schema_version table in %s: %w", path, ErrStorageUnavailable)
	}

	current, err := currentSchemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("scheduler: read schema version of %s: %w", path, ErrStorageUnavailable)
	}
	if current > schemaVersion {
		return fmt.Errorf("scheduler: %s has schema v%d but this LoopWorker build only understands v%d: "+
			"upgrade LoopWorker to open it, or move the file aside (and its -wal/-shm siblings) and restart "+
			"to start from an empty task store; do not delete it first: copy it somewhere safe so the tasks "+
			"can be recovered by the newer build (%w)", path, current, schemaVersion, ErrSchemaTooNew)
	}

	for _, m := range sqliteMigrations {
		if m.Version <= current {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("scheduler: begin migration %d (%s) on %s: %w", m.Version, m.Name, path, ErrStorageUnavailable)
		}
		if _, err := tx.ExecContext(ctx, createSchemaVersionTable); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("scheduler: migration %d (%s): %w", m.Version, m.Name, err)
		}
		if err := m.Apply(ctx, tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("scheduler: migration %d (%s) failed on %s: %w (the database was left at v%d; "+
				"restore a backup of it before retrying)", m.Version, m.Name, path, ErrStorageUnavailable, current)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT OR REPLACE INTO schema_version(version, name, applied_at) VALUES (?, ?, ?)",
			m.Version, m.Name, time.Now().UnixNano()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("scheduler: record schema version %d: %w", m.Version, ErrStorageUnavailable)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("scheduler: commit migration %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

func currentSchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v sql.NullInt64
	err := db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_version").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return int(v.Int64), nil
}

func tableExists(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func escapeDSN(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.ReplaceAll(p, "#", "%23")
	p = strings.ReplaceAll(p, "?", "%3f")
	return p
}

func (s *sqliteStore) opErr(op, id string, err error) error {
	if err == nil {
		return nil
	}
	s.lastError.Store(err.Error())
	s.storageErrors.Add(1)
	if isNoSpace(err) {
		return fmt.Errorf("scheduler: %s task %s: %w (no space left on the volume holding %s: free space, "+
			"or lower retention limits, then retry): %w", op, id, ErrStorageUnavailable, s.path, err)
	}
	return fmt.Errorf("scheduler: %s task %s in %s: %w: %w", op, id, s.path, lwerrors.ErrDatabaseError, err)
}

func (s *sqliteStore) save(t *Task) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rec, err := taskToRecord(t)
	if err != nil {
		return s.opErr("serialize", t.ID, err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO tasks (
			id, type, state, priority, config_json, input, result, error, retry, max_retry,
			created_at, started_at, ended_at, metadata_json, dependencies, is_agent, agent_config_json)
		VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16,?17)
		ON CONFLICT(id) DO UPDATE SET
			type=excluded.type, state=excluded.state, priority=excluded.priority,
			config_json=excluded.config_json, input=excluded.input, result=excluded.result,
			error=excluded.error, retry=excluded.retry, max_retry=excluded.max_retry,
			created_at=excluded.created_at, started_at=excluded.started_at, ended_at=excluded.ended_at,
			metadata_json=excluded.metadata_json, dependencies=excluded.dependencies,
			is_agent=excluded.is_agent, agent_config_json=excluded.agent_config_json`,
		rec.ID, rec.Type, rec.State, rec.Priority, rec.ConfigJSON, rec.Input, rec.Result, rec.Error,
		rec.Retry, rec.MaxRetry, rec.CreatedAt, rec.StartedAt, rec.EndedAt, rec.MetadataJSON,
		rec.Dependencies, rec.IsAgent, rec.AgentConfigJSON)
	if err != nil {
		return s.opErr("save", t.ID, err)
	}
	return nil
}

func (s *sqliteStore) get(id string) (*Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	row := s.db.QueryRowContext(ctx, selectTaskColumns+` WHERE id = ?`, id)
	rec, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: get task %s from %s", lwerrors.ErrTaskNotFound, id, s.path)
	}
	if err != nil {
		return nil, s.opErr("get", id, err)
	}
	return recordToTask(rec)
}

// COALESCE keeps rows written by older builds (which allowed NULLs) readable.
const selectTaskColumns = `SELECT id, COALESCE(type, ''), COALESCE(state, ''), COALESCE(priority, 1),
	config_json, input, result, COALESCE(error, ''), COALESCE(retry, 0), COALESCE(max_retry, 0),
	COALESCE(created_at, 0), started_at, ended_at, metadata_json, dependencies,
	COALESCE(is_agent, 0), agent_config_json
	FROM tasks`

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanRecord(s rowScanner) (*taskRecord, error) {
	rec := &taskRecord{}
	err := s.Scan(&rec.ID, &rec.Type, &rec.State, &rec.Priority, &rec.ConfigJSON, &rec.Input, &rec.Result,
		&rec.Error, &rec.Retry, &rec.MaxRetry, &rec.CreatedAt, &rec.StartedAt, &rec.EndedAt,
		&rec.MetadataJSON, &rec.Dependencies, &rec.IsAgent, &rec.AgentConfigJSON)
	if err != nil {
		return nil, err
	}
	return rec, nil
}

func (s *sqliteStore) list(filter TaskFilter) ([]*Task, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var conds []string
	var args []interface{}
	if len(filter.States) > 0 {
		placeholders := strings.Repeat("?,", len(filter.States))
		conds = append(conds, "state IN ("+strings.TrimSuffix(placeholders, ",")+")")
		for _, s := range filter.States {
			args = append(args, string(s))
		}
	}
	if len(filter.Types) > 0 {
		placeholders := strings.Repeat("?,", len(filter.Types))
		conds = append(conds, "type IN ("+strings.TrimSuffix(placeholders, ",")+")")
		for _, t := range filter.Types {
			args = append(args, t)
		}
	}
	if filter.Priority != nil {
		conds = append(conds, "priority = ?")
		args = append(args, int(*filter.Priority))
	}

	query := selectTaskColumns
	if len(conds) > 0 {
		query += " WHERE " + strings.Join(conds, " AND ")
	}
	query += " ORDER BY created_at DESC"
	if filter.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, filter.Limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, s.opErr("list", "(filter)", err)
	}
	defer rows.Close()

	var out []*Task
	for rows.Next() {
		rec, err := scanRecord(rows)
		if err != nil {
			return nil, s.opErr("list", "(scan)", err)
		}
		t, err := recordToTask(rec)
		if err != nil {
			return nil, s.opErr("decode", rec.ID, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, s.opErr("list", "(rows)", err)
	}
	return out, nil
}

func (s *sqliteStore) counts() (map[string]int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := s.db.QueryContext(ctx, "SELECT state, COUNT(*) FROM tasks GROUP BY state")
	if err != nil {
		return nil, s.opErr("count", "(states)", err)
	}
	defer rows.Close()

	out := make(map[string]int)
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, s.opErr("count", "(scan)", err)
		}
		out[state] = n
	}
	if err := rows.Err(); err != nil {
		return nil, s.opErr("count", "(rows)", err)
	}
	return out, nil
}

// prune deletes expired terminal tasks, caps terminal history, and drops result blobs
// that are older than MaxResultAge. Nothing queued or running is ever touched.
func (s *sqliteStore) prune(cfg RetentionConfig) (PruneResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var res PruneResult
	stateArgs := make([]interface{}, len(terminalStates))
	for i, st := range terminalStates {
		stateArgs[i] = st
	}
	in := "state IN (" + strings.Repeat("?,", len(terminalStates)-1) + "?)"

	if cfg.MaxAge > 0 {
		cutoff := time.Now().Add(-cfg.MaxAge).UnixNano()
		r, err := s.db.ExecContext(ctx,
			"DELETE FROM tasks WHERE "+in+" AND COALESCE(ended_at, created_at) < ? AND ended_at IS NOT NULL",
			append(append([]interface{}{}, stateArgs...), cutoff)...)
		if err != nil {
			return res, s.opErr("prune expired tasks", "(none)", err)
		}
		res.DeletedTasks = int(rowAffected(r))
	}

	if cfg.MaxTasks > 0 {
		r, err := s.db.ExecContext(ctx,
			"DELETE FROM tasks WHERE id IN (SELECT id FROM tasks WHERE "+in+
				" ORDER BY created_at DESC LIMIT -1 OFFSET ?)",
			append(append([]interface{}{}, stateArgs...), cfg.MaxTasks)...)
		if err != nil {
			return res, s.opErr("cap terminal tasks", "(none)", err)
		}
		res.DeletedTasks += int(rowAffected(r))
	}

	if cfg.MaxResultAge > 0 {
		cutoff := time.Now().Add(-cfg.MaxResultAge).UnixNano()
		r, err := s.db.ExecContext(ctx,
			"UPDATE tasks SET result=NULL WHERE "+in+" AND result IS NOT NULL AND COALESCE(ended_at, created_at) < ?",
			append(append([]interface{}{}, stateArgs...), cutoff)...)
		if err != nil {
			return res, s.opErr("prune task results", "(none)", err)
		}
		res.ResultsCleared = int(rowAffected(r))
	}

	if res.DeletedTasks > 0 {
		if _, err := s.db.ExecContext(ctx, "PRAGMA incremental_vacuum"); err != nil {
			return res, s.opErr("incremental vacuum", "(none)", err)
		}
	}

	if res.DeletedTasks > 0 {
		s.tasksDeleted.Add(int64(res.DeletedTasks))
	}
	if res.ResultsCleared > 0 {
		s.resultsCleared.Add(int64(res.ResultsCleared))
	}
	s.lastPrune.Store(time.Now())
	return res, nil
}

func rowAffected(r sql.Result) int64 {
	n, err := r.RowsAffected()
	if err != nil {
		return 0
	}
	return n
}

func (s *sqliteStore) info() StorageInfo {
	out := StorageInfo{
		Persistent:     true,
		Path:           s.path,
		SchemaVersion:  schemaVersion,
		TasksDeleted:   s.tasksDeleted.Load(),
		ResultsCleared: s.resultsCleared.Load(),
		StorageErrors:  s.storageErrors.Load(),
		JournalMode:    strValue(s.journalMode.Load()),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if mode, err := queryJournalMode(ctx, s.db); err == nil {
		out.JournalMode = mode
	}
	if size, err := dbSizeBytes(s.path); err == nil {
		out.SizeBytes = size
	}
	var rows int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks").Scan(&rows); err == nil {
		out.Rows = rows
	}
	if v := s.lastPrune.Load(); v != nil {
		if t, ok := v.(time.Time); ok {
			out.LastPrune = t
		}
	}
	out.LastError = strValue(s.lastError.Load())
	return out
}

func dbSizeBytes(path string) (int64, error) {
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			continue
		}
		total += info.Size()
	}
	return total, nil
}

// backup uses SQLite's own online backup so a copy is safe to take while running.
// The destination rules live in checkBackupDestination, which the offline CLI path
// shares: one implementation, so the in-process API and `loopworker backup` cannot
// drift into disagreeing about what a valid destination is.
func (s *sqliteStore) backup(dest string) error {
	abs, err := checkBackupDestination(s.path, dest)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(context.Background(), "VACUUM INTO ?", abs); err != nil {
		return s.opErr("backup", "(whole database)", err)
	}
	return nil
}

func (s *sqliteStore) close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("scheduler: close task database %s: %w", s.path, err)
	}
	return nil
}

// ---- record <-> Task ----

func taskToRecord(t *Task) (*taskRecord, error) {
	if t == nil {
		return nil, fmt.Errorf("scheduler: serialize: nil task: %w", lwerrors.ErrTaskInvalid)
	}
	rec := &taskRecord{
		ID:        t.ID,
		Type:      t.Type,
		State:     string(t.State),
		Priority:  int(t.Priority),
		Input:     t.Input,
		Result:    t.Result,
		Error:     t.Error,
		Retry:     t.Retry,
		MaxRetry:  t.MaxRetry,
		CreatedAt: t.CreatedAt.UnixNano(),
		IsAgent:   t.IsAgent,
	}
	if err := marshalInto(&rec.ConfigJSON, t.Config, "config", t.ID); err != nil {
		return nil, err
	}
	if err := marshalInto(&rec.MetadataJSON, t.Metadata, "metadata", t.ID); err != nil {
		return nil, err
	}
	if err := marshalInto(&rec.Dependencies, t.Dependencies, "dependencies", t.ID); err != nil {
		return nil, err
	}
	if t.AgentConfig != nil {
		if err := marshalInto(&rec.AgentConfigJSON, t.AgentConfig, "agent config", t.ID); err != nil {
			return nil, err
		}
	}
	if t.StartedAt != nil {
		v := t.StartedAt.UnixNano()
		rec.StartedAt = &v
	}
	if t.EndedAt != nil {
		v := t.EndedAt.UnixNano()
		rec.EndedAt = &v
	}
	return rec, nil
}

func marshalInto(dst *[]byte, v interface{}, what, id string) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("scheduler: serialize %s of task %s: %w", what, id, err)
	}
	*dst = data
	return nil
}

func recordToTask(rec *taskRecord) (*Task, error) {
	t := &Task{
		ID:        rec.ID,
		Type:      rec.Type,
		State:     TaskState(rec.State),
		Priority:  TaskPriority(rec.Priority),
		Input:     rec.Input,
		Result:    rec.Result,
		Error:     rec.Error,
		Retry:     rec.Retry,
		MaxRetry:  rec.MaxRetry,
		CreatedAt: time.Unix(0, rec.CreatedAt),
		IsAgent:   rec.IsAgent,
		DependsOn: make(map[string]bool),
	}
	if err := unmarshalInto(rec.ConfigJSON, &t.Config, "config", t.ID); err != nil {
		return nil, err
	}
	if err := unmarshalInto(rec.MetadataJSON, &t.Metadata, "metadata", t.ID); err != nil {
		return nil, err
	}
	if err := unmarshalInto(rec.Dependencies, &t.Dependencies, "dependencies", t.ID); err != nil {
		return nil, err
	}
	if len(rec.AgentConfigJSON) > 0 {
		var ac AgentConfig
		if err := json.Unmarshal(rec.AgentConfigJSON, &ac); err != nil {
			return nil, fmt.Errorf("scheduler: decode agent config of task %s: %w", t.ID, err)
		}
		t.AgentConfig = &ac
	}
	for _, d := range t.Dependencies {
		t.DependsOn[d] = true
	}
	if rec.StartedAt != nil {
		v := time.Unix(0, *rec.StartedAt)
		t.StartedAt = &v
	}
	if rec.EndedAt != nil {
		v := time.Unix(0, *rec.EndedAt)
		t.EndedAt = &v
	}
	return t, nil
}

func unmarshalInto(data []byte, dst interface{}, what, id string) error {
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("scheduler: decode %s of task %s: %w (row is corrupt; restore %s from a backup)", what, id, err, id)
	}
	return nil
}

func strValue(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// isNoSpace reports whether err is an out-of-disk-space failure.
func isNoSpace(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no space left") || strings.Contains(msg, "database or disk is full") ||
		strings.Contains(msg, "ENOSPC")
}

var _ taskStore = (*sqliteStore)(nil)

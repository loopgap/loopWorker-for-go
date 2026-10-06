package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// seedTasks writes n terminal tasks through the real store so a backup has rows to copy.
func seedTasks(t *testing.T, s *Scheduler, n int) []string {
	t.Helper()
	ctx := context.Background()
	var ids []string
	for i := 0; i < n; i++ {
		task, err := s.CreateTask(ctx, "backup-fixture", map[string]interface{}{"n": i}, []byte("payload"))
		if err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
		if err := s.QueueTask(ctx, task.ID); err != nil {
			t.Fatalf("queue task %d: %v", i, err)
		}
		if err := s.StartTask(ctx, task.ID, "backup-fixture-worker"); err != nil {
			t.Fatalf("start task %d: %v", i, err)
		}
		if err := s.CompleteTask(ctx, task.ID, "backup-fixture-worker", []byte("done")); err != nil {
			t.Fatalf("complete task %d: %v", i, err)
		}
		ids = append(ids, task.ID)
	}
	return ids
}

// artifactIsIntact is the check that makes "consistent copy" a tested claim rather than
// a comment: the file opens as SQLite, passes its own integrity check, and every task
// row is whole. A torn or partial copy fails at least one of these.
func artifactIsIntact(t *testing.T, path string) int64 {
	t.Helper()
	db, err := sql.Open(driverName, "file:"+escapeDSN(path))
	if err != nil {
		t.Fatalf("open backup %s: %v", path, err)
	}
	defer db.Close()

	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity_check on %s: %v", path, err)
	}
	if integrity != "ok" {
		t.Fatalf("backup is not a consistent database: integrity_check = %q", integrity)
	}

	rows, err := db.Query("SELECT id, state, result FROM tasks")
	if err != nil {
		t.Fatalf("read tasks from %s: %v", path, err)
	}
	defer rows.Close()
	var n int64
	for rows.Next() {
		var id, state string
		var result []byte
		if err := rows.Scan(&id, &state, &result); err != nil {
			t.Fatalf("torn row in %s: %v", path, err)
		}
		// A torn copy produces a row that is present but half-written. A completed
		// task must also carry its output: that is the part a partial page drops.
		if id == "" || state == "" || (state == string(StateCompleted) && len(result) == 0) {
			t.Fatalf("torn row in %s: id=%q state=%q result=%d bytes", path, id, state, len(result))
		}
		n++
	}
	return n
}

// TestBackupDatabaseWritesUsableArtifact is the happy path the CLI depends on: an
// operator with no configuration beyond data_dir gets a real SQLite file they can
// restore from.
func TestBackupDatabaseWritesUsableArtifact(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })
	ids := seedTasks(t, s, 3)

	dest := filepath.Join(t.TempDir(), "nested", "tasks-backup.db")
	res, err := BackupDatabase(filepath.Join(dir, tasksDBName), dest)
	if err != nil {
		t.Fatalf("BackupDatabase: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("no artifact at %s: %v", dest, err)
	}
	if info.Size() == 0 {
		t.Fatal("artifact is empty")
	}
	if res.SizeBytes != info.Size() {
		t.Errorf("reported size %d does not match the file on disk (%d)", res.SizeBytes, info.Size())
	}
	if res.Rows != 3 {
		t.Errorf("reported rows = %d, want 3", res.Rows)
	}
	if got := artifactIsIntact(t, dest); got != 3 {
		t.Errorf("artifact holds %d tasks, want 3", got)
	}
	_ = ids
}

// TestBackupDatabaseIsConsistentWhileTheServerWrites is the test the old comment was
// missing. The documented safe-reset is "back up, then delete the file", which is only
// useful if the copy can be taken while the server owns the database and is still a
// whole database afterwards.
func TestBackupDatabaseIsConsistentWhileTheServerWrites(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx := context.Background()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := s.CreateTask(ctx, "churn", nil, []byte("x")); err != nil {
				return
			}
		}
	}()
	// Let the writer get going so the backup really does race it.
	time.Sleep(150 * time.Millisecond)

	src := filepath.Join(dir, tasksDBName)
	for attempt := 0; attempt < 3; attempt++ {
		dest := filepath.Join(t.TempDir(), "live.db")
		res, err := BackupDatabase(src, dest)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("attempt %d: backup must work while the server writes: %v", attempt, err)
		}
		got := artifactIsIntact(t, dest)
		if got < 1 {
			t.Errorf("attempt %d: artifact holds %d tasks, want at least 1", attempt, got)
		}
		if res.Rows != got {
			t.Errorf("attempt %d: reported %d rows, the file holds %d", attempt, res.Rows, got)
		}
	}
	close(stop)
	wg.Wait()
}

// TestBackupDatabaseRefusesDestinationInsideDataDir enforces the rule the artifact must
// obey: a copy stored next to the database it copies is deleted by the very reset it was
// taken for, and it silently inflates the size every future backup measures.
func TestBackupDatabaseRefusesDestinationInsideDataDir(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })
	seedTasks(t, s, 1)
	src := filepath.Join(dir, tasksDBName)

	for _, dest := range []string{
		filepath.Join(dir, "backup.db"),
		filepath.Join(dir, "backups", "tasks.db"),
		filepath.Join(dir, tasksDBName+"-wal"),
	} {
		_, statErr := os.Stat(dest)
		existed := statErr == nil
		_, err := BackupDatabase(src, dest)
		if err == nil {
			t.Fatalf("%s is inside the data directory and must be refused", dest)
		}
		// A refused backup must not leave anything behind. The -wal sibling already
		// exists because the store runs in WAL mode, so only new paths are checked.
		if existed {
			continue
		}
		if _, statErr := os.Stat(dest); statErr == nil {
			t.Errorf("refused backup still created %s", dest)
		}
		msg := err.Error()
		if !strings.Contains(msg, dest) || !strings.Contains(msg, "loopworker backup --help") {
			t.Errorf("error must name the path and the doc anchor, got: %v", err)
		}
	}
}

// TestBackupDatabaseRefusesExistingDestination: VACUUM INTO reports a pre-existing file
// as "file is not a database (26)", which sends an operator hunting for a corrupt
// source. Say what is actually wrong.
func TestBackupDatabaseRefusesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })
	seedTasks(t, s, 1)
	src := filepath.Join(dir, tasksDBName)

	existing := filepath.Join(t.TempDir(), "already.db")
	if err := os.WriteFile(existing, []byte("do not clobber me"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := BackupDatabase(src, existing)
	if err == nil {
		t.Fatal("an existing destination must never be overwritten")
	}
	if !strings.Contains(err.Error(), existing) || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error must say the destination already exists: %v", err)
	}
	if data, _ := os.ReadFile(existing); string(data) != "do not clobber me" {
		t.Errorf("the existing file was modified: %q", data)
	}
}

// TestBackupDatabaseExplainsADirectoryDestination: "unable to open database file ... (526)"
// is a support ticket. Name the cause and the fix.
func TestBackupDatabaseExplainsADirectoryDestination(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })
	seedTasks(t, s, 1)

	dest := filepath.Join(t.TempDir(), "adir")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := BackupDatabase(filepath.Join(dir, tasksDBName), dest)
	if err == nil {
		t.Fatal("a directory is not a destination")
	}
	msg := err.Error()
	if !strings.Contains(msg, dest) || !strings.Contains(msg, "directory") {
		t.Errorf("error must name the path and say it is a directory: %v", err)
	}
}

// TestBackupDatabaseReportsAnUnwritableDestination: Windows ignores mode bits on
// directories for their owner, so the only portable unwritable destination is one whose
// parent is a regular file. The operator still gets path + cause + fix.
func TestBackupDatabaseReportsAnUnwritableDestination(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })
	seedTasks(t, s, 1)

	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(blocker, "backup.db")
	_, err := BackupDatabase(filepath.Join(dir, tasksDBName), dest)
	if err == nil {
		t.Fatal("writing through a regular file must fail")
	}
	msg := err.Error()
	if !strings.Contains(msg, dest) {
		t.Errorf("error must name the destination: %v", err)
	}
	if !strings.Contains(msg, "loopworker backup --help") {
		t.Errorf("error must carry the doc anchor: %v", err)
	}
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Errorf("error must wrap ErrStorageUnavailable so callers can classify it: %v", err)
	}
}

// TestBackupDatabaseRefusesAMissingSource: an operator who mistyped data_dir must not get
// a valid-looking empty artifact.
func TestBackupDatabaseRefusesAMissingSource(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope", tasksDBName)
	_, err := BackupDatabase(missing, filepath.Join(t.TempDir(), "out.db"))
	if err == nil {
		t.Fatal("backing up a database that does not exist must fail")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error must name the missing path: %v", err)
	}
}

// TestInspectDatabaseAnswersWhereIsMyData: this is the question that precedes every
// backup question.
func TestInspectDatabaseAnswersWhereIsMyData(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })
	seedTasks(t, s, 4)
	src := filepath.Join(dir, tasksDBName)

	info, err := InspectDatabase(src)
	if err != nil {
		t.Fatalf("InspectDatabase: %v", err)
	}
	if info.Path != src {
		t.Errorf("path = %q, want %q", info.Path, src)
	}
	if info.Rows != 4 {
		t.Errorf("rows = %d, want 4", info.Rows)
	}
	if info.SizeBytes <= 0 {
		t.Errorf("size = %d, want > 0", info.SizeBytes)
	}
	if info.SchemaVersion != schemaVersion {
		t.Errorf("schema version = %d, want %d", info.SchemaVersion, schemaVersion)
	}
	if !info.Persistent {
		t.Error("a real file on disk is persistent storage")
	}
	if info.JournalMode == "" {
		t.Error("journal mode must be reported: WAL is what makes a live backup possible")
	}
}

// TestInspectDatabaseSaysWhenNothingHasRunYet: "0 rows" reads like an empty database;
// "the server has not created it yet" is the truth and it is a different instruction.
func TestInspectDatabaseSaysWhenNothingHasRunYet(t *testing.T) {
	missing := filepath.Join(t.TempDir(), tasksDBName)
	_, err := InspectDatabase(missing)
	if err == nil {
		t.Fatal("a database that does not exist must not report a healthy empty store")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error must name the path: %v", err)
	}
	if !strings.Contains(err.Error(), "loopworker storage") {
		t.Errorf("error must carry the doc anchor: %v", err)
	}
}

// TestInspectDatabaseRejectsAForeignFile: pointing data_dir at a directory of unrelated
// files must not print zeros.
func TestInspectDatabaseRejectsAForeignFile(t *testing.T) {
	junk := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(junk, []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDatabase(junk); err == nil {
		t.Fatal("a file that is not a task database must be rejected")
	}
}

// TestBackupDatabaseMatchesTheLiveScheduler: the CLI and a running server resolve the
// same path from the same config, so the CLI must be able to back up exactly the file
// the server has open - without being refused by the instance lock.
func TestBackupDatabaseMatchesTheLiveScheduler(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })
	seedTasks(t, s, 2)

	if got := s.StorageInfo().Path; got != filepath.Join(dir, tasksDBName) {
		t.Fatalf("scheduler path = %q", got)
	}
	res, err := BackupDatabase(s.StorageInfo().Path, filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatalf("the CLI must be able to back up the database a running server holds: %v", err)
	}
	if res.Rows != 2 {
		t.Errorf("rows = %d, want 2", res.Rows)
	}
}

// TestSchedulerBackupToStillAgreesWithBackupDatabase keeps the in-process API and the
// CLI on one implementation: a comment is only worth what it claims if both paths
// produce the same artifact.
func TestSchedulerBackupToStillAgreesWithBackupDatabase(t *testing.T) {
	dir := t.TempDir()
	s := openTestScheduler(t, dir, nil)
	t.Cleanup(func() { _ = s.Close() })
	seedTasks(t, s, 2)

	viaMethod := filepath.Join(t.TempDir(), "method.db")
	if err := s.BackupTo(viaMethod); err != nil {
		t.Fatalf("BackupTo: %v", err)
	}
	viaFunc := filepath.Join(t.TempDir(), "func.db")
	if _, err := BackupDatabase(s.StorageInfo().Path, viaFunc); err != nil {
		t.Fatalf("BackupDatabase: %v", err)
	}

	a, err := os.Stat(viaMethod)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(viaFunc)
	if err != nil {
		t.Fatal(err)
	}
	if a.Size() != b.Size() {
		t.Errorf("the two entry points disagree on the artifact: %d vs %d bytes", a.Size(), b.Size())
	}
	// And the refusal rules must hold for the method too: it is the same code path.
	if _, err := BackupDatabase(s.StorageInfo().Path, filepath.Join(dir, "sneaky.db")); err == nil {
		t.Error("destination inside the data directory must be refused by both entry points")
	}
}

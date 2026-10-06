// Offline access to the task database for operator commands.
//
// A running server owns its database: the instance lock, the retention pruner and the
// in-memory priority queue are all tied to the process. A host command must therefore
// open the file itself rather than construct a Scheduler, otherwise "back this up" only
// works while the server is stopped - which is the one moment nobody wants to be true.
//
// Both entry points below open the file read-only and never take the instance lock, so
// they are safe to run against a live server. SQLite's own online backup (VACUUM INTO)
// reads a single snapshot inside one read transaction, so the artifact is consistent
// even while tasks are being written. That is a claim worth a test, and it has one:
// TestBackupDatabaseIsConsistentWhileTheServerWrites.
package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	lwerrors "loopworker/pkg/errors"
)

// backupTimeout bounds one backup. VACUUM INTO rewrites the whole database, so it is
// bounded by the size of the store, not by how long a task takes.
const backupTimeout = 30 * time.Minute

// BackupResult describes the artifact BackupDatabase wrote, so the caller can state
// where it landed, how big it is and what it contains without re-stat-ing the file.
type BackupResult struct {
	// SourcePath is the task database that was copied.
	SourcePath string
	// Path is the artifact that was written.
	Path string
	// SizeBytes is the artifact's size on disk.
	SizeBytes int64
	// Rows is how many task rows it holds.
	Rows int64
}

// BackupDatabase writes a consistent copy of the task database at dbPath to destPath.
// It works while a server is running and does not require the instance lock.
//
// destPath must not exist and must not be inside the data directory holding dbPath:
// a copy stored next to its own source is destroyed by the reset it was taken for.
// The rules exist because SQLite's own diagnostics for both cases ("file is not a
// database (26)" and "unable to open database file (526)") name the wrong problem.
func BackupDatabase(dbPath, destPath string) (BackupResult, error) {
	src, db, err := openOfflineDatabase(dbPath)
	if err != nil {
		return BackupResult{}, err
	}
	defer db.Close()

	res := BackupResult{SourcePath: src}
	res.Path, err = checkBackupDestination(src, destPath)
	if err != nil {
		return BackupResult{}, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	defer cancel()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", res.Path); err != nil {
		return BackupResult{}, fmt.Errorf("scheduler: backup %s to %s: %w (%v).\n"+
			"  Fix: make sure %s exists and is writable by this user, and that the volume has free space.\n"+
			"  See: loopworker backup --help", src, res.Path, ErrStorageUnavailable, err, filepath.Dir(res.Path))
	}

	info, err := os.Stat(res.Path)
	if err != nil {
		return BackupResult{}, fmt.Errorf("scheduler: backup %s reported success but %s cannot be read: %w "+
			"(do not trust this artifact; re-run and check the volume) (%v)", src, res.Path, ErrStorageUnavailable, err)
	}
	res.SizeBytes = info.Size()
	if res.SizeBytes == 0 {
		return BackupResult{}, fmt.Errorf("scheduler: backup of %s produced an empty %s: %w.\n"+
			"  Fix: check free space on the volume holding %s, then re-run.\n"+
			"  See: loopworker backup --help", src, res.Path, ErrStorageUnavailable, filepath.Dir(res.Path))
	}
	// Count the rows in the artifact, not in the source: the source may be being written
	// to right now, and the number an operator can verify by opening the copy is the one
	// the copy itself holds.
	if err := db.Close(); err != nil {
		return BackupResult{}, fmt.Errorf("scheduler: close %s after backup: %w (%v)", src, ErrStorageUnavailable, err)
	}
	if res.Rows, err = countArtifactRows(res.Path); err != nil {
		return BackupResult{}, fmt.Errorf("scheduler: backup %s reported success but %s cannot be read back: %w "+
			"(do not trust this artifact; re-run and check the volume) (%v)", src, res.Path, ErrStorageUnavailable, err)
	}
	return res, nil
}

// countArtifactRows opens the finished copy and counts its tasks. A backup that cannot be
// opened afterwards is not a backup, and the operator should hear that from this command
// rather than from the day they need to restore.
func countArtifactRows(path string) (int64, error) {
	db, err := sql.Open(driverName, "file:"+escapeDSN(path)+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), backupTimeout)
	defer cancel()
	return countTaskRows(ctx, db)
}

// InspectDatabase answers "where is my data and how big is it" for a database file,
// without a server, a lock or a migration.
func InspectDatabase(dbPath string) (StorageInfo, error) {
	path, db, err := openOfflineDatabase(dbPath)
	if err != nil {
		return StorageInfo{}, err
	}
	defer db.Close()

	info := StorageInfo{Persistent: true, Path: path, SchemaVersion: schemaVersion}
	if size, err := dbSizeBytes(path); err == nil {
		info.SizeBytes = size
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if mode, err := queryJournalMode(ctx, db); err == nil {
		info.JournalMode = mode
	}
	if info.Rows, err = countTaskRows(ctx, db); err != nil {
		return StorageInfo{}, err
	}
	return info, nil
}

// openOfflineDatabase opens an existing task database for reading only. It deliberately
// does not run migrations: a host command must never write to the file it is reporting
// on or copying, and it must work against a database created by a newer build.
func openOfflineDatabase(dbPath string) (string, *sql.DB, error) {
	path, err := filepath.Abs(strings.TrimSpace(dbPath))
	if err != nil {
		return "", nil, fmt.Errorf("scheduler: resolve task database %q: %w (%v)", dbPath, ErrStorageUnavailable, err)
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("scheduler: no task database at %s: %w.\n"+
			"  The scheduler creates it on first use, so this usually means the server has never run\n"+
			"  against this data directory.\n"+
			"  Fix: start loopworker once, or point --data-dir at the directory that holds it.\n"+
			"  See: loopworker storage", path, ErrStorageUnavailable)
	}
	if err != nil {
		return "", nil, fmt.Errorf("scheduler: task database %s cannot be read: %w "+
			"(is it readable by this user?) (%v)", path, ErrStorageUnavailable, err)
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("scheduler: %s is a directory, not a task database: %w.\n"+
			"  Fix: point --data-dir at the directory and let data.db_file name the file.\n"+
			"  See: loopworker storage", path, ErrStorageUnavailable)
	}

	// mode=ro keeps this honest: a host command that promises not to touch the file
	// must be unable to. busy_timeout is what lets the read wait for the server's
	// current write transaction instead of failing.
	db, err := sql.Open(driverName, "file:"+escapeDSN(path)+"?mode=ro&_pragma="+url.QueryEscape("busy_timeout(5000)"))
	if err != nil {
		return "", nil, fmt.Errorf("scheduler: open task database %s: %w (%v)", path, ErrStorageUnavailable, err)
	}
	var probe int64
	if err := db.QueryRow("SELECT COUNT(*) FROM tasks").Scan(&probe); err != nil {
		db.Close()
		return "", nil, fmt.Errorf("scheduler: %s is not a LoopWorker task database: %w (%v).\n"+
			"  Fix: check data.db_file and data.dir; point --data-dir at the directory that holds the file.\n"+
			"  See: loopworker storage", path, ErrStorageUnavailable, err)
	}
	return path, db, nil
}

// checkBackupDestination enforces the two rules SQLite cannot express, and returns the
// absolute destination path.
func checkBackupDestination(srcPath, destPath string) (string, error) {
	dest := strings.TrimSpace(destPath)
	if dest == "" {
		return "", fmt.Errorf("scheduler: no backup destination given: %w.\n"+
			"  Fix: pass the file to write, for example `loopworker backup ./tasks-%s.db`.\n"+
			"  See: loopworker backup --help", ErrStorageInvalid, time.Now().Format("20060102-150405"))
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return "", fmt.Errorf("scheduler: resolve backup destination %q: %w (%v)", destPath, ErrStorageUnavailable, err)
	}
	if samePath(abs, srcPath) {
		return "", fmt.Errorf("scheduler: backup destination %s is the task database itself: %w.\n"+
			"  Fix: name a different file, for example `loopworker backup ./%s.backup.db`.\n"+
			"  See: loopworker backup --help", abs, ErrStorageInvalid, filepath.Base(srcPath))
	}
	if insideDir(filepath.Dir(srcPath), abs) {
		return "", fmt.Errorf("scheduler: backup destination %s is inside the data directory %s: %w.\n"+
			"  A copy stored next to its own source is deleted by the very reset it was taken for,\n"+
			"  and it is counted again by every future copy.\n"+
			"  Fix: write it outside, for example `loopworker backup ./tasks-backup.db`.\n"+
			"  See: loopworker backup --help", abs, filepath.Dir(srcPath), ErrStorageInvalid)
	}
	if info, err := os.Stat(abs); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("scheduler: backup destination %s is a directory: %w.\n"+
				"  Fix: give the file to write, for example `loopworker backup %s`.\n"+
				"  See: loopworker backup --help", abs, ErrStorageInvalid, filepath.Join(abs, "tasks.db"))
		}
		return "", fmt.Errorf("scheduler: backup destination %s already exists (%d bytes): %w.\n"+
			"  LoopWorker never overwrites a backup: the old one may be the only copy left.\n"+
			"  Fix: choose another name, or delete that file deliberately first.\n"+
			"  See: loopworker backup --help", abs, info.Size(), ErrStorageInvalid)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("scheduler: backup destination %s cannot be used: %w (%v)\n"+
			"  See: loopworker backup --help", abs, ErrStorageUnavailable, err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fmt.Errorf("scheduler: cannot create the directory for backup destination %s: %w (%v).\n"+
			"  Fix: create %s first, or give a destination whose parent directory exists.\n"+
			"  See: loopworker backup --help", abs, ErrStorageUnavailable, err, filepath.Dir(abs))
	}
	return abs, nil
}

// countTaskRows counts the tasks in a task database.
func countTaskRows(ctx context.Context, db *sql.DB) (int64, error) {
	var rows int64
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks").Scan(&rows); err != nil {
		return 0, fmt.Errorf("scheduler: count tasks: %w (%v)", lwerrors.ErrDatabaseError, err)
	}
	return rows, nil
}

// samePath compares two paths the way the host does: case-insensitively on Windows,
// where C:\Data and c:\data are one directory.
func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// insideDir reports whether path is dir itself or below it.
func insideDir(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

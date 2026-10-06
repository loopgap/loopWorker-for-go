package selfheal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// monitor starts a healer on a 5ms interval with checks already registered,
// which is the order boot() uses: register, then StartHealthChecks. It is also
// the only order that works - StartHealthChecks snapshots the registry, so a
// check registered afterwards is never run.
func monitor(t *testing.T, checks ...*HealthCheck) (*SelfHealer, func()) {
	t.Helper()
	cfg := fastConfig()
	cfg.HealthInterval = 5 * time.Millisecond
	sh := NewSelfHealer(cfg)
	for _, c := range checks {
		c.Interval, c.Timeout = 5*time.Millisecond, time.Second
		sh.RegisterHealthCheck(c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := sh.StartHealthChecks(ctx)
	return sh, func() { stop(); cancel() }
}

// TestNoRegisteredCheckStaysUnknown is the invariant that makes a registered
// check worth registering: within one interval every entry in HealthReports
// must carry a verdict. A check pinned at HealthUnknown is the shape of the
// dead registry these checks are registered into - present in the map, never
// executed - and only this assertion catches it.
func TestNoRegisteredCheckStaysUnknown(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "tasks.db")
	if err := os.WriteFile(db, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	sh, stop := monitor(t, WritableFileCheck("task_database", db), WritableDirCheck("data_dir", root))
	defer stop()

	waitFor(t, 2*time.Second, func() bool {
		for _, r := range sh.HealthReports() {
			if r.Status == HealthUnknown {
				return false
			}
		}
		return true
	})

	got := sh.HealthReports()
	if len(got) != 2 {
		t.Fatalf("HealthReports has %d entries, want 2", len(got))
	}
	if r := got["task_database"]; r.Status != HealthHealthy {
		t.Errorf("task_database = %v (%v), want healthy", r.Status, r.Error)
	}
	if r := got["data_dir"]; r.Status != HealthHealthy {
		t.Errorf("data_dir = %v (%v), want healthy", r.Status, r.Error)
	}
}

// TestWritableDirCheckRunsAndReports pins the first real check: the data
// directory must still accept a file, which is the only way disk-full and
// permission loss surface before a task write fails.
func TestWritableDirCheckRunsAndReports(t *testing.T) {
	dir := t.TempDir()
	sh, stop := monitor(t, WritableDirCheck("data_dir", dir))
	defer stop()

	waitFor(t, 2*time.Second, func() bool {
		r, ok := sh.HealthStatus("data_dir")
		return ok && !r.LastRun.IsZero()
	})
	r, _ := sh.HealthStatus("data_dir")
	if r.Status != HealthHealthy {
		t.Fatalf("status = %v (%v), want healthy for a writable dir", r.Status, r.Error)
	}
	// The probe must not leave anything behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("write probe left %d file(s) in %s", len(entries), dir)
	}
}

// TestWritableDirCheckFailsWithCause proves the failure path an operator has
// to be able to read: a directory that cannot be written is reported unhealthy
// with the directory and the OS reason, not swallowed.
func TestWritableDirCheckFailsWithCause(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	sh, stop := monitor(t, WritableDirCheck("data_dir", missing))
	defer stop()

	waitFor(t, 2*time.Second, func() bool {
		r, ok := sh.HealthStatus("data_dir")
		return ok && !r.LastRun.IsZero() && r.Status != HealthHealthy
	})
	r, _ := sh.HealthStatus("data_dir")
	if r.Status != HealthUnhealthy {
		t.Fatalf("status = %v, want unhealthy", r.Status)
	}
	if r.Error == nil || !errors.Is(r.Error, errCheckFailed) {
		t.Errorf("error %v does not wrap errCheckFailed", r.Error)
	}
	if !strings.Contains(r.Error.Error(), missing) {
		t.Errorf("error %q does not name the directory %q", r.Error, missing)
	}
	if !strings.Contains(r.Error.Error(), "not writable") {
		t.Errorf("error %q does not say what is wrong", r.Error)
	}
}

// TestWritableFileCheckProvesTheDatabaseIsWritable covers the scheduler
// storage check: tasks.db must be openable for writing, not merely present.
// Pointing the check at a directory produces a real OS failure on Windows and
// on Linux alike, so the failure path is observed rather than assumed.
func TestWritableFileCheckProvesTheDatabaseIsWritable(t *testing.T) {
	db := filepath.Join(t.TempDir(), "tasks.db")
	if err := os.WriteFile(db, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	sh, stop := monitor(t, WritableFileCheck("task_database", db))
	defer stop()

	waitFor(t, 2*time.Second, func() bool {
		r, ok := sh.HealthStatus("task_database")
		return ok && !r.LastRun.IsZero()
	})
	if r, _ := sh.HealthStatus("task_database"); r.Status != HealthHealthy {
		t.Fatalf("status = %v (%v), want healthy for a writable file", r.Status, r.Error)
	}

	// A directory is not openable for writing: the real failure an operator
	// hits when data.dir is pointed at something that is not a file.
	asDir := t.TempDir()
	report := sh.RunHealthCheck(context.Background(), WritableFileCheck("task_database", asDir))
	if report.Status != HealthUnhealthy {
		t.Fatalf("status = %v, want unhealthy when the database path is a directory", report.Status)
	}
	if !strings.Contains(report.Error.Error(), asDir) {
		t.Errorf("error %q does not name the path %q", report.Error, asDir)
	}
}

// TestWritableFileCheckFailsWhenTheFileIsGone proves a missing database is
// reported rather than passing on a nil error from a skipped probe.
func TestWritableFileCheckFailsWhenTheFileIsGone(t *testing.T) {
	sh := NewSelfHealer(fastConfig())
	missing := filepath.Join(t.TempDir(), "tasks.db")
	report := sh.RunHealthCheck(context.Background(), WritableFileCheck("task_database", missing))
	if report.Status != HealthUnhealthy {
		t.Fatalf("status = %v, want unhealthy for a missing database file", report.Status)
	}
	if report.Error == nil || !strings.Contains(report.Error.Error(), missing) {
		t.Errorf("error %q does not name the missing path %q", report.Error, missing)
	}
}

// TestChecksRunConcurrentlyUnderRealDependencies is the load-bearing guard:
// every constructed check is actually executed by the monitor, and none of them
// stays pinned at HealthUnknown, which is what an empty registry looks like
// from the outside.
func TestChecksRunConcurrentlyUnderRealDependencies(t *testing.T) {
	root := t.TempDir()
	var dirs []string
	for _, name := range []string{"one", "two", "three"} {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		dirs = append(dirs, d)
	}

	// Register before starting so the unknown-state assertion below is
	// meaningful, then start the monitor exactly like boot() does.
	cfg := fastConfig()
	cfg.HealthInterval = 5 * time.Millisecond
	sh := NewSelfHealer(cfg)
	for _, d := range dirs {
		c := WritableDirCheck(filepath.Base(d), d)
		c.Interval, c.Timeout = 5*time.Millisecond, time.Second
		sh.RegisterHealthCheck(c)
	}

	reports := sh.HealthReports()
	if len(reports) != len(dirs) {
		t.Fatalf("registered %d check(s), want %d", len(reports), len(dirs))
	}
	for name, r := range reports {
		if r.Status != HealthUnknown {
			t.Errorf("%s = %v, want unknown before the monitor runs it", name, r.Status)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := sh.StartHealthChecks(ctx)
	defer stop()

	for _, d := range dirs {
		name := filepath.Base(d)
		waitFor(t, 2*time.Second, func() bool {
			r, ok := sh.HealthStatus(name)
			return ok && !r.LastRun.IsZero()
		})
		if r, _ := sh.HealthStatus(name); r.Status != HealthHealthy {
			t.Errorf("%s = %v (%v), want healthy", name, r.Status, r.Error)
		}
	}
}

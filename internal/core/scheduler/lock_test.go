package scheduler

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These tests own the directory they delete. t.TempDir() deletes it from a cleanup,
// and a cleanup failure reads like a harness bug instead of the leaked handle under test.

func ownedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "schedlock-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	return dir
}

// The lock file records who owns the data directory; the ownership decision is made
// from that content plus its mtime, never from an open OS handle. Holding a handle
// anyway only leaks it: on Windows the file can then not be unlinked, so an aborted
// or mis-tested shutdown leaves the data directory undeletable.
func TestInstanceLockDoesNotPinTheLockFile(t *testing.T) {
	dir := ownedTempDir(t)

	lock, err := acquireInstanceLockTiming(dir, filepath.Join(dir, "tasks.db"), time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	t.Cleanup(func() { _ = lock.release() })

	if err := os.Remove(dir + string(os.PathSeparator) + lockFileName); err != nil {
		t.Fatalf("lock file is pinned although the lock is live: %v", err)
	}
}

// release is what cleans the lock file up, and it must still remove it.
func TestInstanceLockReleaseRemovesTheLockFile(t *testing.T) {
	dir := ownedTempDir(t)

	lock, err := acquireInstanceLockTiming(dir, filepath.Join(dir, "tasks.db"), time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if err := lock.release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, lockFileName)); !os.IsNotExist(err) {
		t.Errorf("release must remove the lock file, stat returned %v", err)
	}
}

// A lock that was never released must not wedge the directory forever: the next start
// has to be able to reclaim it, which is what the stale-after rule is for.
func TestInstanceLockStaleEntryIsReclaimed(t *testing.T) {
	dir := ownedTempDir(t)

	stale := filepath.Join(dir, lockFileName)
	if err := os.WriteFile(stale, []byte("pid=999999\nhost=other\n"), 0o644); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("age the stale lock: %v", err)
	}

	lock, err := acquireInstanceLockTiming(dir, filepath.Join(dir, "tasks.db"), time.Second, time.Hour)
	if err != nil {
		t.Fatalf("a stale lock must be reclaimable: %v", err)
	}
	t.Cleanup(func() { _ = lock.release() })
}

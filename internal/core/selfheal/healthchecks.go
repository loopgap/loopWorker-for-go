package selfheal

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// WritableDirCheck returns a health check that proves dir still accepts a new
// file, by writing and removing one probe file.
//
// This is the check for the failure mode nothing else reports: the disk filled
// up, or the permissions on the directory changed, and every task write and
// event append afterwards fails with an error the operator never sees. The
// probe deliberately does not create dir - "the directory vanished" (an
// unmounted volume, a renamed data.dir) is the case worth reporting, and
// MkdirAll would paper over it.
func WritableDirCheck(name, dir string) *HealthCheck {
	return &HealthCheck{
		Name: name,
		Check: func(context.Context) error {
			probe := filepath.Join(dir, fmt.Sprintf(".loopworker-health-%d", os.Getpid()))
			if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
				return fmt.Errorf("directory %s is not writable: %w", dir, err)
			}
			// A leftover probe is worse than a failed one: it makes the next
			// run's directory listing lie, so a failure to clean up is the
			// verdict too.
			if err := os.Remove(probe); err != nil {
				return fmt.Errorf("directory %s accepted a write but the probe %s could not be removed: %w", dir, probe, err)
			}
			return nil
		},
	}
}

// WritableFileCheck returns a health check that proves an existing file can
// still be opened for writing. This is the check for the scheduler's SQLite
// store: a database that is present but not writable (read-only mount, wrong
// owner, the path turned into a directory) accepts every read and fails every
// task, so stat() alone would report it as healthy.
//
// The caller owns Interval and Timeout; the default HealthInterval is fine for
// a local file operation.
func WritableFileCheck(name, path string) *HealthCheck {
	return &HealthCheck{
		Name: name,
		Check: func(context.Context) error {
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				return fmt.Errorf("file %s is not openable for writing: %w", path, err)
			}
			return f.Close()
		},
	}
}

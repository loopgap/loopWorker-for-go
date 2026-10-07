package scheduler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// lockFileName sits next to the task database. One data directory means one process:
// the task database, the event store and the in-memory priority queue are only
// consistent together, so a second server must refuse to start rather than share them.
const lockFileName = "loopworker.lock"

const (
	defaultLockStaleAfter    = 10 * time.Second
	defaultLockHeartbeatStep = defaultLockStaleAfter / 3
)

// instanceLock is a heartbeat lock file holding the owner's pid. A crashed owner stops
// refreshing, so the lock expires on its own and the next start takes over.
type instanceLock struct {
	path   string
	dbPath string

	staleAfter time.Duration
	heartbeat  time.Duration

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

type lockRecord struct {
	PID     int
	Host    string
	Started time.Time
	DBPath  string
}

// heldLocks tracks locks claimed by this process, so a second scheduler pointed at the
// same file is refused even before the lock file is consulted.
var heldLocks sync.Map // absolute lock path -> *instanceLock

func acquireInstanceLock(dir, dbPath string) (*instanceLock, error) {
	return acquireInstanceLockTiming(dir, dbPath, defaultLockStaleAfter, defaultLockHeartbeatStep)
}

func acquireInstanceLockTiming(dir, dbPath string, staleAfter, heartbeat time.Duration) (*instanceLock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("scheduler: create lock directory %s: %w (is it writable? point data_dir "+
			"at a writable directory)", dir, ErrStorageUnavailable)
	}
	l := &instanceLock{
		path:       filepath.Join(dir, lockFileName),
		dbPath:     dbPath,
		staleAfter: staleAfter,
		heartbeat:  heartbeat,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}

	abs, err := filepath.Abs(l.path)
	if err != nil {
		abs = l.path
	}
	if existing, ok := heldLocks.Load(abs); ok {
		owner, ok := existing.(*instanceLock)
		if !ok {
			// Only Acquire stores into this map, and it stores *instanceLock.
			// Guarding anyway keeps a future caller from turning a type bug
			// into a panic on the startup path.
			return nil, fmt.Errorf("scheduler: internal lock registry is corrupt at %s", abs)
		}
		return nil, &InstanceRunningError{
			DBPath:   owner.dbPath,
			LockPath: owner.path,
			PID:      os.Getpid(),
			Host:     hostname(),
			Holder:   "another scheduler in this process",
			Seen:     time.Now(),
		}
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if holder, held := l.holder(); held {
			return nil, holder
		}
		if _, loaded := heldLocks.LoadOrStore(abs, l); loaded {
			return nil, &InstanceRunningError{
				DBPath:   dbPath,
				LockPath: l.path,
				PID:      os.Getpid(),
				Host:     hostname(),
				Holder:   "another scheduler in this process",
				Seen:     time.Now(),
			}
		}
		if err := l.write(); err != nil {
			heldLocks.Delete(abs)
			lastErr = err
			time.Sleep(10 * time.Millisecond)
			continue
		}
		// Confirm we are still the owner: two processes racing a takeover must not both
		// believe they won.
		if rec, ok := l.read(); ok && rec.PID != os.Getpid() {
			heldLocks.Delete(abs)
			lastErr = nil
			time.Sleep(10 * time.Millisecond)
			continue
		}
		go l.refreshLoop()
		return l, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &InstanceRunningError{DBPath: dbPath, LockPath: l.path, Host: hostname(),
		Holder: "another process", Seen: time.Now()}
}

// write stamps the ownership record and returns with the handle closed: ownership is
// decided from the content and the mtime, so a kept-open handle would only leak (and on
// Windows would make the file unlinkable for as long as the process lives).
func (l *instanceLock) write() error {
	rec := lockRecord{PID: os.Getpid(), Host: hostname(), Started: time.Now(), DBPath: l.dbPath}
	data := []byte(fmt.Sprintf("pid=%d\nhost=%s\nstarted=%s\ndb=%s\n", rec.PID, rec.Host, rec.Started.Format(time.RFC3339Nano), rec.DBPath))

	err := writeFileSynced(l.path, data)
	if err != nil {
		return fmt.Errorf("scheduler: write instance lock %s: %w", l.path, errors.Join(ErrStorageUnavailable, err))
	}
	return nil
}

func writeFileSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	return errors.Join(werr, f.Close())
}

// holder reports who owns the lock, if anyone alive does.
func (l *instanceLock) holder() (*InstanceRunningError, bool) {
	info, err := os.Stat(l.path)
	if err != nil {
		//nolint:nilerr // "no lock file" is an answer to "does anyone hold it", not a failure
		return nil, false
	}
	rec, ok := l.read()
	if !ok {
		// Unreadable or empty: treat as stale so a crash mid-write cannot wedge the dir.
		return nil, false
	}
	if rec.PID == os.Getpid() && rec.DBPath == l.dbPath {
		// Our own leftovers from a previous run that did not clean up.
		return nil, false
	}
	if time.Since(info.ModTime()) > l.staleAfter {
		return nil, false
	}
	return &InstanceRunningError{
		DBPath:   l.dbPath,
		LockPath: l.path,
		PID:      rec.PID,
		Host:     rec.Host,
		Holder:   describeHolder(rec),
		Seen:     info.ModTime(),
	}, true
}

func describeHolder(rec lockRecord) string {
	if rec.Host != "" && rec.Host != hostname() {
		return fmt.Sprintf("LoopWorker on host %q", rec.Host)
	}
	return "LoopWorker"
}

func (l *instanceLock) read() (lockRecord, bool) {
	data, err := os.ReadFile(l.path)
	if err != nil {
		return lockRecord{}, false
	}
	rec := lockRecord{}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		switch k {
		case "pid":
			rec.PID, _ = strconv.Atoi(v)
		case "host":
			rec.Host = v
		case "started":
			rec.Started, _ = time.Parse(time.RFC3339Nano, v)
		case "db":
			rec.DBPath = v
		}
	}
	if rec.PID <= 0 {
		return lockRecord{}, false
	}
	return rec, true
}

func (l *instanceLock) refreshLoop() {
	defer close(l.done)
	t := time.NewTicker(l.heartbeat)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			// Rewrite both the content and the mtime; the mtime is the liveness signal.
			if _, held := l.holder(); held {
				continue
			}
			_ = l.write()
		}
	}
}

func (l *instanceLock) release() error {
	if l.done == nil {
		return nil
	}
	l.stopOnce.Do(func() { close(l.stop) })
	<-l.done

	abs, err := filepath.Abs(l.path)
	if err != nil {
		abs = l.path
	}
	heldLocks.Delete(abs)

	var errs []error
	if rec, ok := l.read(); ok && rec.PID == os.Getpid() {
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("scheduler: release instance lock %s: %w", l.path, errors.Join(errs...))
	}
	return nil
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "unknown-host"
	}
	return h
}

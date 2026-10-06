package event

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.uber.org/zap"
	lwerrors "loopworker/pkg/errors"
	"loopworker/pkg/logger"
)

type EventFilter struct {
	Types     []EventType
	StartTime *time.Time
	EndTime   *time.Time
	Limit     int
}

// EventStore persists published events and reads them back.
type EventStore interface {
	Append(ctx context.Context, event Event) error
	Load(ctx context.Context, filter EventFilter) ([]Event, error)
	Replay(ctx context.Context, from time.Time, to time.Time) ([]Event, error)
}

// StoreStatistics is implemented by stores that report their own size, so the "the disk
// grew forever" question is answerable without inspecting files by hand.
type StoreStatistics interface {
	Stats() StoreStats
}

var (
	// ErrDiskFull means a write was rejected for lack of space: events are not persisted.
	ErrDiskFull    = errors.New("event store out of disk space")
	ErrStoreClosed = errors.New("event store closed")

	manifestFileName  = "manifest.json"
	segmentFileFormat = "seg-%08d.jsonl"
	manifestVersion   = 1
)

// StoreConfig bounds on-disk growth. Unset (zero) fields take DefaultStoreConfig;
// a negative value disables that limit.
type StoreConfig struct {
	// BasePath holds the events directory (<BasePath>/events).
	BasePath string
	// SegmentMaxBytes seals a segment once it reaches this size. It also bounds boot
	// work, because boot only re-reads the open segment.
	SegmentMaxBytes int64
	// Retention limits: retention drops whole segments, oldest first, and never drops
	// the only remaining data.
	MaxEvents     int64
	MaxTotalBytes int64
	MaxAge        time.Duration
	// CompactInterval is how often the background compactor enforces retention.
	CompactInterval time.Duration
	AutoCompact     bool
	// SyncOnAppend fsyncs every event: durable across power loss, slower.
	SyncOnAppend bool
}

func DefaultStoreConfig(basePath string) StoreConfig {
	return StoreConfig{
		BasePath:        basePath,
		SegmentMaxBytes: 1 << 20,
		MaxEvents:       200000,
		MaxTotalBytes:   64 << 20,
		MaxAge:          14 * 24 * time.Hour,
		CompactInterval: 5 * time.Minute,
		AutoCompact:     true,
	}
}

func (c StoreConfig) withDefaults() StoreConfig {
	d := DefaultStoreConfig("")
	if c.BasePath != "" {
		d.BasePath = c.BasePath
	}
	if c.SegmentMaxBytes > 0 {
		d.SegmentMaxBytes = c.SegmentMaxBytes
	}
	// 0 means "use the default", a negative value means "no limit".
	if c.MaxEvents == 0 {
		c.MaxEvents = d.MaxEvents
	}
	if c.MaxTotalBytes == 0 {
		c.MaxTotalBytes = d.MaxTotalBytes
	}
	if c.MaxAge == 0 {
		c.MaxAge = d.MaxAge
	}
	d.MaxEvents = c.MaxEvents
	d.MaxTotalBytes = c.MaxTotalBytes
	d.MaxAge = c.MaxAge
	if c.CompactInterval > 0 {
		d.CompactInterval = c.CompactInterval
	}
	d.AutoCompact = c.AutoCompact
	d.SyncOnAppend = c.SyncOnAppend
	return d
}

type eventRecord struct {
	ID        string            `json:"id"`
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Payload   interface{}       `json:"payload,omitempty"`
	Metadata  map[string]string `json:"metadata"`
}

// segmentInfo is one entry of the compacted index kept in manifest.json.
type segmentInfo struct {
	ID      int64    `json:"id"`
	Events  int64    `json:"events"`
	Bytes   int64    `json:"bytes"`
	FirstNS int64    `json:"first_ns"`
	LastNS  int64    `json:"last_ns"`
	Types   []string `json:"types,omitempty"`
}

type manifest struct {
	Version       int           `json:"version"`
	NextSegmentID int64         `json:"next_segment_id"`
	Current       segmentInfo   `json:"current"`
	Sealed        []segmentInfo `json:"sealed"`
}

type Retention struct {
	MaxEvents     int64         `json:"max_events"`
	MaxTotalBytes int64         `json:"max_total_bytes"`
	MaxAge        time.Duration `json:"max_age"`
}

type StoreStats struct {
	BasePath    string    `json:"base_path"`
	Events      int64     `json:"events"`
	TotalBytes  int64     `json:"total_bytes"`
	Segments    int       `json:"segments"`
	Oldest      time.Time `json:"oldest"`
	Newest      time.Time `json:"newest"`
	Appended    int64     `json:"appended"`
	Evicted     int64     `json:"evicted_by_retention"`
	WriteErrors int64     `json:"write_errors"`
	DiskFull    bool      `json:"disk_full"`
	BootTime    string    `json:"boot_time"`
	Retention   Retention `json:"retention"`
}

// LocalEventStore appends JSON lines to bounded segments and keeps a compacted manifest
// index, so startup cost does not grow with the event history.
type LocalEventStore struct {
	cfg StoreConfig
	dir string

	mu   sync.Mutex
	m    *manifest
	open segmentOpener
	name string

	bootDur     time.Duration
	appended    atomic.Int64
	evicted     atomic.Int64
	writeErrors atomic.Int64
	diskFull    atomic.Bool
	closed      atomic.Bool

	compactStop chan struct{}
	compactDone chan struct{}
	stopOnce    sync.Once
}

// segmentWriter is the append target. Tests substitute a failing writer to exercise the
// disk-full path.
type segmentWriter interface {
	io.WriteCloser
	Sync() error
}

// segmentOpener produces the writer for a single append. Opening per write is
// deliberate: a handle held between appends makes Windows refuse to unlink or back up
// the event log, pins the file for every other reader, and survives a crash that never
// runs Close.
type segmentOpener func(path string) (segmentWriter, error)

func openSegment(path string) (segmentWriter, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

func NewLocalEventStore(basePath string) (*LocalEventStore, error) {
	return NewLocalEventStoreWithConfig(StoreConfig{BasePath: basePath, AutoCompact: true})
}

func NewLocalEventStoreWithConfig(cfg StoreConfig) (*LocalEventStore, error) {
	cfg = cfg.withDefaults()
	if cfg.BasePath == "" {
		return nil, fmt.Errorf("event store: base path must not be empty (%w)", lwerrors.ErrConfigInvalid)
	}

	dir := filepath.Join(cfg.BasePath, "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, classifyWriteErr("create events dir "+dir, err)
	}

	s := &LocalEventStore{
		cfg:         cfg,
		dir:         dir,
		open:        openSegment,
		compactStop: make(chan struct{}),
		compactDone: make(chan struct{}),
	}

	start := time.Now()
	if err := s.boot(); err != nil {
		return nil, err
	}
	s.bootDur = time.Since(start)

	if s.cfg.AutoCompact {
		go s.compactLoop()
	}
	return s, nil
}

// boot reads one small manifest and re-scans only the open segment.
func (s *LocalEventStore) boot() error {
	m, err := s.readManifest()
	if err != nil {
		return err
	}
	if m == nil {
		if m, err = s.rebuildManifest(); err != nil {
			return err
		}
	}

	kept := m.Sealed[:0]
	for _, seg := range m.Sealed {
		path := filepath.Join(s.dir, fmt.Sprintf(segmentFileFormat, seg.ID))
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return classifyWriteErr("stat segment "+path, err)
		}
		kept = append(kept, seg)
	}
	m.Sealed = kept
	if m.NextSegmentID <= m.Current.ID {
		m.NextSegmentID = m.Current.ID + 1
	}

	name := s.segmentName(m.Current.ID)
	stats, err := scanSegment(name)
	if err != nil {
		return fmt.Errorf("event store: scan open segment %s: %w", name, err)
	}
	stats.ID = m.Current.ID
	m.Current = stats

	s.m = m
	s.name = name

	if err := s.writeManifest(m); err != nil {
		return err
	}
	return s.enforceRetentionLocked()
}

func (s *LocalEventStore) segmentName(id int64) string {
	return filepath.Join(s.dir, fmt.Sprintf(segmentFileFormat, id))
}

func (s *LocalEventStore) readManifest() (*manifest, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, manifestFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, classifyWriteErr("read manifest", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		// A torn manifest is recoverable: the segment files are the source of truth.
		return nil, nil
	}
	if m.Version > manifestVersion {
		return nil, fmt.Errorf("event store: manifest in %s is index v%d and this build reads up to v%d: "+
			"upgrade LoopWorker, or move %s aside (keeping the .jsonl files) to reopen the history with a fresh index",
			s.dir, m.Version, manifestVersion, filepath.Join(s.dir, manifestFileName))
	}
	m.Version = manifestVersion
	return &m, nil
}

// rebuildManifest scans every segment once. It runs only when the manifest is missing or
// unreadable; the normal boot path never reaches it.
func (s *LocalEventStore) rebuildManifest() (*manifest, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, classifyWriteErr("read events dir", err)
	}

	m := &manifest{Version: manifestVersion}
	var ids []int64
	stats := map[int64]segmentInfo{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "seg-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id, convErr := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(name, "seg-"), ".jsonl"), 10, 64)
		if convErr != nil {
			continue
		}
		info, scanErr := scanSegment(filepath.Join(s.dir, name))
		if scanErr != nil {
			continue
		}
		info.ID = id
		ids = append(ids, id)
		stats[id] = info
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	if len(ids) > 0 {
		last := ids[len(ids)-1]
		m.Current = stats[last]
		for _, id := range ids[:len(ids)-1] {
			m.Sealed = append(m.Sealed, stats[id])
		}
		m.NextSegmentID = last + 1
	}
	return m, nil
}

func (s *LocalEventStore) writeManifest(m *manifest) error {
	m.Version = manifestVersion
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("event store: marshal manifest: %w", err)
	}
	tmp := filepath.Join(s.dir, manifestFileName+".tmp")
	final := filepath.Join(s.dir, manifestFileName)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return classifyWriteErr("write manifest "+tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return classifyWriteErr("replace manifest "+final, err)
	}
	return nil
}

// Append writes one JSON line to the open segment, seals it once it reaches
// SegmentMaxBytes and then enforces retention.
func (s *LocalEventStore) Append(ctx context.Context, evt Event) error {
	if evt == nil {
		return fmt.Errorf("event store: append: nil event (%w)", lwerrors.ErrConfigInvalid)
	}
	if s.closed.Load() {
		return fmt.Errorf("event store: append event %s: %w", shortID(evt.ID()), ErrStoreClosed)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("event store: append event %s: %w", shortID(evt.ID()), err)
	}

	rec := eventRecord{
		ID:        evt.ID(),
		Type:      evt.Type(),
		Timestamp: evt.Timestamp(),
		Payload:   evt.Payload(),
		Metadata:  evt.Metadata(),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("event store: marshal event %s (%s): %w", shortID(rec.ID), rec.Type, err)
	}
	line := append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() {
		return fmt.Errorf("event store: append event %s: %w", shortID(rec.ID), ErrStoreClosed)
	}

	n, err := s.appendLineLocked(line)
	if err != nil {
		return s.failWriteLocked(fmt.Sprintf("append event %s (%s)", shortID(rec.ID), rec.Type), err)
	}

	s.recordAppendedLocked(rec, n)
	s.appended.Add(1)

	if s.m.Current.Bytes >= s.cfg.SegmentMaxBytes {
		if err := s.rotateLocked(); err != nil {
			return err
		}
	}
	return s.enforceRetentionLocked()
}

// appendLineLocked writes one line and releases the handle again, so nothing stays
// open between appends.
func (s *LocalEventStore) appendLineLocked(line []byte) (int, error) {
	w, err := s.open(s.name)
	if err != nil {
		return 0, classifyWriteErr("open segment "+s.name, err)
	}
	n, err := w.Write(line)
	if err == nil && s.cfg.SyncOnAppend {
		err = w.Sync()
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return n, err
}

func (s *LocalEventStore) failWriteLocked(op string, err error) error {
	s.writeErrors.Add(1)
	if isNoSpace(err) {
		s.diskFull.Store(true)
	}
	return classifyWriteErr(op, err)
}

func (s *LocalEventStore) recordAppendedLocked(rec eventRecord, n int) {
	ns := rec.Timestamp.UnixNano()
	info := &s.m.Current
	if info.Events == 0 {
		info.FirstNS = ns
	}
	if ns > info.LastNS {
		info.LastNS = ns
	}
	info.Events++
	info.Bytes += int64(n)
	if !containsType(info.Types, string(rec.Type)) {
		info.Types = append(info.Types, string(rec.Type))
	}
}

func containsType(types []string, t string) bool {
	for _, x := range types {
		if x == t {
			return true
		}
	}
	return false
}

// rotateLocked seals the open segment into the manifest and starts the next one. The
// manifest is written before the switch, so a crash leaves either the old segment intact
// or a new empty one, never a hole.
func (s *LocalEventStore) rotateLocked() error {
	if s.m.Current.Events == 0 {
		return nil
	}
	sealed := s.m.Current
	newID := s.m.NextSegmentID
	if newID <= sealed.ID {
		newID = sealed.ID + 1
	}
	next := manifest{
		Version:       manifestVersion,
		NextSegmentID: newID + 1,
		Current:       segmentInfo{ID: newID},
		Sealed:        append(append([]segmentInfo{}, s.m.Sealed...), sealed),
	}
	if err := s.writeManifest(&next); err != nil {
		// Keep appending to the current segment rather than losing events.
		return err
	}
	s.m = &next
	s.name = s.segmentName(newID)
	return nil
}

// enforceRetentionLocked drops whole segments, oldest first, until the store fits its
// limits. Expired data is deleted even while idle, because the compactor calls this too.
func (s *LocalEventStore) enforceRetentionLocked() error {
	cfg := s.cfg

	// An idle store must still shrink: if everything in the open segment expired, seal
	// it so it becomes eligible for eviction.
	if cfg.MaxAge > 0 && s.m.Current.Events > 0 &&
		time.Unix(0, s.m.Current.LastNS).Before(time.Now().Add(-cfg.MaxAge)) {
		if err := s.rotateLocked(); err != nil {
			return err
		}
	}

	var totalEvents, totalBytes int64
	for _, seg := range s.m.Sealed {
		totalEvents += seg.Events
		totalBytes += seg.Bytes
	}
	totalEvents += s.m.Current.Events
	totalBytes += s.m.Current.Bytes

	over := func() bool {
		return (cfg.MaxEvents > 0 && totalEvents > cfg.MaxEvents) ||
			(cfg.MaxTotalBytes > 0 && totalBytes > cfg.MaxTotalBytes)
	}

	cutoff := int64(0)
	if cfg.MaxAge > 0 {
		cutoff = time.Now().Add(-cfg.MaxAge).UnixNano()
	}

	kept := s.m.Sealed[:0]
	var evicted int64
	for i, seg := range s.m.Sealed {
		expired := cutoff > 0 && seg.LastNS < cutoff
		drop := expired
		if !drop && over() && !(i == len(s.m.Sealed)-1 && s.m.Current.Events == 0) {
			drop = true
		}
		if drop {
			totalEvents -= seg.Events
			totalBytes -= seg.Bytes
			evicted += seg.Events
			path := filepath.Join(s.dir, fmt.Sprintf(segmentFileFormat, seg.ID))
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return classifyWriteErr("remove segment "+path, err)
			}
			continue
		}
		kept = append(kept, seg)
	}
	s.m.Sealed = kept

	if evicted > 0 {
		s.evicted.Add(evicted)
		return s.writeManifest(s.m)
	}
	return nil
}

// Load returns stored events matching the filter, oldest first, bounded by Limit.
func (s *LocalEventStore) Load(ctx context.Context, filter EventFilter) ([]Event, error) {
	paths, err := s.readPlan(filter)
	if err != nil {
		return nil, err
	}

	var result []Event
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, err := appendMatching(path, filter, &result); err != nil {
			return nil, err
		}
		if filter.Limit > 0 && len(result) >= filter.Limit {
			result = result[:filter.Limit]
			break
		}
	}
	return result, nil
}

// Replay returns every event with a timestamp in [from, to].
func (s *LocalEventStore) Replay(ctx context.Context, from time.Time, to time.Time) ([]Event, error) {
	return s.Load(ctx, EventFilter{StartTime: &from, EndTime: &to})
}

func (s *LocalEventStore) readPlan(filter EventFilter) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var paths []string
	for _, seg := range s.m.Sealed {
		if !segmentInWindow(seg, filter) {
			continue
		}
		paths = append(paths, filepath.Join(s.dir, fmt.Sprintf(segmentFileFormat, seg.ID)))
	}
	if s.m.Current.Events > 0 {
		paths = append(paths, s.segmentName(s.m.Current.ID))
	}
	sort.Strings(paths)
	return paths, nil
}

func segmentInWindow(seg segmentInfo, filter EventFilter) bool {
	if filter.StartTime != nil && seg.LastNS > 0 && seg.LastNS < filter.StartTime.UnixNano() {
		return false
	}
	if filter.EndTime != nil && seg.FirstNS > 0 && seg.FirstNS > filter.EndTime.UnixNano() {
		return false
	}
	if len(filter.Types) > 0 && len(seg.Types) > 0 {
		found := false
		for _, want := range filter.Types {
			if containsType(seg.Types, string(want)) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func appendMatching(path string, filter EventFilter, result *[]Event) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, classifyWriteErr("open segment "+path, err)
	}
	defer f.Close()

	count := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec eventRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			// A torn final line from a crash: skip it and serve the rest.
			continue
		}
		if !matchesFilter(rec, filter) {
			continue
		}
		*result = append(*result, &BaseEvent{
			id:        rec.ID,
			eventType: rec.Type,
			timestamp: rec.Timestamp,
			payload:   rec.Payload,
			metadata:  rec.Metadata,
		})
		count++
		if filter.Limit > 0 && len(*result) >= filter.Limit {
			break
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return count, classifyWriteErr("read segment "+path, err)
	}
	return count, nil
}

func matchesFilter(record eventRecord, filter EventFilter) bool {
	if len(filter.Types) > 0 {
		found := false
		for _, t := range filter.Types {
			if record.Type == t {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.StartTime != nil && record.Timestamp.Before(*filter.StartTime) {
		return false
	}
	if filter.EndTime != nil && record.Timestamp.After(*filter.EndTime) {
		return false
	}
	return true
}

// scanSegment counts events and finds the time range of one segment file.
func scanSegment(path string) (segmentInfo, error) {
	info := segmentInfo{}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return info, nil
		}
		return info, err
	}
	defer f.Close()

	if stat, err := f.Stat(); err == nil {
		info.Bytes = stat.Size()
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec eventRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		ns := rec.Timestamp.UnixNano()
		if info.Events == 0 {
			info.FirstNS = ns
		}
		if ns > info.LastNS {
			info.LastNS = ns
		}
		info.Events++
		if !containsType(info.Types, string(rec.Type)) {
			info.Types = append(info.Types, string(rec.Type))
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return info, err
	}
	return info, nil
}

// Compact enforces retention now; the background compactor calls it and an operator
// maintenance endpoint can too.
func (s *LocalEventStore) Compact(ctx context.Context) error {
	if s.closed.Load() {
		return fmt.Errorf("event store: compact: %w", ErrStoreClosed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enforceRetentionLocked()
}

func (s *LocalEventStore) Stats() StoreStats {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := StoreStats{
		BasePath:    s.cfg.BasePath,
		Appended:    s.appended.Load(),
		Evicted:     s.evicted.Load(),
		WriteErrors: s.writeErrors.Load(),
		DiskFull:    s.diskFull.Load(),
		BootTime:    s.bootDur.String(),
		Retention: Retention{
			MaxEvents:     s.cfg.MaxEvents,
			MaxTotalBytes: s.cfg.MaxTotalBytes,
			MaxAge:        s.cfg.MaxAge,
		},
	}
	if s.m == nil {
		return st
	}
	for _, seg := range s.m.Sealed {
		st.Events += seg.Events
		st.TotalBytes += seg.Bytes
		st.Segments++
		st.merge(seg)
	}
	if s.m.Current.Events > 0 {
		st.Events += s.m.Current.Events
		st.TotalBytes += s.m.Current.Bytes
		st.Segments++
		st.merge(s.m.Current)
	}
	return st
}

func (st *StoreStats) merge(seg segmentInfo) {
	if seg.FirstNS == 0 {
		return
	}
	if st.Oldest.IsZero() || time.Unix(0, seg.FirstNS).Before(st.Oldest) {
		st.Oldest = time.Unix(0, seg.FirstNS)
	}
	if time.Unix(0, seg.LastNS).After(st.Newest) {
		st.Newest = time.Unix(0, seg.LastNS)
	}
}

// BootDuration is how long the store took to open. It stays flat as history grows, which
// is the point of the compacted index.
func (s *LocalEventStore) BootDuration() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bootDur
}

func (s *LocalEventStore) compactLoop() {
	defer close(s.compactDone)
	ticker := time.NewTicker(s.cfg.CompactInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.compactStop:
			return
		case <-ticker.C:
			if err := s.Compact(context.Background()); err != nil && !errors.Is(err, ErrStoreClosed) {
				logger.Error("event store compaction failed", zap.Error(err))
			}
		}
	}
}

// Close flushes the manifest and releases the segment files. Idempotent.
func (s *LocalEventStore) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.stopOnce.Do(func() { close(s.compactStop) })
	if s.cfg.AutoCompact {
		<-s.compactDone
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.m == nil {
		return nil
	}
	if err := s.writeManifest(s.m); err != nil {
		return fmt.Errorf("event store: close: %w", err)
	}
	return nil
}

// classifyWriteErr turns a filesystem error into something an operator can act on.
func classifyWriteErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if isNoSpace(err) {
		return fmt.Errorf("event store: %s: %w (ENOSPC): free space on the volume holding the events directory and restart; "+
			"events written since are NOT persisted. Set events.max_age, events.max_events or events.max_total_bytes "+
			"so it cannot fill up again: %v", op, ErrDiskFull, err)
	}
	return fmt.Errorf("event store: %s: %w: %v", op, lwerrors.ErrDatabaseError, err)
}

func isNoSpace(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrDiskFull) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.ENOSPC || errno == syscall.EDQUOT
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return isNoSpace(pathErr.Err)
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return isNoSpace(linkErr.Err)
	}
	return false
}

// shortID never panics on IDs shorter than 8 characters.
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

var (
	_ EventStore      = (*LocalEventStore)(nil)
	_ StoreStatistics = (*LocalEventStore)(nil)
)

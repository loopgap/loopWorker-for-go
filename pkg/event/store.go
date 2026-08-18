package event

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	lwerrors "loopworker/pkg/errors"
)

type EventFilter struct {
	Types     []EventType
	StartTime *time.Time
	EndTime   *time.Time
	Limit     int
}

type EventStore interface {
	Append(ctx context.Context, event Event) error
	Load(ctx context.Context, filter EventFilter) ([]Event, error)
	Snapshot(ctx context.Context, stateID string, state []byte) error
	LoadSnapshot(ctx context.Context, stateID string) ([]byte, error)
	Replay(ctx context.Context, from time.Time, to time.Time) ([]Event, error)
}

type eventRecord struct {
	ID        string            `json:"id"`
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Payload   interface{}       `json:"payload"`
	Metadata  map[string]string `json:"metadata"`
}

type LocalEventStore struct {
	basePath string
	mu       sync.RWMutex
	index    []eventRecord
}

func NewLocalEventStore(basePath string) (*LocalEventStore, error) {
	eventsDir := filepath.Join(basePath, "events")
	snapshotDir := filepath.Join(basePath, "snapshots")

	if err := os.MkdirAll(eventsDir, 0755); err != nil {
		return nil, fmt.Errorf("create events dir: %w", err)
	}
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return nil, fmt.Errorf("create snapshots dir: %w", err)
	}

	store := &LocalEventStore{
		basePath: basePath,
		index:    make([]eventRecord, 0),
	}

	if err := store.loadIndex(); err != nil {
		return nil, fmt.Errorf("load index: %w", err)
	}

	return store, nil
}

func (s *LocalEventStore) Append(ctx context.Context, event Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	record := eventRecord{
		ID:        event.ID(),
		Type:      event.Type(),
		Timestamp: event.Timestamp(),
		Payload:   event.Payload(),
		Metadata:  event.Metadata(),
	}

	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	filename := fmt.Sprintf("%s_%s.json", record.Timestamp.Format("20060102_150405.000"), record.ID[:8])
	filepath := filepath.Join(s.basePath, "events", filename)

	if err := os.WriteFile(filepath, data, 0644); err != nil {
		return fmt.Errorf("write event file: %w", err)
	}

	s.index = append(s.index, record)
	return nil
}

func (s *LocalEventStore) Load(ctx context.Context, filter EventFilter) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Event
	for _, record := range s.index {
		if !s.matchesFilter(record, filter) {
			continue
		}

		event := &BaseEvent{
			id:        record.ID,
			eventType: record.Type,
			timestamp: record.Timestamp,
			payload:   record.Payload,
			metadata:  record.Metadata,
		}
		result = append(result, event)

		if filter.Limit > 0 && len(result) >= filter.Limit {
			break
		}
	}

	return result, nil
}

func (s *LocalEventStore) Snapshot(ctx context.Context, stateID string, state []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshotDir := filepath.Join(s.basePath, "snapshots")
	filename := fmt.Sprintf("%s_%s.json", time.Now().Format("20060102_150405"), stateID)
	filepath := filepath.Join(snapshotDir, filename)

	return os.WriteFile(filepath, state, 0644)
}

func (s *LocalEventStore) LoadSnapshot(ctx context.Context, stateID string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshotDir := filepath.Join(s.basePath, "snapshots")
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("read snapshots dir: %w", err)
	}

	var latestFile string
	var latestTime time.Time

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestTime) {
			latestTime = info.ModTime()
			latestFile = entry.Name()
		}
	}

	if latestFile == "" {
		return nil, fmt.Errorf("%w: %s", lwerrors.ErrDatabaseError, stateID)
	}

	return os.ReadFile(filepath.Join(snapshotDir, latestFile))
}

func (s *LocalEventStore) Replay(ctx context.Context, from time.Time, to time.Time) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []Event
	for _, record := range s.index {
		if record.Timestamp.Before(from) || record.Timestamp.After(to) {
			continue
		}

		event := &BaseEvent{
			id:        record.ID,
			eventType: record.Type,
			timestamp: record.Timestamp,
			payload:   record.Payload,
			metadata:  record.Metadata,
		}
		result = append(result, event)
	}

	return result, nil
}

func (s *LocalEventStore) matchesFilter(record eventRecord, filter EventFilter) bool {
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

func (s *LocalEventStore) loadIndex() error {
	eventsDir := filepath.Join(s.basePath, "events")
	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read events dir: %w", err)
	}

	type namedEntry struct {
		name    string
		modTime time.Time
	}

	var sortedEntries []namedEntry
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		sortedEntries = append(sortedEntries, namedEntry{name: entry.Name(), modTime: info.ModTime()})
	}

	sort.Slice(sortedEntries, func(i, j int) bool {
		return sortedEntries[i].modTime.Before(sortedEntries[j].modTime)
	})

	for _, ne := range sortedEntries {
		data, err := os.ReadFile(filepath.Join(eventsDir, ne.name))
		if err != nil {
			continue
		}

		var record eventRecord
		if err := json.Unmarshal(data, &record); err != nil {
			continue
		}

		s.index = append(s.index, record)
	}

	return nil
}

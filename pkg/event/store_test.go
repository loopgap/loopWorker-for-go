package event

import (
	"context"
	"os"
	"testing"
)

// these tests own the directory they delete, so t.TempDir() is not used: its cleanup
// failure would be reported as "TempDir RemoveAll cleanup" and read like a harness
// problem instead of the handle leak under test.

func ownedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "eventstore-handle-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	return dir
}

// A store must not keep an OS handle on a segment file once the write that needed it
// is done. Windows refuses to unlink an open file, so a leaked handle turns the next
// temp-dir cleanup (or a data-dir backup, or a second process pruning the log) into
// "The process cannot access the file because it is being used by another process".
func TestEventStoreDoesNotHoldSegmentHandleAfterAppend(t *testing.T) {
	dir := ownedTempDir(t)
	store, err := NewLocalEventStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	if err := store.Append(context.Background(), NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)); err != nil {
		t.Fatalf("append: %v", err)
	}

	seg := store.segmentName(0)
	if err := os.Remove(seg); err != nil {
		t.Fatalf("segment is locked after Append although Close was never called: %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("directory is not removable after Close: %v", err)
	}
}

// A second store on the same directory is how readers and maintenance tooling inspect a
// live event log. It must not be able to pin the segment either.
func TestEventStoreSecondInstanceDoesNotLockSegment(t *testing.T) {
	dir := ownedTempDir(t)
	ctx := context.Background()

	first, err := NewLocalEventStore(dir)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	if err := first.Append(ctx, NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)); err != nil {
		t.Fatalf("append: %v", err)
	}

	second, err := NewLocalEventStore(dir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	events, err := second.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("load through second store: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("second store read %d events, want 1", len(events))
	}

	if err := os.Remove(first.segmentName(0)); err != nil {
		t.Fatalf("segment is locked by the reopened stores: %v", err)
	}

	for i, s := range []*LocalEventStore{first, second} {
		if err := s.Close(); err != nil {
			t.Fatalf("close store %d: %v", i, err)
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("directory is not removable after both stores closed: %v", err)
	}
}

// Close is the contract every caller can rely on: after it, nothing in the store's
// directory is held open, even when the background compactor was running.
func TestEventStoreCloseReleasesEverySegmentHandle(t *testing.T) {
	dir := ownedTempDir(t)
	ctx := context.Background()

	store, err := NewLocalEventStoreWithConfig(StoreConfig{
		BasePath:        dir,
		SegmentMaxBytes: 200, // force several rotations
		AutoCompact:     true,
		CompactInterval: 1e9, // never ticks during the test
	})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	for i := 0; i < 40; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if n := countSegments(t, dir); n < 2 {
		t.Fatalf("expected the store to rotate, got %d segment(s)", n)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("Close must release every segment handle: %v", err)
	}
}

func countSegments(t *testing.T, basePath string) int {
	t.Helper()
	entries, err := os.ReadDir(basePath + "/events")
	if err != nil {
		t.Fatalf("read events dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if len(e.Name()) > 6 && e.Name()[:4] == "seg-" && len(e.Name()) > 6 && e.Name()[len(e.Name())-6:] == ".jsonl" {
			n++
		}
	}
	return n
}

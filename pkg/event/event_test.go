package event

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNewEvent(t *testing.T) {
	evt := NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)
	if evt.ID() == "" {
		t.Error("event ID should not be empty")
	}
	if evt.Type() != EventTaskCreated {
		t.Errorf("expected type %s, got %s", EventTaskCreated, evt.Type())
	}
	if evt.Timestamp().IsZero() {
		t.Error("timestamp should not be zero")
	}
	if len(evt.Metadata()) != 0 {
		t.Error("metadata should default to an empty map")
	}
}

func TestEventBusPublishSubscribe(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, 10)
	evt := NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)

	if err := bus.Publish(context.Background(), evt); err != nil {
		t.Fatalf("publish failed: %v", err)
	}
	select {
	case received := <-sub.Chan():
		if received.ID() != evt.ID() {
			t.Errorf("expected event ID %s, got %s", evt.ID(), received.ID())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestEventBusUnsubscribe(t *testing.T) {
	bus := NewEventBus(nil)
	sub := bus.Subscribe(EventTaskCreated, 10)
	bus.Unsubscribe(sub)
	if _, ok := <-sub.Chan(); ok {
		t.Error("channel should be closed after unsubscribe")
	}
	bus.Unsubscribe(sub)
	bus.Unsubscribe(nil)
	if !sub.IsClosed() {
		t.Error("subscriber should report closed")
	}
	if bus.GetStats().SubscriberCount != 0 {
		t.Errorf("subscriber count = %d, want 0", bus.GetStats().SubscriberCount)
	}
}

func TestEventBusGetStats(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	_ = bus.Subscribe(EventTaskCreated, 10)
	if err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil)); err != nil {
		t.Fatal(err)
	}

	stats := bus.GetStats()
	if stats.EventsPublished != 1 {
		t.Errorf("published = %d, want 1", stats.EventsPublished)
	}
	if stats.EventsDelivered != 1 {
		t.Errorf("delivered = %d, want 1", stats.EventsDelivered)
	}
	if stats.EventsDropped != 0 {
		t.Errorf("dropped = %d, want 0", stats.EventsDropped)
	}
	if stats.SubscriberCount != 1 {
		t.Errorf("subscribers = %d, want 1", stats.SubscriberCount)
	}
	if stats.AvgDeliveryTime < 0 || stats.MaxDeliveryTime < 0 {
		t.Error("delivery timings must not be negative")
	}
}

func TestBusGetStatsZeroPublished(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	stats := bus.GetStats()
	if stats.EventsPublished != 0 || stats.EventsDelivered != 0 || stats.EventsDropped != 0 {
		t.Errorf("fresh bus must report zeros: %+v", stats)
	}
	if stats.AvgDeliveryTime != 0 || stats.DroppedEvents != 0 || stats.StoreErrors != 0 {
		t.Errorf("fresh bus must report zeros: %+v", stats)
	}
}

// Item 8: the advertised backpressure must be real, countable and exported.
func TestEventBusBackpressureIsCounted(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, 1)
	for i := 0; i < 10; i++ {
		if err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}

	stats := bus.GetStats()
	if stats.EventsDelivered != 1 {
		t.Errorf("delivered = %d, want 1 (buffer size 1)", stats.EventsDelivered)
	}
	if stats.EventsDropped != 9 {
		t.Errorf("dropped deliveries = %d, want 9", stats.EventsDropped)
	}
	if stats.DroppedEvents != 9 {
		t.Errorf("distinct dropped events = %d, want 9", stats.DroppedEvents)
	}
	if sub.Drops() != 9 {
		t.Errorf("per-subscriber drops = %d, want 9", sub.Drops())
	}

	<-sub.Chan()
	if err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if bus.GetStats().EventsDelivered != 2 {
		t.Errorf("delivered after drain = %d, want 2", bus.GetStats().EventsDelivered)
	}
}

func TestEventBusDropsAcrossMultipleSubscribers(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	fast := bus.Subscribe(EventTaskCreated, 5)
	slow := bus.Subscribe(EventTaskCreated, 1)

	for i := 0; i < 5; i++ {
		_ = bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil))
	}
	stats := bus.GetStats()
	if stats.EventsPublished != 5 {
		t.Errorf("published = %d, want 5", stats.EventsPublished)
	}
	if stats.EventsDelivered != 6 {
		t.Errorf("deliveries = %d, want 6 (5 fast + 1 slow)", stats.EventsDelivered)
	}
	if stats.EventsDropped != 4 || stats.DroppedEvents != 4 {
		t.Errorf("dropped = %d/%d, want 4/4", stats.EventsDropped, stats.DroppedEvents)
	}
	if slow.Drops() != 4 || fast.Drops() != 0 {
		t.Errorf("per-subscriber drops: fast=%d slow=%d, want 0 and 4", fast.Drops(), slow.Drops())
	}
}

func TestPublishToNoSubscribers(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	if err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil)); err != nil {
		t.Errorf("publish with no subscribers should succeed, got %v", err)
	}
	if bus.GetStats().EventsPublished != 1 {
		t.Error("expected 1 published")
	}
}

func TestPublishNilEvent(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()
	if err := bus.Publish(context.Background(), nil); err == nil {
		t.Error("publishing nil must fail")
	}
}

func TestMultipleEventTypes(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	subCreated := bus.Subscribe(EventTaskCreated, 10)
	subCompleted := bus.Subscribe(EventTaskCompleted, 10)

	_ = bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil))
	_ = bus.Publish(context.Background(), NewEvent(EventTaskCompleted, nil, nil))
	_ = bus.Publish(context.Background(), NewEvent(EventTaskFailed, nil, nil))

	for name, ch := range map[string]<-chan Event{
		"task.created":   subCreated.Chan(),
		"task.completed": subCompleted.Chan(),
	} {
		select {
		case evt := <-ch:
			if string(evt.Type()) != name {
				t.Errorf("subscriber for %s got %s", name, evt.Type())
			}
		case <-time.After(time.Second):
			t.Errorf("timeout on %s subscriber", name)
		}
	}
	select {
	case <-subCreated.Chan():
		t.Error("task.failed must not reach the task.created subscriber")
	default:
	}
}

func TestEventMetadata(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, 10)
	evt := NewEvent(EventTaskCreated, "payload", map[string]string{"key": "value"})
	_ = bus.Publish(context.Background(), evt)

	select {
	case received := <-sub.Chan():
		if received.Metadata()["key"] != "value" {
			t.Error("metadata should be preserved")
		}
	case <-time.After(time.Second):
		t.Error("timeout")
	}
}

func TestCloseBusIsIdempotent(t *testing.T) {
	bus := NewEventBus(nil)
	sub := bus.Subscribe(EventTaskCreated, 10)
	bus.Close()
	bus.Close()
	if _, ok := <-sub.Chan(); ok {
		t.Error("channel should be closed")
	}
}

func TestPublishAfterCloseIsSafe(t *testing.T) {
	bus := NewEventBus(nil)
	bus.Subscribe(EventTaskCreated, 10)
	bus.Close()
	if err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil)); err != nil {
		t.Fatalf("publish after close must not panic: %v", err)
	}
}

func TestSubscriberIDsAreUnique(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		sub := bus.Subscribe(EventTaskCreated, 1)
		if sub.ID() == "" || seen[sub.ID()] {
			t.Errorf("subscriber IDs must be unique and non-empty, got %q", sub.ID())
		}
		seen[sub.ID()] = true
	}
	if got := bus.GetStats().SubscriberCount; got != 5 {
		t.Errorf("subscriber count = %d, want 5", got)
	}
}

func TestSubscribeUnbufferedDrops(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, -5)
	if err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if sub.Drops() != 1 {
		t.Error("an unread subscriber must be counted as dropping")
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, 1000)
	var wg sync.WaitGroup
	const n = 50
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			if err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil)); err != nil {
				t.Errorf("publish: %v", err)
			}
		}()
	}
	wg.Wait()

	received := 0
	for {
		select {
		case <-sub.Chan():
			received++
		default:
			goto done
		}
	}
done:
	if received != n {
		t.Errorf("expected %d events, got %d", n, received)
	}
}

func TestConcurrentSubscribeUnsubscribe(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	var wg sync.WaitGroup
	subs := make([]*Subscriber, 100)
	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func(idx int) {
			defer wg.Done()
			subs[idx] = bus.Subscribe(EventTaskCreated, 10)
		}(i)
	}
	wg.Wait()
	if got := bus.GetStats().SubscriberCount; got != 100 {
		t.Errorf("subscribers = %d, want 100", got)
	}

	wg.Add(100)
	for i := 0; i < 100; i++ {
		go func(idx int) {
			defer wg.Done()
			bus.Unsubscribe(subs[idx])
		}(i)
	}
	wg.Wait()
	if got := bus.GetStats().SubscriberCount; got != 0 {
		t.Errorf("subscribers after unsubscribe = %d, want 0", got)
	}
}

func TestConcurrentStatsReads(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()
	bus.Subscribe(EventTaskCreated, 10000)

	var wg sync.WaitGroup
	const n = 500
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil))
			_ = bus.GetStats()
		}()
	}
	wg.Wait()
	if got := bus.GetStats().EventsPublished; got != n {
		t.Errorf("published = %d, want %d", got, n)
	}
}

// ---- store tests ----

func newStore(tb testing.TB, cfg StoreConfig) *LocalEventStore {
	tb.Helper()
	if cfg.BasePath == "" {
		cfg.BasePath = tb.TempDir()
	}
	store, err := NewLocalEventStoreWithConfig(cfg)
	if err != nil {
		tb.Fatalf("create store: %v", err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	return store
}

func TestLocalEventStoreAppendAndLoad(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir()})
	ctx := context.Background()

	if err := store.Append(ctx, NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Append(ctx, NewEvent(EventTaskCompleted, TaskCompletedPayload{TaskID: "t1"}, nil)); err != nil {
		t.Fatalf("append: %v", err)
	}

	events, err := store.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].Type() != EventTaskCreated || events[1].Type() != EventTaskCompleted {
		t.Error("events must come back in append order")
	}
	if events[0].Metadata() == nil {
		t.Error("metadata must not be nil after a round trip")
	}
}

func TestEventStoreFilters(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir()})
	ctx := context.Background()
	now := time.Now()

	_ = store.Append(ctx, NewEvent(EventTaskCreated, nil, nil))
	_ = store.Append(ctx, NewEvent(EventTaskCompleted, nil, nil))
	_ = store.Append(ctx, NewEvent(EventTaskFailed, nil, nil))

	cases := []struct {
		name   string
		filter EventFilter
		want   int
	}{
		{"by type", EventFilter{Types: []EventType{EventTaskCreated}}, 1},
		{"by two types", EventFilter{Types: []EventType{EventTaskCreated, EventTaskFailed}}, 2},
		{"after start", EventFilter{StartTime: ptrTime(now.Add(-time.Minute))}, 3},
		{"before end in the past", EventFilter{EndTime: ptrTime(now.Add(-time.Hour))}, 0},
		{"limit", EventFilter{Limit: 2}, 2},
		{"all", EventFilter{}, 3},
	}
	for _, c := range cases {
		got, err := store.Load(ctx, c.filter)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: got %d, want %d", c.name, len(got), c.want)
		}
	}
}

func TestEventStoreReplayWindow(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir()})
	ctx := context.Background()
	_ = store.Append(ctx, NewEvent(EventTaskCreated, nil, nil))
	_ = store.Append(ctx, NewEvent(EventTaskCompleted, nil, nil))

	now := time.Now()
	if got, _ := store.Replay(ctx, now.Add(-time.Hour), now.Add(time.Hour)); len(got) != 2 {
		t.Errorf("replay returned %d, want 2", len(got))
	}
	if got, _ := store.Replay(ctx, now.Add(time.Hour), now.Add(2*time.Hour)); len(got) != 0 {
		t.Errorf("future replay returned %d, want 0", len(got))
	}
}

func TestEventStorePersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocalEventStore(dir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	ctx := context.Background()
	evt := NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1", TaskType: "echo"}, map[string]string{"k": "v"})
	if err := store.Append(ctx, evt); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened, err := NewLocalEventStore(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	events, err := reopened.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("load after reopen: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event after reopen, got %d", len(events))
	}
	if events[0].ID() != evt.ID() {
		t.Errorf("event ID = %s, want %s", events[0].ID(), evt.ID())
	}
	if events[0].Metadata()["k"] != "v" {
		t.Errorf("metadata lost: %v", events[0].Metadata())
	}
}

func TestEventStoreRotatesIntoSegments(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir(), SegmentMaxBytes: 200, AutoCompact: false})
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: fmt.Sprintf("t%d", i)}, nil)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	stats := store.Stats()
	if stats.Events != 200 {
		t.Errorf("stats events = %d, want 200", stats.Events)
	}
	if stats.Segments < 2 {
		t.Errorf("expected several segments, got %d", stats.Segments)
	}
	events, err := store.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 200 {
		t.Fatalf("loaded %d, want 200", len(events))
	}
	if stats.TotalBytes <= 0 || stats.Newest.Before(stats.Oldest) {
		t.Errorf("stats not describing the store: %+v", stats)
	}
}

// Item 6: retention must actually delete.
func TestEventStoreRetentionByCount(t *testing.T) {
	store := newStore(t, StoreConfig{
		BasePath:        t.TempDir(),
		SegmentMaxBytes: 200,
		MaxEvents:       30,
		AutoCompact:     false,
	})
	ctx := context.Background()
	for i := 0; i < 300; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, nil, nil)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	stats := store.Stats()
	if stats.Evicted == 0 {
		t.Fatal("retention deleted nothing")
	}
	if stats.Events > 60 {
		t.Errorf("events = %d, retention should hold near the 30 cap", stats.Events)
	}
	if files := countSegmentFiles(t, store.dir); files > 40 {
		t.Errorf("segment files on disk = %d, expected the oldest to be gone", files)
	}
	if got, err := store.Load(ctx, EventFilter{}); err != nil || len(got) == 0 {
		t.Errorf("retained events unreadable: n=%d err=%v", len(got), err)
	}
}

func TestEventStoreRetentionByBytes(t *testing.T) {
	store := newStore(t, StoreConfig{
		BasePath:        t.TempDir(),
		SegmentMaxBytes: 512,
		MaxTotalBytes:   4096,
		AutoCompact:     false,
	})
	ctx := context.Background()
	payload := TaskCreatedPayload{TaskID: strings.Repeat("x", 128)}
	for i := 0; i < 100; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, payload, nil)); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if got := store.Stats().TotalBytes; got > 8192 {
		t.Errorf("total bytes = %d, retention should keep it near 4096", got)
	}
}

func TestEventStoreRetentionByAge(t *testing.T) {
	store := newStore(t, StoreConfig{
		BasePath:        t.TempDir(),
		SegmentMaxBytes: 1 << 20,
		MaxAge:          40 * time.Millisecond,
		AutoCompact:     false,
	})
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(80 * time.Millisecond)

	if err := store.Compact(ctx); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if got := store.Stats(); got.Events != 0 || got.Evicted < 5 {
		t.Errorf("expired events should be gone: %+v", got)
	}
	for _, f := range segmentFiles(t, store.dir) {
		if info, err := os.Stat(f); err == nil && info.Size() > 0 {
			t.Errorf("segment %s still holds expired data (%d bytes)", filepath.Base(f), info.Size())
		}
	}
}

func TestEventStoreBackgroundCompactorRuns(t *testing.T) {
	store := newStore(t, StoreConfig{
		BasePath:        t.TempDir(),
		SegmentMaxBytes: 1 << 20,
		MaxAge:          20 * time.Millisecond,
		CompactInterval: 10 * time.Millisecond,
		AutoCompact:     true,
	})
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if store.Stats().Events == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("background compactor never enforced age retention")
}

func TestEventStoreRetentionKeepsNewestData(t *testing.T) {
	store := newStore(t, StoreConfig{
		BasePath: t.TempDir(), SegmentMaxBytes: 150, MaxEvents: 5, AutoCompact: false,
	})
	ctx := context.Background()
	lastID := ""
	for i := 0; i < 50; i++ {
		lastID = fmt.Sprintf("task-%d", i)
		if err := store.Append(ctx, NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: lastID}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("nothing left after retention")
	}
	payload, ok := got[len(got)-1].Payload().(map[string]interface{})
	if !ok {
		t.Fatalf("payload type = %T", got[len(got)-1].Payload())
	}
	if payload["TaskID"] != lastID {
		t.Errorf("the newest event must survive retention, got %v", payload["TaskID"])
	}
}

// Item 6: startup must not read the whole history.
func TestEventStoreBootUsesTheCompactedIndex(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, StoreConfig{BasePath: dir, SegmentMaxBytes: 4096, AutoCompact: false})
	ctx := context.Background()
	for i := 0; i < 2000; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: fmt.Sprintf("t%d", i)}, nil)); err != nil {
			t.Fatal(err)
		}
	}
	segments := countSegmentFiles(t, dir)
	if segments < 2 {
		t.Fatalf("expected several segments, got %d", segments)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewLocalEventStoreWithConfig(StoreConfig{BasePath: dir, SegmentMaxBytes: 4096, AutoCompact: false})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()

	if got := reopened.Stats().Events; got != 2000 {
		t.Errorf("events after reopen = %d, want 2000", got)
	}
	// Only the open segment may be re-read; sealed ones are trusted from the manifest.
	opened, err := reopened.Load(ctx, EventFilter{Types: []EventType{EventTaskCreated}, Limit: 1})
	if err != nil || len(opened) != 1 {
		t.Fatalf("read after reopen: n=%d err=%v", len(opened), err)
	}
}

// A missing or torn manifest must be recoverable from the segments themselves.
func TestEventStoreRebuildsMissingManifest(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, StoreConfig{BasePath: dir, SegmentMaxBytes: 300, AutoCompact: false})
	ctx := context.Background()
	for i := 0; i < 40; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "events", manifestFileName)
	if err := os.WriteFile(manifestPath, []byte("{ torn"), 0o644); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewLocalEventStore(dir)
	if err != nil {
		t.Fatalf("a store with a torn manifest should recover: %v", err)
	}
	defer reopened.Close()

	events, err := reopened.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 40 {
		t.Errorf("recovered %d events, want 40", len(events))
	}
}

// A crash mid-append leaves a torn line; boot and reads must survive it.
func TestEventStoreSkipsTornLine(t *testing.T) {
	dir := t.TempDir()
	store := newStore(t, StoreConfig{BasePath: dir, AutoCompact: false})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	f, err := os.OpenFile(store.name, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"id":"cut`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	events, err := store.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("load with a torn line: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 readable events, got %d", len(events))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewLocalEventStoreWithConfig(StoreConfig{BasePath: dir, AutoCompact: false})
	if err != nil {
		t.Fatalf("boot over a torn line: %v", err)
	}
	defer reopened.Close()
	if got := reopened.Stats().Events; got != 3 {
		t.Errorf("events after boot = %d, want 3", got)
	}
}

func TestEventStoreRejectsNewerManifestVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "events"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]interface{}{"version": 999})
	if err := os.WriteFile(filepath.Join(dir, "events", manifestFileName), data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := NewLocalEventStore(dir)
	if err == nil {
		t.Fatal("a newer index must be refused")
	}
	for _, want := range []string{"upgrade", "aside"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error must be actionable, missing %q: %v", want, err)
		}
	}
}

func TestEventStoreCloseIsIdempotentAndRejectsWrites(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLocalEventStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second close must be clean: %v", err)
	}
	if err := store.Append(context.Background(), NewEvent(EventTaskCreated, nil, nil)); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("expected ErrStoreClosed, got %v", err)
	}
	if err := store.Compact(context.Background()); !errors.Is(err, ErrStoreClosed) {
		t.Errorf("compact after close: %v", err)
	}
}

// Item 6: ENOSPC must be a loud, typed, actionable error instead of a silent drop.
type failingWriter struct{ err error }

func (f *failingWriter) Write(p []byte) (int, error) { return 0, f.err }
func (f *failingWriter) Close() error                { return nil }
func (f *failingWriter) Sync() error                 { return nil }

func failNextWrite(t *testing.T, store *LocalEventStore) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	store.open = func(string) (segmentWriter, error) {
		return &failingWriter{err: &os.PathError{Op: "write", Path: store.name, Err: syscall.ENOSPC}}, nil
	}
}

func TestEventStoreDiskFullIsTypedAndActionable(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir(), AutoCompact: false})
	failNextWrite(t, store)

	err := store.Append(context.Background(), NewEvent(EventTaskCreated, nil, nil))
	if !errors.Is(err, ErrDiskFull) {
		t.Fatalf("expected ErrDiskFull, got %v", err)
	}
	for _, want := range []string{"free space", "max_age", "NOT persisted"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("disk-full error must be actionable, missing %q: %v", want, err)
		}
	}
	stats := store.Stats()
	if !stats.DiskFull || stats.WriteErrors != 1 {
		t.Errorf("the store must report its disk-full state: %+v", stats)
	}
}

func TestEventStoreClassifiesDiskFullErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"raw ENOSPC", syscall.ENOSPC, true},
		{"EDQUOT", syscall.EDQUOT, true},
		{"PathError(ENOSPC)", &os.PathError{Op: "write", Err: syscall.ENOSPC}, true},
		{"LinkError(ENOSPC)", &os.LinkError{Op: "rename", Err: syscall.ENOSPC}, true},
		{"wrapped disk full", fmt.Errorf("wrap: %w", ErrDiskFull), true},
		{"permission", syscall.EACCES, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isNoSpace(c.err); got != c.want {
			t.Errorf("%s: isNoSpace = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBusReportsStoreFailure(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir(), AutoCompact: false})
	failNextWrite(t, store)

	bus := NewEventBus(store)
	defer bus.Close()
	sub := bus.Subscribe(EventTaskCreated, 10)

	err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil))
	if !errors.Is(err, ErrDiskFull) {
		t.Fatalf("publish must surface the disk-full error, got %v", err)
	}
	stats := bus.GetStats()
	if stats.StoreErrors != 1 || !stats.DiskFull {
		t.Errorf("the bus must count store errors: %+v", stats)
	}
	if stats.LastStoreError == "" {
		t.Error("the bus must expose the last store error")
	}
	if stats.EventsPublished != 0 {
		t.Error("an event that could not be persisted must not count as published")
	}
	select {
	case <-sub.Chan():
		t.Error("nothing may be delivered when the durable write failed")
	default:
	}
}

func TestBusStatsIncludeStoreSize(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir(), AutoCompact: false})
	bus := NewEventBus(store)
	defer bus.Close()

	for i := 0; i < 3; i++ {
		if err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	stats := bus.GetStats()
	if stats.StoreEvents != 3 {
		t.Errorf("store events = %d, want 3", stats.StoreEvents)
	}
	if stats.StoreBytes <= 0 {
		t.Errorf("store bytes = %d, want > 0", stats.StoreBytes)
	}
}

func TestEventStoreStatsDescribeTheStore(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir()})
	if store.BootDuration() <= 0 {
		t.Error("boot duration must be recorded")
	}
	st := store.Stats()
	if st.BasePath == "" || st.BootTime == "" {
		t.Errorf("stats must describe the store: %+v", st)
	}
	if st.Retention.MaxEvents <= 0 || st.Retention.MaxTotalBytes <= 0 || st.Retention.MaxAge <= 0 {
		t.Errorf("stats must report the retention limits: %+v", st.Retention)
	}
}

func TestEventStoreConfigValidation(t *testing.T) {
	if _, err := NewLocalEventStoreWithConfig(StoreConfig{}); err == nil {
		t.Error("an empty base path must fail")
	}

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocalEventStore(filepath.Join(blocker, "events")); err == nil {
		t.Error("expected an error when the events directory cannot be created")
	}
}

func TestEventStoreNegativeLimitsDisableRetention(t *testing.T) {
	cfg := StoreConfig{MaxEvents: -1, MaxTotalBytes: -1, MaxAge: -1}.withDefaults()
	if cfg.MaxEvents != -1 || cfg.MaxTotalBytes != -1 || cfg.MaxAge != -1 {
		t.Errorf("negative limits must survive so a caller can disable them: %+v", cfg)
	}
	store := newStore(t, StoreConfig{
		BasePath: t.TempDir(), SegmentMaxBytes: 200,
		MaxEvents: -1, MaxTotalBytes: -1, MaxAge: -1, AutoCompact: false,
	})
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated, nil, nil)); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.Stats(); got.Events != 50 || got.Evicted != 0 {
		t.Errorf("retention disabled but touched data: %+v", got)
	}
}

// Item 6: record.ID[:8] used to panic on short IDs.
type tinyEvent struct {
	id   string
	at   time.Time
	meta map[string]string
}

func (e *tinyEvent) ID() string                  { return e.id }
func (e *tinyEvent) Type() EventType             { return EventTaskCreated }
func (e *tinyEvent) Timestamp() time.Time        { return e.at }
func (e *tinyEvent) Payload() interface{}        { return "small" }
func (e *tinyEvent) Metadata() map[string]string { return e.meta }

func TestEventStoreShortIDsDoNotPanic(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir()})
	ctx := context.Background()

	ids := []string{"", "a", "ab", "abc", "1234567", "12345678", "123456789"}
	for _, id := range ids {
		evt := &tinyEvent{id: id, at: time.Now(), meta: map[string]string{"x": "y"}}
		if err := store.Append(ctx, evt); err != nil {
			t.Fatalf("append id %q: %v", id, err)
		}
	}
	events, err := store.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(ids) {
		t.Errorf("loaded %d, want %d", len(events), len(ids))
	}

	if got := shortID("abc"); got != "abc" {
		t.Errorf("shortID(abc) = %s", got)
	}
	if got := shortID(""); got != "" {
		t.Errorf("shortID('') = %s", got)
	}
	if got := shortID("abcdefghijklmnop"); got != "abcdefgh" {
		t.Errorf("shortID(long) = %s", got)
	}
}

func TestEventStoreAppendBadInput(t *testing.T) {
	store := newStore(t, StoreConfig{BasePath: t.TempDir()})
	ctx := context.Background()

	if err := store.Append(ctx, nil); err == nil {
		t.Error("appending nil must fail")
	}
	if err := store.Append(ctx, &unmarshalableEvent{}); err == nil {
		t.Error("an unmarshalable payload must be reported, not dropped")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := store.Append(cancelled, NewEvent(EventTaskCreated, nil, nil)); !errors.Is(err, context.Canceled) {
		t.Errorf("append with a cancelled context = %v", err)
	}
}

type unmarshalableEvent struct{}

func (e *unmarshalableEvent) ID() string                  { return "bad" }
func (e *unmarshalableEvent) Type() EventType             { return EventTaskCreated }
func (e *unmarshalableEvent) Timestamp() time.Time        { return time.Now() }
func (e *unmarshalableEvent) Payload() interface{}        { return make(chan int) }
func (e *unmarshalableEvent) Metadata() map[string]string { return nil }

func TestEventStoreDefaultConfigIsBounded(t *testing.T) {
	d := DefaultStoreConfig("")
	if d.MaxEvents <= 0 || d.MaxTotalBytes <= 0 || d.MaxAge <= 0 || d.SegmentMaxBytes <= 0 {
		t.Errorf("defaults must bound growth: %+v", d)
	}
	if !d.AutoCompact {
		t.Error("compaction must be on by default")
	}
}

func segmentFiles(tb testing.TB, dir string) []string {
	tb.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "seg-*.jsonl"))
	if err != nil {
		tb.Fatal(err)
	}
	return files
}

func countSegmentFiles(tb testing.TB, basePath string) int {
	tb.Helper()
	n := 0
	for _, f := range segmentFiles(tb, filepath.Join(basePath, "events")) {
		if info, err := os.Stat(f); err == nil && info.Size() > 0 {
			n++
		}
	}
	return n
}

func ptrTime(t time.Time) *time.Time { return &t }

// ---- benchmarks ----

const bootEvents = 10000

// BenchmarkEventStoreBoot is the "after" number: reopening a store that holds 10k events.
// The segment size is swept to show that boot tracks the number of segments, not the
// number of events.
func BenchmarkEventStoreBoot(b *testing.B) {
	sizes := map[string]int64{
		"default_1MiB_segments": 1 << 20,
		"stress_4KiB_segments":  4096,
	}
	for name, size := range sizes {
		b.Run(name, func(b *testing.B) { benchmarkBootWithSegmentSize(b, size) })
	}
}

func benchmarkBootWithSegmentSize(b *testing.B, segmentBytes int64) {
	dir := b.TempDir()
	store, err := NewLocalEventStoreWithConfig(StoreConfig{BasePath: dir, SegmentMaxBytes: segmentBytes, AutoCompact: false})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < bootEvents; i++ {
		if err := store.Append(ctx, NewEvent(EventTaskCreated,
			TaskCreatedPayload{TaskID: fmt.Sprintf("task-%d", i), TaskType: "echo"},
			map[string]string{"k": "v"})); err != nil {
			b.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(countSegmentFiles(b, dir)), "segments")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, err := NewLocalEventStoreWithConfig(StoreConfig{BasePath: dir, SegmentMaxBytes: segmentBytes, AutoCompact: false})
		if err != nil {
			b.Fatal(err)
		}
		if got := s.Stats().Events; got != bootEvents {
			b.Fatalf("events = %d", got)
		}
		if err := s.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkLegacyPerFileBoot measures what the previous implementation did at startup:
// one JSON file per event, index rebuilt by reading every file.
func BenchmarkLegacyPerFileBoot(b *testing.B) {
	eventsDir := filepath.Join(b.TempDir(), "events")
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < bootEvents; i++ {
		rec := eventRecord{
			ID:        fmt.Sprintf("%08x%024x", i, i),
			Type:      EventTaskCreated,
			Timestamp: time.Now(),
			Payload:   TaskCreatedPayload{TaskID: fmt.Sprintf("task-%d", i), TaskType: "echo"},
			Metadata:  map[string]string{"k": "v"},
		}
		data, _ := json.Marshal(rec)
		name := fmt.Sprintf("20240101_000000.000_%s.json", rec.ID[:8])
		if err := os.WriteFile(filepath.Join(eventsDir, name), data, 0o644); err != nil {
			b.Fatal(err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entries, err := os.ReadDir(eventsDir)
		if err != nil {
			b.Fatal(err)
		}
		index := make([]eventRecord, 0, len(entries))
		for _, e := range entries {
			data, err := os.ReadFile(filepath.Join(eventsDir, e.Name()))
			if err != nil {
				continue
			}
			var rec eventRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				continue
			}
			index = append(index, rec)
		}
		if len(index) != bootEvents {
			b.Fatalf("indexed %d", len(index))
		}
	}
}

func BenchmarkEventStoreAppend(b *testing.B) {
	store := newStore(b, StoreConfig{
		BasePath: b.TempDir(), SegmentMaxBytes: 1 << 20,
		MaxEvents: -1, MaxTotalBytes: -1, MaxAge: -1,
	})
	ctx := context.Background()
	evt := NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t"}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Append(ctx, evt); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEventBusPublish(b *testing.B) {
	bus := NewEventBus(nil)
	defer bus.Close()
	bus.Subscribe(EventTaskCreated, 10000)
	evt := NewEvent(EventTaskCreated, nil, nil)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bus.Publish(ctx, evt)
	}
}

func BenchmarkNewEvent(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewEvent(EventTaskCreated, nil, nil)
	}
}

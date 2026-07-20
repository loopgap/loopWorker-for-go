package event

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewEvent(t *testing.T) {
	event := NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)

	if event.ID() == "" {
		t.Error("event ID should not be empty")
	}
	if event.Type() != EventTaskCreated {
		t.Errorf("expected type %s, got %s", EventTaskCreated, event.Type())
	}
	if event.Timestamp().IsZero() {
		t.Error("timestamp should not be zero")
	}
}

func TestEventBusPublishSubscribe(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, 10)
	event := NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)

	ctx := context.Background()
	if err := bus.Publish(ctx, event); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	select {
	case received := <-sub.Chan():
		if received.ID() != event.ID() {
			t.Errorf("expected event ID %s, got %s", event.ID(), received.ID())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestEventBusSyncSubscriber(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.SubscribeSync(EventTaskStarted)
	event := NewEvent(EventTaskStarted, TaskStartedPayload{TaskID: "t1", WorkerID: "w1"}, nil)

	ctx := context.Background()
	if err := bus.Publish(ctx, event); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	select {
	case received := <-sub.Chan():
		if received.Type() != EventTaskStarted {
			t.Errorf("expected type %s, got %s", EventTaskStarted, received.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for sync event")
	}
}

func TestEventBusUnsubscribe(t *testing.T) {
	bus := NewEventBus(nil)

	sub := bus.Subscribe(EventTaskCreated, 10)
	bus.Unsubscribe(sub)

	// After unsubscribe, the channel is closed, so reading returns immediately with zero value
	_, ok := <-sub.Chan()
	if ok {
		t.Error("channel should be closed after unsubscribe")
	}
}

func TestEventBusDoubleUnsubscribe(t *testing.T) {
	bus := NewEventBus(nil)

	sub := bus.Subscribe(EventTaskCreated, 10)
	bus.Unsubscribe(sub)
	bus.Unsubscribe(sub) // Should not panic
}

func TestEventBusGetStats(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	_ = bus.Subscribe(EventTaskCreated, 10)

	evt := NewEvent(EventTaskCreated, nil, nil)
	_ = bus.Publish(context.Background(), evt)

	stats := bus.GetStats()
	if stats.EventsPublished != 1 {
		t.Errorf("expected 1 event published, got %d", stats.EventsPublished)
	}
	if stats.EventsDelivered != 1 {
		t.Errorf("expected 1 event delivered, got %d", stats.EventsDelivered)
	}
	if stats.SubscriberCount != 1 {
		t.Errorf("expected 1 subscriber, got %d", stats.SubscriberCount)
	}
}

func TestEventBusBackpressure(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	_ = bus.Subscribe(EventTaskCreated, 1)

	for i := 0; i < 10; i++ {
		_ = bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil))
	}

	stats := bus.GetStats()
	if stats.EventsDropped == 0 {
		t.Error("expected some events to be dropped due to backpressure")
	}
}

func TestLocalEventStoreAppendAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()
	event1 := NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)
	event2 := NewEvent(EventTaskCompleted, TaskCompletedPayload{TaskID: "t1"}, nil)

	if err := store.Append(ctx, event1); err != nil {
		t.Fatalf("append event1: %v", err)
	}
	if err := store.Append(ctx, event2); err != nil {
		t.Fatalf("append event2: %v", err)
	}

	events, err := store.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %d", len(events))
	}
}

func TestLocalEventStoreFilterByType(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()
	_ = store.Append(ctx, NewEvent(EventTaskCreated, nil, nil))
	_ = store.Append(ctx, NewEvent(EventTaskCompleted, nil, nil))
	_ = store.Append(ctx, NewEvent(EventTaskFailed, nil, nil))

	events, err := store.Load(ctx, EventFilter{Types: []EventType{EventTaskCreated}})
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
}

func TestLocalEventStoreFilterByTime(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()
	now := time.Now()

	_ = store.Append(ctx, NewEvent(EventTaskCreated, nil, nil))

	start := now.Add(-time.Minute)
	events, err := store.Load(ctx, EventFilter{StartTime: &start})
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
}

func TestLocalEventStoreSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()
	state := []byte(`{"counter": 42}`)
	if err := store.Snapshot(ctx, "test-state", state); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	loaded, err := store.LoadSnapshot(ctx, "test-state")
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if string(loaded) != string(state) {
		t.Errorf("expected %s, got %s", state, loaded)
	}
}

func TestLocalEventStoreReplay(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()
	_ = store.Append(ctx, NewEvent(EventTaskCreated, nil, nil))

	from := time.Now().Add(-time.Hour)
	to := time.Now().Add(time.Hour)
	events, err := store.Replay(ctx, from, to)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event, got %d", len(events))
	}
}

func TestEventBusWithStore(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	bus := NewEventBus(store)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, 10)
	event := NewEvent(EventTaskCreated, TaskCreatedPayload{TaskID: "t1"}, nil)

	ctx := context.Background()
	if err := bus.Publish(ctx, event); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-sub.Chan():
	case <-time.After(time.Second):
		t.Error("timeout")
	}

	events, err := store.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("load from store: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event in store, got %d", len(events))
	}
}

func TestLocalEventStorePersistence(t *testing.T) {
	tmpDir := t.TempDir()
	store1, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store1: %v", err)
	}

	ctx := context.Background()
	_ = store1.Append(ctx, NewEvent(EventTaskCreated, nil, nil))

	store2, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store2: %v", err)
	}

	events, err := store2.Load(ctx, EventFilter{})
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event after reload, got %d", len(events))
	}
}

func TestConcurrentPublishSubscribe(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, 1000)
	var wg sync.WaitGroup
	n := 50

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_ = bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil))
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

func TestPublishAfterClose(t *testing.T) {
	bus := NewEventBus(nil)
	sub := bus.Subscribe(EventTaskCreated, 10)
	bus.Close()

	_, ok := <-sub.Chan()
	if ok {
		t.Error("channel should be closed after bus.Close()")
	}
}

func TestPublishToNoSubscribers(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	err := bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil))
	if err != nil {
		t.Errorf("publish with no subscribers should succeed, got: %v", err)
	}

	stats := bus.GetStats()
	if stats.EventsPublished != 1 {
		t.Errorf("expected 1 published, got %d", stats.EventsPublished)
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

	select {
	case evt := <-subCreated.Chan():
		if evt.Type() != EventTaskCreated {
			t.Errorf("expected task.created, got %s", evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout")
	}

	select {
	case evt := <-subCompleted.Chan():
		if evt.Type() != EventTaskCompleted {
			t.Errorf("expected task.completed, got %s", evt.Type())
		}
	case <-time.After(time.Second):
		t.Error("timeout")
	}

	select {
	case <-subCreated.Chan():
		t.Error("should not receive task.failed on task.created subscriber")
	default:
		// correct: no more events
	}
}

func TestEventMetadata(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	sub := bus.Subscribe(EventTaskCreated, 10)
	meta := map[string]string{"key": "value"}
	evt := NewEvent(EventTaskCreated, "payload", meta)
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

func TestEventBusStatsDeliveryTime(t *testing.T) {
	bus := NewEventBus(nil)
	defer bus.Close()

	_ = bus.Subscribe(EventTaskCreated, 10)
	_ = bus.Publish(context.Background(), NewEvent(EventTaskCreated, nil, nil))

	stats := bus.GetStats()
	if stats.EventsPublished != 1 {
		t.Errorf("expected 1 published, got %d", stats.EventsPublished)
	}
	if stats.EventsDelivered != 1 {
		t.Errorf("expected 1 delivered, got %d", stats.EventsDelivered)
	}
}

func TestSubscriberIsClosed(t *testing.T) {
	bus := NewEventBus(nil)
	sub := bus.Subscribe(EventTaskCreated, 10)

	if sub.IsClosed() {
		t.Error("subscriber should not be closed initially")
	}

	bus.Unsubscribe(sub)
	if !sub.IsClosed() {
		t.Error("subscriber should be closed after unsubscribe")
	}
}

func TestLocalEventStoreLoadSnapshotNoFile(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	_, err = store.LoadSnapshot(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error loading nonexistent snapshot")
	}
}

func TestLocalEventStoreReplayNoMatch(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	_ = store.Append(context.Background(), NewEvent(EventTaskCreated, nil, nil))

	future := time.Now().Add(time.Hour)
	farFuture := time.Now().Add(2 * time.Hour)
	events, err := store.Replay(context.Background(), future, farFuture)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events in future replay, got %d", len(events))
	}
}

func TestLocalEventStoreFilterByTimeEnd(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	_ = store.Append(context.Background(), NewEvent(EventTaskCreated, nil, nil))

	past := time.Now().Add(-time.Hour)
	events, err := store.Load(context.Background(), EventFilter{EndTime: &past})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected 0 events before past EndTime, got %d", len(events))
	}
}

// Benchmarks
func BenchmarkEventBusPublish(b *testing.B) {
	bus := NewEventBus(nil)
	defer bus.Close()
	_ = bus.Subscribe(EventTaskCreated, 10000)
	ctx := context.Background()
	evt := NewEvent(EventTaskCreated, nil, nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bus.Publish(ctx, evt)
	}
}

func BenchmarkEventBusPublishParallel(b *testing.B) {
	bus := NewEventBus(nil)
	defer bus.Close()
	_ = bus.Subscribe(EventTaskCreated, 100000)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = bus.Publish(ctx, NewEvent(EventTaskCreated, nil, nil))
		}
	})
}

func BenchmarkEventBusSubscribe(b *testing.B) {
	bus := NewEventBus(nil)
	defer bus.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sub := bus.Subscribe(EventTaskCreated, 1)
		bus.Unsubscribe(sub)
	}
}

func BenchmarkNewEvent(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewEvent(EventTaskCreated, nil, nil)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

func TestEventFilterLimit(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "event_test_limit")
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)

	store, err := NewLocalEventStore(tmpDir)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = store.Append(ctx, NewEvent(EventTaskCreated, nil, nil))
	}

	events, err := store.Load(ctx, EventFilter{Limit: 3})
	if err != nil {
		t.Fatalf("load events: %v", err)
	}
	if len(events) != 3 {
		t.Errorf("expected 3 events with limit, got %d", len(events))
	}
}

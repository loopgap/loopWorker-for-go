// Package event implements a typed, observable event bus — the nervous system of LoopWorker.
//
// Architecture Pattern: Pub/Sub with Backpressure
// =================================================
// The EventBus decouples producers (scheduler, executor) from consumers (observer, API SSE).
// Every state transition in the system emits a typed event, enabling:
//   - Real-time monitoring via SSE (Server-Sent Events)
//   - Prometheus metrics collection
//   - Audit logging and distributed tracing
//
// Design Decisions:
//   - Events are typed (EventType string) to enable selective subscription
//   - Subscribers receive events via buffered channels with configurable capacity
//   - When a subscriber's buffer is full, events are DROPPED (not blocking the producer)
//   - Drops are counted, per subscriber and per bus, and exported through GetStats so a
//     slow consumer is visible instead of silently losing audit data
//   - Publishing never blocks on a consumer; ctx applies to the durable store write only
//
// Usage Example:
//
//	bus := event.NewEventBus(nil)
//	sub := bus.Subscribe(event.EventTaskCompleted, 100)
//	go func() {
//	    for evt := range sub.Chan() {
//	        log.Printf("Task completed: %v", evt.Payload())
//	    }
//	}()
//	bus.Publish(ctx, event.NewEvent(event.EventTaskCompleted, payload, nil))
package event

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"loopworker/pkg/logger"
)

type Subscriber struct {
	id        string
	ch        chan Event
	eventType EventType

	closed atomic.Bool
	drops  atomic.Int64
}

// Drops is how many events this subscriber missed because its buffer was full.
func (s *Subscriber) Drops() int64 { return s.drops.Load() }

type EventBus struct {
	subscribers map[EventType][]*Subscriber
	allSubs     []*Subscriber
	store       EventStore
	mu          sync.RWMutex
	nextID      atomic.Int64
	counts      busCounts
}

// busCounts holds the live counters. They are atomic.Int64/Int32 values rather than
// plain ints cast to *int64, which keeps 64-bit alignment correct on 32-bit builds.
type busCounts struct {
	published        atomic.Int64
	delivered        atomic.Int64
	dropped          atomic.Int64
	droppedEvents    atomic.Int64
	storeErrors      atomic.Int64
	deliveryNanosSum atomic.Int64
	maxDeliveryNanos atomic.Int64
	subscribers      atomic.Int32
	lastStoreErr     atomic.Value // string
	storeDiskFull    atomic.Bool
}

type BusStats struct {
	EventsPublished int64
	// EventsDelivered counts (event, subscriber) pairs that made it into a buffer.
	EventsDelivered int64
	// EventsDropped counts (event, subscriber) pairs missed because a buffer was full.
	EventsDropped int64
	// DroppedEvents counts distinct events missed by at least one live subscriber.
	DroppedEvents   int64
	SubscriberCount int32
	AvgDeliveryTime time.Duration
	MaxDeliveryTime time.Duration
	// StoreErrors / LastStoreError report durable-write failures. DiskFull means the
	// event store rejected writes for lack of space and is no longer persisting.
	StoreErrors    int64
	LastStoreError string
	DiskFull       bool
	// StoreEvents / StoreBytes are filled in when the store reports its own size.
	StoreEvents int64
	StoreBytes  int64
}

func NewEventBus(store EventStore) *EventBus {
	return &EventBus{
		subscribers: make(map[EventType][]*Subscriber),
		store:       store,
	}
}

func (eb *EventBus) Subscribe(eventType EventType, bufferSize int) *Subscriber {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if bufferSize < 0 {
		bufferSize = 0
	}
	sub := &Subscriber{
		id:        "sub-" + strconv.FormatInt(eb.nextID.Add(1), 10),
		ch:        make(chan Event, bufferSize),
		eventType: eventType,
	}

	eb.subscribers[eventType] = append(eb.subscribers[eventType], sub)
	eb.allSubs = append(eb.allSubs, sub)
	eb.counts.subscribers.Add(1)
	return sub
}

func (eb *EventBus) Unsubscribe(sub *Subscriber) {
	if sub == nil {
		return
	}
	eb.mu.Lock()
	defer eb.mu.Unlock()
	eb.removeLocked(sub)
}

func (eb *EventBus) removeLocked(sub *Subscriber) {
	if !sub.closed.CompareAndSwap(false, true) {
		return
	}

	subs := eb.subscribers[sub.eventType]
	for i, s := range subs {
		if s.id == sub.id {
			eb.subscribers[sub.eventType] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	for i, s := range eb.allSubs {
		if s.id == sub.id {
			eb.allSubs = append(eb.allSubs[:i], eb.allSubs[i+1:]...)
			break
		}
	}

	close(sub.ch)
	eb.counts.subscribers.Add(-1)
}

// Publish persists the event (when a store is configured) and hands it to every
// subscriber of its type without ever blocking. A subscriber whose buffer is full drops
// the event, which is counted in GetStats.
func (eb *EventBus) Publish(ctx context.Context, event Event) error {
	if event == nil {
		return errors.New("event bus: publish: nil event")
	}
	start := time.Now()

	if eb.store != nil {
		if err := eb.store.Append(ctx, event); err != nil {
			eb.counts.storeErrors.Add(1)
			eb.counts.lastStoreErr.Store(err.Error())
			if errors.Is(err, ErrDiskFull) {
				eb.counts.storeDiskFull.Store(true)
				logger.Error("event store is out of disk space: event not persisted",
					zap.String("event_id", event.ID()),
					zap.String("event_type", string(event.Type())),
					zap.Error(err))
			}
			return fmt.Errorf("event bus: publish %s (%s): %w", event.Type(), event.ID(), err)
		}
	}

	eb.mu.RLock()
	subs := make([]*Subscriber, len(eb.subscribers[event.Type()]))
	copy(subs, eb.subscribers[event.Type()])
	eb.mu.RUnlock()

	eb.counts.published.Add(1)

	var delivered, dropped int
	for _, sub := range subs {
		if sub.closed.Load() {
			continue
		}
		select {
		case sub.ch <- event:
			delivered++
		default:
			dropped++
			sub.drops.Add(1)
		}
	}
	if delivered > 0 {
		eb.counts.delivered.Add(int64(delivered))
	}
	if dropped > 0 {
		eb.counts.dropped.Add(int64(dropped))
		eb.counts.droppedEvents.Add(1)
	}

	eb.recordDelivery(time.Since(start))
	return nil
}

func (eb *EventBus) recordDelivery(elapsed time.Duration) {
	nanos := elapsed.Nanoseconds()
	eb.counts.deliveryNanosSum.Add(nanos)
	for {
		current := eb.counts.maxDeliveryNanos.Load()
		if nanos <= current {
			return
		}
		if eb.counts.maxDeliveryNanos.CompareAndSwap(current, nanos) {
			return
		}
	}
}

// GetStats returns a snapshot of bus health: throughput, backpressure drops, durable
// store errors and (when the store reports them) its size. This is the method the API
// and metrics endpoints should read.
func (eb *EventBus) GetStats() *BusStats {
	stats := &BusStats{
		EventsPublished: eb.counts.published.Load(),
		EventsDelivered: eb.counts.delivered.Load(),
		EventsDropped:   eb.counts.dropped.Load(),
		DroppedEvents:   eb.counts.droppedEvents.Load(),
		SubscriberCount: eb.counts.subscribers.Load(),
		StoreErrors:     eb.counts.storeErrors.Load(),
		DiskFull:        eb.counts.storeDiskFull.Load(),
	}
	if v, ok := eb.counts.lastStoreErr.Load().(string); ok {
		stats.LastStoreError = v
	}
	if published := stats.EventsPublished; published > 0 {
		stats.AvgDeliveryTime = time.Duration(eb.counts.deliveryNanosSum.Load() / published)
	}
	stats.MaxDeliveryTime = time.Duration(eb.counts.maxDeliveryNanos.Load())

	if eb.store != nil {
		if reporter, ok := eb.store.(StoreStatistics); ok {
			st := reporter.Stats()
			stats.StoreEvents = st.Events
			stats.StoreBytes = st.TotalBytes
			stats.DiskFull = stats.DiskFull || st.DiskFull
		}
	}
	return stats
}

func (eb *EventBus) Close() {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	for _, sub := range eb.allSubs {
		eb.removeLocked(sub)
	}
	eb.subscribers = make(map[EventType][]*Subscriber)
	eb.allSubs = nil
	eb.counts.subscribers.Store(0)
}

func (s *Subscriber) Chan() <-chan Event {
	return s.ch
}

func (s *Subscriber) ID() string {
	return s.id
}

func (s *Subscriber) IsClosed() bool {
	return s.closed.Load()
}

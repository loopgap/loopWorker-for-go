package event

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type Subscriber struct {
	id        string
	ch        chan Event
	eventType EventType
	sync      bool
	closed    int32
}

type EventBus struct {
	subscribers map[EventType][]*Subscriber
	allSubs     []*Subscriber
	store       EventStore
	mu          sync.RWMutex
	nextID      int
	stats       *BusStats
}

type BusStats struct {
	EventsPublished int64
	EventsDelivered int64
	EventsDropped   int64
	SubscriberCount int32
	AvgDeliveryTime time.Duration
	MaxDeliveryTime time.Duration
}

func NewEventBus(store EventStore) *EventBus {
	return &EventBus{
		subscribers: make(map[EventType][]*Subscriber),
		store:       store,
		stats:       &BusStats{},
	}
}

func (eb *EventBus) Subscribe(eventType EventType, bufferSize int) *Subscriber {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	eb.nextID++
	sub := &Subscriber{
		id:        fmt.Sprintf("sub-%d", eb.nextID),
		ch:        make(chan Event, bufferSize),
		eventType: eventType,
		sync:      false,
	}

	eb.subscribers[eventType] = append(eb.subscribers[eventType], sub)
	eb.allSubs = append(eb.allSubs, sub)
	atomic.AddInt32(&eb.stats.SubscriberCount, 1)
	return sub
}

func (eb *EventBus) SubscribeSync(eventType EventType) *Subscriber {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	eb.nextID++
	sub := &Subscriber{
		id:        fmt.Sprintf("sub-%d", eb.nextID),
		ch:        make(chan Event, 1),
		eventType: eventType,
		sync:      true,
	}

	eb.subscribers[eventType] = append(eb.subscribers[eventType], sub)
	eb.allSubs = append(eb.allSubs, sub)
	atomic.AddInt32(&eb.stats.SubscriberCount, 1)
	return sub
}

func (eb *EventBus) Unsubscribe(sub *Subscriber) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if !atomic.CompareAndSwapInt32(&sub.closed, 0, 1) {
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
	atomic.AddInt32(&eb.stats.SubscriberCount, -1)
}

func (eb *EventBus) Publish(ctx context.Context, event Event) error {
	start := time.Now()

	if eb.store != nil {
		if err := eb.store.Append(ctx, event); err != nil {
			return fmt.Errorf("event store append: %w", err)
		}
	}

	eb.mu.RLock()
	subs := make([]*Subscriber, len(eb.subscribers[event.Type()]))
	copy(subs, eb.subscribers[event.Type()])
	eb.mu.RUnlock()

	atomic.AddInt64(&eb.stats.EventsPublished, 1)

	for _, sub := range subs {
		if atomic.LoadInt32(&sub.closed) == 1 {
			continue
		}

		if sub.sync {
			select {
			case sub.ch <- event:
				atomic.AddInt64(&eb.stats.EventsDelivered, 1)
			case <-ctx.Done():
				return ctx.Err()
			default:
				atomic.AddInt64(&eb.stats.EventsDropped, 1)
			}
		} else {
			select {
			case sub.ch <- event:
				atomic.AddInt64(&eb.stats.EventsDelivered, 1)
			default:
				atomic.AddInt64(&eb.stats.EventsDropped, 1)
			}
		}
	}

	elapsed := time.Since(start)
	atomic.AddInt64((*int64)(&eb.stats.AvgDeliveryTime), int64(elapsed))

	// Update max delivery time atomically using compare-and-swap loop
	for {
		current := atomic.LoadInt64((*int64)(&eb.stats.MaxDeliveryTime))
		if elapsed <= time.Duration(current) {
			break
		}
		if atomic.CompareAndSwapInt64((*int64)(&eb.stats.MaxDeliveryTime), current, int64(elapsed)) {
			break
		}
	}

	return nil
}

// PublishBatch publishes a batch of events with a single lock acquisition per event type.
// This reduces lock contention compared to calling Publish for each event individually.
// Events are grouped by type, and each group is dispatched under one RLock.
func (eb *EventBus) PublishBatch(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	start := time.Now()

	// Persist all events first (outside the lock)
	if eb.store != nil {
		for _, evt := range events {
			if err := eb.store.Append(ctx, evt); err != nil {
				return fmt.Errorf("event store append: %w", err)
			}
		}
	}

	// Group events by type to minimise lock acquisitions
	typeGroups := make(map[EventType][]Event)
	for _, evt := range events {
		typeGroups[evt.Type()] = append(typeGroups[evt.Type()], evt)
	}

	var totalPublished, totalDelivered, totalDropped int64

	for _, group := range typeGroups {
		// Snapshot subscribers once per event type
		eb.mu.RLock()
		typ := group[0].Type()
		subs := make([]*Subscriber, len(eb.subscribers[typ]))
		copy(subs, eb.subscribers[typ])
		eb.mu.RUnlock()

		for _, sub := range subs {
			if atomic.LoadInt32(&sub.closed) == 1 {
				continue
			}

			for _, evt := range group {
				if sub.sync {
					select {
					case sub.ch <- evt:
						totalDelivered++
					case <-ctx.Done():
						goto done
					default:
						totalDropped++
					}
				} else {
					select {
					case sub.ch <- evt:
						totalDelivered++
					default:
						totalDropped++
					}
				}
			}
		}
		totalPublished += int64(len(group))
	}
done:

	atomic.AddInt64(&eb.stats.EventsPublished, totalPublished)
	atomic.AddInt64(&eb.stats.EventsDelivered, totalDelivered)
	atomic.AddInt64(&eb.stats.EventsDropped, totalDropped)

	elapsed := time.Since(start)
	atomic.AddInt64((*int64)(&eb.stats.AvgDeliveryTime), int64(elapsed))

	for {
		current := atomic.LoadInt64((*int64)(&eb.stats.MaxDeliveryTime))
		if elapsed <= time.Duration(current) {
			break
		}
		if atomic.CompareAndSwapInt64((*int64)(&eb.stats.MaxDeliveryTime), current, int64(elapsed)) {
			break
		}
	}

	return nil
}

func (eb *EventBus) Close() {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	for _, sub := range eb.allSubs {
		if atomic.CompareAndSwapInt32(&sub.closed, 0, 1) {
			close(sub.ch)
		}
	}
	eb.subscribers = make(map[EventType][]*Subscriber)
	eb.allSubs = nil
	atomic.StoreInt32(&eb.stats.SubscriberCount, 0)
}

func (eb *EventBus) GetStats() *BusStats {
	published := atomic.LoadInt64(&eb.stats.EventsPublished)
	delivered := atomic.LoadInt64(&eb.stats.EventsDelivered)
	dropped := atomic.LoadInt64(&eb.stats.EventsDropped)

	var avgDelivery time.Duration
	if published > 0 {
		avgDelivery = time.Duration(atomic.LoadInt64((*int64)(&eb.stats.AvgDeliveryTime)) / published)
	}

	return &BusStats{
		EventsPublished: published,
		EventsDelivered: delivered,
		EventsDropped:   dropped,
		SubscriberCount: atomic.LoadInt32(&eb.stats.SubscriberCount),
		AvgDeliveryTime: avgDelivery,
		MaxDeliveryTime: time.Duration(atomic.LoadInt64((*int64)(&eb.stats.MaxDeliveryTime))),
	}
}

func (s *Subscriber) Chan() <-chan Event {
	return s.ch
}

func (s *Subscriber) ID() string {
	return s.id
}

func (s *Subscriber) IsClosed() bool {
	return atomic.LoadInt32(&s.closed) == 1
}

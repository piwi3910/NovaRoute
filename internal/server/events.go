// Package server implements the gRPC RouteControl service for NovaRoute.
package server

import (
	"sync"

	pb "github.com/piwi3910/NovaRoute/api/v1"
)

// EventBus provides a publish-subscribe mechanism for RouteEvents. Subscribers
// can filter by owner and event type. Each subscriber receives events on a
// buffered channel; slow consumers that fall behind will have events dropped
// to avoid blocking the publisher.
type EventBus struct {
	subscribers map[uint64]*subscription
	mu          sync.RWMutex
	nextID      uint64
}

// subscription tracks a single subscriber's channel and filter criteria.
type subscription struct {
	ch          chan *pb.RouteEvent
	ownerFilter string
	typeFilter  map[string]struct{}
}

// NewEventBus creates a new EventBus ready to accept subscribers.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[uint64]*subscription),
	}
}

// Subscribe registers a new subscriber that will receive RouteEvents matching
// the given filters. If ownerFilter is non-empty, only events for that owner
// are delivered. If types is non-empty, only events whose EventType name matches
// one of the given strings are delivered. Returns a read-only channel of events.
func (eb *EventBus) Subscribe(ownerFilter string, types []string) <-chan *pb.RouteEvent {
	ch := make(chan *pb.RouteEvent, 256)

	typeFilter := make(map[string]struct{}, len(types))
	for _, t := range types {
		typeFilter[t] = struct{}{}
	}

	eb.mu.Lock()
	id := eb.nextID
	eb.nextID++
	eb.subscribers[id] = &subscription{
		ch:          ch,
		ownerFilter: ownerFilter,
		typeFilter:  typeFilter,
	}
	eb.mu.Unlock()

	return ch
}

// Unsubscribe removes the subscriber associated with the given channel and
// closes the channel. It is safe to call with a channel that has already been
// unsubscribed.
func (eb *EventBus) Unsubscribe(ch <-chan *pb.RouteEvent) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	for id, sub := range eb.subscribers {
		if sub.ch == ch {
			close(sub.ch)
			delete(eb.subscribers, id)
			return
		}
	}
}

// Publish sends an event to all matching subscribers. Events are delivered
// non-blockingly: if a subscriber's channel buffer is full, the event is
// dropped for that subscriber to avoid stalling the publisher.
func (eb *EventBus) Publish(event *pb.RouteEvent) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	for _, sub := range eb.subscribers {
		if !eb.matches(sub, event) {
			continue
		}
		// Non-blocking send; drop events for slow consumers.
		select {
		case sub.ch <- event:
		default:
		}
	}
}

// matches returns true if the event passes the subscription's filters.
func (eb *EventBus) matches(sub *subscription, event *pb.RouteEvent) bool {
	// Owner filter: if set, only deliver events for that owner.
	if sub.ownerFilter != "" && event.GetOwner() != sub.ownerFilter {
		return false
	}
	// Type filter: if set, only deliver events whose type name is in the set.
	if len(sub.typeFilter) > 0 {
		typeName := event.GetType().String()
		if _, ok := sub.typeFilter[typeName]; !ok {
			return false
		}
	}
	return true
}

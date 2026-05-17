package server

import (
	"context"
	"strconv"
	"sync"
)

type event struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties"`
}

type eventBus struct {
	mu     sync.Mutex
	nextID int64
	subs   map[chan event]struct{}
}

func newEventBus() *eventBus {
	return &eventBus{subs: map[chan event]struct{}{}}
}

func (bus *eventBus) publish(eventType string, properties map[string]any) {
	if bus == nil {
		return
	}
	bus.mu.Lock()
	bus.nextID++
	item := event{ID: formatEventID(bus.nextID), Type: eventType, Properties: properties}
	subscribers := make([]chan event, 0, len(bus.subs))
	for ch := range bus.subs {
		subscribers = append(subscribers, ch)
	}
	bus.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- item:
		default:
		}
	}
}

func (bus *eventBus) subscribe(ctx context.Context) (<-chan event, func()) {
	ch := make(chan event, 32)
	if bus == nil {
		close(ch)
		return ch, func() {}
	}
	bus.mu.Lock()
	bus.subs[ch] = struct{}{}
	bus.mu.Unlock()

	unsubscribe := func() {
		bus.mu.Lock()
		if _, ok := bus.subs[ch]; ok {
			delete(bus.subs, ch)
			close(ch)
		}
		bus.mu.Unlock()
	}
	go func() {
		<-ctx.Done()
		unsubscribe()
	}()
	return ch, unsubscribe
}

func formatEventID(id int64) string {
	return "evt_" + strconv.FormatInt(id, 10)
}

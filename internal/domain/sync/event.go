// Package sync contains local sync event contracts shared by storage and HTTP.
package sync

// Event is the serialized sync event contract.
type Event struct {
	ID          string         `json:"id"`
	AggregateID string         `json:"aggregateID"`
	Seq         int64          `json:"seq"`
	Type        string         `json:"type"`
	Data        map[string]any `json:"data"`
}

// HistoryEvent is the storage row returned by /sync/history.
type HistoryEvent struct {
	ID          string         `json:"id"`
	AggregateID string         `json:"aggregate_id"`
	Seq         int64          `json:"seq"`
	Type        string         `json:"type"`
	Data        map[string]any `json:"data"`
}

// Store is the persistence boundary for migrated sync routes.
type Store interface {
	AppendEvents(aggregateID string, events []Event) error
	EventsAfter(cursor map[string]int64) ([]HistoryEvent, error)
}

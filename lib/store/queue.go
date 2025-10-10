package store

import (
	"container/ring"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"crovlune/plaxt/lib/common"
)

// QueuedScrobbleEvent represents a scrobble event awaiting transmission to Trakt.
type QueuedScrobbleEvent struct {
	// Identity
	ID     string `json:"id"`      // UUID v4, generated on enqueue
	UserID string `json:"user_id"` // Foreign key to User.ID

	// Scrobble Data
	ScrobbleBody common.ScrobbleBody `json:"scrobble_body"` // Reuses existing struct
	Action       string              `json:"action"`        // "start" | "pause" | "stop"
	Progress     int                 `json:"progress"`      // Playback progress percentage (0-100)

	// Metadata
	CreatedAt   time.Time `json:"created_at"`   // Original webhook receipt time
	RetryCount  int       `json:"retry_count"`  // Number of send attempts (0-5)
	LastAttempt time.Time `json:"last_attempt"` // Timestamp of most recent send attempt

	// Deduplication Keys
	PlayerUUID string `json:"player_uuid"` // Plex player UUID
	RatingKey  string `json:"rating_key"`  // Plex media rating key
}

// InMemoryBuffer provides fallback storage during backend failures.
// Uses a circular buffer per user with fixed capacity.
type InMemoryBuffer struct {
	ring     *ring.Ring
	capacity int
	mu       sync.RWMutex
}

// NewInMemoryBuffer creates a new circular buffer with the specified capacity.
func NewInMemoryBuffer(capacity int) *InMemoryBuffer {
	return &InMemoryBuffer{
		ring:     ring.New(capacity),
		capacity: capacity,
	}
}

// Push adds an event to the buffer, evicting the oldest if full.
func (b *InMemoryBuffer) Push(event QueuedScrobbleEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ring.Value = event
	b.ring = b.ring.Next()
}

// GetAll retrieves all non-nil events from the buffer.
func (b *InMemoryBuffer) GetAll() []QueuedScrobbleEvent {
	b.mu.RLock()
	defer b.mu.RUnlock()

	var events []QueuedScrobbleEvent
	b.ring.Do(func(v interface{}) {
		if v != nil {
			if event, ok := v.(QueuedScrobbleEvent); ok {
				events = append(events, event)
			}
		}
	})

	return events
}

// Clear removes all events from the buffer.
func (b *InMemoryBuffer) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.ring.Do(func(v interface{}) {
		if v != nil {
			v = nil
		}
	})
}

// Size returns the number of events currently in the buffer.
func (b *InMemoryBuffer) Size() int {
	b.mu.RLock()
	defer b.mu.RUnlock()

	count := 0
	b.ring.Do(func(v interface{}) {
		if v != nil {
			count++
		}
	})

	return count
}

// generateEventID creates a UUID v4 for event identification.
func generateEventID() (string, error) {
	uuid := make([]byte, 16)
	if _, err := rand.Read(uuid); err != nil {
		return "", fmt.Errorf("failed to generate UUID: %w", err)
	}

	// Set version (4) and variant bits
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // Version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // Variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:16]), nil
}

// validateEvent checks if an event has all required fields.
func validateEvent(event QueuedScrobbleEvent) error {
	if event.UserID == "" {
		return errors.New("user_id is required")
	}

	if event.Action != "start" && event.Action != "pause" && event.Action != "stop" {
		return fmt.Errorf("invalid action: %s (must be start, pause, or stop)", event.Action)
	}

	if event.Progress < 0 || event.Progress > 100 {
		return fmt.Errorf("invalid progress: %d (must be 0-100)", event.Progress)
	}

	if event.PlayerUUID == "" {
		return errors.New("player_uuid is required")
	}

	if event.RatingKey == "" {
		return errors.New("rating_key is required")
	}

	return nil
}

// serializeEvent converts a QueuedScrobbleEvent to JSON.
func serializeEvent(event QueuedScrobbleEvent) ([]byte, error) {
	data, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize event: %w", err)
	}
	return data, nil
}

// deserializeEvent converts JSON to a QueuedScrobbleEvent.
func deserializeEvent(data []byte) (QueuedScrobbleEvent, error) {
	var event QueuedScrobbleEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return QueuedScrobbleEvent{}, fmt.Errorf("failed to deserialize event: %w", err)
	}
	return event, nil
}

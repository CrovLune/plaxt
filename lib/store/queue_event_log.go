package store

import (
	"container/ring"
	"sync"
	"time"
)

// QueueLogEvent represents a single queue operation for monitoring/debugging.
type QueueLogEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Operation  string    `json:"operation"` // e.g., "queue_enqueue", "queue_event_scrobbled"
	UserID     string    `json:"user_id"`
	Username   string    `json:"username,omitempty"`
	EventID    string    `json:"event_id,omitempty"`
	QueueSize  int       `json:"queue_size,omitempty"`
	RetryCount int       `json:"retry_count,omitempty"`
	Error      string    `json:"error,omitempty"`
	Details    string    `json:"details,omitempty"`
}

// QueueEventLog is a thread-safe circular buffer for storing recent queue events.
type QueueEventLog struct {
	events   *ring.Ring
	capacity int
	mu       sync.RWMutex
}

// NewQueueEventLog creates a new queue event log with the specified capacity.
func NewQueueEventLog(capacity int) *QueueEventLog {
	return &QueueEventLog{
		events:   ring.New(capacity),
		capacity: capacity,
	}
}

// Append adds a new event to the log (thread-safe).
// Oldest events are automatically evicted when capacity is reached.
func (l *QueueEventLog) Append(event QueueLogEvent) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events.Value = event
	l.events = l.events.Next()
}

// GetRecent returns up to N most recent events in reverse chronological order.
func (l *QueueEventLog) GetRecent(n int) []QueueLogEvent {
	l.mu.RLock()
	defer l.mu.RUnlock()

	if n > l.capacity {
		n = l.capacity
	}

	events := make([]QueueLogEvent, 0, n)
	
	// Collect all non-nil events
	l.events.Do(func(v interface{}) {
		if v != nil {
			if event, ok := v.(QueueLogEvent); ok {
				events = append(events, event)
			}
		}
	})

	// Sort by timestamp descending (most recent first)
	// Simple bubble sort since we're dealing with small slices
	for i := 0; i < len(events)-1; i++ {
		for j := i + 1; j < len(events); j++ {
			if events[i].Timestamp.Before(events[j].Timestamp) {
				events[i], events[j] = events[j], events[i]
			}
		}
	}

	// Return up to N events
	if len(events) > n {
		events = events[:n]
	}

	return events
}

// Clear removes all events from the log.
func (l *QueueEventLog) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events.Do(func(v interface{}) {
		if v != nil {
			v = nil
		}
	})
}

// Size returns the number of events currently in the log.
func (l *QueueEventLog) Size() int {
	l.mu.RLock()
	defer l.mu.RUnlock()

	count := 0
	l.events.Do(func(v interface{}) {
		if v != nil {
			count++
		}
	})

	return count
}

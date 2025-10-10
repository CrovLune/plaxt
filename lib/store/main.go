package store

import (
	"context"

	"crovlune/plaxt/lib/common"
)

// Store is the interface for All the store types
type Store interface {
	// ========== EXISTING METHODS ==========
	WriteUser(user User)
	GetUser(id string) *User
	GetUserByName(username string) *User
	DeleteUser(id, username string) bool
	ListUsers() []User
	GetScrobbleBody(playerUuid, ratingKey string) common.CacheItem
	WriteScrobbleBody(item common.CacheItem)
	Ping(ctx context.Context) error

	// ========== QUEUE METHODS ==========

	// EnqueueScrobble adds a scrobble event to the queue.
	// If storage is unavailable, implementation should use in-memory fallback buffer.
	// If queue is at capacity (1000 events per user), oldest event is evicted (FIFO).
	//
	// Returns:
	//   - error: storage failure (logged but non-fatal, fallback buffer engaged)
	EnqueueScrobble(ctx context.Context, event QueuedScrobbleEvent) error

	// DequeueScrobbles retrieves oldest N events for a specific user in chronological order.
	// Events remain in queue until explicitly deleted via DeleteQueuedScrobble.
	//
	// Parameters:
	//   - userID: User to retrieve events for
	//   - limit: Maximum events to retrieve (typically 100 for batch processing)
	//
	// Returns:
	//   - []QueuedScrobbleEvent: Events ordered by CreatedAt ASC
	//   - error: storage failure
	DequeueScrobbles(ctx context.Context, userID string, limit int) ([]QueuedScrobbleEvent, error)

	// DeleteQueuedScrobble removes a successfully sent event from the queue.
	//
	// Parameters:
	//   - eventID: UUID of the event to delete
	//
	// Returns:
	//   - error: storage failure (logged, drain continues with next event)
	DeleteQueuedScrobble(ctx context.Context, eventID string) error

	// UpdateQueuedScrobbleRetry increments retry count and updates last attempt timestamp.
	// Used after transient failures (429, 503) before re-queueing for backoff.
	//
	// Parameters:
	//   - eventID: UUID of the event
	//   - retryCount: New retry count (incremented by caller)
	//
	// Returns:
	//   - error: storage failure
	UpdateQueuedScrobbleRetry(ctx context.Context, eventID string, retryCount int) error

	// GetQueueSize returns current queue event count for a specific user.
	// Used for capacity enforcement and observability logging.
	//
	// Parameters:
	//   - userID: User to count events for
	//
	// Returns:
	//   - int: Number of queued events
	//   - error: storage failure
	GetQueueSize(ctx context.Context, userID string) (int, error)

	// GetQueueStatus returns observability metrics for a specific user's queue.
	// Constructs QueueStatus from current queue state (not persisted separately).
	//
	// Parameters:
	//   - userID: User to retrieve status for
	//
	// Returns:
	//   - common.QueueStatus: Current metrics (queue size, oldest event, etc.)
	//   - error: storage failure
	GetQueueStatus(ctx context.Context, userID string) (common.QueueStatus, error)

	// ListUsersWithQueuedEvents returns all user IDs that have pending queue events.
	// Used during drain initiation to spawn per-user drain goroutines.
	//
	// Returns:
	//   - []string: User IDs with queue size > 0
	//   - error: storage failure
	ListUsersWithQueuedEvents(ctx context.Context) ([]string, error)

	// PurgeQueueForUser deletes all queued events for a specific user.
	// Used when user is deleted or credentials are revoked.
	//
	// Parameters:
	//   - userID: User whose queue should be purged
	//
	// Returns:
	//   - int: Number of events purged
	//   - error: storage failure
	PurgeQueueForUser(ctx context.Context, userID string) (int, error)
}

// Utils
func flatTransform(s string) []string { return []string{} }

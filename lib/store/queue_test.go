package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"crovlune/plaxt/lib/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cleanupQueue removes the queue directory for testing
func cleanupQueue(t *testing.T) {
	t.Helper()
	_ = os.RemoveAll("keystore/queue")
}

// TestQueueBasicOperations tests enqueue, dequeue, and delete operations
func TestQueueBasicOperations(t *testing.T) {
	stores := []struct {
		name  string
		store Store
	}{
		{"Disk", NewDiskStore()},
		// Add Redis and PostgreSQL stores when ready
		// {"Redis", NewRedisStore("localhost:6379", "", 0)},
		// {"PostgreSQL", NewPostgresqlStore("connection-string")},
	}

	for _, tc := range stores {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := tc.store

			// Create test event
			title := "Test Movie"
			event := QueuedScrobbleEvent{
				ID:       "test-event-1",
				UserID:   "user-123",
				ScrobbleBody: common.ScrobbleBody{
					Progress: 95,
					Movie:    &common.Movie{Title: &title},
				},
				Action:     "stop",
				Progress:   95,
				CreatedAt:  time.Now(),
				RetryCount: 0,
				PlayerUUID: "player-1",
				RatingKey:  "rating-1",
			}

			// Test enqueue
			err := store.EnqueueScrobble(ctx, event)
			assert.NoError(t, err, "should enqueue event successfully")

			// Verify queue size
			size, err := store.GetQueueSize(ctx, event.UserID)
			assert.NoError(t, err)
			assert.Equal(t, 1, size, "queue should have 1 event")

			// Test dequeue
			events, err := store.DequeueScrobbles(ctx, event.UserID, 10)
			assert.NoError(t, err)
			require.Len(t, events, 1, "should dequeue 1 event")
			assert.Equal(t, event.ID, events[0].ID)
			assert.Equal(t, event.UserID, events[0].UserID)

			// Test delete
			err = store.DeleteQueuedScrobble(ctx, event.ID)
			assert.NoError(t, err, "should delete event successfully")

			// Verify queue is empty
			size, err = store.GetQueueSize(ctx, event.UserID)
			assert.NoError(t, err)
			assert.Equal(t, 0, size, "queue should be empty after delete")
		})
	}
}

// TestQueueOrdering tests that events are returned in chronological order
func TestQueueOrdering(t *testing.T) {
	cleanupQueue(t)
	defer cleanupQueue(t)

	ctx := context.Background()
	store := NewDiskStore()

	userID := "user-ordering"
	baseTime := time.Now()

	// Create events with different timestamps
	events := []QueuedScrobbleEvent{
		{
			ID:         "event-3",
			UserID:     userID,
			Action:     "stop",
			CreatedAt:  baseTime.Add(3 * time.Second),
			PlayerUUID: "player-3",
			RatingKey:  "rating-3",
		},
		{
			ID:         "event-1",
			UserID:     userID,
			Action:     "stop",
			CreatedAt:  baseTime.Add(1 * time.Second),
			PlayerUUID: "player-1",
			RatingKey:  "rating-1",
		},
		{
			ID:         "event-2",
			UserID:     userID,
			Action:     "stop",
			CreatedAt:  baseTime.Add(2 * time.Second),
			PlayerUUID: "player-2",
			RatingKey:  "rating-2",
		},
	}

	// Enqueue in random order
	for _, event := range events {
		err := store.EnqueueScrobble(ctx, event)
		require.NoError(t, err)
	}

	// Dequeue and verify chronological order
	dequeued, err := store.DequeueScrobbles(ctx, userID, 10)
	require.NoError(t, err)
	require.Len(t, dequeued, 3)

	assert.Equal(t, "event-1", dequeued[0].ID, "oldest event should be first")
	assert.Equal(t, "event-2", dequeued[1].ID, "middle event should be second")
	assert.Equal(t, "event-3", dequeued[2].ID, "newest event should be last")
}

// TestQueueUserIsolation tests that events are isolated by user
func TestQueueUserIsolation(t *testing.T) {
	cleanupQueue(t)
	defer cleanupQueue(t)

	ctx := context.Background()
	store := NewDiskStore()

	// Create events for different users
	event1 := QueuedScrobbleEvent{
		ID:         "user1-event",
		UserID:     "user-1",
		Action:     "stop",
		CreatedAt:  time.Now(),
		PlayerUUID: "player-1",
		RatingKey:  "rating-1",
	}

	event2 := QueuedScrobbleEvent{
		ID:         "user2-event",
		UserID:     "user-2",
		Action:     "stop",
		CreatedAt:  time.Now(),
		PlayerUUID: "player-2",
		RatingKey:  "rating-2",
	}

	// Enqueue for both users
	require.NoError(t, store.EnqueueScrobble(ctx, event1))
	require.NoError(t, store.EnqueueScrobble(ctx, event2))

	// Verify user 1 only sees their event
	user1Events, err := store.DequeueScrobbles(ctx, "user-1", 10)
	require.NoError(t, err)
	require.Len(t, user1Events, 1)
	assert.Equal(t, "user1-event", user1Events[0].ID)

	// Verify user 2 only sees their event
	user2Events, err := store.DequeueScrobbles(ctx, "user-2", 10)
	require.NoError(t, err)
	require.Len(t, user2Events, 1)
	assert.Equal(t, "user2-event", user2Events[0].ID)
}

// TestQueueRetryUpdate tests updating retry count
func TestQueueRetryUpdate(t *testing.T) {
	cleanupQueue(t)
	defer cleanupQueue(t)

	ctx := context.Background()
	store := NewDiskStore()

	event := QueuedScrobbleEvent{
		ID:         "retry-event",
		UserID:     "user-retry",
		Action:     "stop",
		CreatedAt:  time.Now(),
		RetryCount: 0,
		PlayerUUID: "player-retry",
		RatingKey:  "rating-retry",
	}

	// Enqueue event
	require.NoError(t, store.EnqueueScrobble(ctx, event))

	// Update retry count
	err := store.UpdateQueuedScrobbleRetry(ctx, event.ID, 1)
	assert.NoError(t, err, "should update retry count")

	// Verify retry count was updated
	events, err := store.DequeueScrobbles(ctx, event.UserID, 1)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, 1, events[0].RetryCount, "retry count should be updated")
}

// TestQueuePurge tests purging all events for a user
func TestQueuePurge(t *testing.T) {
	cleanupQueue(t)
	defer cleanupQueue(t)

	ctx := context.Background()
	store := NewDiskStore()

	userID := "user-purge"

	// Enqueue multiple events
	for i := 0; i < 5; i++ {
		event := QueuedScrobbleEvent{
			ID:         fmt.Sprintf("purge-event-%d", i),
			UserID:     userID,
			Action:     "stop",
			CreatedAt:  time.Now(),
			PlayerUUID: fmt.Sprintf("player-%d", i),
			RatingKey:  fmt.Sprintf("rating-%d", i),
		}
		require.NoError(t, store.EnqueueScrobble(ctx, event))
	}

	// Verify queue has events
	size, err := store.GetQueueSize(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, 5, size)

	// Purge queue
	count, err := store.PurgeQueueForUser(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, 5, count, "should purge 5 events")

	// Verify queue is empty
	size, err = store.GetQueueSize(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, 0, size, "queue should be empty after purge")
}

// TestListUsersWithQueuedEvents tests finding users with pending events
func TestListUsersWithQueuedEvents(t *testing.T) {
	cleanupQueue(t)
	defer cleanupQueue(t)

	ctx := context.Background()
	store := NewDiskStore()

	// Enqueue events for multiple users
	users := []string{"user-a", "user-b", "user-c"}
	for _, userID := range users {
		event := QueuedScrobbleEvent{
			ID:         userID + "-event",
			UserID:     userID,
			Action:     "stop",
			CreatedAt:  time.Now(),
			PlayerUUID: "player-" + userID,
			RatingKey:  "rating-" + userID,
		}
		require.NoError(t, store.EnqueueScrobble(ctx, event))
	}

	// List users with queued events
	userList, err := store.ListUsersWithQueuedEvents(ctx)
	require.NoError(t, err)
	assert.Len(t, userList, 3, "should find 3 users with events")

	// Verify all users are in the list
	userMap := make(map[string]bool)
	for _, uid := range userList {
		userMap[uid] = true
	}
	for _, expectedUser := range users {
		assert.True(t, userMap[expectedUser], "user %s should be in the list", expectedUser)
	}
}

// TestQueueDeduplication tests that duplicate events are handled
func TestQueueDeduplication(t *testing.T) {
	t.Skip("Deduplication behavior depends on store implementation")
	// TODO: Implement based on actual deduplication strategy
}

// TestQueueStaleEvents tests handling of old events
func TestQueueStaleEvents(t *testing.T) {
	cleanupQueue(t)
	defer cleanupQueue(t)

	ctx := context.Background()
	store := NewDiskStore()

	// Create old event (8 days ago)
	oldEvent := QueuedScrobbleEvent{
		ID:         "old-event",
		UserID:     "user-stale",
		Action:     "stop",
		CreatedAt:  time.Now().Add(-8 * 24 * time.Hour),
		PlayerUUID: "player-old",
		RatingKey:  "rating-old",
	}

	// Enqueue old event
	require.NoError(t, store.EnqueueScrobble(ctx, oldEvent))

	// Dequeue and verify it's still returned
	events, err := store.DequeueScrobbles(ctx, oldEvent.UserID, 1)
	require.NoError(t, err)
	require.Len(t, events, 1, "old events should still be returned")

	// Calculate age
	age := time.Since(events[0].CreatedAt)
	assert.True(t, age > 7*24*time.Hour, "event should be older than 7 days")
}

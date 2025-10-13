package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTraktScrobbler implements TraktScrobbler for testing
type mockTraktScrobbler struct {
	scrobbleFn func(action string, item common.CacheItem, token string) error
}

func (m *mockTraktScrobbler) ScrobbleFromQueue(action string, item common.CacheItem, token string) error {
	if m.scrobbleFn != nil {
		return m.scrobbleFn(action, item, token)
	}
	return nil
}

// mockNotifier implements Notifier for testing
type mockNotifier struct {
	notifyFn func(ctx context.Context, groupID, memberID, username, mediaTitle, errorMsg string) error
	calls    []notifyCall
}

type notifyCall struct {
	groupID    string
	memberID   string
	username   string
	mediaTitle string
	errorMsg   string
}

func (m *mockNotifier) NotifyPermanentFailure(ctx context.Context, groupID, memberID, username, mediaTitle, errorMsg string) error {
	m.calls = append(m.calls, notifyCall{
		groupID:    groupID,
		memberID:   memberID,
		username:   username,
		mediaTitle: mediaTitle,
		errorMsg:   errorMsg,
	})
	if m.notifyFn != nil {
		return m.notifyFn(ctx, groupID, memberID, username, mediaTitle, errorMsg)
	}
	return nil
}

// mockWorkerStore implements store.Store with queue and member methods
type mockWorkerStore struct {
	store.Store
	listDueFn      func(context.Context, time.Time, int) ([]*store.RetryQueueItem, error)
	markSuccessFn  func(context.Context, string) error
	markFailureFn  func(context.Context, string, int, time.Time, string, bool) error
	getMemberFn    func(context.Context, string) (*store.GroupMember, error)
}

func (m *mockWorkerStore) ListDueRetryItems(ctx context.Context, now time.Time, limit int) ([]*store.RetryQueueItem, error) {
	if m.listDueFn != nil {
		return m.listDueFn(ctx, now, limit)
	}
	return nil, nil
}

func (m *mockWorkerStore) MarkRetrySuccess(ctx context.Context, id string) error {
	if m.markSuccessFn != nil {
		return m.markSuccessFn(ctx, id)
	}
	return nil
}

func (m *mockWorkerStore) MarkRetryFailure(ctx context.Context, id string, attempt int, nextAttempt time.Time, lastErr string, permanent bool) error {
	if m.markFailureFn != nil {
		return m.markFailureFn(ctx, id, attempt, nextAttempt, lastErr, permanent)
	}
	return nil
}

func (m *mockWorkerStore) GetGroupMember(ctx context.Context, memberID string) (*store.GroupMember, error) {
	if m.getMemberFn != nil {
		return m.getMemberFn(ctx, memberID)
	}
	return nil, nil
}

func TestWorker_processItem_Success(t *testing.T) {
	ctx := context.Background()

	// Create test scrobble payload
	title := "Test Movie"
	year := 2023
	payload := common.ScrobbleBody{
		Progress: 95,
		Movie: &common.Movie{
			Title: &title,
			Year:  &year,
		},
	}
	payloadJSON, _ := json.Marshal(payload)

	item := &store.RetryQueueItem{
		ID:            "item-success",
		FamilyGroupID: "group-1",
		GroupMemberID: "member-1",
		Payload:       payloadJSON,
		AttemptCount:  1,
		NextAttemptAt: time.Now(),
		LastError:     "previous error",
		Status:        "queued",
	}

	member := &store.GroupMember{
		ID:            "member-1",
		TraktUsername: "testuser",
		AccessToken:   "token-abc",
	}

	// Mock store
	successMarked := false
	mockStore := &mockWorkerStore{
		getMemberFn: func(ctx context.Context, memberID string) (*store.GroupMember, error) {
			assert.Equal(t, "member-1", memberID)
			return member, nil
		},
		markSuccessFn: func(ctx context.Context, id string) error {
			assert.Equal(t, "item-success", id)
			successMarked = true
			return nil
		},
	}

	// Mock Trakt scrobbler - success
	scrobbleCalled := false
	mockTrakt := &mockTraktScrobbler{
		scrobbleFn: func(action string, cacheItem common.CacheItem, token string) error {
			scrobbleCalled = true
			assert.Equal(t, "token-abc", token)
			assert.Equal(t, payload.Progress, cacheItem.Body.Progress)
			return nil
		},
	}

	// Create worker
	repo := NewPostgresRepo(mockStore)
	worker := NewWorker(WorkerConfig{
		Repo:     repo,
		Trakt:    mockTrakt,
		Notifier: nil,
		Store:    mockStore,
	})

	// Process item
	worker.processItem(ctx, item)

	// Verify
	assert.True(t, scrobbleCalled, "Trakt scrobble should be called")
	assert.True(t, successMarked, "Item should be marked as success")
}

func TestWorker_processItem_TransientFailure(t *testing.T) {
	ctx := context.Background()

	payload := common.ScrobbleBody{Progress: 50}
	payloadJSON, _ := json.Marshal(payload)

	item := &store.RetryQueueItem{
		ID:            "item-transient",
		FamilyGroupID: "group-1",
		GroupMemberID: "member-1",
		Payload:       payloadJSON,
		AttemptCount:  2,
		NextAttemptAt: time.Now(),
		Status:        "queued",
	}

	member := &store.GroupMember{
		ID:            "member-1",
		TraktUsername: "testuser",
		AccessToken:   "token-xyz",
	}

	// Track failure marking
	failureMarked := false
	var markedAttempt int
	var markedPermanent bool

	mockStore := &mockWorkerStore{
		getMemberFn: func(ctx context.Context, memberID string) (*store.GroupMember, error) {
			return member, nil
		},
		markFailureFn: func(ctx context.Context, id string, attempt int, nextAttempt time.Time, lastErr string, permanent bool) error {
			failureMarked = true
			markedAttempt = attempt
			markedPermanent = permanent
			assert.Equal(t, "item-transient", id)
			assert.Contains(t, lastErr, "HTTP 429")
			assert.True(t, nextAttempt.After(time.Now()))
			return nil
		},
	}

	// Mock Trakt scrobbler - transient failure (rate limit)
	mockTrakt := &mockTraktScrobbler{
		scrobbleFn: func(action string, item common.CacheItem, token string) error {
			return errors.New("HTTP 429 Too Many Requests")
		},
	}

	repo := NewPostgresRepo(mockStore)
	worker := NewWorker(WorkerConfig{
		Repo:     repo,
		Trakt:    mockTrakt,
		Notifier: nil,
		Store:    mockStore,
	})

	// Process item
	worker.processItem(ctx, item)

	// Verify
	assert.True(t, failureMarked, "Failure should be marked")
	assert.Equal(t, 3, markedAttempt, "Attempt count should be incremented")
	assert.False(t, markedPermanent, "Should not be marked as permanent")
}

func TestWorker_processItem_PermanentFailure(t *testing.T) {
	ctx := context.Background()

	title := "Failing Show"
	season := 1
	episode := 5
	payload := common.ScrobbleBody{
		Progress: 95,
		Show: &common.Show{
			Title: &title,
		},
		Episode: &common.Episode{
			Season: &season,
			Number: &episode,
		},
	}
	payloadJSON, _ := json.Marshal(payload)

	item := &store.RetryQueueItem{
		ID:            "item-permanent",
		FamilyGroupID: "group-1",
		GroupMemberID: "member-1",
		Payload:       payloadJSON,
		AttemptCount:  4, // 5th attempt will be the last
		NextAttemptAt: time.Now(),
		Status:        "queued",
	}

	member := &store.GroupMember{
		ID:            "member-1",
		TraktUsername: "failuser",
		AccessToken:   "token-fail",
	}

	// Track permanent failure marking
	permanentMarked := false
	var markedAttempt int

	mockStore := &mockWorkerStore{
		getMemberFn: func(ctx context.Context, memberID string) (*store.GroupMember, error) {
			return member, nil
		},
		markFailureFn: func(ctx context.Context, id string, attempt int, nextAttempt time.Time, lastErr string, permanent bool) error {
			permanentMarked = permanent
			markedAttempt = attempt
			assert.Equal(t, "item-permanent", id)
			assert.Contains(t, lastErr, "persistent error")
			return nil
		},
	}

	// Mock Trakt scrobbler - always fails
	mockTrakt := &mockTraktScrobbler{
		scrobbleFn: func(action string, item common.CacheItem, token string) error {
			return errors.New("persistent error")
		},
	}

	// Mock notifier - track calls
	mockNotifier := &mockNotifier{}

	repo := NewPostgresRepo(mockStore)
	worker := NewWorker(WorkerConfig{
		Repo:     repo,
		Trakt:    mockTrakt,
		Notifier: mockNotifier,
		Store:    mockStore,
	})

	// Process item
	worker.processItem(ctx, item)

	// Verify
	assert.True(t, permanentMarked, "Should be marked as permanent failure")
	assert.Equal(t, MaxRetryAttempts, markedAttempt, "Should reach max attempts")

	// Verify notification was sent
	require.Len(t, mockNotifier.calls, 1, "Notification should be sent")
	call := mockNotifier.calls[0]
	assert.Equal(t, "group-1", call.groupID)
	assert.Equal(t, "member-1", call.memberID)
	assert.Equal(t, "failuser", call.username)
	assert.Equal(t, "Failing Show S01E05", call.mediaTitle)
	assert.Contains(t, call.errorMsg, "persistent error")
}

func TestWorker_processItem_MemberNotFound(t *testing.T) {
	ctx := context.Background()

	payload := common.ScrobbleBody{Progress: 50}
	payloadJSON, _ := json.Marshal(payload)

	item := &store.RetryQueueItem{
		ID:            "item-orphan",
		FamilyGroupID: "group-1",
		GroupMemberID: "member-deleted",
		Payload:       payloadJSON,
		AttemptCount:  1,
		NextAttemptAt: time.Now(),
		Status:        "queued",
	}

	// Track permanent failure marking
	permanentMarked := false

	mockStore := &mockWorkerStore{
		getMemberFn: func(ctx context.Context, memberID string) (*store.GroupMember, error) {
			return nil, errors.New("member not found")
		},
		markFailureFn: func(ctx context.Context, id string, attempt int, nextAttempt time.Time, lastErr string, permanent bool) error {
			permanentMarked = permanent
			assert.Equal(t, "item-orphan", id)
			assert.Equal(t, MaxRetryAttempts, attempt)
			assert.Contains(t, lastErr, "member not found")
			return nil
		},
	}

	repo := NewPostgresRepo(mockStore)
	worker := NewWorker(WorkerConfig{
		Repo:     repo,
		Trakt:    nil, // Should not be called
		Notifier: nil,
		Store:    mockStore,
	})

	// Process item
	worker.processItem(ctx, item)

	// Verify
	assert.True(t, permanentMarked, "Should be marked as permanent failure when member missing")
}

func TestWorker_processItem_InvalidPayload(t *testing.T) {
	ctx := context.Background()

	item := &store.RetryQueueItem{
		ID:            "item-corrupt",
		FamilyGroupID: "group-1",
		GroupMemberID: "member-1",
		Payload:       []byte("invalid json{{{"),
		AttemptCount:  0,
		NextAttemptAt: time.Now(),
		Status:        "queued",
	}

	member := &store.GroupMember{
		ID:            "member-1",
		TraktUsername: "testuser",
		AccessToken:   "token-abc",
	}

	permanentMarked := false

	mockStore := &mockWorkerStore{
		getMemberFn: func(ctx context.Context, memberID string) (*store.GroupMember, error) {
			return member, nil
		},
		markFailureFn: func(ctx context.Context, id string, attempt int, nextAttempt time.Time, lastErr string, permanent bool) error {
			permanentMarked = permanent
			assert.Equal(t, "item-corrupt", id)
			assert.Contains(t, lastErr, "invalid payload")
			return nil
		},
	}

	repo := NewPostgresRepo(mockStore)
	worker := NewWorker(WorkerConfig{
		Repo:     repo,
		Trakt:    nil, // Should not be called
		Notifier: nil,
		Store:    mockStore,
	})

	// Process item
	worker.processItem(ctx, item)

	// Verify
	assert.True(t, permanentMarked, "Should be marked as permanent failure for corrupt payload")
}

func TestWorker_processBatch(t *testing.T) {
	ctx := context.Background()

	t.Run("processes multiple items", func(t *testing.T) {
		payload := common.ScrobbleBody{Progress: 50}
		payloadJSON, _ := json.Marshal(payload)

		items := []*store.RetryQueueItem{
			{
				ID:            "item-1",
				FamilyGroupID: "group-1",
				GroupMemberID: "member-1",
				Payload:       payloadJSON,
				AttemptCount:  0,
			},
			{
				ID:            "item-2",
				FamilyGroupID: "group-1",
				GroupMemberID: "member-2",
				Payload:       payloadJSON,
				AttemptCount:  1,
			},
		}

		member1 := &store.GroupMember{ID: "member-1", TraktUsername: "user1", AccessToken: "token1"}
		member2 := &store.GroupMember{ID: "member-2", TraktUsername: "user2", AccessToken: "token2"}

		processedItems := []string{}

		mockStore := &mockWorkerStore{
			listDueFn: func(ctx context.Context, now time.Time, limit int) ([]*store.RetryQueueItem, error) {
				return items, nil
			},
			getMemberFn: func(ctx context.Context, memberID string) (*store.GroupMember, error) {
				if memberID == "member-1" {
					return member1, nil
				}
				return member2, nil
			},
			markSuccessFn: func(ctx context.Context, id string) error {
				processedItems = append(processedItems, id)
				return nil
			},
		}

		mockTrakt := &mockTraktScrobbler{
			scrobbleFn: func(action string, item common.CacheItem, token string) error {
				return nil // Success
			},
		}

		repo := NewPostgresRepo(mockStore)
		worker := NewWorker(WorkerConfig{
			Repo:     repo,
			Trakt:    mockTrakt,
			Notifier: nil,
			Store:    mockStore,
		})

		// Process batch
		worker.processBatch(ctx)

		// Verify both items processed
		assert.Len(t, processedItems, 2)
		assert.Contains(t, processedItems, "item-1")
		assert.Contains(t, processedItems, "item-2")
	})

	t.Run("handles empty batch", func(t *testing.T) {
		mockStore := &mockWorkerStore{
			listDueFn: func(ctx context.Context, now time.Time, limit int) ([]*store.RetryQueueItem, error) {
				return []*store.RetryQueueItem{}, nil
			},
		}

		repo := NewPostgresRepo(mockStore)
		worker := NewWorker(WorkerConfig{
			Repo:     repo,
			Trakt:    nil,
			Notifier: nil,
			Store:    mockStore,
		})

		// Should not panic
		worker.processBatch(ctx)
	})

	t.Run("handles fetch error gracefully", func(t *testing.T) {
		mockStore := &mockWorkerStore{
			listDueFn: func(ctx context.Context, now time.Time, limit int) ([]*store.RetryQueueItem, error) {
				return nil, errors.New("database connection lost")
			},
		}

		repo := NewPostgresRepo(mockStore)
		worker := NewWorker(WorkerConfig{
			Repo:     repo,
			Trakt:    nil,
			Notifier: nil,
			Store:    mockStore,
		})

		// Should log error but not panic
		worker.processBatch(ctx)
	})
}

func TestWorker_Start_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	mockStore := &mockWorkerStore{
		listDueFn: func(ctx context.Context, now time.Time, limit int) ([]*store.RetryQueueItem, error) {
			return []*store.RetryQueueItem{}, nil
		},
	}

	repo := NewPostgresRepo(mockStore)
	worker := NewWorker(WorkerConfig{
		Repo:         repo,
		Trakt:        nil,
		Notifier:     nil,
		Store:        mockStore,
		PollInterval: 1 * time.Millisecond, // Very short for testing
	})

	// Start worker in goroutine
	done := make(chan bool)
	go func() {
		worker.Start(ctx)
		done <- true
	}()

	// Let it run briefly
	time.Sleep(10 * time.Millisecond)

	// Cancel context
	cancel()

	// Wait for shutdown
	select {
	case <-done:
		// Success - worker stopped
	case <-time.After(1 * time.Second):
		t.Fatal("Worker did not stop after context cancellation")
	}
}

func TestCalculateBackoff(t *testing.T) {
	tests := []struct {
		name     string
		attempt  int
		expected time.Duration
	}{
		{
			name:     "attempt 0",
			attempt:  0,
			expected: BaseBackoffDelay,
		},
		{
			name:     "attempt 1",
			attempt:  1,
			expected: 30 * time.Second,
		},
		{
			name:     "attempt 2",
			attempt:  2,
			expected: 60 * time.Second,
		},
		{
			name:     "attempt 3",
			attempt:  3,
			expected: 2 * time.Minute,
		},
		{
			name:     "attempt 4",
			attempt:  4,
			expected: 4 * time.Minute,
		},
		{
			name:     "attempt 5",
			attempt:  5,
			expected: 8 * time.Minute,
		},
		{
			name:     "attempt 10 (should cap at MaxBackoffDelay)",
			attempt:  10,
			expected: MaxBackoffDelay,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := calculateBackoff(tt.attempt)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractMediaTitle(t *testing.T) {
	tests := []struct {
		name     string
		body     common.ScrobbleBody
		expected string
	}{
		{
			name: "movie with year",
			body: common.ScrobbleBody{
				Movie: &common.Movie{
					Title: stringPtr("Inception"),
					Year:  intPtr(2010),
				},
			},
			expected: "Inception (2010)",
		},
		{
			name: "movie without year",
			body: common.ScrobbleBody{
				Movie: &common.Movie{
					Title: stringPtr("The Matrix"),
				},
			},
			expected: "The Matrix",
		},
		{
			name: "show with episode",
			body: common.ScrobbleBody{
				Show: &common.Show{
					Title: stringPtr("Breaking Bad"),
				},
				Episode: &common.Episode{
					Season: intPtr(5),
					Number: intPtr(14),
				},
			},
			expected: "Breaking Bad S05E14",
		},
		{
			name: "show without episode",
			body: common.ScrobbleBody{
				Show: &common.Show{
					Title: stringPtr("The Wire"),
				},
			},
			expected: "The Wire",
		},
		{
			name:     "unknown media",
			body:     common.ScrobbleBody{},
			expected: "Unknown Media",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMediaTitle(tt.body)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Helper functions
func stringPtr(s string) *string {
	return &s
}

func intPtr(i int) *int {
	return &i
}

package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStore implements store.Store for testing
type mockStore struct {
	store.Store
	enqueueFn         func(context.Context, *store.RetryQueueItem) error
	listDueFn         func(context.Context, time.Time, int) ([]*store.RetryQueueItem, error)
	markSuccessFn     func(context.Context, string) error
	markFailureFn     func(context.Context, string, int, time.Time, string, bool) error
}

func (m *mockStore) EnqueueRetryItem(ctx context.Context, item *store.RetryQueueItem) error {
	if m.enqueueFn != nil {
		return m.enqueueFn(ctx, item)
	}
	return nil
}

func (m *mockStore) ListDueRetryItems(ctx context.Context, now time.Time, limit int) ([]*store.RetryQueueItem, error) {
	if m.listDueFn != nil {
		return m.listDueFn(ctx, now, limit)
	}
	return nil, nil
}

func (m *mockStore) MarkRetrySuccess(ctx context.Context, id string) error {
	if m.markSuccessFn != nil {
		return m.markSuccessFn(ctx, id)
	}
	return nil
}

func (m *mockStore) MarkRetryFailure(ctx context.Context, id string, attempt int, nextAttempt time.Time, lastErr string, permanent bool) error {
	if m.markFailureFn != nil {
		return m.markFailureFn(ctx, id, attempt, nextAttempt, lastErr, permanent)
	}
	return nil
}

func TestPostgresRepo_FetchDueItems(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("success", func(t *testing.T) {
		expectedItems := []*store.RetryQueueItem{
			{
				ID:            "item-1",
				FamilyGroupID: "group-1",
				GroupMemberID: "member-1",
				AttemptCount:  1,
			},
			{
				ID:            "item-2",
				FamilyGroupID: "group-1",
				GroupMemberID: "member-2",
				AttemptCount:  2,
			},
		}

		ms := &mockStore{
			listDueFn: func(ctx context.Context, fetchTime time.Time, limit int) ([]*store.RetryQueueItem, error) {
				assert.Equal(t, now, fetchTime)
				assert.Equal(t, 50, limit)
				return expectedItems, nil
			},
		}

		repo := NewPostgresRepo(ms)
		items, err := repo.FetchDueItems(ctx, now, 50)

		require.NoError(t, err)
		assert.Equal(t, expectedItems, items)
	})

	t.Run("empty result", func(t *testing.T) {
		ms := &mockStore{
			listDueFn: func(ctx context.Context, fetchTime time.Time, limit int) ([]*store.RetryQueueItem, error) {
				return []*store.RetryQueueItem{}, nil
			},
		}

		repo := NewPostgresRepo(ms)
		items, err := repo.FetchDueItems(ctx, now, 100)

		require.NoError(t, err)
		assert.Empty(t, items)
	})

	t.Run("storage error", func(t *testing.T) {
		ms := &mockStore{
			listDueFn: func(ctx context.Context, fetchTime time.Time, limit int) ([]*store.RetryQueueItem, error) {
				return nil, errors.New("database connection failed")
			},
		}

		repo := NewPostgresRepo(ms)
		items, err := repo.FetchDueItems(ctx, now, 50)

		require.Error(t, err)
		assert.Nil(t, items)
		assert.Contains(t, err.Error(), "fetch due items")
		assert.Contains(t, err.Error(), "database connection failed")
	})
}

func TestPostgresRepo_MarkSuccess(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		ms := &mockStore{
			markSuccessFn: func(ctx context.Context, id string) error {
				assert.Equal(t, "item-123", id)
				return nil
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.MarkSuccess(ctx, "item-123")

		require.NoError(t, err)
	})

	t.Run("item not found", func(t *testing.T) {
		ms := &mockStore{
			markSuccessFn: func(ctx context.Context, id string) error {
				return sql.ErrNoRows
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.MarkSuccess(ctx, "nonexistent")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mark success")
	})

	t.Run("storage error", func(t *testing.T) {
		ms := &mockStore{
			markSuccessFn: func(ctx context.Context, id string) error {
				return errors.New("deadlock detected")
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.MarkSuccess(ctx, "item-456")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mark success")
		assert.Contains(t, err.Error(), "deadlock detected")
	})
}

func TestPostgresRepo_MarkFailure(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	nextAttempt := now.Add(1 * time.Minute)

	t.Run("transient failure", func(t *testing.T) {
		ms := &mockStore{
			markFailureFn: func(ctx context.Context, id string, attempt int, next time.Time, lastErr string, permanent bool) error {
				assert.Equal(t, "item-789", id)
				assert.Equal(t, 2, attempt)
				assert.Equal(t, nextAttempt, next)
				assert.Equal(t, "HTTP 429", lastErr)
				assert.False(t, permanent)
				return nil
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.MarkFailure(ctx, "item-789", 2, nextAttempt, "HTTP 429", false)

		require.NoError(t, err)
	})

	t.Run("permanent failure", func(t *testing.T) {
		ms := &mockStore{
			markFailureFn: func(ctx context.Context, id string, attempt int, next time.Time, lastErr string, permanent bool) error {
				assert.Equal(t, "item-max", id)
				assert.Equal(t, 5, attempt)
				assert.Equal(t, "max attempts reached", lastErr)
				assert.True(t, permanent)
				return nil
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.MarkFailure(ctx, "item-max", 5, now, "max attempts reached", true)

		require.NoError(t, err)
	})

	t.Run("storage error", func(t *testing.T) {
		ms := &mockStore{
			markFailureFn: func(ctx context.Context, id string, attempt int, next time.Time, lastErr string, permanent bool) error {
				return errors.New("constraint violation")
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.MarkFailure(ctx, "item-bad", 3, nextAttempt, "error", false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mark failure")
		assert.Contains(t, err.Error(), "constraint violation")
	})
}

func TestPostgresRepo_Enqueue(t *testing.T) {
	ctx := context.Background()

	t.Run("success", func(t *testing.T) {
		payload := common.ScrobbleBody{
			Progress: 50,
		}
		payloadJSON, _ := json.Marshal(payload)

		item := &store.RetryQueueItem{
			ID:            "new-item",
			FamilyGroupID: "group-abc",
			GroupMemberID: "member-xyz",
			Payload:       payloadJSON,
			AttemptCount:  0,
			NextAttemptAt: time.Now().Add(30 * time.Second),
			Status:        "queued",
		}

		ms := &mockStore{
			enqueueFn: func(ctx context.Context, i *store.RetryQueueItem) error {
				assert.Equal(t, item, i)
				return nil
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.Enqueue(ctx, item)

		require.NoError(t, err)
	})

	t.Run("duplicate item", func(t *testing.T) {
		item := &store.RetryQueueItem{
			ID:            "duplicate",
			FamilyGroupID: "group-1",
			GroupMemberID: "member-1",
		}

		ms := &mockStore{
			enqueueFn: func(ctx context.Context, i *store.RetryQueueItem) error {
				return errors.New("duplicate key value violates unique constraint")
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.Enqueue(ctx, item)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "enqueue item")
		assert.Contains(t, err.Error(), "duplicate key")
	})

	t.Run("storage error", func(t *testing.T) {
		item := &store.RetryQueueItem{
			ID:            "error-item",
			FamilyGroupID: "group-2",
			GroupMemberID: "member-2",
		}

		ms := &mockStore{
			enqueueFn: func(ctx context.Context, i *store.RetryQueueItem) error {
				return errors.New("foreign key constraint fails")
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.Enqueue(ctx, item)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "enqueue item")
		assert.Contains(t, err.Error(), "foreign key constraint")
	})
}

// TestPostgresRepo_ErrorWrapping verifies error context is preserved
func TestPostgresRepo_ErrorWrapping(t *testing.T) {
	ctx := context.Background()

	t.Run("FetchDueItems wraps errors", func(t *testing.T) {
		originalErr := errors.New("original storage error")
		ms := &mockStore{
			listDueFn: func(context.Context, time.Time, int) ([]*store.RetryQueueItem, error) {
				return nil, originalErr
			},
		}

		repo := NewPostgresRepo(ms)
		_, err := repo.FetchDueItems(ctx, time.Now(), 10)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "fetch due items")
		assert.ErrorIs(t, err, originalErr)
	})

	t.Run("MarkSuccess wraps errors", func(t *testing.T) {
		originalErr := errors.New("delete failed")
		ms := &mockStore{
			markSuccessFn: func(context.Context, string) error {
				return originalErr
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.MarkSuccess(ctx, "test-id")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mark success")
		assert.ErrorIs(t, err, originalErr)
	})

	t.Run("MarkFailure wraps errors", func(t *testing.T) {
		originalErr := errors.New("update failed")
		ms := &mockStore{
			markFailureFn: func(context.Context, string, int, time.Time, string, bool) error {
				return originalErr
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.MarkFailure(ctx, "test-id", 1, time.Now(), "error", false)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "mark failure")
		assert.ErrorIs(t, err, originalErr)
	})

	t.Run("Enqueue wraps errors", func(t *testing.T) {
		originalErr := errors.New("insert failed")
		ms := &mockStore{
			enqueueFn: func(context.Context, *store.RetryQueueItem) error {
				return originalErr
			},
		}

		repo := NewPostgresRepo(ms)
		err := repo.Enqueue(ctx, &store.RetryQueueItem{})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "enqueue item")
		assert.ErrorIs(t, err, originalErr)
	})
}

// TestPostgresRepo_ContextCancellation verifies context is propagated
func TestPostgresRepo_ContextCancellation(t *testing.T) {
	t.Run("FetchDueItems respects context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		ms := &mockStore{
			listDueFn: func(c context.Context, fetchTime time.Time, limit int) ([]*store.RetryQueueItem, error) {
				assert.Equal(t, ctx, c)
				return nil, c.Err()
			},
		}

		repo := NewPostgresRepo(ms)
		_, err := repo.FetchDueItems(ctx, time.Now(), 10)

		require.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	})
}

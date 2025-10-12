package queue

import (
	"context"
	"fmt"
	"time"

	"crovlune/plaxt/lib/store"
)

// PostgresRepo wraps store queue operations with additional context and error handling.
// It provides a clean interface for the queue worker to interact with PostgreSQL storage.
type PostgresRepo struct {
	store store.Store
}

// NewPostgresRepo creates a new PostgreSQL-backed queue repository.
func NewPostgresRepo(s store.Store) *PostgresRepo {
	return &PostgresRepo{store: s}
}

// FetchDueItems retrieves retry items ready for processing.
// Uses FOR UPDATE SKIP LOCKED for worker concurrency safety.
//
// Parameters:
//   - ctx: Context for cancellation
//   - now: Current time for due item comparison
//   - limit: Maximum items to fetch (typically 50-100)
//
// Returns:
//   - []*store.RetryQueueItem: Items ready for retry
//   - error: Storage failure
func (r *PostgresRepo) FetchDueItems(ctx context.Context, now time.Time, limit int) ([]*store.RetryQueueItem, error) {
	items, err := r.store.ListDueRetryItems(ctx, now, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch due items: %w", err)
	}
	return items, nil
}

// MarkSuccess deletes a successfully retried item from the queue.
//
// Parameters:
//   - ctx: Context for cancellation
//   - id: UUID of the retry queue item
//
// Returns:
//   - error: Storage failure or item not found
func (r *PostgresRepo) MarkSuccess(ctx context.Context, id string) error {
	if err := r.store.MarkRetrySuccess(ctx, id); err != nil {
		return fmt.Errorf("mark success: %w", err)
	}
	return nil
}

// MarkFailure updates a retry item after a failed attempt.
// Increments attempt count and schedules next retry with exponential backoff.
//
// Parameters:
//   - ctx: Context for cancellation
//   - id: UUID of the retry queue item
//   - attempt: New attempt count (1-5)
//   - nextAttempt: Scheduled time for next retry
//   - lastErr: Error message from failed attempt
//   - permanent: True if max attempts reached (5)
//
// Returns:
//   - error: Storage failure
func (r *PostgresRepo) MarkFailure(ctx context.Context, id string, attempt int, nextAttempt time.Time, lastErr string, permanent bool) error {
	if err := r.store.MarkRetryFailure(ctx, id, attempt, nextAttempt, lastErr, permanent); err != nil {
		return fmt.Errorf("mark failure: %w", err)
	}
	return nil
}

// Enqueue adds a new retry item to the queue.
//
// Parameters:
//   - ctx: Context for cancellation
//   - item: Retry queue item to enqueue
//
// Returns:
//   - error: Storage failure
func (r *PostgresRepo) Enqueue(ctx context.Context, item *store.RetryQueueItem) error {
	if err := r.store.EnqueueRetryItem(ctx, item); err != nil {
		return fmt.Errorf("enqueue item: %w", err)
	}
	return nil
}

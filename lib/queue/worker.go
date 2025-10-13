package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/store"
)

const (
	// MaxRetryAttempts is the ceiling for retry attempts per FR-016.
	MaxRetryAttempts = 5

	// DefaultPollInterval is how often the worker checks for due items.
	DefaultPollInterval = 15 * time.Second

	// DefaultBatchSize is the maximum items processed per poll.
	DefaultBatchSize = 50

	// BaseBackoffDelay is the initial retry delay (30 seconds).
	BaseBackoffDelay = 30 * time.Second

	// MaxBackoffDelay caps exponential backoff at 30 minutes.
	MaxBackoffDelay = 30 * time.Minute
)

// TraktScrobbler defines the interface for sending scrobbles to Trakt.
// This allows the worker to call BroadcastScrobble without circular dependencies.
type TraktScrobbler interface {
	// ScrobbleFromQueue sends a queued scrobble to Trakt.
	// Returns nil on success, error on failure (transient or permanent).
	ScrobbleFromQueue(action string, item common.CacheItem, accessToken string) error
}

// Notifier defines the interface for sending notifications to group owners.
type Notifier interface {
	// NotifyPermanentFailure sends a banner notification for a permanently failed scrobble.
	NotifyPermanentFailure(ctx context.Context, groupID, memberID, memberUsername, mediaTitle, errorMsg string) error
}

// Worker processes the retry queue with exponential backoff and permanent failure handling.
type Worker struct {
	repo         *PostgresRepo
	trakt        TraktScrobbler
	notifier     Notifier
	pollInterval time.Duration
	batchSize    int
	store        store.Store // Needed to fetch group member tokens
}

// WorkerConfig configures the queue worker.
type WorkerConfig struct {
	Repo         *PostgresRepo
	Trakt        TraktScrobbler
	Notifier     Notifier
	Store        store.Store
	PollInterval time.Duration
	BatchSize    int
}

// NewWorker creates a new queue worker with the given configuration.
func NewWorker(cfg WorkerConfig) *Worker {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultBatchSize
	}

	return &Worker{
		repo:         cfg.Repo,
		trakt:        cfg.Trakt,
		notifier:     cfg.Notifier,
		pollInterval: cfg.PollInterval,
		batchSize:    cfg.BatchSize,
		store:        cfg.Store,
	}
}

// Start begins the worker loop. Blocks until context is cancelled.
//
// The worker:
// 1. Polls for due retry items every PollInterval
// 2. Attempts to scrobble each item
// 3. On success: deletes item from queue
// 4. On transient failure: schedules next retry with exponential backoff
// 5. On reaching MaxRetryAttempts: marks permanent failure + notifies owner
//
// Parameters:
//   - ctx: Context for cancellation (graceful shutdown)
func (w *Worker) Start(ctx context.Context) {
	slog.Info("queue worker started",
		"poll_interval", w.pollInterval,
		"batch_size", w.batchSize,
		"max_attempts", MaxRetryAttempts,
	)

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("queue worker shutting down")
			return
		case <-ticker.C:
			w.processBatch(ctx)
		}
	}
}

// processBatch fetches and processes a batch of due retry items.
func (w *Worker) processBatch(ctx context.Context) {
	items, err := w.repo.FetchDueItems(ctx, time.Now(), w.batchSize)
	if err != nil {
		slog.Error("queue worker fetch error", "error", err)
		return
	}

	if len(items) == 0 {
		return // No items due for retry
	}

	slog.Info("queue worker processing batch", "count", len(items))

	for _, item := range items {
		w.processItem(ctx, item)
	}
}

// processItem attempts to retry a single queued scrobble.
func (w *Worker) processItem(ctx context.Context, item *store.RetryQueueItem) {
	// Fetch member to get current access token
	member, err := w.store.GetGroupMember(ctx, item.GroupMemberID)
	if err != nil {
		slog.Error("queue worker member lookup failed",
			"item_id", item.ID,
			"member_id", item.GroupMemberID,
			"error", err,
		)
		// Mark permanent failure if member no longer exists
		_ = w.repo.MarkFailure(ctx, item.ID, MaxRetryAttempts, time.Now(), "member not found", true)
		return
	}

	// Deserialize scrobble payload
	var scrobbleBody common.ScrobbleBody
	if err := json.Unmarshal(item.Payload, &scrobbleBody); err != nil {
		slog.Error("queue worker payload unmarshal error",
			"item_id", item.ID,
			"error", err,
		)
		// Permanent failure - corrupt payload
		_ = w.repo.MarkFailure(ctx, item.ID, MaxRetryAttempts, time.Now(), "invalid payload", true)
		return
	}

	// Build cache item for scrobble
	cacheItem := common.CacheItem{
		Body: scrobbleBody,
	}

	// Extract action from item (default to "stop" if not stored)
	action := "stop" // TODO: Store action in RetryQueueItem if needed

	// Attempt scrobble
	err = w.trakt.ScrobbleFromQueue(action, cacheItem, member.AccessToken)

	if err == nil {
		// Success - remove from queue
		if delErr := w.repo.MarkSuccess(ctx, item.ID); delErr != nil {
			slog.Error("queue worker mark success error", "item_id", item.ID, "error", delErr)
		} else {
			slog.Info("queue worker retry success",
				"item_id", item.ID,
				"member", member.TraktUsername,
				"attempt", item.AttemptCount+1,
			)
		}
		return
	}

	// Failure - increment attempt and schedule next retry
	newAttempt := item.AttemptCount + 1

	if newAttempt >= MaxRetryAttempts {
		// Permanent failure - notify owner
		slog.Error("queue worker permanent failure",
			"item_id", item.ID,
			"member", member.TraktUsername,
			"attempts", newAttempt,
			"error", err.Error(),
		)

		// Mark as permanent failure
		_ = w.repo.MarkFailure(ctx, item.ID, newAttempt, time.Now(), err.Error(), true)

		// Trigger notification (FR-008a)
		if w.notifier != nil {
			mediaTitle := extractMediaTitle(scrobbleBody)
			_ = w.notifier.NotifyPermanentFailure(ctx, item.FamilyGroupID, member.ID, member.TraktUsername, mediaTitle, err.Error())
		}

		return
	}

	// Calculate exponential backoff
	nextAttempt := time.Now().Add(calculateBackoff(newAttempt))

	slog.Warn("queue worker retry failure, rescheduling",
		"item_id", item.ID,
		"member", member.TraktUsername,
		"attempt", newAttempt,
		"next_attempt", nextAttempt.Format(time.RFC3339),
		"error", err.Error(),
	)

	// Update queue item
	_ = w.repo.MarkFailure(ctx, item.ID, newAttempt, nextAttempt, err.Error(), false)
}

// calculateBackoff computes exponential backoff delay with cap.
// Formula: min(BaseBackoffDelay * 2^(attempt-1), MaxBackoffDelay)
func calculateBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return BaseBackoffDelay
	}

	// Exponential: 30s, 60s, 2m, 4m, 8m, ...
	delay := BaseBackoffDelay * time.Duration(1<<uint(attempt-1))

	if delay > MaxBackoffDelay {
		return MaxBackoffDelay
	}

	return delay
}

// extractMediaTitle creates a human-readable media title from ScrobbleBody.
func extractMediaTitle(body common.ScrobbleBody) string {
	if body.Movie != nil && body.Movie.Title != nil {
		title := *body.Movie.Title
		if body.Movie.Year != nil {
			return fmt.Sprintf("%s (%d)", title, *body.Movie.Year)
		}
		return title
	}

	if body.Show != nil {
		showTitle := "Unknown Show"
		if body.Show.Title != nil {
			showTitle = *body.Show.Title
		}
		if body.Episode != nil && body.Episode.Season != nil && body.Episode.Number != nil {
			return fmt.Sprintf("%s S%02dE%02d", showTitle, *body.Episode.Season, *body.Episode.Number)
		}
		return showTitle
	}

	return "Unknown Media"
}

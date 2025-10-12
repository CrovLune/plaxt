package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// MaxRetryAttempts defines the ceiling for retry attempts per queue item.
	MaxRetryAttempts = 5

	// retry status values persisted in the database.
	RetryQueueStatusQueued           = "queued"
	RetryQueueStatusRetrying         = "retrying"
	RetryQueueStatusPermanentFailure = "permanent_failure"
)

var (
	// ErrInvalidRetryItem indicates the queue item violates validation rules.
	ErrInvalidRetryItem = errors.New("store: retry queue item is invalid")
	// ErrRetryAttemptsExceeded indicates the retry ceiling has been reached.
	ErrRetryAttemptsExceeded = errors.New("store: retry attempts exceeded")
)

// RetryQueueItem persists a failed scrobble for durable retry processing.
type RetryQueueItem struct {
	ID            string          `json:"id"`
	FamilyGroupID string          `json:"family_group_id"`
	GroupMemberID string          `json:"group_member_id"`
	Payload       json.RawMessage `json:"payload"`
	AttemptCount  int             `json:"attempt_count"`
	NextAttemptAt time.Time       `json:"next_attempt_at"`
	LastError     string          `json:"last_error,omitempty"`
	Status        string          `json:"status"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// Normalize aligns status casing for downstream comparisons.
func (item *RetryQueueItem) Normalize() {
	if item == nil {
		return
	}
	item.Status = strings.TrimSpace(strings.ToLower(item.Status))
}

// Validate ensures invariants hold prior to persistence or state transitions.
func (item *RetryQueueItem) Validate() error {
	if item == nil {
		return ErrInvalidRetryItem
	}
	item.Normalize()
	if item.FamilyGroupID == "" {
		return fmt.Errorf("%w: family group id is required", ErrInvalidRetryItem)
	}
	if item.GroupMemberID == "" {
		return fmt.Errorf("%w: group member id is required", ErrInvalidRetryItem)
	}
	if len(item.Payload) == 0 {
		return fmt.Errorf("%w: payload cannot be empty", ErrInvalidRetryItem)
	}
	if item.AttemptCount < 0 || item.AttemptCount > MaxRetryAttempts {
		return fmt.Errorf("%w: attempt count %d outside 0-%d", ErrInvalidRetryItem, item.AttemptCount, MaxRetryAttempts)
	}
	if item.NextAttemptAt.IsZero() {
		return fmt.Errorf("%w: next attempt timestamp required", ErrInvalidRetryItem)
	}
	if !isValidRetryStatus(item.Status) {
		return fmt.Errorf("%w: unknown status %q", ErrInvalidRetryItem, item.Status)
	}
	if item.Status == RetryQueueStatusPermanentFailure && item.AttemptCount < MaxRetryAttempts {
		return fmt.Errorf("%w: permanent failure requires attempt count %d", ErrInvalidRetryItem, MaxRetryAttempts)
	}
	return nil
}

// ScheduleNextAttempt updates the item for another retry using exponential backoff.
// baseDelay is used as the initial backoff value; defaults to 30 seconds when <=0.
func (item *RetryQueueItem) ScheduleNextAttempt(now time.Time, baseDelay time.Duration) error {
	if item == nil {
		return ErrInvalidRetryItem
	}
	if item.AttemptCount >= MaxRetryAttempts {
		return ErrRetryAttemptsExceeded
	}
	delay := nextRetryDelay(item.AttemptCount, baseDelay)
	item.AttemptCount++
	item.Status = RetryQueueStatusQueued
	item.NextAttemptAt = now.Add(delay)
	return nil
}

func isValidRetryStatus(status string) bool {
	switch status {
	case RetryQueueStatusQueued,
		RetryQueueStatusRetrying,
		RetryQueueStatusPermanentFailure:
		return true
	default:
		return false
	}
}

func nextRetryDelay(attempt int, baseDelay time.Duration) time.Duration {
	if baseDelay <= 0 {
		baseDelay = 30 * time.Second
	}
	if attempt < 0 {
		attempt = 0
	}
	maxBackoff := 30 * time.Minute
	delay := baseDelay * time.Duration(1<<uint(attempt))
	if delay > maxBackoff {
		return maxBackoff
	}
	return delay
}

package trakt

import (
	"fmt"
	"net/http"

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/store"
)

// Trakt is a client for interacting with the Trakt API. It holds HTTP client
// configuration and references to storage used for caching and scrobbling state.
type Trakt struct {
	ClientId      string
	clientSecret  string
	storage       store.Store
	httpClient    *http.Client
	ml            common.MultipleLock
	queueEventLog *store.QueueEventLog
}

// HttpError implements the error interface for HTTP errors returned by handlers.
type HttpError struct {
	Code    int
	Message string
}

// BroadcastError represents a failed scrobble attempt for a specific group member.
// Used by BroadcastScrobble to return actionable error information including
// member details for retry queue enrollment.
type BroadcastError struct {
	Member     *store.GroupMember // Member whose scrobble failed
	Err        error              // Underlying error
	HTTPStatus int                // HTTP status code (0 if network error)
	EventID    string             // Plex webhook event ID for correlation
	MediaTitle string             // Human-readable media title for logging
}

// Error implements the error interface
func (b BroadcastError) Error() string {
	if b.HTTPStatus > 0 {
		return fmt.Sprintf("member %s (HTTP %d): %v", b.Member.TraktUsername, b.HTTPStatus, b.Err)
	}
	return fmt.Sprintf("member %s: %v", b.Member.TraktUsername, b.Err)
}

// IsRetryable returns true if this error should be queued for retry (transient failure).
func (b BroadcastError) IsRetryable() bool {
	// Network errors (status 0) and specific HTTP status codes are retryable
	return b.HTTPStatus == 0 ||
		b.HTTPStatus == http.StatusTooManyRequests ||
		b.HTTPStatus == http.StatusServiceUnavailable ||
		b.HTTPStatus == http.StatusBadGateway ||
		b.HTTPStatus == http.StatusGatewayTimeout
}

// SetQueueEventLog sets the queue event log for monitoring.
func (t *Trakt) SetQueueEventLog(log *store.QueueEventLog) {
	t.queueEventLog = log
}

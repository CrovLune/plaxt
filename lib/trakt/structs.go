package trakt

import (
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

// SetQueueEventLog sets the queue event log for monitoring.
func (t *Trakt) SetQueueEventLog(log *store.QueueEventLog) {
	t.queueEventLog = log
}

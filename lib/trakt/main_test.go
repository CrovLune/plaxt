package trakt

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newTestTrakt(rt roundTripFunc) *Trakt {
	tr := New("client-id", "client-secret", nil)
	tr.httpClient = &http.Client{Transport: rt}
	return tr
}

func TestFetchDisplayNameSuccessTruncatesLongName(t *testing.T) {
	longName := strings.Repeat("A", common.MaxTraktDisplayNameLength+10)
	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		assert.Equal(t, "Bearer token-123", req.Header.Get("Authorization"))
		assert.Equal(t, "2", req.Header.Get("trakt-api-version"))
		assert.Equal(t, "client-id", req.Header.Get("trakt-api-key"))

		payload := fmt.Sprintf(`{"user":{"name":"%s","username":"fallback"}}`, longName)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       ioutil.NopCloser(strings.NewReader(payload)),
			Header:     make(http.Header),
		}, nil
	})

	tr := newTestTrakt(handler)
	name, truncated, err := tr.FetchDisplayName(context.Background(), "token-123")
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Len(t, name, common.MaxTraktDisplayNameLength)
}

func TestFetchDisplayNameFallsBackToUsername(t *testing.T) {
	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		payload := `{"user":{"name":"","display":"","username":"final-choice"}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       ioutil.NopCloser(strings.NewReader(payload)),
			Header:     make(http.Header),
		}, nil
	})

	tr := newTestTrakt(handler)
	name, truncated, err := tr.FetchDisplayName(context.Background(), "token")
	require.NoError(t, err)
	assert.False(t, truncated)
	assert.Equal(t, "final-choice", name)
}

func TestFetchDisplayNameReturnsErrorOnHTTPFailure(t *testing.T) {
	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Status:     "500 Internal Server Error",
			Body:       ioutil.NopCloser(strings.NewReader("rate limited")),
			Header:     make(http.Header),
		}, nil
	})

	tr := newTestTrakt(handler)
	_, _, err := tr.FetchDisplayName(context.Background(), "token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "trakt users/settings")
}

// --- BroadcastScrobble Tests (T014) ---

func TestBroadcastScrobbleSuccess(t *testing.T) {
	// All members succeed
	callCount := 0
	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		callCount++
		assert.Equal(t, "POST", req.Method)
		assert.Contains(t, req.URL.Path, "/scrobble/start")
		assert.Equal(t, "2", req.Header.Get("trakt-api-version"))
		assert.Equal(t, "client-id", req.Header.Get("trakt-api-key"))

		// Verify Authorization header
		authHeader := req.Header.Get("Authorization")
		assert.True(t, strings.HasPrefix(authHeader, "Bearer token-"))

		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       ioutil.NopCloser(strings.NewReader(`{"action":"start","progress":10}`)),
			Header:     make(http.Header),
		}, nil
	})

	tr := newTestTrakt(handler)

	members := []*store.GroupMember{
		{ID: "m1", TraktUsername: "user1", AccessToken: "token-1"},
		{ID: "m2", TraktUsername: "user2", AccessToken: "token-2"},
		{ID: "m3", TraktUsername: "user3", AccessToken: "token-3"},
	}

	movieTitle := "Test Movie"
	body := common.ScrobbleBody{
		Movie: &common.Movie{
			Title: &movieTitle,
		},
		Progress: 10,
	}

	errors := tr.BroadcastScrobble(context.Background(), "start", body, members, "event-123", "Test Movie (2024)")

	assert.Empty(t, errors)
	assert.Equal(t, 3, callCount)
}

func TestBroadcastScrobblePartialFailure(t *testing.T) {
	// Some members succeed, some fail
	callCount := 0
	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		callCount++
		authHeader := req.Header.Get("Authorization")

		// Fail for user2, succeed for others
		if strings.Contains(authHeader, "token-2") {
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Body:       ioutil.NopCloser(strings.NewReader(`{"error":"rate_limit"}`)),
				Header:     make(http.Header),
			}, nil
		}

		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       ioutil.NopCloser(strings.NewReader(`{"action":"pause","progress":50}`)),
			Header:     make(http.Header),
		}, nil
	})

	tr := newTestTrakt(handler)

	members := []*store.GroupMember{
		{ID: "m1", TraktUsername: "user1", AccessToken: "token-1"},
		{ID: "m2", TraktUsername: "user2", AccessToken: "token-2"},
		{ID: "m3", TraktUsername: "user3", AccessToken: "token-3"},
	}

	errors := tr.BroadcastScrobble(
		context.Background(),
		"pause",
		common.ScrobbleBody{Progress: 50},
		members,
		"event-456",
		"Show S01E01",
	)

	assert.Len(t, errors, 1)
	assert.Equal(t, "user2", errors[0].Member.TraktUsername)
	assert.Equal(t, http.StatusTooManyRequests, errors[0].HTTPStatus)
	assert.True(t, errors[0].IsRetryable())
	assert.Equal(t, 3, callCount)
}

func TestBroadcastScrobbleAllFailures(t *testing.T) {
	// All members fail
	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       ioutil.NopCloser(strings.NewReader(`{"error":"service_unavailable"}`)),
			Header:     make(http.Header),
		}, nil
	})

	tr := newTestTrakt(handler)

	members := []*store.GroupMember{
		{ID: "m1", TraktUsername: "user1", AccessToken: "token-1"},
		{ID: "m2", TraktUsername: "user2", AccessToken: "token-2"},
	}

	errors := tr.BroadcastScrobble(
		context.Background(),
		"stop",
		common.ScrobbleBody{Progress: 95},
		members,
		"event-789",
		"Movie (2024)",
	)

	assert.Len(t, errors, 2)
	for _, err := range errors {
		assert.Equal(t, http.StatusServiceUnavailable, err.HTTPStatus)
		assert.True(t, err.IsRetryable())
	}
}

func TestBroadcastScrobbleNetworkError(t *testing.T) {
	// Network transport error
	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("network timeout")
	})

	tr := newTestTrakt(handler)

	members := []*store.GroupMember{
		{ID: "m1", TraktUsername: "user1", AccessToken: "token-1"},
	}

	errors := tr.BroadcastScrobble(
		context.Background(),
		"start",
		common.ScrobbleBody{Progress: 0},
		members,
		"event-net",
		"Test Content",
	)

	assert.Len(t, errors, 1)
	assert.Contains(t, errors[0].Err.Error(), "network timeout")
	assert.Equal(t, 0, errors[0].HTTPStatus) // Network error = no HTTP status
	assert.True(t, errors[0].IsRetryable())
}

func TestBroadcastScrobbleContextCancellation(t *testing.T) {
	// Context cancelled during broadcast
	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})

	tr := newTestTrakt(handler)

	members := []*store.GroupMember{
		{ID: "m1", TraktUsername: "user1", AccessToken: "token-1"},
		{ID: "m2", TraktUsername: "user2", AccessToken: "token-2"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	errors := tr.BroadcastScrobble(ctx, "start", common.ScrobbleBody{}, members, "event-cancel", "Test")

	assert.Len(t, errors, 2)
	for _, err := range errors {
		assert.Contains(t, err.Err.Error(), "context canceled")
	}
}

func TestBroadcastScrobbleEmptyMembers(t *testing.T) {
	// Empty member list
	tr := newTestTrakt(nil)

	errors := tr.BroadcastScrobble(
		context.Background(),
		"start",
		common.ScrobbleBody{},
		[]*store.GroupMember{},
		"event-empty",
		"Test",
	)

	assert.Nil(t, errors)
}

func TestBroadcastScrobbleLoggingFields(t *testing.T) {
	// Verify FR-008b logging requirements (timestamp, username, media title, error, event ID)
	// This test checks that the BroadcastScrobble method calls are correctly structured
	// The actual log assertion is difficult without log capture, but we verify error structure

	handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Body:       ioutil.NopCloser(strings.NewReader(`{"error":"invalid_media"}`)),
			Header:     make(http.Header),
		}, nil
	})

	tr := newTestTrakt(handler)

	members := []*store.GroupMember{
		{ID: "m1", TraktUsername: "logging-user", AccessToken: "token-1"},
	}

	errors := tr.BroadcastScrobble(
		context.Background(),
		"stop",
		common.ScrobbleBody{Progress: 90},
		members,
		"event-logging-test",
		"Logged Movie (2024)",
	)

	assert.Len(t, errors, 1)
	// Verify BroadcastError contains all required FR-008b fields
	assert.Equal(t, "logging-user", errors[0].Member.TraktUsername)
	assert.Equal(t, "event-logging-test", errors[0].EventID)
	assert.Equal(t, "Logged Movie (2024)", errors[0].MediaTitle)
	assert.NotNil(t, errors[0].Err)
	assert.Equal(t, http.StatusBadRequest, errors[0].HTTPStatus)
}

func TestBroadcastScrobblePermanentVsTransientErrors(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		shouldRetry  bool
	}{
		{"429 Too Many Requests", http.StatusTooManyRequests, true},
		{"503 Service Unavailable", http.StatusServiceUnavailable, true},
		{"502 Bad Gateway", http.StatusBadGateway, true},
		{"504 Gateway Timeout", http.StatusGatewayTimeout, true},
		{"400 Bad Request", http.StatusBadRequest, false},
		{"401 Unauthorized", http.StatusUnauthorized, false},
		{"404 Not Found", http.StatusNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tt.statusCode,
					Body:       ioutil.NopCloser(strings.NewReader(`{"error":"test"}`)),
					Header:     make(http.Header),
				}, nil
			})

			tr := newTestTrakt(handler)

			members := []*store.GroupMember{
				{ID: "m1", TraktUsername: "user1", AccessToken: "token-1"},
			}

			errors := tr.BroadcastScrobble(
				context.Background(),
				"start",
				common.ScrobbleBody{},
				members,
				"event-status",
				"Test",
			)

			require.Len(t, errors, 1)
			assert.Equal(t, tt.statusCode, errors[0].HTTPStatus)
			assert.Equal(t, tt.shouldRetry, errors[0].IsRetryable(), "Status %d retryable mismatch", tt.statusCode)
		})
	}
}

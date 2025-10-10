package trakt

import (
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"crovlune/plaxt/lib/common"
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

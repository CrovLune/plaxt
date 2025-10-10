package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/store"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
)

func TestSelfRoot(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.Host = "foo.bar"
	assert.Equal(t, "http://foo.bar", SelfRoot(req))

	req = httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.Host = "foo.bar"
	req.Header.Set("X-Forwarded-Proto", "https, http")
	assert.Equal(t, "https://foo.bar", SelfRoot(req))

	req = httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.Header.Set("Forwarded", "for=1.2.3.4;proto=https;host=external.example")
	assert.Equal(t, "https://external.example", SelfRoot(req))

	req = httptest.NewRequest(http.MethodGet, "/authorize", nil)
	req.Header.Set("X-Forwarded-Host", "plaxt.example")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Port", "8443")
	assert.Equal(t, "https://plaxt.example:8443", SelfRoot(req))
}

func TestAllowedHostsHandler_single_hostname(t *testing.T) {
	f := allowedHostsHandler("foo.bar")

	rr := httptest.NewRecorder()
	r, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "foo.bar"

	f(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rr, r)
	assert.Equal(t, http.StatusOK, rr.Result().StatusCode)
}

func TestAllowedHostsHandler_multiple_hostnames(t *testing.T) {
	f := allowedHostsHandler("foo.bar, bar.foo")

	rr := httptest.NewRecorder()
	r, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "bar.foo"

	f(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rr, r)
	assert.Equal(t, http.StatusOK, rr.Result().StatusCode)
}

func TestAllowedHostsHandler_mismatch_hostname(t *testing.T) {
	f := allowedHostsHandler("unknown.host")

	rr := httptest.NewRecorder()
	r, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "known.host"

	f(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rr, r)
	assert.Equal(t, http.StatusUnauthorized, rr.Result().StatusCode)
}

func TestAllowedHostsHandler_alwaysAllowHealthcheck(t *testing.T) {
	storage = &MockSuccessStore{}
	f := allowedHostsHandler("unknown.host")

	rr := httptest.NewRecorder()
	r, err := http.NewRequest("GET", "/healthcheck", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "known.host"

	f(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rr, r)
	assert.Equal(t, http.StatusOK, rr.Result().StatusCode)
}

func TestAllowedHostsHandler_allowsRequestWithPortWhenAllowedHasNoPort(t *testing.T) {
	f := allowedHostsHandler("foo.bar")

	rr := httptest.NewRecorder()
	r, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Host = "foo.bar:443"

	f(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rr, r)
	assert.Equal(t, http.StatusOK, rr.Result().StatusCode)
}

type MockSuccessStore struct{}

func (s MockSuccessStore) Ping(ctx context.Context) error            { return nil }
func (s MockSuccessStore) WriteUser(user store.User)                 {}
func (s MockSuccessStore) GetUser(id string) *store.User             { return nil }
func (s MockSuccessStore) GetUserByName(username string) *store.User { return nil }
func (s MockSuccessStore) DeleteUser(id, username string) bool       { return true }
func (s MockSuccessStore) ListUsers() []store.User                   { return nil }
func (s MockSuccessStore) GetScrobbleBody(playerUuid, ratingKey string) common.CacheItem {
	return common.CacheItem{}
}
func (s MockSuccessStore) WriteScrobbleBody(item common.CacheItem) {}
func (s MockSuccessStore) EnqueueScrobble(ctx context.Context, event store.QueuedScrobbleEvent) error {
	return nil
}
func (s MockSuccessStore) DequeueScrobbles(ctx context.Context, userID string, limit int) ([]store.QueuedScrobbleEvent, error) {
	return nil, nil
}
func (s MockSuccessStore) DeleteQueuedScrobble(ctx context.Context, eventID string) error {
	return nil
}
func (s MockSuccessStore) UpdateQueuedScrobbleRetry(ctx context.Context, eventID string, retryCount int) error {
	return nil
}
func (s MockSuccessStore) GetQueueSize(ctx context.Context, userID string) (int, error) {
	return 0, nil
}
func (s MockSuccessStore) GetQueueStatus(ctx context.Context, userID string) (common.QueueStatus, error) {
	return common.QueueStatus{}, nil
}
func (s MockSuccessStore) ListUsersWithQueuedEvents(ctx context.Context) ([]string, error) {
	return nil, nil
}
func (s MockSuccessStore) PurgeQueueForUser(ctx context.Context, userID string) (int, error) {
	return 0, nil
}

type MockFailStore struct{}

func (s MockFailStore) Ping(ctx context.Context) error            { return errors.New("OH NO") }
func (s MockFailStore) WriteUser(user store.User)                 { panic(errors.New("OH NO")) }
func (s MockFailStore) GetUser(id string) *store.User             { panic(errors.New("OH NO")) }
func (s MockFailStore) GetUserByName(username string) *store.User { panic(errors.New("OH NO")) }
func (s MockFailStore) DeleteUser(id, username string) bool       { return false }
func (s MockFailStore) ListUsers() []store.User                   { panic(errors.New("OH NO")) }
func (s MockFailStore) GetScrobbleBody(playerUuid, ratingKey string) common.CacheItem {
	panic(errors.New("OH NO"))
}
func (s MockFailStore) WriteScrobbleBody(item common.CacheItem) { panic(errors.New("OH NO")) }
func (s MockFailStore) EnqueueScrobble(ctx context.Context, event store.QueuedScrobbleEvent) error {
	return errors.New("OH NO")
}
func (s MockFailStore) DequeueScrobbles(ctx context.Context, userID string, limit int) ([]store.QueuedScrobbleEvent, error) {
	return nil, errors.New("OH NO")
}
func (s MockFailStore) DeleteQueuedScrobble(ctx context.Context, eventID string) error {
	return errors.New("OH NO")
}
func (s MockFailStore) UpdateQueuedScrobbleRetry(ctx context.Context, eventID string, retryCount int) error {
	return errors.New("OH NO")
}
func (s MockFailStore) GetQueueSize(ctx context.Context, userID string) (int, error) {
	return 0, errors.New("OH NO")
}
func (s MockFailStore) GetQueueStatus(ctx context.Context, userID string) (common.QueueStatus, error) {
	return common.QueueStatus{}, errors.New("OH NO")
}
func (s MockFailStore) ListUsersWithQueuedEvents(ctx context.Context) ([]string, error) {
	return nil, errors.New("OH NO")
}
func (s MockFailStore) PurgeQueueForUser(ctx context.Context, userID string) (int, error) {
	return 0, errors.New("OH NO")
}

func TestHealthcheck(t *testing.T) {
	var rr *httptest.ResponseRecorder

	r, err := http.NewRequest("GET", "/healthcheck", nil)
	if err != nil {
		t.Fatal(err)
	}

	storage = &MockSuccessStore{}
	rr = httptest.NewRecorder()
	http.Handler(healthcheckHandler()).ServeHTTP(rr, r)
	assert.Equal(t, http.StatusOK, rr.Result().StatusCode)
	assert.Equal(t, "{\"status\":\"OK\"}\n", rr.Body.String())

	storage = &MockFailStore{}
	rr = httptest.NewRecorder()
	http.Handler(healthcheckHandler()).ServeHTTP(rr, r)
	assert.Equal(t, http.StatusServiceUnavailable, rr.Result().StatusCode)
	assert.Equal(t, "{\"status\":\"Service Unavailable\",\"errors\":{\"storage\":\"OH NO\"}}\n", rr.Body.String())
}

func TestPersistAuthorizedUserRenewsExistingUser(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore

	existing := store.NewUser("tester", "oldAccess", "oldRefresh", nil, testStore)

	user, reused, err := persistAuthorizedUser("tester", existing.ID, "newAccess", "newRefresh", nil)
	assert.NoError(t, err)

	assert.True(t, reused)
	assert.NotNil(t, user)
	assert.Equal(t, existing.ID, user.ID)
	assert.Equal(t, "newAccess", user.AccessToken)
	assert.Equal(t, "newRefresh", user.RefreshToken)

	stored := storage.GetUser(existing.ID)
	if assert.NotNil(t, stored) {
		assert.Equal(t, "newAccess", stored.AccessToken)
		assert.Equal(t, "newRefresh", stored.RefreshToken)
	}
}

func TestPersistAuthorizedUserAllowsCaseInsensitiveMatch(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore

	existing := store.NewUser("MixedCaseUser", "oldAccess", "oldRefresh", nil, testStore)

	user, reused, err := persistAuthorizedUser("mixedcaseuser", existing.ID, "newAccess", "newRefresh", nil)
	assert.NoError(t, err)
	assert.True(t, reused)
	if assert.NotNil(t, user) {
		assert.Equal(t, "mixedcaseuser", user.Username)
		assert.Equal(t, "newAccess", user.AccessToken)
		assert.Equal(t, "newRefresh", user.RefreshToken)
	}

	stored := storage.GetUser(existing.ID)
	if assert.NotNil(t, stored) {
		assert.Equal(t, "mixedcaseuser", stored.Username)
	}
}

func TestPersistAuthorizedUserCreatesNewWhenIdMismatch(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore

	other := store.NewUser("other", "oldAccess", "oldRefresh", nil, testStore)

	user, reused, err := persistAuthorizedUser("tester", other.ID, "newAccess", "newRefresh", nil)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, errUsernameMismatch))
	assert.False(t, reused)
	assert.Nil(t, user)

	original := storage.GetUser(other.ID)
	if assert.NotNil(t, original) {
		assert.Equal(t, "oldAccess", original.AccessToken)
		assert.Equal(t, "oldRefresh", original.RefreshToken)
	}
}

func TestPersistAuthorizedUserCreatesNewUser(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore

	displayName := "Alice"
	user, reused, err := persistAuthorizedUser("tester", "", "newAccess", "newRefresh", &displayName)
	assert.NoError(t, err)
	assert.False(t, reused)
	if assert.NotNil(t, user) {
		assert.Equal(t, "tester", user.Username)
		assert.Equal(t, "newAccess", user.AccessToken)
		assert.Equal(t, "newRefresh", user.RefreshToken)
		assert.Equal(t, "Alice", user.TraktDisplayName)
	}

	stored := storage.GetUser(user.ID)
	if assert.NotNil(t, stored) {
		assert.Equal(t, "tester", stored.Username)
		assert.Equal(t, "Alice", stored.TraktDisplayName)
	}
}

func TestAuthorizeSuccessRedirectsWithExistingUser(t *testing.T) {
	prevStorage := storage
	prevAuth := authRequestFunc
	prevTrakt := traktSrv
	prevFetch := fetchDisplayNameFunc
	prevStates := authStates
	defer func() {
		storage = prevStorage
		authRequestFunc = prevAuth
		traktSrv = prevTrakt
		fetchDisplayNameFunc = prevFetch
		authStates = prevStates
	}()

	testStore := newPersistTestStore()
	storage = testStore
	existing := store.NewUser("tester", "oldAccess", "oldRefresh", nil, testStore)
	existingID := existing.ID
	authStates = newAuthStateStore()
	corrID := generateCorrelationID()
	stateToken := createStateToken(authState{
		Mode:          "renew",
		Username:      existing.Username,
		SelectedID:    existingID,
		CorrelationID: corrID,
	})
	authStates = newAuthStateStore()
	corrID = generateCorrelationID()
	stateToken = createStateToken(authState{
		Mode:          "renew",
		Username:      existing.Username,
		SelectedID:    existingID,
		CorrelationID: corrID,
	})

	authRequestFunc = func(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
		return map[string]interface{}{
			"access_token":  "newAccess",
			"refresh_token": "newRefresh",
		}, true
	}

	fetchDisplayNameFunc = func(ctx context.Context, token string) (string, bool, error) {
		return "Alice", false, nil
	}

	req := httptest.NewRequest("GET", "/manual/authorize", nil)
	q := req.URL.Query()
	q.Set("state", stateToken)
	q.Set("code", "abc")
	req.URL.RawQuery = q.Encode()
	req.Host = "plaxt.test"
	resp := httptest.NewRecorder()

	authorize(resp, req)

	assert.Equal(t, http.StatusFound, resp.Code)
	location := resp.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("failed to parse redirect: %v", err)
	}
	vals := parsed.Query()
	assert.Equal(t, "success", vals.Get("result"))
	assert.Equal(t, existingID, vals.Get("id"))
	assert.Equal(t, "renew", vals.Get("mode"))
	assert.Equal(t, "tester", vals.Get("username"))
	assert.Equal(t, "Alice", vals.Get("display_name"))
	assert.Empty(t, vals.Get("display_name_missing"))
	assert.Equal(t, truncateCorrelationID(corrID), vals.Get("correlation_id"))

	updated := storage.GetUser(existingID)
	if assert.NotNil(t, updated) {
		assert.Equal(t, "newAccess", updated.AccessToken)
		assert.Equal(t, "newRefresh", updated.RefreshToken)
		assert.Equal(t, "Alice", updated.TraktDisplayName)
	}
}

func TestAuthorizeSuccessUsesForwardedHeaders(t *testing.T) {
	prevStorage := storage
	prevAuth := authRequestFunc
	prevTrakt := traktSrv
	prevFetch := fetchDisplayNameFunc
	prevStates := authStates
	defer func() {
		storage = prevStorage
		authRequestFunc = prevAuth
		traktSrv = prevTrakt
		fetchDisplayNameFunc = prevFetch
		authStates = prevStates
	}()

	testStore := newPersistTestStore()
	storage = testStore
	existing := store.NewUser("tester", "oldAccess", "oldRefresh", nil, testStore)

	authStates = newAuthStateStore()
	corrID := generateCorrelationID()
	stateToken := createStateToken(authState{
		Mode:          "renew",
		Username:      existing.Username,
		SelectedID:    existing.ID,
		CorrelationID: corrID,
	})

	authRequestFunc = func(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
		return map[string]interface{}{
			"access_token":  "newAccess",
			"refresh_token": "newRefresh",
		}, true
	}
	fetchDisplayNameFunc = func(ctx context.Context, token string) (string, bool, error) {
		return "Alice", false, nil
	}

	req := httptest.NewRequest("GET", "/manual/authorize", nil)
	q := req.URL.Query()
	q.Set("state", stateToken)
	q.Set("code", "def")
	req.URL.RawQuery = q.Encode()
	req.Host = "internal.local:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "plaxt.example")
	req.Header.Set("X-Forwarded-Port", "8443")

	resp := httptest.NewRecorder()
	authorize(resp, req)

	assert.Equal(t, http.StatusFound, resp.Code)
	location := resp.Header().Get("Location")
	assert.True(t, strings.HasPrefix(location, "https://plaxt.example:8443/"), "expected https location, got %s", location)
	parsed, err := url.Parse(location)
	assert.NoError(t, err)
	values := parsed.Query()
	assert.Equal(t, "success", values.Get("result"))
	assert.Equal(t, "renew", values.Get("mode"))
	assert.Equal(t, existing.ID, values.Get("id"))
	assert.Equal(t, truncateCorrelationID(corrID), values.Get("correlation_id"))
}

func TestAuthorizeManualRenewFallsBackToStoredUsername(t *testing.T) {
	prevStorage := storage
	prevAuth := authRequestFunc
	prevTrakt := traktSrv
	prevFetch := fetchDisplayNameFunc
	defer func() {
		storage = prevStorage
		authRequestFunc = prevAuth
		traktSrv = prevTrakt
		fetchDisplayNameFunc = prevFetch
	}()

	testStore := newPersistTestStore()
	storage = testStore
	existing := store.NewUser("MixedCaseUser", "oldAccess", "oldRefresh", nil, testStore)
	existingID := existing.ID
	authStates = newAuthStateStore()
	corrID := generateCorrelationID()
	stateToken := createStateToken(authState{
		Mode:          "renew",
		Username:      "",
		SelectedID:    existingID,
		CorrelationID: corrID,
	})

	var authUsername string
	authRequestFunc = func(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
		authUsername = username
		return map[string]interface{}{
			"access_token":  "newAccess",
			"refresh_token": "newRefresh",
		}, true
	}

	fetchDisplayNameFunc = func(ctx context.Context, token string) (string, bool, error) {
		return "", false, nil
	}
	traktSrv = nil

	req := httptest.NewRequest("GET", "/manual/authorize", nil)
	q := req.URL.Query()
	q.Set("state", stateToken)
	q.Set("code", "abc")
	req.URL.RawQuery = q.Encode()
	req.Host = "plaxt.test"
	resp := httptest.NewRecorder()

	authorize(resp, req)

	assert.Equal(t, http.StatusFound, resp.Code)
	assert.Equal(t, "mixedcaseuser", authUsername)

	location := resp.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("failed to parse redirect: %v", err)
	}
	vals := parsed.Query()
	assert.Equal(t, "success", vals.Get("result"))
	assert.Equal(t, existingID, vals.Get("id"))
	assert.Equal(t, "renew", vals.Get("mode"))
	assert.Equal(t, "mixedcaseuser", vals.Get("username"))
	assert.Equal(t, "1", vals.Get("display_name_missing"))
	assert.Equal(t, truncateCorrelationID(corrID), vals.Get("correlation_id"))

	stored := storage.GetUser(existingID)
	if assert.NotNil(t, stored) {
		assert.Equal(t, "mixedcaseuser", stored.Username)
	}
}

func TestAuthorizeCancellationDoesNotUpdateTokens(t *testing.T) {
	prevStorage := storage
	prevAuth := authRequestFunc
	prevTrakt := traktSrv
	prevFetch := fetchDisplayNameFunc
	defer func() {
		storage = prevStorage
		authRequestFunc = prevAuth
		traktSrv = prevTrakt
		fetchDisplayNameFunc = prevFetch
	}()

	testStore := newPersistTestStore()
	storage = testStore
	existing := store.NewUser("tester", "oldAccess", "oldRefresh", nil, testStore)
	existingID := existing.ID
	authStates = newAuthStateStore()
	corrID := generateCorrelationID()
	stateToken := createStateToken(authState{
		Mode:          "renew",
		Username:      existing.Username,
		SelectedID:    existingID,
		CorrelationID: corrID,
	})

	authRequestFunc = func(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
		panic("should not be called when code missing")
	}

	fetchDisplayNameFunc = func(ctx context.Context, token string) (string, bool, error) {
		return "", false, nil
	}

	req := httptest.NewRequest("GET", "/manual/authorize", nil)
	q := req.URL.Query()
	q.Set("state", stateToken)
	req.URL.RawQuery = q.Encode()
	req.Host = "plaxt.test"
	resp := httptest.NewRecorder()

	authorize(resp, req)

	assert.Equal(t, http.StatusFound, resp.Code)
	location := resp.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("failed to parse redirect: %v", err)
	}
	vals := parsed.Query()
	assert.Equal(t, "cancelled", vals.Get("result"))
	assert.Equal(t, existingID, vals.Get("id"))
	assert.Equal(t, "renew", vals.Get("mode"))
	// Correlation ID should be present for manual renewal
	assert.Equal(t, truncateCorrelationID(corrID), vals.Get("correlation_id"))
	assert.Equal(t, "tester", vals.Get("username"))

	stored := storage.GetUser(existingID)
	if assert.NotNil(t, stored) {
		assert.Equal(t, "oldAccess", stored.AccessToken)
		assert.Equal(t, "oldRefresh", stored.RefreshToken)
	}
}

func TestAuthorizeRequestsManualDisplayNameOnFetchFailure(t *testing.T) {
	prevStorage := storage
	prevAuth := authRequestFunc
	prevFetch := fetchDisplayNameFunc
	defer func() {
		storage = prevStorage
		authRequestFunc = prevAuth
		fetchDisplayNameFunc = prevFetch
	}()

	testStore := newPersistTestStore()
	storage = testStore
	existing := store.NewUser("tester", "oldAccess", "oldRefresh", nil, testStore)
	authStates = newAuthStateStore()
	corrID := generateCorrelationID()
	stateToken := createStateToken(authState{
		Mode:          "renew",
		Username:      existing.Username,
		SelectedID:    existing.ID,
		CorrelationID: corrID,
	})

	authRequestFunc = func(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
		return map[string]interface{}{
			"access_token":  "newAccess",
			"refresh_token": "newRefresh",
		}, true
	}

	fetchDisplayNameFunc = func(ctx context.Context, token string) (string, bool, error) {
		return "", false, errors.New("boom")
	}

	req := httptest.NewRequest("GET", "/manual/authorize", nil)
	q := req.URL.Query()
	q.Set("state", stateToken)
	q.Set("code", "abc")
	req.URL.RawQuery = q.Encode()
	req.Host = "plaxt.test"
	resp := httptest.NewRecorder()

	authorize(resp, req)

	assert.Equal(t, http.StatusFound, resp.Code)
	parsed, _ := url.Parse(resp.Header().Get("Location"))
	vals := parsed.Query()
	assert.Equal(t, "1", vals.Get("display_name_missing"))
	assert.Equal(t, "", vals.Get("display_name"))
	assert.Equal(t, truncateCorrelationID(corrID), vals.Get("correlation_id"))

	stored := storage.GetUser(existing.ID)
	if assert.NotNil(t, stored) {
		assert.Equal(t, "", stored.TraktDisplayName)
	}
}

func TestAuthorizeSuccessWithNewUserKeepsOnboardingMode(t *testing.T) {
	prevStorage := storage
	prevAuth := authRequestFunc
	prevTrakt := traktSrv
	defer func() {
		storage = prevStorage
		authRequestFunc = prevAuth
		traktSrv = prevTrakt
	}()

	testStore := newPersistTestStore()
	storage = testStore
	authStates = newAuthStateStore()
	stateToken := createStateToken(authState{
		Mode:     "onboarding",
		Username: "freshuser",
	})

	authRequestFunc = func(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
		return map[string]interface{}{
			"access_token":  "access",
			"refresh_token": "refresh",
		}, true
	}

	traktSrv = nil

	req := httptest.NewRequest("GET", "/authorize", nil)
	q := req.URL.Query()
	q.Set("state", stateToken)
	q.Set("code", "abc")
	req.URL.RawQuery = q.Encode()
	req.Host = "plaxt.test"
	resp := httptest.NewRecorder()

	authorize(resp, req)

	assert.Equal(t, http.StatusFound, resp.Code)
	parsed, err := url.Parse(resp.Header().Get("Location"))
	if err != nil {
		t.Fatalf("failed to parse redirect: %v", err)
	}
	vals := parsed.Query()
	assert.Equal(t, "success", vals.Get("result"))
	assert.Equal(t, "onboarding", vals.Get("mode"))
	assert.Equal(t, "freshuser", vals.Get("username"))

	if vals.Get("id") == "" {
		t.Fatalf("expected new user id in redirect")
	}
}

func TestAuthorizeMissingUsernameRedirectsToError(t *testing.T) {
	prevStorage := storage
	prevAuth := authRequestFunc
	defer func() {
		storage = prevStorage
		authRequestFunc = prevAuth
	}()

	testStore := newPersistTestStore()
	storage = testStore

	req := httptest.NewRequest("GET", "/authorize", nil)
	req.Host = "plaxt.test"
	resp := httptest.NewRecorder()

	authorize(resp, req)

	assert.Equal(t, http.StatusFound, resp.Code)
	parsed, err := url.Parse(resp.Header().Get("Location"))
	if err != nil {
		t.Fatalf("failed to parse redirect: %v", err)
	}
	vals := parsed.Query()
	assert.Equal(t, "error", vals.Get("result"))
	assert.Equal(t, "onboarding", vals.Get("mode"))
	assert.Equal(t, "Missing username; please try again.", vals.Get("error"))
}

func TestAuthorizeWithTraktErrorReturnsDetailedError(t *testing.T) {
	prevStorage := storage
	prevAuth := authRequestFunc
	prevTrakt := traktSrv
	defer func() {
		storage = prevStorage
		authRequestFunc = prevAuth
		traktSrv = prevTrakt
	}()

	testStore := newPersistTestStore()
	storage = testStore
	existing := store.NewUser("tester", "oldAccess", "oldRefresh", nil, testStore)
	existingID := existing.ID

	// Mock Trakt returning error details
	authRequestFunc = func(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
		return map[string]interface{}{
			"http_status":       400,
			"http_status_text":  "400 Bad Request",
			"error":             "invalid_grant",
			"error_description": "The authorization code has expired",
		}, false
	}

	traktSrv = nil

	authStates = newAuthStateStore()
	corrID := generateCorrelationID()
	stateToken := createStateToken(authState{
		Mode:          "renew",
		Username:      existing.Username,
		SelectedID:    existingID,
		CorrelationID: corrID,
	})

	req := httptest.NewRequest("GET", "/manual/authorize", nil)
	q := req.URL.Query()
	q.Set("state", stateToken)
	q.Set("code", "expiredcode")
	req.URL.RawQuery = q.Encode()
	req.Host = "plaxt.test"
	resp := httptest.NewRecorder()

	authorize(resp, req)

	assert.Equal(t, http.StatusFound, resp.Code)
	location := resp.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil {
		t.Fatalf("failed to parse redirect: %v", err)
	}
	vals := parsed.Query()
	assert.Equal(t, "error", vals.Get("result"))
	assert.Equal(t, existingID, vals.Get("id"))
	assert.Equal(t, "renew", vals.Get("mode"))
	// Should have user-friendly error message for invalid_grant
	assert.Equal(t, "Authorization code expired or invalid. Please try authorizing again.", vals.Get("error"))
	// Should have correlation ID for manual renewal
	assert.Equal(t, truncateCorrelationID(corrID), vals.Get("correlation_id"))

	// Tokens should remain unchanged
	stored := storage.GetUser(existingID)
	if assert.NotNil(t, stored) {
		assert.Equal(t, "oldAccess", stored.AccessToken)
		assert.Equal(t, "oldRefresh", stored.RefreshToken)
	}
}

func TestPrepareAuthorizePage_OnboardingSuccessShowsWebhookStep(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore
	user := store.NewUser("tester", "access", "refresh", nil, testStore)

	req := httptest.NewRequest("GET", "/?result=success&id="+user.ID+"&username=tester", nil)
	req.Host = "plaxt.test"

	page := prepareAuthorizePage(req)

	if len(page.Onboarding.Steps) != 3 {
		t.Fatalf("expected 3 onboarding steps, got %d", len(page.Onboarding.Steps))
	}
	assert.Equal(t, StepComplete, page.Onboarding.Steps[0].State)
	assert.Equal(t, StepComplete, page.Onboarding.Steps[1].State)
	assert.Equal(t, StepActive, page.Onboarding.Steps[2].State)
	assert.Contains(t, page.Onboarding.WebhookURL, user.ID)
}

func TestPrepareAuthorizePage_ManualSuccessActivatesResultStep(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore
	user := store.NewUser("tester", "access", "refresh", nil, testStore)

	req := httptest.NewRequest("GET", "/?mode=renew&id="+user.ID+"&result=success&username=tester", nil)
	req.Host = "plaxt.test"

	page := prepareAuthorizePage(req)
	assert.Equal(t, "renew", page.Mode)
	if len(page.Manual.Steps) != 3 {
		t.Fatalf("expected 3 manual steps, got %d", len(page.Manual.Steps))
	}
	assert.Equal(t, StepComplete, page.Manual.Steps[0].State)
	assert.Equal(t, StepComplete, page.Manual.Steps[1].State)
	assert.Equal(t, StepActive, page.Manual.Steps[2].State)
	if page.Manual.Banner == nil {
		t.Fatalf("expected banner for manual success")
	}
}

func TestPrepareAuthorizePage_ManualErrorShowsBanner(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore
	user := store.NewUser("tester", "access", "refresh", nil, testStore)

	req := httptest.NewRequest("GET", "/?mode=renew&id="+user.ID+"&result=error&error=boom&username=tester", nil)
	req.Host = "plaxt.test"

	page := prepareAuthorizePage(req)
	if assert.NotNil(t, page.Manual.Banner) {
		assert.Equal(t, "error", page.Manual.Banner.Type)
		assert.Equal(t, "boom", page.Manual.Banner.Message)
	}
	assert.Equal(t, StepActive, page.Manual.Steps[2].State)
}

func TestPrepareAuthorizePage_ManualNoSelectionDefaultsToSelectStep(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	storage = newPersistTestStore()

	req := httptest.NewRequest("GET", "/?mode=renew", nil)
	req.Host = "plaxt.test"

	page := prepareAuthorizePage(req)
	if len(page.Manual.Steps) == 0 {
		t.Fatal("expected manual steps")
	}
	assert.Equal(t, StepActive, page.Manual.Steps[0].State)
}

func TestPrepareAuthorizePage_ManualIncludesDisplayName(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore
	display := "Alice Smith"
	user := store.NewUser("tester", "access", "refresh", &display, testStore)

	req := httptest.NewRequest("GET", "/?mode=renew&id="+user.ID, nil)
	req.Host = "plaxt.test"

	page := prepareAuthorizePage(req)
	if assert.NotNil(t, page.Manual.SelectedUser) {
		assert.Equal(t, "Alice Smith", page.Manual.SelectedUser.TraktDisplayName)
	}
	assert.Equal(t, "Alice Smith", page.Manual.DisplayName)
	assert.False(t, page.Manual.DisplayNameMissing)
}

func TestPrepareAuthorizePage_ManualMarksDisplayNameMissing(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore
	user := store.NewUser("tester", "access", "refresh", nil, testStore)

	req := httptest.NewRequest("GET", "/?mode=renew&id="+user.ID+"&display_name_missing=1", nil)
	req.Host = "plaxt.test"

	page := prepareAuthorizePage(req)
	assert.True(t, page.Manual.DisplayNameMissing)
	assert.Equal(t, "", page.Manual.DisplayName)
}

func createStateToken(state authState) string {
	if state.Created.IsZero() {
		state.Created = time.Now()
	}
	return authStates.Create(state)
}

func TestUpdateTraktDisplayNameSuccess(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	testStore := newPersistTestStore()
	storage = testStore
	user := store.NewUser("tester", "access", "refresh", nil, testStore)

	body := bytes.NewBufferString(`{"display_name":"` + strings.Repeat("Z", common.MaxTraktDisplayNameLength+3) + `"}`)
	req := httptest.NewRequest("POST", "/users/"+user.ID+"/trakt-display-name", body)
	req = mux.SetURLVars(req, map[string]string{"id": user.ID})
	resp := httptest.NewRecorder()

	updateTraktDisplayName(resp, req)

	assert.Equal(t, http.StatusOK, resp.Code)
	var payload map[string]interface{}
	_ = json.Unmarshal(resp.Body.Bytes(), &payload)
	if value, ok := payload["display_name"].(string); ok {
		assert.Len(t, value, common.MaxTraktDisplayNameLength)
	}
	assert.Equal(t, true, payload["truncated"])

	stored := storage.GetUser(user.ID)
	if assert.NotNil(t, stored) {
		assert.Len(t, stored.TraktDisplayName, common.MaxTraktDisplayNameLength)
	}
}

func TestUpdateTraktDisplayNameNotFound(t *testing.T) {
	prevStorage := storage
	defer func() { storage = prevStorage }()

	storage = newPersistTestStore()

	body := bytes.NewBuffer(nil)
	_ = json.NewEncoder(body).Encode(map[string]string{"display_name": "name"})
	req := httptest.NewRequest("POST", "/users/missing/trakt-display-name", body)
	req = mux.SetURLVars(req, map[string]string{"id": "missing"})
	resp := httptest.NewRecorder()

	updateTraktDisplayName(resp, req)
	assert.Equal(t, http.StatusNotFound, resp.Code)
}

type persistTestStore struct {
	users  map[string]store.User
	byName map[string]string
}

func newPersistTestStore() *persistTestStore {
	return &persistTestStore{
		users:  make(map[string]store.User),
		byName: make(map[string]string),
	}
}

func (s *persistTestStore) Ping(ctx context.Context) error { return nil }

func (s *persistTestStore) WriteUser(user store.User) {
	if s.users == nil {
		s.users = make(map[string]store.User)
	}
	if s.byName == nil {
		s.byName = make(map[string]string)
	}
	s.users[user.ID] = user
	s.byName[user.Username] = user.ID
}

func (s *persistTestStore) GetUser(id string) *store.User {
	if s.users == nil {
		return nil
	}
	user, ok := s.users[id]
	if !ok {
		return nil
	}
	u := user
	return &u
}

func (s *persistTestStore) GetUserByName(username string) *store.User {
	if s.byName == nil {
		return nil
	}
	id, ok := s.byName[username]
	if !ok {
		return nil
	}
	return s.GetUser(id)
}

func (s *persistTestStore) DeleteUser(id, username string) bool {
	if s.users != nil {
		delete(s.users, id)
	}
	if s.byName != nil {
		delete(s.byName, username)
	}
	return true
}

func (s *persistTestStore) ListUsers() []store.User {
	users := make([]store.User, 0, len(s.users))
	for _, user := range s.users {
		users = append(users, user)
	}
	return users
}

func (s *persistTestStore) GetScrobbleBody(playerUuid, ratingKey string) common.CacheItem {
	return common.CacheItem{}
}

func (s *persistTestStore) WriteScrobbleBody(item common.CacheItem) {}

func (s *persistTestStore) EnqueueScrobble(ctx context.Context, event store.QueuedScrobbleEvent) error {
	return nil
}

func (s *persistTestStore) DequeueScrobbles(ctx context.Context, userID string, limit int) ([]store.QueuedScrobbleEvent, error) {
	return nil, nil
}

func (s *persistTestStore) DeleteQueuedScrobble(ctx context.Context, eventID string) error {
	return nil
}

func (s *persistTestStore) UpdateQueuedScrobbleRetry(ctx context.Context, eventID string, retryCount int) error {
	return nil
}

func (s *persistTestStore) GetQueueSize(ctx context.Context, userID string) (int, error) {
	return 0, nil
}

func (s *persistTestStore) GetQueueStatus(ctx context.Context, userID string) (common.QueueStatus, error) {
	return common.QueueStatus{}, nil
}

func (s *persistTestStore) ListUsersWithQueuedEvents(ctx context.Context) ([]string, error) {
	return nil, nil
}

func (s *persistTestStore) PurgeQueueForUser(ctx context.Context, userID string) (int, error) {
	return 0, nil
}

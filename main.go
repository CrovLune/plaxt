package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"crovlune/plaxt/lib/config"
	"crovlune/plaxt/lib/logging"
	"crovlune/plaxt/lib/store"
	"crovlune/plaxt/lib/trakt"
	"crovlune/plaxt/plexhooks"

	"github.com/etherlabsio/healthcheck"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"golang.org/x/sync/singleflight"
)

var (
	version       string
	commit        string
	date          string
	storage       store.Store
	apiSf         *singleflight.Group
	traktSrv      *trakt.Trakt
	trustProxy    bool   = true
	requestLogMod string = "errors" // errors|important|all|off
	webhookCache  *webhookDedupeCache
)

// webhookDedupeCache prevents rapid-fire duplicate webhook requests
type webhookDedupeCache struct {
	mu      sync.RWMutex
	entries map[string]time.Time
	traktScrobbles map[string]time.Time // tracks scrobbles by trakt account
}

func newWebhookDedupeCache() *webhookDedupeCache {
	return &webhookDedupeCache{
		entries: make(map[string]time.Time),
		traktScrobbles: make(map[string]time.Time),
	}
}

// shouldProcess returns true if this webhook should be processed (not a recent duplicate)
// Deduplicates by Trakt account to prevent multiple Plaxt users from scrobbling the same event
func (c *webhookDedupeCache) shouldProcess(plaxtID, traktDisplayName, event, ratingKey string, viewOffset int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	
	// Key for this specific plaxt ID + media event
	specificKey := fmt.Sprintf("%s:%s:%s:%d", plaxtID, event, ratingKey, viewOffset)
	// Key for this Trakt account + media event (to prevent duplicate scrobbles to same Trakt)
	traktKey := fmt.Sprintf("TRAKT:%s:%s:%s:%d", traktDisplayName, event, ratingKey, viewOffset)
	
	now := time.Now()
	
	// Check if THIS plaxt ID already processed this event recently (within 2 seconds)
	if lastSeen, exists := c.entries[specificKey]; exists {
		if time.Since(lastSeen) < 2*time.Second {
			return false // Same plaxt ID, duplicate within 2 seconds
		}
	}
	
	// Check if this Trakt account already scrobbled this media event recently (within 1 second)
	// This prevents multiple Plaxt users connected to the same Trakt from duplicate scrobbling
	if lastSeen, exists := c.traktScrobbles[traktKey]; exists {
		if time.Since(lastSeen) < 1*time.Second {
			return false // Same Trakt account already scrobbled within 1 second
		}
	}
	
	// Update timestamps
	c.entries[specificKey] = now
	c.traktScrobbles[traktKey] = now
	
	// Clean up old entries (older than 10 seconds) to prevent memory leak
	cutoff := now.Add(-10 * time.Second)
	for k, t := range c.entries {
		if t.Before(cutoff) {
			delete(c.entries, k)
		}
	}
	for k, t := range c.traktScrobbles {
		if t.Before(cutoff) {
			delete(c.traktScrobbles, k)
		}
	}
	
	return true
}

var errUsernameMismatch = errors.New("manual renewal username mismatch")

type authState struct {
	Mode          string
	Username      string
	SelectedID    string
	CorrelationID string
	Created       time.Time
}

type authStateStore struct {
	mu     sync.Mutex
	states map[string]authState
}

func newAuthStateStore() *authStateStore {
	return &authStateStore{
		states: make(map[string]authState),
	}
}

func (s *authStateStore) Create(state authState) string {
	if state.Created.IsZero() {
		state.Created = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var token string
	for {
		token = generateCorrelationID()
		if _, exists := s.states[token]; !exists {
			s.states[token] = state
			break
		}
	}
	return token
}

func (s *authStateStore) Consume(token string) (authState, bool) {
	if token == "" {
		return authState{}, false
	}
	s.mu.Lock()
	state, ok := s.states[token]
	if ok {
		delete(s.states, token)
	}
	s.mu.Unlock()
	if !ok {
		return authState{}, false
	}
	if time.Since(state.Created) > 15*time.Minute {
		return authState{}, false
	}
	return state, true
}

var authStates = newAuthStateStore()

type StepState string

const (
	StepFuture   StepState = "future"
	StepActive   StepState = "active"
	StepComplete StepState = "complete"
)

type WizardStep struct {
	ID          string
	Title       string
	Description string
	State       StepState
	Summary     string
}

type Banner struct {
	Type          string
	Message       string
	Detail        string // Secondary guidance (optional)
	CorrelationID string // Truncated (8-char) for display (optional)
}

type ManualUser struct {
	ID               string
	Username         string
	TraktDisplayName string
	DisplayLabel     string
	WebhookURL       string
	LastUpdated      string
	UpdatedAt        time.Time
}

type OnboardingContext struct {
	Steps      []WizardStep
	Username   string
	WebhookURL string
	Result     string
	Banner     *Banner
}

type ManualRenewContext struct {
	Enabled            bool
	Steps              []WizardStep
	Users              []ManualUser
	SelectedID         string
	WebhookURL         string
	Result             string
	Banner             *Banner
	EmptyMessage       string
	HasUsers           bool
	SelectedUser       *ManualUser
	DisplayName        string
	DisplayNameWarning string
	DisplayNameMissing bool
}

type AuthorizePage struct {
	SelfRoot   string
	ClientID   string
	Mode       string
	Onboarding OnboardingContext
	Manual     ManualRenewContext
}

var authRequestFunc = func(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
	if traktSrv == nil {
		return map[string]interface{}{}, false
	}
	return traktSrv.AuthRequest(redirectURI, username, code, refreshToken, grantType)
}

var fetchDisplayNameFunc = func(ctx context.Context, accessToken string) (string, bool, error) {
	if traktSrv == nil {
		return "", false, nil
	}
	return traktSrv.FetchDisplayName(ctx, accessToken)
}

// generateCorrelationID creates a unique ID for tracking manual renewal attempts
func generateCorrelationID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		// Fallback to timestamp if crypto/rand unavailable
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	// UUID v4 format
	bytes[6] = (bytes[6] & 0x0f) | 0x40 // Version 4
	bytes[8] = (bytes[8] & 0x3f) | 0x80 // Variant 10
	return hex.EncodeToString(bytes)
}

// truncateCorrelationID returns the first 8 characters for display
func truncateCorrelationID(fullID string) string {
	if len(fullID) <= 8 {
		return fullID
	}
	return fullID[:8]
}

// SelfRoot determines our external root URL (scheme://host[:port]) taking into account
// trusted proxy headers if enabled via TRUST_PROXY.
func SelfRoot(r *http.Request) string {
	firstForwardVal := func(raw string) string {
		if raw == "" {
			return ""
		}
		parts := strings.Split(raw, ",")
		if len(parts) == 0 {
			return ""
		}
		return strings.TrimSpace(parts[0])
	}

	parseForwarded := func(raw string) (host, proto string) {
		if raw == "" {
			return "", ""
		}
		for _, segment := range strings.Split(raw, ",") {
			segment = strings.TrimSpace(segment)
			if segment == "" {
				continue
			}
			for _, pair := range strings.Split(segment, ";") {
				pair = strings.TrimSpace(pair)
				if pair == "" {
					continue
				}
				kv := strings.SplitN(pair, "=", 2)
				if len(kv) != 2 {
					continue
				}
				key := strings.ToLower(strings.TrimSpace(kv[0]))
				value := strings.Trim(strings.TrimSpace(kv[1]), "\"")
				switch key {
				case "host":
					if host == "" && value != "" {
						host = value
					}
				case "proto":
					if proto == "" && value != "" {
						proto = strings.ToLower(value)
					}
				}
			}
			if host != "" && proto != "" {
				break
			}
		}
		return host, proto
	}

	scheme := strings.TrimSpace(r.URL.Scheme)
	host := strings.TrimSpace(r.Host)

	if trustProxy {
		if forwardedHost, forwardedProto := parseForwarded(r.Header.Get("Forwarded")); forwardedHost != "" || forwardedProto != "" {
			if forwardedHost != "" {
				host = forwardedHost
			}
			if forwardedProto != "" {
				scheme = forwardedProto
			}
		}
		if xfHost := firstForwardVal(r.Header.Get("X-Forwarded-Host")); xfHost != "" {
			host = xfHost
		}
		if scheme == "" {
			if xfProto := firstForwardVal(r.Header.Get("X-Forwarded-Proto")); xfProto != "" {
				scheme = strings.ToLower(xfProto)
			}
		}
	}

	if scheme == "" && r.TLS != nil {
		scheme = "https"
	}
	if scheme == "" {
		scheme = "http"
	}

	if host == "" && r.URL.Host != "" {
		host = r.URL.Host
	}
	if host == "" {
		host = "localhost"
	}

	if trustProxy && !strings.Contains(host, ":") {
		if xfPort := firstForwardVal(r.Header.Get("X-Forwarded-Port")); xfPort != "" {
			switch xfPort {
			case "80":
				if scheme != "http" {
					host = host + ":" + xfPort
				}
			case "443":
				if scheme != "https" {
					host = host + ":" + xfPort
				}
			default:
				host = host + ":" + xfPort
			}
		}
	}

	u := &url.URL{
		Scheme: scheme,
		Host:   host,
		Path:   "",
	}
	return u.String()
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload != nil {
		_ = json.NewEncoder(w).Encode(payload)
	}
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func createAuthState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Mode     string `json:"mode"`
		Username string `json:"username"`
		ID       string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode != "renew" {
		mode = "onboarding"
	}

	var (
		username      = strings.ToLower(strings.TrimSpace(req.Username))
		selectedID    string
		correlationID string
	)

	switch mode {
	case "renew":
		if storage == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "storage unavailable")
			return
		}
		selectedID = strings.TrimSpace(req.ID)
		if selectedID == "" {
			writeJSONError(w, http.StatusBadRequest, "missing user id")
			return
		}
		user := storage.GetUser(selectedID)
		if user == nil {
			writeJSONError(w, http.StatusNotFound, "user not found")
			return
		}
		username = strings.ToLower(strings.TrimSpace(user.Username))
		if username == "" {
			writeJSONError(w, http.StatusConflict, "user record missing username")
			return
		}
		correlationID = generateCorrelationID()
	case "onboarding":
		if username == "" {
			writeJSONError(w, http.StatusBadRequest, "missing username")
			return
		}
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported mode")
		return
	}

	state := authState{
		Mode:          mode,
		Username:      username,
		SelectedID:    selectedID,
		CorrelationID: correlationID,
		Created:       time.Now(),
	}
	token := authStates.Create(state)

	writeJSON(w, http.StatusOK, map[string]string{"state": token})
}

func authorize(w http.ResponseWriter, r *http.Request) {
	args := r.URL.Query()
	stateToken := strings.TrimSpace(args.Get("state"))
	root := SelfRoot(r)

	mode := "onboarding"
	if strings.ToLower(strings.TrimSpace(args.Get("mode"))) == "renew" {
		mode = "renew"
	}
	username := strings.ToLower(strings.TrimSpace(args.Get("username")))
	existingID := strings.TrimSpace(args.Get("id"))
	correlationID := ""

	if stateToken != "" {
		stateData, ok := authStates.Consume(stateToken)
		if !ok {
			slog.Warn("authorization state expired or invalid", "state", stateToken)
			values := url.Values{}
			values.Set("result", "error")
			values.Set("error", "Authorization session expired. Please start again.")
			if mode == "renew" {
				values.Set("mode", "renew")
				values.Set("step", "confirm")
			} else {
				values.Set("mode", "onboarding")
				values.Set("step", "authorize")
			}
			target := root + "/"
			if len(values) > 0 {
				target = fmt.Sprintf("%s?%s", target, values.Encode())
			}
			http.Redirect(w, r, target, http.StatusFound)
			return
		}
		if strings.TrimSpace(stateData.Mode) != "" {
			mode = stateData.Mode
		}
		if strings.TrimSpace(stateData.Username) != "" {
			username = strings.ToLower(strings.TrimSpace(stateData.Username))
		}
		if strings.TrimSpace(stateData.SelectedID) != "" {
			existingID = strings.TrimSpace(stateData.SelectedID)
		}
		if strings.TrimSpace(stateData.CorrelationID) != "" {
			correlationID = stateData.CorrelationID
		}
	}

	if mode == "renew" && correlationID == "" {
		correlationID = generateCorrelationID()
	}

	redirectWith := func(params map[string]string) {
		values := url.Values{}
		for key, value := range params {
			if strings.TrimSpace(value) != "" {
				values.Set(key, value)
			}
		}
		target := root + "/"
		if len(values) > 0 {
			target = fmt.Sprintf("%s?%s", target, values.Encode())
		}
		http.Redirect(w, r, target, http.StatusFound)
	}

	var manualStoredUser *store.User
	if mode == "renew" && existingID != "" && storage != nil {
		manualStoredUser = storage.GetUser(existingID)
		if manualStoredUser != nil {
			storedUsername := strings.ToLower(strings.TrimSpace(manualStoredUser.Username))
			if storedUsername != "" {
				if username != "" && storedUsername != username {
					if correlationID != "" {
						slog.Info("manual renewal overriding supplied username", "correlation_id", correlationID, "plaxt_id", existingID, "supplied_username", username, "stored_username", storedUsername)
					} else {
						slog.Info("manual renewal overriding supplied username", "supplied_username", username, "plaxt_id", existingID)
					}
				}
				username = storedUsername
			}
		}
	}

	if username == "" {
		if mode == "renew" && correlationID != "" {
			slog.Error("manual renewal error: missing username", "correlation_id", correlationID)
		} else {
			slog.Warn("authorization request missing username")
		}
		errorMessage := "Missing username; please try again."
		if mode == "renew" && existingID != "" && manualStoredUser == nil {
			errorMessage = "Selected user no longer exists. Please choose another user."
		}
		redirectWith(map[string]string{
			"result":         "error",
			"mode":           mode,
			"id":             existingID,
			"error":          errorMessage,
			"correlation_id": truncateCorrelationID(correlationID),
		})
		return
	}

	code := strings.TrimSpace(args.Get("code"))
	if code == "" {
		if mode == "renew" && correlationID != "" {
			slog.Info("manual renewal cancelled", "correlation_id", correlationID, "username", username, "plaxt_id", existingID)
		} else {
			slog.Info("authorization cancelled", "username", username, "plaxt_id", existingID)
		}
		// Redirect back to step 1 of the appropriate flow with cancellation message
		if mode == "renew" {
			redirectWith(map[string]string{
				"result":         "cancelled",
				"mode":           "renew",
				"step":           "select",
				"id":             existingID,
				"username":       username,
				"correlation_id": truncateCorrelationID(correlationID),
			})
		} else {
			redirectWith(map[string]string{
				"result": "cancelled",
				"mode":   "onboarding",
				"step":   "username",
			})
		}
		return
	}

	slog.Info("authorize handling", "username", username, "mode", mode, "plaxt_id", existingID)
	callbackPath := "/authorize"
	if mode == "renew" {
		callbackPath = "/manual/authorize"
	}
	redirectURI := root + callbackPath

	result, ok := authRequestFunc(redirectURI, username, code, "", "authorization_code")
	if !ok {
		// Extract detailed error information from result map
		httpStatus := 0
		if statusVal, exists := result["http_status"]; exists {
			if statusInt, ok := statusVal.(int); ok {
				httpStatus = statusInt
			}
		}
		traktError := "unknown"
		if errVal, exists := result["error"]; exists {
			if errStr, ok := errVal.(string); ok && errStr != "" {
				traktError = errStr
			}
		}
		traktErrorDesc := ""
		if descVal, exists := result["error_description"]; exists {
			if descStr, ok := descVal.(string); ok && descStr != "" {
				traktErrorDesc = descStr
			}
		}

		// Build detailed error message for logs
		errorDetail := fmt.Sprintf("Trakt token exchange failed: %s", traktError)
		if httpStatus != 0 {
			errorDetail = fmt.Sprintf("Trakt token exchange failed: HTTP %d - %s", httpStatus, traktError)
		}
		if traktErrorDesc != "" {
			errorDetail = fmt.Sprintf("%s (%s)", errorDetail, traktErrorDesc)
		}

		// Build user-friendly error message
		userError := "Trakt token exchange failed. Please try again."
		if traktError == "invalid_grant" {
			userError = "Authorization code expired or invalid. Please try authorizing again."
		} else if traktError == "invalid_client" {
			userError = "Invalid Trakt client credentials. Contact the administrator."
		} else if httpStatus == 429 {
			userError = "Too many requests. Please wait a moment and try again."
		} else if traktErrorDesc != "" {
			userError = fmt.Sprintf("Trakt error: %s", traktErrorDesc)
		}

		if mode == "renew" && correlationID != "" {
			slog.Error("manual renewal trakt exchange error", "correlation_id", correlationID, "username", username, "plaxt_id", existingID, "http_status", httpStatus, "trakt_error", traktError, "detail", errorDetail)
		} else {
			slog.Error("authorization failed", "username", username, "plaxt_id", existingID, "detail", errorDetail)
		}

		stepParam := "authorize"
		if mode == "renew" {
			stepParam = "confirm"
		}
		redirectWith(map[string]string{
			"result":         "error",
			"mode":           mode,
			"step":           stepParam,
			"id":             existingID,
			"username":       username,
			"error":          userError,
			"correlation_id": truncateCorrelationID(correlationID),
		})
		return
	}

	accessToken, accessOK := result["access_token"].(string)
	refreshToken, refreshOK := result["refresh_token"].(string)
	if !accessOK || !refreshOK || accessToken == "" || refreshToken == "" {
		if mode == "renew" && correlationID != "" {
			slog.Error("manual renewal trakt response missing tokens", "correlation_id", correlationID, "username", username, "plaxt_id", existingID)
		} else {
			slog.Error("authorization response missing tokens", "username", username, "plaxt_id", existingID)
		}
		stepParam := "authorize"
		if mode == "renew" {
			stepParam = "confirm"
		}
		redirectWith(map[string]string{
			"result":         "error",
			"mode":           mode,
			"step":           stepParam,
			"id":             existingID,
			"username":       username,
			"error":          "Trakt response missing tokens. Please retry.",
			"correlation_id": truncateCorrelationID(correlationID),
		})
		return
	}

	var (
		displayNameValue   string
		displayNamePointer *string
		displayNamePrompt  bool
		displayNameWarning string
	)

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	name, truncated, err := fetchDisplayNameFunc(ctx, accessToken)
	if err != nil {
		displayNamePrompt = true
		if mode == "renew" && correlationID != "" {
			slog.Warn("display name fetch error", "correlation_id", correlationID, "username", username, "plaxt_id", existingID, "error", err)
		} else {
			slog.Warn("display name fetch error", "username", username, "error", err)
		}
	} else if strings.TrimSpace(name) != "" {
		displayNameValue = strings.TrimSpace(name)
		displayNamePointer = &displayNameValue
		if truncated {
			displayNameWarning = "truncated"
		}
	} else {
		displayNamePrompt = true
	}

	user, reused, persistErr := persistAuthorizedUser(username, existingID, accessToken, refreshToken, displayNamePointer)
	if persistErr != nil {
		errMessage := ""
		switch  persistErr{
		case errUsernameMismatch:
			errMessage = "Username mismatch. Authorization was for a different Plex user."
		default:
			errMessage = "Selected user no longer exists. Please choose another user."
		}
		if mode == "renew" && correlationID != "" {
			slog.Error("manual renewal persist error", "correlation_id", correlationID, "username", username, "plaxt_id", existingID, "error", persistErr)
		} else {
			slog.Error("manual renewal failed", "username", username, "plaxt_id", existingID, "error", persistErr)
		}
		stepParam := "authorize"
		if mode == "renew" {
			stepParam = "confirm"
		}
		redirectWith(map[string]string{
			"result":         "error",
			"mode":           mode,
			"step":           stepParam,
			"id":             existingID,
			"username":       username,
			"error":          errMessage,
			"correlation_id": truncateCorrelationID(correlationID),
		})
		return
	}

	if strings.TrimSpace(displayNameValue) == "" {
		displayNameValue = strings.TrimSpace(user.TraktDisplayName)
	}
	if displayNameValue == "" {
		displayNamePointer = nil
	}
	if displayNamePrompt && displayNameValue != "" {
		displayNamePrompt = false
	}

	params := map[string]string{
		"result":   "success",
		"username": user.Username,
		"id":       user.ID,
	}
	if displayNameValue != "" {
		params["display_name"] = displayNameValue
	}
	if displayNameWarning != "" {
		params["display_name_warning"] = displayNameWarning
	}
	if displayNamePrompt {
		params["display_name_missing"] = "1"
	}
	if displayNameWarning == "truncated" {
		if mode == "renew" && correlationID != "" {
			slog.Info("display name truncated", "correlation_id", correlationID, "username", username, "plaxt_id", user.ID)
		} else {
			slog.Info("display name truncated", "username", user.Username)
		}
	}
	if mode == "renew" {
		params["mode"] = "renew"
		params["step"] = "result"
	} else {
		params["mode"] = "onboarding"
		params["step"] = "webhook"
	}

	if reused {
		if correlationID != "" {
			slog.Info("manual renewal success", "correlation_id", correlationID, "username", username, "plaxt_id", user.ID)
			params["correlation_id"] = truncateCorrelationID(correlationID)
		} else {
			slog.Info("manual renewal success", "username", username, "plaxt_id", user.ID)
		}
	} else if existingID != "" && user.ID != existingID {
		// User ID changed during renewal - keep renewal mode but log the change
		slog.Info("manual renewal created new user", "username", username, "new_plaxt_id", user.ID, "previous_id", existingID)
		if correlationID != "" {
			params["correlation_id"] = truncateCorrelationID(correlationID)
		}
	} else {
		slog.Info("authorized", "plaxt_id", user.ID)
	}

	redirectWith(params)
}

func persistAuthorizedUser(username, existingID, accessToken, refreshToken string, displayName *string) (*store.User, bool, error) {
	if existingID != "" {
		existing := storage.GetUser(existingID)
		if existing == nil {
			return nil, false, fmt.Errorf("selected user %s no longer exists", existingID)
		}
		inputUsername := strings.ToLower(strings.TrimSpace(username))
		existingUsername := strings.ToLower(strings.TrimSpace(existing.Username))

		switch {
		case existingUsername == "" && inputUsername != "":
			existingUsername = inputUsername
		case inputUsername == "" && existingUsername != "":
			inputUsername = existingUsername
		}

		if existingUsername != "" && inputUsername != "" && existingUsername != inputUsername {
			return nil, false, errUsernameMismatch
		}
		if inputUsername == "" {
			return nil, false, fmt.Errorf("selected user %s missing username", existingID)
		}

		existing.Username = inputUsername
		existing.UpdateUser(accessToken, refreshToken, displayName)
		return existing, true, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(username))
	newUser := store.NewUser(normalized, accessToken, refreshToken, displayName, storage)
	return &newUser, false, nil
}

func renderLandingPage(w http.ResponseWriter, r *http.Request) {
	page := prepareAuthorizePage(r)
	tmpl := template.Must(template.ParseFiles("static/index.html"))
	if err := tmpl.Execute(w, page); err != nil {
		slog.Error("failed to render landing page", "error", err)
	}
}

func prepareAuthorizePage(r *http.Request) AuthorizePage {
	root := SelfRoot(r)
	query := r.URL.Query()
	mode := strings.ToLower(query.Get("mode"))
	manualUsers := buildManualUsers(root)
	if mode != "renew" {
		mode = "onboarding"
	}
	if mode == "renew" && len(manualUsers) == 0 {
		mode = "onboarding"
	}

	clientID := ""
	if traktSrv != nil {
		clientID = traktSrv.ClientId
	}

	onboarding := buildOnboardingContext(root, query)
	manual := buildManualContext(root, manualUsers, query, mode)

	return AuthorizePage{
		SelfRoot:   root,
		ClientID:   clientID,
		Mode:       mode,
		Onboarding: onboarding,
		Manual:     manual,
	}
}

func buildManualUsers(root string) []ManualUser {
	if storage == nil {
		return nil
	}
	storedUsers := storage.ListUsers()
	manual := make([]ManualUser, 0, len(storedUsers))
	for _, u := range storedUsers {
		refreshed := "unknown"
		if !u.Updated.IsZero() {
			refreshed = u.Updated.UTC().Format("2006-01-02 15:04 MST")
		}
		displayName := strings.TrimSpace(u.TraktDisplayName)
		display := u.Username
		if displayName != "" {
			display = fmt.Sprintf("%s (%s)", u.Username, displayName)
		}
		manual = append(manual, ManualUser{
			ID:               u.ID,
			Username:         u.Username,
			TraktDisplayName: displayName,
			DisplayLabel:     fmt.Sprintf("%s • refreshed %s", display, refreshed),
			WebhookURL:       fmt.Sprintf("%s/api?id=%s", root, u.ID),
			LastUpdated:      refreshed,
			UpdatedAt:        u.Updated,
		})
	}
	if len(manual) > 1 {
		sort.SliceStable(manual, func(i, j int) bool {
			return manual[i].UpdatedAt.After(manual[j].UpdatedAt)
		})
	}
	return manual
}

func buildOnboardingContext(root string, query url.Values) OnboardingContext {
	username := strings.TrimSpace(query.Get("username"))
	modeParam := strings.ToLower(strings.TrimSpace(query.Get("mode")))
	result := strings.ToLower(strings.TrimSpace(query.Get("result")))
	stepHint := strings.ToLower(strings.TrimSpace(query.Get("step")))
	selectedID := strings.TrimSpace(query.Get("id"))
	defaultWebhook := fmt.Sprintf("%s/api?id=generate-your-own-silly", root)
	webhook := defaultWebhook
	if selectedID != "" {
		webhook = fmt.Sprintf("%s/api?id=%s", root, selectedID)
	}

	if modeParam == "renew" {
		result = ""
		stepHint = ""
		username = ""
	}

	steps := []WizardStep{
		{ID: "username", Title: "1. Enter Plex username", Description: "Provide your Plex username to personalise the flow."},
		{ID: "authorize", Title: "2. Authorize with Trakt", Description: "Grant Plaxt permission to scrobble on your behalf."},
		{ID: "webhook", Title: "3. Connect Plex webhook", Description: "Copy the Plaxt URL into Plex settings."},
	}

	activeIndex := 0
	// Check explicit step parameter first, fall back to result-based logic
	switch stepHint {
	case "webhook":
		activeIndex = 2
	case "authorize":
		activeIndex = 1
	case "username":
		activeIndex = 0
	default:
		// Fallback to existing result-based logic for backwards compatibility
		switch  result{
		case "success":
			activeIndex = 2
		case "error", "cancelled":
			activeIndex = 1
		default:
			activeIndex = 0
		}
	}
	steps = applyStepStates(steps, activeIndex)

	// Summaries
	if username != "" {
		steps[0].Summary = fmt.Sprintf("Plex username: %s", username)
	}
	switch result {
	case "success":
		steps[1].Summary = "Trakt authorization complete"
		steps[2].Summary = fmt.Sprintf("Webhook ready: %s", webhook)
	case "error", "cancelled":
		steps[1].Summary = "Awaiting successful Trakt authorization"
	}

	var banner *Banner
	switch result {
	case "success":
		message := "Tokens refreshed! You can keep using your Plaxt webhook."
		if modeParam != "renew" {
			message = "Plaxt is ready! Copy your webhook into Plex to finish setup."
		}
		banner = &Banner{Type: "success", Message: message}
	case "error":
		errMsg := strings.TrimSpace(query.Get("error"))
		if errMsg == "" {
			errMsg = "Unable to refresh tokens. Please try again."
		}
		banner = &Banner{Type: "error", Message: errMsg}
	case "cancelled":
		banner = &Banner{Type: "cancelled", Message: "Trakt authorization was cancelled. Existing tokens are unchanged."}
	}

	return OnboardingContext{
		Steps:      steps,
		Username:   username,
		WebhookURL: webhook,
		Result:     result,
		Banner:     banner,
	}
}

func buildManualContext(_ string, manualUsers []ManualUser, query url.Values, mode string) ManualRenewContext {
	selectedID := strings.TrimSpace(query.Get("id"))
	result := strings.ToLower(strings.TrimSpace(query.Get("result")))
	stepParam := strings.ToLower(strings.TrimSpace(query.Get("step")))
	correlationID := strings.TrimSpace(query.Get("correlation_id"))
	displayNameParam := strings.TrimSpace(query.Get("display_name"))
	displayNameWarning := strings.TrimSpace(query.Get("display_name_warning"))
	displayNameMissing := strings.TrimSpace(query.Get("display_name_missing")) == "1"

	if mode != "renew" {
		selectedID = ""
		result = ""
		stepParam = ""
		correlationID = ""
		displayNameParam = ""
		displayNameWarning = ""
		displayNameMissing = false
	}
	steps := []WizardStep{
		{ID: "select", Title: "1. Choose Plaxt user", Description: "Pick the user whose tokens need renewal."},
		{ID: "confirm", Title: "2. Confirm details", Description: "Review the webhook that will keep working."},
		{ID: "result", Title: "3. Review outcome", Description: "See whether the renewal succeeded."},
	}

	activeIndex := 0
	if mode == "renew" {
		// Check explicit step parameter first, fall back to result-based logic
		switch stepParam {
		case "result":
			activeIndex = 2
		case "confirm":
			activeIndex = 1
		case "select":
			activeIndex = 0
		default:
			// Fallback to existing result-based logic for backwards compatibility
			switch result {
			case "success", "error", "cancelled":
				activeIndex = 2
			case "":
				if selectedID != "" {
					activeIndex = 1
				}
			}
		}
	}
	steps = applyStepStates(steps, activeIndex)

	var selectedUser *ManualUser
	webhook := ""
	for i := range manualUsers {
		if manualUsers[i].ID == selectedID {
			selectedUser = &manualUsers[i]
			webhook = manualUsers[i].WebhookURL
			display := manualUsers[i].Username
			if strings.TrimSpace(manualUsers[i].TraktDisplayName) != "" {
				display = fmt.Sprintf("%s (%s)", manualUsers[i].Username, manualUsers[i].TraktDisplayName)
			}
			steps[0].Summary = fmt.Sprintf("Selected user: %s", display)
			steps[1].Summary = fmt.Sprintf("Confirm renewal for %s", display)
			break
		}
	}

	resolvedDisplayName := displayNameParam
	if resolvedDisplayName == "" && selectedUser != nil {
		resolvedDisplayName = selectedUser.TraktDisplayName
	}
	if strings.TrimSpace(resolvedDisplayName) != "" {
		displayNameMissing = false
	}

	var banner *Banner
	switch result {
	case "success":
		banner = &Banner{
			Type:          "success",
			Message:       "Manual renewal completed. Tokens refreshed.",
			CorrelationID: truncateCorrelationID(correlationID),
		}
		if displayNameWarning == "truncated" {
			banner.Detail = "Trakt display name was truncated to 50 characters."
		}
		steps[2].Summary = "Renewal succeeded"
	case "error":
		errMsg := strings.TrimSpace(query.Get("error"))
		if errMsg == "" {
			errMsg = "Manual renewal failed. Please retry."
		}
		banner = &Banner{
			Type:          "error",
			Message:       errMsg,
			Detail:        "Check the server logs for details or contact support.",
			CorrelationID: truncateCorrelationID(correlationID),
		}
		steps[2].Summary = "Renewal failed"
	case "cancelled":
		banner = &Banner{
			Type:          "cancelled",
			Message:       "Manual renewal was cancelled. No changes applied.",
			Detail:        "Your existing tokens remain active.",
			CorrelationID: truncateCorrelationID(correlationID),
		}
		steps[2].Summary = "Renewal cancelled"
	}

	return ManualRenewContext{
		Enabled:            len(manualUsers) > 0,
		Steps:              steps,
		Users:              manualUsers,
		SelectedID:         selectedID,
		WebhookURL:         webhook,
		Result:             result,
		Banner:             banner,
		EmptyMessage:       "No Plaxt users yet. Ask a maintainer to authorize with Trakt first.",
		HasUsers:           len(manualUsers) > 0,
		SelectedUser:       selectedUser,
		DisplayName:        resolvedDisplayName,
		DisplayNameWarning: displayNameWarning,
		DisplayNameMissing: displayNameMissing,
	}
}

func applyStepStates(steps []WizardStep, activeIndex int) []WizardStep {
	if activeIndex < 0 {
		activeIndex = 0
	}
	if activeIndex >= len(steps) {
		activeIndex = len(steps) - 1
	}
	for i := range steps {
		switch {
		case i < activeIndex:
			steps[i].State = StepComplete
		case i == activeIndex:
			steps[i].State = StepActive
		default:
			steps[i].State = StepFuture
		}
	}
	return steps
}

func updateTraktDisplayName(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}
	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["id"])
	if id == "" {
		http.Error(w, "missing user id", http.StatusBadRequest)
		return
	}
	var payload struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	user := storage.GetUser(id)
	if user == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	trimmed := strings.TrimSpace(payload.DisplayName)
	var namePtr *string
	if trimmed != "" {
		namePtr = &trimmed
	}
	truncated := user.UpdateDisplayName(namePtr)

	slog.Info("updated display name", "username", user.Username, "plaxt_id", user.ID, "truncated", truncated)

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"display_name": user.TraktDisplayName,
		"truncated":    truncated,
	}); err != nil {
		slog.Error("failed to encode display name response", "error", err)
	}
}

func api(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	var payload []byte
	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/x-www-form-urlencoded") {
		// Handle urlencoded payload=...
		if err := r.ParseForm(); err == nil {
			if val := r.PostFormValue("payload"); strings.TrimSpace(val) != "" {
				payload = []byte(val)
			}
		}
	}
	if len(payload) == 0 && strings.Contains(ct, "multipart/form-data") {
		mr, mErr := r.MultipartReader()
		if mErr == nil {
			for {
				part, perr := mr.NextPart()
				if perr == io.EOF {
					break
				}
				if perr != nil {
					break
				}
				if part.FormName() == "payload" {
					payload, _ = io.ReadAll(part)
					break
				}
			}
		}
	}
	if len(payload) == 0 {
		payload = body
		// Also handle legacy body starting with "payload=" (url-encoded)
		if bytes.HasPrefix(bytes.TrimSpace(body), []byte("payload=")) {
			parts := strings.SplitN(string(body), "=", 2)
			if len(parts) == 2 {
				if decoded, uerr := url.QueryUnescape(parts[1]); uerr == nil {
					payload = []byte(decoded)
				}
			}
		}
	}
	// Try strict JSON first; fall back to legacy regex extraction
	webhook, err := plexhooks.ParseWebhook(payload)
	if err != nil || webhook == nil {
		regex := regexp.MustCompile("({.*})")
		match := regex.FindStringSubmatch(string(payload))
		if len(match) == 0 {
			slog.Error("webhook bad request: missing or invalid payload", "content_type", ct)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		webhook, err = plexhooks.ParseWebhook([]byte(match[0]))
		if err != nil || webhook == nil {
			slog.Error("webhook bad request: payload parse failed", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	username := strings.ToLower(webhook.Account.Title)

	// Handle the requests of the same user one at a time
	key := fmt.Sprintf("%s@%s", username, id)
	userInf, err, _ := apiSf.Do(key, func() (any, error) {
		user := storage.GetUser(id)
		if user == nil {
			slog.Warn("invalid id", "id", id)
			return nil, trakt.NewHttpError(http.StatusForbidden, "id is invalid")
		}
		if webhook.Owner && username != user.Username {
			user = storage.GetUserByName(username)
		}

		if user == nil {
			slog.Warn("user not found", "id", id, "username", username)
			return nil, trakt.NewHttpError(http.StatusNotFound, "user not found")
		}

		tokenAge := time.Since(user.Updated).Hours()
		if tokenAge > 23 { // tokens expire after 24 hours, so we refresh after 23
			slog.Info("token refresh request", "username", user.Username, "plaxt_id", user.ID)
			redirectURI := SelfRoot(r) + "/authorize"
			result, success := traktSrv.AuthRequest(redirectURI, user.Username, "", user.RefreshToken, "refresh_token")
			if success {
				user.UpdateUser(result["access_token"].(string), result["refresh_token"].(string), nil)
				slog.Info("token refresh success", "username", user.Username, "plaxt_id", user.ID)
			} else {
				slog.Warn("token refresh failed", "username", user.Username, "plaxt_id", user.ID)
				// Do not delete user on transient failure; return 401 so caller can retry later
				return nil, trakt.NewHttpError(http.StatusUnauthorized, "fail")
			}
		}
		return user, nil
	})
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(err.(trakt.HttpError).Code)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	user := userInf.(*store.User)
	
	// Check for duplicate scrobble to same Trakt account
	if !webhookCache.shouldProcess(id, user.TraktDisplayName, webhook.Event, webhook.Metadata.RatingKey, webhook.Metadata.ViewOffset) {
		slog.Debug("webhook duplicate filtered", "event", webhook.Event, "username", username, "id", id, "trakt_display_name", user.TraktDisplayName, "rating_key", webhook.Metadata.RatingKey)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "duplicate_filtered"})
		return
	}
	
	slog.Info("webhook received", "event", webhook.Event, "username", username, "id", id, "type", strings.ToLower(webhook.Metadata.Type), "title", webhook.Metadata.Title, "show", webhook.Metadata.GrandparentTitle, "season", webhook.Metadata.ParentIndex, "episode", webhook.Metadata.Index, "server", webhook.Server.Title, "client", webhook.Player.Title)

	if username == user.Username {
		traktSrv.Handle(webhook, *user)
	} else {
		slog.Info("username mismatch; skipping", "plex_username", strings.ToLower(webhook.Account.Title), "plaxt_username", user.Username)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"result": "success"})
}

func allowedHostsHandler(allowedHostnames string) func(http.Handler) http.Handler {
	raw := strings.ToLower(allowedHostnames)
	parts := strings.Split(raw, ",")
	allowedHosts := make([]string, 0, len(parts))
	allowedBare := make([]string, 0, len(parts)) // entries without an explicit port
	for _, p := range parts {
		h := strings.TrimSpace(p)
		if h == "" {
			continue
		}
		// Strip optional scheme and any path suffix to keep only host[:port]
		h = strings.TrimPrefix(strings.TrimPrefix(h, "https://"), "http://")
		if idx := strings.Index(h, "/"); idx != -1 {
			h = h[:idx]
		}
		allowedHosts = append(allowedHosts, h)
		// If the allowed entry does NOT specify a port, also remember the bare hostname for matching
		if _, _, err := net.SplitHostPort(h); err != nil {
			// No explicit port present
			allowedBare = append(allowedBare, h)
		}
	}
	slog.Info("allowed hostnames", "hosts", allowedHosts)
	return func(h http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			if r.URL.EscapedPath() == "/healthcheck" {
				h.ServeHTTP(w, r)
				return
			}
			isAllowedHost := false
			lcHost := strings.ToLower(strings.TrimSpace(r.Host))
			// 1) Exact host[:port] match
			for _, value := range allowedHosts {
				if lcHost == value {
					isAllowedHost = true
					break
				}
			}
			// 2) If not matched, try host-only comparison when allowed entry had no explicit port
			if !isAllowedHost && len(allowedBare) > 0 {
				reqHostOnly := lcHost
				if host, _, err := net.SplitHostPort(lcHost); err == nil {
					reqHostOnly = host
				} else {
					// Fall back for inputs like "example.com:443" without brackets
					if idx := strings.LastIndex(lcHost, ":"); idx != -1 && !strings.Contains(lcHost[idx+1:], ":") {
						reqHostOnly = lcHost[:idx]
					}
				}
				for _, base := range allowedBare {
					if reqHostOnly == base {
						isAllowedHost = true
						break
					}
				}
			}
			if !isAllowedHost {
				w.WriteHeader(http.StatusUnauthorized)
				w.Header().Set("Content-Type", "text/plain")
				fmt.Fprintf(w, "Oh no!")
				return
			}
			h.ServeHTTP(w, r)
		}

		return http.HandlerFunc(fn)
	}
}

func healthcheckHandler() http.Handler {
	return healthcheck.Handler(
		healthcheck.WithTimeout(5*time.Second),
		healthcheck.WithChecker("storage", healthcheck.CheckerFunc(func(ctx context.Context) error {
			return storage.Ping(ctx)
		})),
	)
}

// Admin API handlers

type adminUserResponse struct {
	ID               string    `json:"id"`
	Username         string    `json:"username"`
	TraktDisplayName string    `json:"trakt_display_name"`
	WebhookURL       string    `json:"webhook_url"`
	Updated          time.Time `json:"updated"`
	TokenAge         float64   `json:"token_age_hours"`
	Status           string    `json:"status"` // "healthy", "warning", "expired"
}

// listAdminUsers returns a list of all users with their status
func listAdminUsers(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	users := storage.ListUsers()
	response := make([]adminUserResponse, 0, len(users))
	root := SelfRoot(r)

	for _, user := range users {
		tokenAge := time.Since(user.Updated).Hours()
		status := "healthy"
		if tokenAge >= 24 {
			status = "expired"
		} else if tokenAge >= 20 {
			status = "warning"
		}

		response = append(response, adminUserResponse{
			ID:               user.ID,
			Username:         user.Username,
			TraktDisplayName: user.TraktDisplayName,
			WebhookURL:       fmt.Sprintf("%s/api?id=%s", root, user.ID),
			Updated:          user.Updated,
			TokenAge:         tokenAge,
			Status:           status,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// getAdminUser returns details for a specific user
func getAdminUser(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["id"])
	if id == "" {
		http.Error(w, "missing user id", http.StatusBadRequest)
		return
	}

	user := storage.GetUser(id)
	if user == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	root := SelfRoot(r)
	tokenAge := time.Since(user.Updated).Hours()
	status := "healthy"
	if tokenAge >= 24 {
		status = "expired"
	} else if tokenAge >= 20 {
		status = "warning"
	}

	response := adminUserResponse{
		ID:               user.ID,
		Username:         user.Username,
		TraktDisplayName: user.TraktDisplayName,
		WebhookURL:       fmt.Sprintf("%s/api?id=%s", root, user.ID),
		Updated:          user.Updated,
		TokenAge:         tokenAge,
		Status:           status,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// updateAdminUser updates user details
func updateAdminUser(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["id"])
	if id == "" {
		http.Error(w, "missing user id", http.StatusBadRequest)
		return
	}

	user := storage.GetUser(id)
	if user == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	var payload struct {
		Username         *string `json:"username"`
		TraktDisplayName *string `json:"trakt_display_name"`
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Update fields if provided
	if payload.Username != nil && strings.TrimSpace(*payload.Username) != "" {
		user.Username = strings.ToLower(strings.TrimSpace(*payload.Username))
	}

	if payload.TraktDisplayName != nil {
		user.TraktDisplayName = strings.TrimSpace(*payload.TraktDisplayName)
	}

	// Save the updated user
	storage.WriteUser(*user)

	slog.Info("admin user updated", "id", id, "username", user.Username, "display_name", user.TraktDisplayName)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "User updated successfully",
	})
}

// deleteAdminUser deletes a user
func deleteAdminUser(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	id := strings.TrimSpace(vars["id"])
	if id == "" {
		http.Error(w, "missing user id", http.StatusBadRequest)
		return
	}

	user := storage.GetUser(id)
	if user == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Delete the user
	if !storage.DeleteUser(id, user.Username) {
		http.Error(w, "failed to delete user", http.StatusInternalServerError)
		return
	}

	slog.Info("admin user deleted", "id", id, "username", user.Username)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "User deleted successfully",
	})
}

// renderAdminDashboard serves the admin dashboard HTML
func renderAdminDashboard(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "static/admin.html")
}

func main() {
	// init structured logging
	logging.Init()
	// read trust proxy flag
	trustProxy = true
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("TRUST_PROXY"))); v != "" {
		trustProxy = v == "1" || v == "true" || v == "yes"
	}
	// request logging mode
	if m := strings.ToLower(strings.TrimSpace(os.Getenv("REQUEST_LOG"))); m != "" {
		requestLogMod = m
	}

	slog.Info("starting", "version", version, "commit", commit, "date", date)
	if os.Getenv("POSTGRESQL_URL") != "" {
		storage = store.NewPostgresqlStore(store.NewPostgresqlClient(os.Getenv("POSTGRESQL_URL")))
		slog.Info("using postgres storage", "url", os.Getenv("POSTGRESQL_URL"))
	} else if os.Getenv("REDIS_URL") != "" {
		storage = store.NewRedisStore(store.NewRedisClientWithUrl(os.Getenv("REDIS_URL")))
		slog.Info("using redis storage", "url", os.Getenv("REDIS_URL"))
	} else if os.Getenv("REDIS_URI") != "" {
		storage = store.NewRedisStore(store.NewRedisClient(os.Getenv("REDIS_URI"), os.Getenv("REDIS_PASSWORD")))
		slog.Info("using redis storage", "uri", os.Getenv("REDIS_URI"))
	} else {
		storage = store.NewDiskStore()
		slog.Info("using disk storage")
	}
	apiSf = &singleflight.Group{}
	webhookCache = newWebhookDedupeCache()
	traktSrv = trakt.New(config.TraktClientId, config.TraktClientSecret, storage)

	router := mux.NewRouter()
	// Assumption: Behind a proper web server (nginx/traefik, etc) that removes/replaces trusted headers
	router.Use(recoveryMiddleware)
	router.Use(requestLoggerMiddleware())
	if trustProxy {
		router.Use(handlers.ProxyHeaders)
	}
	// which hostnames we are allowing
	// REDIRECT_URI = old legacy list
	// ALLOWED_HOSTNAMES = new accurate config variable
	// No env = all hostnames
	if os.Getenv("REDIRECT_URI") != "" {
		router.Use(allowedHostsHandler(os.Getenv("REDIRECT_URI")))
	} else if os.Getenv("ALLOWED_HOSTNAMES") != "" {
		router.Use(allowedHostsHandler(os.Getenv("ALLOWED_HOSTNAMES")))
	}
	router.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	router.HandleFunc("/authorize", authorize).Methods("GET")
	router.HandleFunc("/manual/authorize", authorize).Methods("GET")
	router.HandleFunc("/oauth/state", createAuthState).Methods("POST")
	router.HandleFunc("/api", api).Methods("POST")
	router.HandleFunc("/users/{id}/trakt-display-name", updateTraktDisplayName).Methods("POST")
	router.Handle("/healthcheck", healthcheckHandler()).Methods("GET")
	
	// Admin routes
	router.HandleFunc("/admin", renderAdminDashboard).Methods("GET")
	router.HandleFunc("/admin/api/users", listAdminUsers).Methods("GET")
	router.HandleFunc("/admin/api/users/{id}", getAdminUser).Methods("GET")
	router.HandleFunc("/admin/api/users/{id}", updateAdminUser).Methods("PUT")
	router.HandleFunc("/admin/api/users/{id}", deleteAdminUser).Methods("DELETE")
	
	router.HandleFunc("/", renderLandingPage).Methods("GET")
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = "0.0.0.0:8000"
	}
	slog.Info("server starting", "listen", listen, "version", version, "commit", commit, "date", date)
	slog.Error("server exited", "error", http.ListenAndServe(listen, router))
}

// requestLoggerMiddleware logs method, path, status, and duration for each request.
func requestLoggerMiddleware() mux.MiddlewareFunc {
	interesting := map[string]struct{}{
		"/api":               {},
		"/authorize":         {},
		"/manual/authorize":  {},
		"/oauth/state":       {},
		"/healthcheck":       {},
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sr := &statusRecorder{ResponseWriter: w, status: 200}
			start := time.Now()
			next.ServeHTTP(sr, r)
			d := time.Since(start)

			shouldLog := false
			switch requestLogMod {
			case "off":
				shouldLog = false
			case "all":
				shouldLog = true
			case "important":
				_, ok := interesting[r.URL.Path]
				shouldLog = ok || sr.status >= 400
			default: // errors
				shouldLog = sr.status >= 400
			}
			if !shouldLog {
				return
			}
			attrs := []any{"method", r.Method, "path", r.URL.Path, "status", sr.status, "duration_ms", d.Milliseconds(), "remote", r.RemoteAddr}
			if sr.status >= 500 {
				slog.Error("request", attrs...)
			} else if sr.status >= 400 {
				slog.Error("request", attrs...)
			} else {
				slog.Info("request", attrs...)
			}
		})
	}
}

// statusRecorder captures HTTP status codes.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// recoveryMiddleware logs panics and prevents server crashes by returning 500.
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic", "method", r.Method, "path", r.URL.Path, "remote", r.RemoteAddr, "error", rec, "stack", string(debug.Stack()))
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

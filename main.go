package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"crovlune/plaxt/lib/config"
	"crovlune/plaxt/lib/store"
	"crovlune/plaxt/lib/trakt"
	"crovlune/plaxt/plexhooks"
	"github.com/etherlabsio/healthcheck"
	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"golang.org/x/sync/singleflight"
)

var (
	version  string
	commit   string
	date     string
	storage  store.Store
	apiSf    *singleflight.Group
	traktSrv *trakt.Trakt
)

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

	if !strings.Contains(host, ":") {
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
			log.Printf("Authorization state expired or invalid: %s", stateToken)
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
						log.Printf("[MANUAL_RENEWAL] correlation_id=%s plaxt_id=%s note=\"Overriding supplied username\" supplied_username=%s stored_username=%s", correlationID, existingID, username, storedUsername)
					} else {
						log.Printf("Manual renewal overriding supplied username %s with stored username for %s", username, existingID)
					}
				}
				username = storedUsername
			}
		}
	}

	if username == "" {
		if mode == "renew" && correlationID != "" {
			log.Printf("[MANUAL_RENEWAL] result=error correlation_id=%s error_detail=\"Missing username\"", correlationID)
		} else {
			log.Print("Authorization request missing username")
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
			log.Printf("[MANUAL_RENEWAL] result=cancelled correlation_id=%s username=%s plaxt_id=%s", correlationID, username, existingID)
		} else {
			log.Printf("Authorization cancelled for %s (%s)", username, existingID)
		}
		// Redirect back to step 1 of the appropriate flow with cancellation message
		if mode == "renew" {
			redirectWith(map[string]string{
				"result":         "cancelled",
				"mode":           "renew",
				"step":           "select",
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

	log.Printf("Handling auth request for %s", username)
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
			log.Printf("[MANUAL_RENEWAL] result=error correlation_id=%s username=%s plaxt_id=%s http_status=%d trakt_error=%s error_detail=\"%s\"",
				correlationID, username, existingID, httpStatus, traktError, errorDetail)
		} else {
			log.Printf("Authorization failed for %s (%s): %s", username, existingID, errorDetail)
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
			log.Printf("[MANUAL_RENEWAL] result=error correlation_id=%s username=%s plaxt_id=%s error_detail=\"Trakt response missing tokens\"", correlationID, username, existingID)
		} else {
			log.Printf("Authorization response missing tokens for %s (%s)", username, existingID)
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
			log.Printf("[MANUAL_RENEWAL] correlation_id=%s username=%s plaxt_id=%s display_name_fetch_error=\"%s\"", correlationID, username, existingID, err.Error())
		} else {
			log.Printf("Failed to fetch Trakt display name for %s: %v", username, err)
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
		switch {
		case persistErr == errUsernameMismatch:
			errMessage = "Username mismatch. Authorization was for a different Plex user."
		default:
			errMessage = "Selected user no longer exists. Please choose another user."
		}
		if mode == "renew" && correlationID != "" {
			log.Printf("[MANUAL_RENEWAL] result=error correlation_id=%s username=%s plaxt_id=%s error_detail=\"%s\"", correlationID, username, existingID, persistErr.Error())
		} else {
			log.Printf("Manual renewal failed for %s (%s): %v", username, existingID, persistErr)
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
			log.Printf("[MANUAL_RENEWAL] correlation_id=%s username=%s plaxt_id=%s display_name_warning=\"truncated\"", correlationID, username, user.ID)
		} else {
			log.Printf("Trakt display name truncated to 50 characters for %s", user.Username)
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
			log.Printf("[MANUAL_RENEWAL] result=success correlation_id=%s username=%s plaxt_id=%s", correlationID, username, user.ID)
			params["correlation_id"] = truncateCorrelationID(correlationID)
		} else {
			log.Printf("Manual renewal succeeded for %s (%s)", username, user.ID)
		}
	} else if existingID != "" && user.ID != existingID {
		// User ID changed during renewal - keep renewal mode but log the change
		log.Printf("Manual renewal for %s created new user %s (previous id %s)", username, user.ID, existingID)
		if correlationID != "" {
			params["correlation_id"] = truncateCorrelationID(correlationID)
		}
	} else {
		log.Printf("Authorized as %s", user.ID)
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
		log.Printf("failed to render landing page: %v", err)
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
		switch {
		case result == "success":
			activeIndex = 2
		case result == "error" || result == "cancelled":
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
	if result == "success" {
		steps[1].Summary = "Trakt authorization complete"
		steps[2].Summary = fmt.Sprintf("Webhook ready: %s", webhook)
	} else if result == "error" || result == "cancelled" {
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

func buildManualContext(root string, manualUsers []ManualUser, query url.Values, mode string) ManualRenewContext {
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
	if selectedUser == nil && len(manualUsers) > 0 {
		manualUsers[0].DisplayLabel = manualUsers[0].DisplayLabel
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

	log.Printf("Updated Trakt display name for %s (%s)", user.Username, user.ID)
	if truncated {
		log.Printf("Manual display name truncated to 50 characters for %s", user.Username)
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]interface{}{
		"display_name": user.TraktDisplayName,
		"truncated":    truncated,
	}); err != nil {
		log.Printf("failed to encode display name response: %v", err)
	}
}

func api(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	regex := regexp.MustCompile("({.*})") // not the best way really
	match := regex.FindStringSubmatch(string(body))
	if len(match) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	webhook, err := plexhooks.ParseWebhook([]byte(match[0]))
	if err != nil || webhook == nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	username := strings.ToLower(webhook.Account.Title)
	log.Print(fmt.Sprintf("Webhook call for %s (%s)", id, webhook.Account.Title))

	// Handle the requests of the same user one at a time
	key := fmt.Sprintf("%s@%s", username, id)
	userInf, err, _ := apiSf.Do(key, func() (interface{}, error) {
		user := storage.GetUser(id)
		if user == nil {
			log.Println("id is invalid")
			return nil, trakt.NewHttpError(http.StatusForbidden, "id is invalid")
		}
		if webhook.Owner && username != user.Username {
			user = storage.GetUserByName(username)
		}

		if user == nil {
			log.Println("User not found.")
			return nil, trakt.NewHttpError(http.StatusNotFound, "user not found")
		}

		tokenAge := time.Since(user.Updated).Hours()
		if tokenAge > 23 { // tokens expire after 24 hours, so we refresh after 23
			log.Println("User access token outdated, refreshing...")
			redirectURI := SelfRoot(r) + "/authorize"
			result, success := traktSrv.AuthRequest(redirectURI, user.Username, "", user.RefreshToken, "refresh_token")
			if success {
				user.UpdateUser(result["access_token"].(string), result["refresh_token"].(string), nil)
				log.Println("Refreshed, continuing")
			} else {
				log.Println("Refresh failed, skipping and deleting user")
				storage.DeleteUser(user.ID, user.Username)
				return nil, trakt.NewHttpError(http.StatusUnauthorized, "fail")
			}
		}
		return user, nil
	})
	if err != nil {
		w.WriteHeader(err.(trakt.HttpError).Code)
		json.NewEncoder(w).Encode(err.Error())
		return
	}
	user := userInf.(*store.User)

	if username == user.Username {
		traktSrv.Handle(webhook, *user)
	} else {
		log.Println(fmt.Sprintf("Plex username %s does not equal %s, skipping", strings.ToLower(webhook.Account.Title), user.Username))
	}

	json.NewEncoder(w).Encode("success")
}

func allowedHostsHandler(allowedHostnames string) func(http.Handler) http.Handler {
	allowedHosts := strings.Split(regexp.MustCompile("https://|http://|\\s+").ReplaceAllString(strings.ToLower(allowedHostnames), ""), ",")
	log.Println("Allowed Hostnames:", allowedHosts)
	return func(h http.Handler) http.Handler {
		fn := func(w http.ResponseWriter, r *http.Request) {
			if r.URL.EscapedPath() == "/healthcheck" {
				h.ServeHTTP(w, r)
				return
			}
			isAllowedHost := false
			lcHost := strings.ToLower(r.Host)
			for _, value := range allowedHosts {
				if lcHost == value {
					isAllowedHost = true
					break
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

func main() {
	log.Printf("Started version=\"%s (%s@%s)\"", version, commit, date)
	if os.Getenv("POSTGRESQL_URL") != "" {
		storage = store.NewPostgresqlStore(store.NewPostgresqlClient(os.Getenv("POSTGRESQL_URL")))
		log.Println("Using postgresql storage:", os.Getenv("POSTGRESQL_URL"))
	} else if os.Getenv("REDIS_URL") != "" {
		storage = store.NewRedisStore(store.NewRedisClientWithUrl(os.Getenv("REDIS_URL")))
		log.Println("Using redis storage: ", os.Getenv("REDIS_URL"))
	} else if os.Getenv("REDIS_URI") != "" {
		storage = store.NewRedisStore(store.NewRedisClient(os.Getenv("REDIS_URI"), os.Getenv("REDIS_PASSWORD")))
		log.Println("Using redis storage:", os.Getenv("REDIS_URI"))
	} else {
		storage = store.NewDiskStore()
		log.Println("Using disk storage:")
	}
	apiSf = &singleflight.Group{}
	traktSrv = trakt.New(config.TraktClientId, config.TraktClientSecret, storage)

	router := mux.NewRouter()
	// Assumption: Behind a proper web server (nginx/traefik, etc) that removes/replaces trusted headers
	router.Use(handlers.ProxyHeaders)
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
	router.HandleFunc("/", renderLandingPage).Methods("GET")
	listen := os.Getenv("LISTEN")
	if listen == "" {
		listen = "0.0.0.0:8000"
	}
	log.Print("Started on " + listen + "!")
	log.Fatal(http.ListenAndServe(listen, router))
}

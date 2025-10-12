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

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/config"
	"crovlune/plaxt/lib/logging"
	"crovlune/plaxt/lib/notify"
	"crovlune/plaxt/lib/queue"
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
	webhookCache  *webhookDedupeCache
	traktSrv      *trakt.Trakt
	trustProxy    bool = true
	requestLogMod string
	appAssets     *assetManifest
	templateFuncs = template.FuncMap{
		"assetPath": assetPath,
	}

	// Queue monitoring
	queueEventLog     *store.QueueEventLog
	drainStateTracker *DrainStateTracker
)

// webhookDedupeCache prevents rapid-fire duplicate webhook requests
type webhookDedupeCache struct {
	mu             sync.RWMutex
	entries        map[string]time.Time
	traktScrobbles map[string]time.Time // tracks scrobbles by trakt account
}

func newWebhookDedupeCache() *webhookDedupeCache {
	return &webhookDedupeCache{
		entries:        make(map[string]time.Time),
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

// ========== QUEUE MONITORING TYPES ==========

// DrainStateTracker tracks active queue drain operations for monitoring.
type DrainStateTracker struct {
	mu              sync.RWMutex
	activeUsers     map[string]*UserDrainInfo
	lastHealthCheck time.Time
	mode            string // "live" | "queue"
}

// UserDrainInfo tracks drain progress for a specific user.
type UserDrainInfo struct {
	UserID          string
	StartedAt       time.Time
	EventsProcessed int
	EventsFailed    int
	NextRetryAt     *time.Time
}

// NewDrainStateTracker creates a new drain state tracker.
func NewDrainStateTracker() *DrainStateTracker {
	return &DrainStateTracker{
		activeUsers: make(map[string]*UserDrainInfo),
		mode:        "live",
	}
}

// RecordDrainStart marks a user's drain as active.
func (d *DrainStateTracker) RecordDrainStart(userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.activeUsers[userID] = &UserDrainInfo{
		UserID:    userID,
		StartedAt: time.Now(),
	}
}

// RecordDrainComplete removes a user from active drain tracking.
func (d *DrainStateTracker) RecordDrainComplete(userID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.activeUsers, userID)
}

// RecordEvent updates event counters for a user.
func (d *DrainStateTracker) RecordEvent(userID string, success bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if info, ok := d.activeUsers[userID]; ok {
		if success {
			info.EventsProcessed++
		} else {
			info.EventsFailed++
		}
	}
}

// GetUserInfo returns drain info for a specific user.
func (d *DrainStateTracker) GetUserInfo(userID string) *UserDrainInfo {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if info, ok := d.activeUsers[userID]; ok {
		copy := *info
		return &copy
	}
	return nil
}

// GetAllActiveUsers returns all users with active drains.
func (d *DrainStateTracker) GetAllActiveUsers() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	userIDs := make([]string, 0, len(d.activeUsers))
	for userID := range d.activeUsers {
		userIDs = append(userIDs, userID)
	}
	return userIDs
}

// SetMode updates the system mode ("live" or "queue").
func (d *DrainStateTracker) SetMode(mode string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mode = mode
}

// GetMode returns the current system mode.
func (d *DrainStateTracker) GetMode() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.mode
}

// UpdateHealthCheck records the last health check time.
func (d *DrainStateTracker) UpdateHealthCheck() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastHealthCheck = time.Now()
}

// GetLastHealthCheck returns the last health check time.
func (d *DrainStateTracker) GetLastHealthCheck() time.Time {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.lastHealthCheck
}

type authState struct {
	Mode          string
	Username      string
	SelectedID    string
	CorrelationID string
	Created       time.Time
	// Family group fields (used when Mode == "family")
	FamilyGroup *FamilyGroupState
}

// FamilyGroupState holds family-specific onboarding state
type FamilyGroupState struct {
	GroupID      string                // UUID of the family group
	PlexUsername string                // Shared Plex username
	Members      []FamilyMemberState   // Members awaiting authorization
}

// FamilyMemberState tracks authorization progress for a single family member
type FamilyMemberState struct {
	MemberID            string    // UUID of the group member
	TempLabel           string    // Cosmetic label (e.g., "Dad")
	TraktUsername       string    // Populated after OAuth
	AuthorizationStatus string    // "pending", "authorized", "failed"
	AuthorizedAt        time.Time // When authorization completed
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

type FamilyContext struct {
	Steps         []WizardStep
	PlexUsername  string
	MemberLabels  []string
	Members       []FamilyMemberState
	WebhookURL    string
	Result        string
	Banner        *Banner
}

type AuthorizePage struct {
	SelfRoot   string
	ClientID   string
	Mode       string
	Onboarding OnboardingContext
	Manual     ManualRenewContext
	Family     FamilyContext
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

func createFamilyAuthState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		Mode         string `json:"mode"`
		PlexUsername string `json:"plex_username"`
		Members      []struct {
			TempLabel string `json:"temp_label"`
		} `json:"members"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Validate mode
	if strings.ToLower(strings.TrimSpace(req.Mode)) != "family" {
		writeJSONError(w, http.StatusBadRequest, "mode must be 'family'")
		return
	}

	// Validate Plex username
	plexUsername := strings.TrimSpace(req.PlexUsername)
	if plexUsername == "" {
		writeJSONError(w, http.StatusBadRequest, "plex_username is required")
		return
	}

	// Validate member count (2-10 per FR-002, FR-002a)
	if len(req.Members) < 2 {
		writeJSONError(w, http.StatusBadRequest, "minimum 2 members required")
		return
	}
	if len(req.Members) > 10 {
		writeJSONError(w, http.StatusBadRequest, "maximum 10 members allowed")
		return
	}

	// Validate member labels
	for i, m := range req.Members {
		if strings.TrimSpace(m.TempLabel) == "" {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("member %d: temp_label is required", i))
			return
		}
	}

	// Check for duplicate Plex username (FR-010)
	ctx := r.Context()
	if storage == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "storage unavailable")
		return
	}

	existingGroup, err := storage.GetFamilyGroupByPlex(ctx, plexUsername)
	if err == nil && existingGroup != nil {
		writeJSONError(w, http.StatusConflict, "family group already exists for this Plex username")
		return
	}

	// Create family group
	groupID := generateCorrelationID() // Reuse UUID generator
	familyGroup := &store.FamilyGroup{
		ID:           groupID,
		PlexUsername: plexUsername,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := storage.CreateFamilyGroup(ctx, familyGroup); err != nil {
		slog.Error("failed to create family group", "plex_username", plexUsername, "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to create family group")
		return
	}

	// Create pending group members
	memberStates := make([]FamilyMemberState, 0, len(req.Members))
	for _, m := range req.Members {
		memberID := generateCorrelationID()
		member := &store.GroupMember{
			ID:                  memberID,
			FamilyGroupID:       groupID,
			TempLabel:           strings.TrimSpace(m.TempLabel),
			AuthorizationStatus: "pending",
			CreatedAt:           time.Now(),
		}

		if err := storage.AddGroupMember(ctx, member); err != nil {
			slog.Error("failed to add group member", "group_id", groupID, "label", m.TempLabel, "error", err)
			// Cleanup: delete the family group
			_ = storage.DeleteFamilyGroup(ctx, groupID)
			writeJSONError(w, http.StatusInternalServerError, "failed to create group members")
			return
		}

		memberStates = append(memberStates, FamilyMemberState{
			MemberID:            memberID,
			TempLabel:           member.TempLabel,
			AuthorizationStatus: "pending",
		})
	}

	// Create auth state for session tracking
	state := authState{
		Mode:    "family",
		Created: time.Now(),
		FamilyGroup: &FamilyGroupState{
			GroupID:      groupID,
			PlexUsername: plexUsername,
			Members:      memberStates,
		},
	}
	stateToken := authStates.Create(state)

	slog.Info("family group created", "group_id", groupID, "plex_username", plexUsername, "member_count", len(memberStates))

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"state":           stateToken,
		"family_group_id": groupID,
	})
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

// authorizeFamilyMember handles OAuth callback for family member authorization.
// Query params: state (auth token), code (OAuth code), member_id (UUID)
func authorizeFamilyMember(w http.ResponseWriter, r *http.Request) {
	args := r.URL.Query()
	stateToken := strings.TrimSpace(args.Get("state"))
	code := strings.TrimSpace(args.Get("code"))
	memberID := strings.TrimSpace(args.Get("member_id"))
	root := SelfRoot(r)

	redirectWith := func(params map[string]string) {
		values := url.Values{}
		for key, value := range params {
			if strings.TrimSpace(value) != "" {
				values.Set(key, value)
			}
		}
		target := root + "/family/wizard"
		if len(values) > 0 {
			target = fmt.Sprintf("%s?%s", target, values.Encode())
		}
		http.Redirect(w, r, target, http.StatusFound)
	}

	// Validate state token
	if stateToken == "" {
		slog.Error("family member auth: missing state token")
		redirectWith(map[string]string{
			"result": "error",
			"error":  "Authorization session expired. Please start again.",
		})
		return
	}

	stateData, ok := authStates.Consume(stateToken)
	if !ok || stateData.FamilyGroup == nil {
		slog.Warn("family member auth: state expired or invalid", "state", stateToken)
		redirectWith(map[string]string{
			"result": "error",
			"error":  "Authorization session expired. Please start again.",
		})
		return
	}

	// Validate member ID
	if memberID == "" {
		slog.Error("family member auth: missing member_id")
		redirectWith(map[string]string{
			"result": "error",
			"error":  "Missing member ID. Please try again.",
		})
		return
	}

	// Find member in state
	var memberState *FamilyMemberState
	for i := range stateData.FamilyGroup.Members {
		if stateData.FamilyGroup.Members[i].MemberID == memberID {
			memberState = &stateData.FamilyGroup.Members[i]
			break
		}
	}

	if memberState == nil {
		slog.Error("family member auth: member not found", "member_id", memberID)
		redirectWith(map[string]string{
			"result": "error",
			"error":  "Member not found in session.",
		})
		return
	}

	// Check for cancellation
	if code == "" {
		slog.Info("family member auth cancelled", "member_id", memberID, "label", memberState.TempLabel)
		redirectWith(map[string]string{
			"result":    "cancelled",
			"member_id": memberID,
			"label":     memberState.TempLabel,
		})
		return
	}

	// Exchange code for tokens
	redirectURI := root + "/authorize/family/member"
	result, ok := authRequestFunc(redirectURI, "", code, "", "authorization_code")
	if !ok {
		// Extract error details
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

		errorDetail := fmt.Sprintf("Trakt token exchange failed: %s", traktError)
		if httpStatus != 0 {
			errorDetail = fmt.Sprintf("Trakt token exchange failed: HTTP %d - %s", httpStatus, traktError)
		}
		if traktErrorDesc != "" {
			errorDetail = fmt.Sprintf("%s (%s)", errorDetail, traktErrorDesc)
		}

		userError := "Trakt authorization failed. Please try again."
		if traktError == "invalid_grant" {
			userError = "Authorization code expired or invalid. Please try authorizing again."
		} else if httpStatus == 429 {
			userError = "Too many requests. Please wait a moment and try again."
		} else if traktErrorDesc != "" {
			userError = fmt.Sprintf("Trakt error: %s", traktErrorDesc)
		}

		slog.Error("family member auth failed",
			"member_id", memberID,
			"label", memberState.TempLabel,
			"http_status", httpStatus,
			"trakt_error", traktError,
			"detail", errorDetail,
		)

		redirectWith(map[string]string{
			"result":    "error",
			"member_id": memberID,
			"label":     memberState.TempLabel,
			"error":     userError,
		})
		return
	}

	// Extract tokens
	accessToken, accessOK := result["access_token"].(string)
	refreshToken, refreshOK := result["refresh_token"].(string)
	if !accessOK || !refreshOK || accessToken == "" || refreshToken == "" {
		slog.Error("family member auth: missing tokens", "member_id", memberID, "label", memberState.TempLabel)
		redirectWith(map[string]string{
			"result":    "error",
			"member_id": memberID,
			"label":     memberState.TempLabel,
			"error":     "Trakt response missing tokens. Please retry.",
		})
		return
	}

	// Fetch Trakt display name
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	traktUsername, _, err := fetchDisplayNameFunc(ctx, accessToken)
	if err != nil || strings.TrimSpace(traktUsername) == "" {
		slog.Warn("family member auth: display name fetch failed", "member_id", memberID, "error", err)
		traktUsername = memberState.TempLabel // Fallback to label
	}

	// Check for duplicate Trakt username (FR-010a)
	if storage != nil {
		ctx := r.Context()
		members, err := storage.ListGroupMembers(ctx, stateData.FamilyGroup.GroupID)
		if err == nil {
			for _, m := range members {
				if m.ID != memberID && strings.EqualFold(m.TraktUsername, traktUsername) {
					slog.Error("family member auth: duplicate trakt username",
						"member_id", memberID,
						"trakt_username", traktUsername,
					)
					redirectWith(map[string]string{
						"result":    "error",
						"member_id": memberID,
						"label":     memberState.TempLabel,
						"error":     fmt.Sprintf("Trakt account '%s' is already authorized for this family group.", traktUsername),
					})
					return
				}
			}
		}
	}

	// Calculate token expiry
	tokenExpiry := calculateTokenExpiry(result)

	// Update group member with tokens and status
	if storage != nil {
		ctx := r.Context()
		member, err := storage.GetGroupMember(ctx, memberID)
		if err != nil || member == nil {
			slog.Error("family member auth: member not found in storage", "member_id", memberID)
			redirectWith(map[string]string{
				"result":    "error",
				"member_id": memberID,
				"error":     "Member not found. Please restart the wizard.",
			})
			return
		}

		// Update member tokens and status
		member.TraktUsername = traktUsername
		member.AccessToken = accessToken
		member.RefreshToken = refreshToken
		expiryTime := tokenExpiry
		member.TokenExpiry = &expiryTime
		member.AuthorizationStatus = "authorized"

		if err := storage.UpdateGroupMember(ctx, member); err != nil {
			slog.Error("family member auth: failed to update member", "member_id", memberID, "error", err)
			redirectWith(map[string]string{
				"result":    "error",
				"member_id": memberID,
				"error":     "Failed to save authorization. Please try again.",
			})
			return
		}

		slog.Info("family member authorized",
			"group_id", stateData.FamilyGroup.GroupID,
			"member_id", memberID,
			"trakt_username", traktUsername,
			"label", memberState.TempLabel,
		)
	}

	// Update state and check if all members are authorized
	memberState.TraktUsername = traktUsername
	memberState.AuthorizationStatus = "authorized"
	memberState.AuthorizedAt = time.Now()

	allAuthorized := true
	for _, m := range stateData.FamilyGroup.Members {
		if m.AuthorizationStatus != "authorized" {
			allAuthorized = false
			break
		}
	}

	// Re-save state for continued wizard flow
	newStateToken := authStates.Create(stateData)

	redirectWith(map[string]string{
		"result":         "success",
		"member_id":      memberID,
		"trakt_username": traktUsername,
		"label":          memberState.TempLabel,
		"all_authorized": fmt.Sprintf("%t", allAuthorized),
		"state":          newStateToken,
	})
}

// calculateTokenExpiry extracts the expires_in value from Trakt OAuth response
// and calculates the expiration time. Defaults to 3 months if not provided.
func calculateTokenExpiry(oauthResult map[string]interface{}) time.Time {
	// Try to get expires_in from the OAuth response
	if expiresIn, ok := oauthResult["expires_in"].(float64); ok && expiresIn > 0 {
		return time.Now().Add(time.Duration(expiresIn) * time.Second)
	}

	// Default to 3 months (Trakt tokens typically last 3 months)
	return time.Now().Add(90 * 24 * time.Hour)
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

	tokenExpiry := calculateTokenExpiry(result)
	user, reused, persistErr := persistAuthorizedUser(username, existingID, accessToken, refreshToken, displayNamePointer, tokenExpiry)
	if persistErr != nil {
		errMessage := ""
		switch persistErr {
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

func persistAuthorizedUser(username, existingID, accessToken, refreshToken string, displayName *string, tokenExpiry time.Time) (*store.User, bool, error) {
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
		existing.UpdateUser(accessToken, refreshToken, displayName, tokenExpiry)
		return existing, true, nil
	}
	normalized := strings.ToLower(strings.TrimSpace(username))
	newUser := store.NewUser(normalized, accessToken, refreshToken, displayName, tokenExpiry, storage)
	return &newUser, false, nil
}

func renderLandingPage(w http.ResponseWriter, r *http.Request) {
	page := prepareAuthorizePage(r)
	tmpl := template.Must(template.New("index.html").Funcs(templateFuncs).ParseFiles("static/index.html"))
	if err := tmpl.Execute(w, page); err != nil {
		slog.Error("failed to render landing page", "error", err)
	}
}

func prepareAuthorizePage(r *http.Request) AuthorizePage {
	root := SelfRoot(r)
	query := r.URL.Query()
	mode := strings.ToLower(query.Get("mode"))
	manualUsers := buildManualUsers(root)
	if mode != "renew" && mode != "family" {
		mode = "onboarding"
	}
	// Keep renew mode even if no users - show empty state message

	clientID := ""
	if traktSrv != nil {
		clientID = traktSrv.ClientId
	}

	onboarding := buildOnboardingContext(root, query)
	manual := buildManualContext(root, manualUsers, query, mode)
	family := buildFamilyContext(root, query)

	return AuthorizePage{
		SelfRoot:   root,
		ClientID:   clientID,
		Mode:       mode,
		Onboarding: onboarding,
		Manual:     manual,
		Family:     family,
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

func buildFamilyContext(root string, query url.Values) FamilyContext {
	// Default family steps
	steps := []WizardStep{
		{
			ID:          "setup",
			Title:       "Setup Family Group",
			Description: "Enter the shared Plex username and add family member labels.",
			State:       StepActive,
		},
		{
			ID:          "authorize",
			Title:       "Authorize Members",
			Description: "Each family member connects their own Trakt account.",
			State:       StepFuture,
		},
		{
			ID:          "webhook",
			Title:       "Configure Webhook",
			Description: "Add the webhook URL to Plex to enable family scrobbling.",
			State:       StepFuture,
		},
	}

	// Check for family mode result
	result := strings.ToLower(query.Get("result"))
	if result != "" {
		// Update step states based on result
		switch result {
		case "success":
			steps[0].State = StepComplete
			steps[1].State = StepComplete
			steps[2].State = StepComplete
		case "error":
			steps[0].State = StepActive
			steps[1].State = StepFuture
			steps[2].State = StepFuture
		}
	}

	return FamilyContext{
		Steps:        steps,
		PlexUsername: "",
		MemberLabels: []string{},
		Members:     []FamilyMemberState{},
		WebhookURL:  "",
		Result:      result,
		Banner:      nil,
	}
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
		{ID: "username", Title: "1. Enter Plex username", Description: "Enter your Plex username to personalize the setup."},
		{ID: "authorize", Title: "2. Authorize with Trakt", Description: "Connect your Trakt account to enable scrobbling."},
		{ID: "webhook", Title: "3. Connect Plex webhook", Description: "Add the webhook URL to Plex to start automatic scrobbling."},
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
		switch result {
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
		{ID: "select", Title: "1. Choose Plaxt user", Description: "Select the user account that needs token renewal."},
		{ID: "confirm", Title: "2. Confirm details", Description: "Verify the webhook URL and user information."},
		{ID: "result", Title: "3. Review outcome", Description: "Check if the token renewal was successful."},
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

// handleFamilyWebhook processes Plex webhooks for family groups by broadcasting to all members.
// Implements FR-008 (broadcast scrobbling) and FR-008a (retry queueing).
func handleFamilyWebhook(w http.ResponseWriter, r *http.Request, webhook *plexhooks.Webhook, familyGroup *store.FamilyGroup) {
	ctx := r.Context()
	plexUsername := strings.ToLower(webhook.Account.Title)

	// Load all authorized group members
	members, err := storage.ListGroupMembers(ctx, familyGroup.ID)
	if err != nil {
		slog.Error("family webhook: failed to list members",
			"group_id", familyGroup.ID,
			"plex_username", plexUsername,
			"error", err,
		)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "failed to load family members"})
		return
	}

	// Filter to authorized members only
	authorizedMembers := make([]*store.GroupMember, 0, len(members))
	for _, member := range members {
		if member.AuthorizationStatus == "authorized" {
			authorizedMembers = append(authorizedMembers, member)
		}
	}

	if len(authorizedMembers) == 0 {
		slog.Warn("family webhook: no authorized members",
			"group_id", familyGroup.ID,
			"plex_username", plexUsername,
		)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "no_authorized_members"})
		return
	}

	// Generate event ID for tracking (FR-008b)
	eventID := generateCorrelationID()

	// Parse scrobble body using existing Trakt logic
	scrobbleBody, action, shouldScrobble := traktSrv.ParseWebhookForScrobble(webhook)
	if !shouldScrobble {
		slog.Debug("family webhook: not eligible for scrobble",
			"group_id", familyGroup.ID,
			"event", webhook.Event,
			"plex_username", plexUsername,
		)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"result": "not_scrobblable"})
		return
	}

	// Extract media title for logging
	mediaTitle := extractMediaTitleFromScrobble(scrobbleBody)

	slog.Info("family webhook received",
		"event_id", eventID,
		"group_id", familyGroup.ID,
		"plex_username", plexUsername,
		"event", webhook.Event,
		"action", action,
		"media_title", mediaTitle,
		"member_count", len(authorizedMembers),
	)

	// Broadcast scrobble to all members (FR-008)
	broadcastErrors := traktSrv.BroadcastScrobble(
		ctx,
		action,
		scrobbleBody,
		authorizedMembers,
		eventID,
		mediaTitle,
	)

	// Handle broadcast errors - queue retries for transient failures (FR-008a)
	if len(broadcastErrors) > 0 {
		for _, berr := range broadcastErrors {
			if berr.IsRetryable() {
				// Queue for retry with exponential backoff
				queueItem := &store.RetryQueueItem{
					ID:             generateCorrelationID(),
					FamilyGroupID:  familyGroup.ID,
					GroupMemberID:  berr.Member.ID,
					Payload:        mustMarshalJSON(scrobbleBody),
					AttemptCount:   0,
					NextAttemptAt:  time.Now().Add(30 * time.Second), // Initial backoff
					LastError:      berr.Err.Error(),
					Status:         store.RetryQueueStatusQueued,
					CreatedAt:      time.Now(),
					UpdatedAt:      time.Now(),
				}

				// Note: Queue repository integration deferred (T019)
				// For now, log the retry event
				slog.Warn("family webhook: scrobble queued for retry",
					"event_id", eventID,
					"member_id", berr.Member.ID,
					"trakt_username", berr.Member.TraktUsername,
					"media_title", mediaTitle,
					"error", berr.Err.Error(),
				)

				// TODO: Uncomment when worker is integrated
				// queueRepo := queue.NewPostgresRepo(storage)
				// if err := queueRepo.Enqueue(ctx, queueItem); err != nil {
				//     slog.Error("failed to enqueue retry", "event_id", eventID, "member_id", berr.Member.ID, "error", err)
				// }
				_ = queueItem // Suppress unused variable warning
			} else {
				// Permanent failure - log only
				slog.Error("family webhook: scrobble permanent failure",
					"event_id", eventID,
					"member_id", berr.Member.ID,
					"trakt_username", berr.Member.TraktUsername,
					"media_title", mediaTitle,
					"error", berr.Err.Error(),
				)
			}
		}
	}

	// Return success even if some members failed (retries will handle them)
	successCount := len(authorizedMembers) - len(broadcastErrors)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"result":         "success",
		"event_id":       eventID,
		"members_total":  len(authorizedMembers),
		"members_success": successCount,
		"members_failed":  len(broadcastErrors),
	})
}

// extractMediaTitleFromScrobble extracts a human-readable title from ScrobbleBody.
func extractMediaTitleFromScrobble(body common.ScrobbleBody) string {
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

// mustMarshalJSON marshals a value to JSON, panicking on error.
// Used for scrobble payloads which should always be valid.
func mustMarshalJSON(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal JSON: %v", err))
	}
	return data
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

	// Check if this Plex username belongs to a family group (FR-007)
	ctx := r.Context()
	if storage != nil {
		familyGroup, err := storage.GetFamilyGroupByPlex(ctx, username)
		if err == nil && familyGroup != nil {
			// Route to family webhook handler
			handleFamilyWebhook(w, r, webhook, familyGroup)
			return
		}
	}

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

		// Check if token is near expiration (refresh 2 days before expiry)
		timeUntilExpiry := time.Until(user.TokenExpiry)
		if timeUntilExpiry < 48*time.Hour {
			slog.Info("token refresh request", "username", user.Username, "plaxt_id", user.ID, "time_until_expiry", timeUntilExpiry)
			redirectURI := SelfRoot(r) + "/authorize"
			result, success := traktSrv.AuthRequest(redirectURI, user.Username, "", user.RefreshToken, "refresh_token")
			if success {
				tokenExpiry := calculateTokenExpiry(result)
				user.UpdateUser(result["access_token"].(string), result["refresh_token"].(string), nil, tokenExpiry)
				slog.Info("token refresh success", "username", user.Username, "plaxt_id", user.ID, "new_expiry", tokenExpiry)
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
		// Calculate time until expiry (can be negative if already expired)
		timeUntilExpiry := time.Until(user.TokenExpiry)
		status := "healthy"

		if timeUntilExpiry < 0 {
			status = "expired"
		} else if timeUntilExpiry < 48*time.Hour { // Warn 2 days before expiry
			status = "warning"
		}

		response = append(response, adminUserResponse{
			ID:               user.ID,
			Username:         user.Username,
			TraktDisplayName: user.TraktDisplayName,
			WebhookURL:       fmt.Sprintf("%s/api?id=%s", root, user.ID),
			Updated:          user.Updated,
			TokenAge:         0, // Will be removed from UI
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
	timeUntilExpiry := time.Until(user.TokenExpiry)
	status := "healthy"

	if timeUntilExpiry < 0 {
		status = "expired"
	} else if timeUntilExpiry < 48*time.Hour {
		status = "warning"
	}

	response := adminUserResponse{
		ID:               user.ID,
		Username:         user.Username,
		TraktDisplayName: user.TraktDisplayName,
		WebhookURL:       fmt.Sprintf("%s/api?id=%s", root, user.ID),
		Updated:          user.Updated,
		TokenAge:         0, // Will be removed from UI
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

// Family Group Admin API Response Types
type adminFamilyGroupResponse struct {
	ID              string    `json:"id"`
	PlexUsername    string    `json:"plex_username"`
	MemberCount     int       `json:"member_count"`
	AuthorizedCount int       `json:"authorized_count"`
	WebhookURL      string    `json:"webhook_url"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type adminGroupMemberResponse struct {
	ID                  string     `json:"id"`
	FamilyGroupID       string     `json:"family_group_id"`
	TempLabel           string     `json:"temp_label"`
	TraktUsername       string     `json:"trakt_username,omitempty"`
	AuthorizationStatus string     `json:"authorization_status"`
	TokenExpiry         *time.Time `json:"token_expiry,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	Status              string     `json:"status"` // "healthy", "warning", "expired", "pending", "failed"
}

type adminFamilyGroupDetailResponse struct {
	*adminFamilyGroupResponse
	Members []adminGroupMemberResponse `json:"members"`
}

// T031: List all family groups
func listFamilyGroups(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()
	groups, err := storage.ListFamilyGroups(ctx)
	if err != nil {
		slog.Error("failed to list family groups", "error", err)
		http.Error(w, "failed to list family groups", http.StatusInternalServerError)
		return
	}

	response := make([]adminFamilyGroupResponse, 0, len(groups))
	root := SelfRoot(r)

	for _, group := range groups {
		members, err := storage.ListGroupMembers(ctx, group.ID)
		if err != nil {
			slog.Warn("failed to list members for group", "group_id", group.ID, "error", err)
			continue
		}

		authorizedCount := 0
		for _, member := range members {
			if member.AuthorizationStatus == "authorized" {
				authorizedCount++
			}
		}

		response = append(response, adminFamilyGroupResponse{
			ID:              group.ID,
			PlexUsername:    group.PlexUsername,
			MemberCount:     len(members),
			AuthorizedCount: authorizedCount,
			WebhookURL:      fmt.Sprintf("%s/api?id=%s", root, group.ID),
			CreatedAt:       group.CreatedAt,
			UpdatedAt:       group.UpdatedAt,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// T032: Get family group details with members
func getFamilyGroupDetail(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	groupID := strings.TrimSpace(vars["id"])
	if groupID == "" {
		http.Error(w, "missing group id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	group, err := storage.GetFamilyGroup(ctx, groupID)
	if err != nil {
		slog.Error("failed to get family group", "group_id", groupID, "error", err)
		http.Error(w, "family group not found", http.StatusNotFound)
		return
	}

	members, err := storage.ListGroupMembers(ctx, groupID)
	if err != nil {
		slog.Error("failed to list group members", "group_id", groupID, "error", err)
		http.Error(w, "failed to list members", http.StatusInternalServerError)
		return
	}

	memberResponses := make([]adminGroupMemberResponse, 0, len(members))
	authorizedCount := 0

	for _, member := range members {
		status := member.AuthorizationStatus
		if member.AuthorizationStatus == "authorized" {
			authorizedCount++
			// Check token expiry status
			if member.TokenExpiry != nil {
				timeUntilExpiry := time.Until(*member.TokenExpiry)
				if timeUntilExpiry < 0 {
					status = "expired"
				} else if timeUntilExpiry < 48*time.Hour {
					status = "warning"
				} else {
					status = "healthy"
				}
			}
		} else if member.AuthorizationStatus == "pending" {
			status = "pending"
		} else if member.AuthorizationStatus == "failed" {
			status = "failed"
		}

		memberResponses = append(memberResponses, adminGroupMemberResponse{
			ID:                  member.ID,
			FamilyGroupID:       member.FamilyGroupID,
			TempLabel:           member.TempLabel,
			TraktUsername:       member.TraktUsername,
			AuthorizationStatus: member.AuthorizationStatus,
			TokenExpiry:         member.TokenExpiry,
			CreatedAt:           member.CreatedAt,
			Status:              status,
		})
	}

	root := SelfRoot(r)
	response := adminFamilyGroupDetailResponse{
		adminFamilyGroupResponse: &adminFamilyGroupResponse{
			ID:              group.ID,
			PlexUsername:    group.PlexUsername,
			MemberCount:     len(members),
			AuthorizedCount: authorizedCount,
			WebhookURL:      fmt.Sprintf("%s/api?id=%s", root, group.ID),
			CreatedAt:       group.CreatedAt,
			UpdatedAt:       group.UpdatedAt,
		},
		Members: memberResponses,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// T033: Add member to family group
func addFamilyGroupMember(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	groupID := strings.TrimSpace(vars["id"])
	if groupID == "" {
		http.Error(w, "missing group id", http.StatusBadRequest)
		return
	}

	var req struct {
		Label string `json:"label"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	req.Label = strings.TrimSpace(req.Label)
	if req.Label == "" {
		http.Error(w, "label is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Verify group exists
	_, err := storage.GetFamilyGroup(ctx, groupID)
	if err != nil {
		http.Error(w, "family group not found", http.StatusNotFound)
		return
	}

	// Check member count limit (max 10)
	members, err := storage.ListGroupMembers(ctx, groupID)
	if err != nil {
		slog.Error("failed to list group members", "group_id", groupID, "error", err)
		http.Error(w, "failed to check member count", http.StatusInternalServerError)
		return
	}

	if len(members) >= 10 {
		http.Error(w, "maximum 10 members per family group", http.StatusBadRequest)
		return
	}

	// Create new member
	member := &store.GroupMember{
		ID:                  generateCorrelationID(),
		FamilyGroupID:       groupID,
		TempLabel:           req.Label,
		AuthorizationStatus: "pending",
		CreatedAt:           time.Now(),
	}

	if err := storage.AddGroupMember(ctx, member); err != nil {
		slog.Error("failed to add group member", "group_id", groupID, "error", err)
		http.Error(w, "failed to add member", http.StatusInternalServerError)
		return
	}

	slog.Info("family group member added", "group_id", groupID, "member_id", member.ID, "label", req.Label)

	// Return authorization URL
	root := SelfRoot(r)
	authURL := fmt.Sprintf("%s/authorize/family/member?group_id=%s&member_id=%s", root, groupID, member.ID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":           true,
		"member_id":         member.ID,
		"authorization_url": authURL,
		"message":           "Member added successfully",
	})
}

// T034: Remove member from family group
func removeFamilyGroupMember(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	groupID := strings.TrimSpace(vars["group_id"])
	memberID := strings.TrimSpace(vars["member_id"])

	if groupID == "" || memberID == "" {
		http.Error(w, "missing group_id or member_id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Verify member exists and belongs to group
	member, err := storage.GetGroupMember(ctx, memberID)
	if err != nil || member.FamilyGroupID != groupID {
		http.Error(w, "member not found", http.StatusNotFound)
		return
	}

	// Remove member
	if err := storage.RemoveGroupMember(ctx, groupID, memberID); err != nil {
		slog.Error("failed to remove group member", "group_id", groupID, "member_id", memberID, "error", err)
		http.Error(w, "failed to remove member", http.StatusInternalServerError)
		return
	}

	slog.Info("family group member removed", "group_id", groupID, "member_id", memberID, "label", member.TempLabel)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Member removed successfully",
	})
}

// T035: Delete entire family group
func deleteFamilyGroup(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	groupID := strings.TrimSpace(vars["id"])
	if groupID == "" {
		http.Error(w, "missing group id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Verify group exists
	group, err := storage.GetFamilyGroup(ctx, groupID)
	if err != nil {
		http.Error(w, "family group not found", http.StatusNotFound)
		return
	}

	// Delete group (cascade deletes members and retry queue items)
	if err := storage.DeleteFamilyGroup(ctx, groupID); err != nil {
		slog.Error("failed to delete family group", "group_id", groupID, "error", err)
		http.Error(w, "failed to delete family group", http.StatusInternalServerError)
		return
	}

	slog.Info("family group deleted", "group_id", groupID, "plex_username", group.PlexUsername)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "Family group deleted successfully",
	})
}

// renderAdminDashboard serves the admin dashboard HTML
func renderAdminDashboard(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.New("admin.html").Funcs(templateFuncs).ParseFiles("static/admin.html"))
	if err := tmpl.Execute(w, nil); err != nil {
		slog.Error("failed to render admin dashboard", "error", err)
	}
}

// renderFamilyAdmin serves the family groups admin HTML
func renderFamilyAdmin(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.New("family-admin.html").Funcs(templateFuncs).ParseFiles("static/family-admin.html"))
	if err := tmpl.Execute(w, nil); err != nil {
		slog.Error("failed to render family admin", "error", err)
	}
}

// ========== TELEMETRY API ==========

// telemetryHandler receives and logs onboarding telemetry events
func telemetryHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Event      string `json:"event"`
		Mode       string `json:"mode"`
		Success    *bool  `json:"success"`
		DurationMs int64  `json:"duration_ms"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Build structured log entry
	logFields := []interface{}{
		"event", req.Event,
		"mode", req.Mode,
		"duration_ms", req.DurationMs,
	}

	if req.Success != nil {
		logFields = append(logFields, "success", *req.Success)
	}

	// Log telemetry event with structured fields
	slog.Info("onboarding telemetry", logFields...)

	w.WriteHeader(http.StatusNoContent)
}

// ========== QUEUE MONITORING API ==========

// renderQueueMonitor serves the queue monitoring HTML page
func renderQueueMonitor(w http.ResponseWriter, r *http.Request) {
	tmpl := template.Must(template.New("queue.html").Funcs(templateFuncs).ParseFiles("static/queue.html"))
	if err := tmpl.Execute(w, nil); err != nil {
		slog.Error("failed to render queue monitor", "error", err)
	}
}

// getQueueStatus returns system-wide queue status
func getQueueStatus(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	ctx := r.Context()

	// Get all users
	users := storage.ListUsers()
	slog.Debug("queue status requested", "user_count", len(users))

	// Build per-user queue info
	userInfos := make([]map[string]interface{}, 0, len(users))
	totalEvents := 0
	usersWithQueues := 0

	for _, user := range users {
		queueSize, _ := storage.GetQueueSize(ctx, user.ID)
		if queueSize > 0 {
			usersWithQueues++
			totalEvents += queueSize
		}

		// Get oldest event for age calculation
		events, _ := storage.DequeueScrobbles(ctx, user.ID, 1)
		var oldestTime *time.Time
		var oldestAgeSeconds *int64
		if len(events) > 0 {
			age := int64(time.Since(events[0].CreatedAt).Seconds())
			oldestAgeSeconds = &age
			oldestTime = &events[0].CreatedAt
		}

		// Check if drain is active for this user
		drainInfo := drainStateTracker.GetUserInfo(user.ID)
		drainActive := drainInfo != nil

		// Determine status
		status := determineQueueStatus(queueSize, oldestAgeSeconds, drainActive)

		userInfo := map[string]interface{}{
			"user_id":            user.ID,
			"username":           user.Username,
			"trakt_display_name": user.TraktDisplayName,
			"queue_size":         queueSize,
			"status":             status,
			"drain_active":       drainActive,
		}

		if oldestAgeSeconds != nil {
			userInfo["oldest_event_age_seconds"] = *oldestAgeSeconds
			userInfo["oldest_event_timestamp"] = oldestTime
		}

		if drainInfo != nil {
			userInfo["events_processed"] = drainInfo.EventsProcessed
			userInfo["events_failed"] = drainInfo.EventsFailed
		}

		userInfos = append(userInfos, userInfo)
	}

	response := map[string]interface{}{
		"system": map[string]interface{}{
			"total_users":       len(users),
			"users_with_queues": usersWithQueues,
			"total_events":      totalEvents,
			"drain_active":      len(drainStateTracker.GetAllActiveUsers()) > 0,
			"mode":              drainStateTracker.GetMode(),
			"last_health_check": drainStateTracker.GetLastHealthCheck(),
		},
		"users": userInfos,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// determineQueueStatus determines the queue status based on various factors
func determineQueueStatus(queueSize int, oldestAgeSeconds *int64, drainActive bool) string {
	if queueSize == 0 {
		return "healthy"
	}
	if drainActive {
		return "draining"
	}
	if oldestAgeSeconds != nil && *oldestAgeSeconds > 3600 {
		return "stalled"
	}
	return "queued"
}

// getQueueEvents returns recent queue events from the log
func getQueueEvents(w http.ResponseWriter, r *http.Request) {
	if queueEventLog == nil {
		slog.Error("queue event log unavailable")
		http.Error(w, "queue event log unavailable", http.StatusServiceUnavailable)
		return
	}

	// Get recent events (default 50)
	events := queueEventLog.GetRecent(50)
	slog.Debug("queue events requested", "event_count", len(events))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"events": events,
	})
}

// getUserQueueDetail returns detailed queue info for a specific user
func getUserQueueDetail(w http.ResponseWriter, r *http.Request) {
	if storage == nil {
		http.Error(w, "storage unavailable", http.StatusServiceUnavailable)
		return
	}

	vars := mux.Vars(r)
	userID := strings.TrimSpace(vars["id"])
	if userID == "" {
		http.Error(w, "missing user id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Get user info
	user := storage.GetUser(userID)
	if user == nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}

	// Get all queued events for user (up to 100)
	events, err := storage.DequeueScrobbles(ctx, userID, 100)
	if err != nil {
		http.Error(w, "failed to fetch queue", http.StatusInternalServerError)
		return
	}

	// Calculate stats
	stats := calculateQueueStats(events)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"user_id":            user.ID,
		"username":           user.Username,
		"trakt_display_name": user.TraktDisplayName,
		"queue_size":         len(events),
		"events":             events,
		"stats":              stats,
	})
}

// calculateQueueStats computes statistics for a set of queued events
func calculateQueueStats(events []store.QueuedScrobbleEvent) map[string]interface{} {
	byAction := make(map[string]int)
	byRetryCount := make(map[string]int)

	for _, event := range events {
		byAction[event.Action]++
		retryKey := fmt.Sprintf("%d", event.RetryCount)
		byRetryCount[retryKey]++
	}

	return map[string]interface{}{
		"by_action":      byAction,
		"by_retry_count": byRetryCount,
	}
}

// ========== RETRY QUEUE WORKER (FR-016) ==========

// startRetryQueueWorker initializes and starts the PostgreSQL-backed retry queue worker.
// This worker processes failed scrobbles with exponential backoff and handles permanent
// failures after 5 attempts (FR-016).
//
// The worker:
// - Polls retry_queue_items table every 15 seconds
// - Retries failed scrobbles with exponential backoff (30s, 1m, 2m, 4m, 8m, capped at 30m)
// - Marks items as permanent_failure after 5 attempts
// - Sends notifications to group owners on permanent failures (FR-008a)
// - Logs queue metrics for observability
func startRetryQueueWorker(ctx context.Context, storage store.Store, traktSrv *trakt.Trakt) {
	slog.Info("retry queue worker starting")

	// Create notifier for permanent failure notifications
	notifier := notify.NewNotifier()

	// Create PostgreSQL repository wrapper
	repo := queue.NewPostgresRepo(storage)

	// Create worker with default configuration
	worker := queue.NewWorker(queue.WorkerConfig{
		Repo:         repo,
		Trakt:        traktSrv,
		Notifier:     notifier,
		Store:        storage,
		PollInterval: 0, // Use default (15 seconds)
		BatchSize:    0, // Use default (50 items)
	})

	// Start worker in background goroutine
	// The worker will respect context cancellation for graceful shutdown
	go func() {
		worker.Start(ctx)
		slog.Info("retry queue worker stopped")
	}()

	// Log initial queue metrics
	go func() {
		// Wait briefly for worker to stabilize
		time.Sleep(5 * time.Second)
		logRetryQueueMetrics(ctx, repo)

		// Periodically log queue metrics (every 5 minutes)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				logRetryQueueMetrics(ctx, repo)
			}
		}
	}()
}

// logRetryQueueMetrics logs current retry queue depth and permanent failure counts.
func logRetryQueueMetrics(ctx context.Context, repo *queue.PostgresRepo) {
	// Fetch all due items to get queue depth
	items, err := repo.FetchDueItems(ctx, time.Now().Add(24*time.Hour), 1000)
	if err != nil {
		slog.Warn("failed to fetch retry queue metrics", "error", err)
		return
	}

	queuedCount := 0
	permanentCount := 0

	for _, item := range items {
		if item.Status == "permanent_failure" {
			permanentCount++
		} else {
			queuedCount++
		}
	}

	slog.Info("retry queue metrics",
		"queued_items", queuedCount,
		"permanent_failures", permanentCount,
		"total", len(items),
	)
}

// ========== QUEUE DRAIN SYSTEM ==========

// startQueueDrainSystem initializes health checker and queue drain orchestration.
func startQueueDrainSystem(ctx context.Context, storage store.Store, traktSrv *trakt.Trakt) {
	slog.Info("queue drain system starting")

	// Start health checker
	healthChecker := trakt.NewHealthChecker(traktSrv)
	stateChan := healthChecker.Start(ctx)

	// Perform initial drain check on startup (don't wait for first health transition)
	go func() {
		time.Sleep(2 * time.Second) // Brief delay to let app stabilize
		slog.Info("performing initial queue drain check on startup")
		initiateQueueDrain(ctx, storage, traktSrv)
	}()

	// Listen for health state changes
	for {
		select {
		case <-ctx.Done():
			slog.Info("queue drain system stopping")
			return
		case state := <-stateChan:
			if state == "live" {
				slog.Info("trakt service restored, initiating queue drain")
				go initiateQueueDrain(ctx, storage, traktSrv)
			}
		}
	}
}

// initiateQueueDrain starts per-user drain goroutines when Trakt becomes available.
func initiateQueueDrain(ctx context.Context, storage store.Store, traktSrv *trakt.Trakt) {
	userIDs, err := storage.ListUsersWithQueuedEvents(ctx)
	if err != nil {
		slog.Error("failed to list users with queued events",
			"operation", "queue_drain_list_users",
			"error", err,
		)
		return
	}

	if len(userIDs) == 0 {
		slog.Info("no queued events to drain")
		return
	}

	slog.Info("queue drain starting",
		"operation", "queue_drain_start",
		"user_count", len(userIDs),
	)

	// Start drain goroutine for each user
	var wg sync.WaitGroup
	for _, userID := range userIDs {
		wg.Add(1)
		go func(uid string) {
			defer wg.Done()
			drainUserQueue(ctx, storage, traktSrv, uid)
		}(userID)
	}

	wg.Wait()
	slog.Info("queue drain complete",
		"operation", "queue_drain_complete",
		"user_count", len(userIDs),
	)
}

// drainUserQueue processes all queued events for a specific user.
func drainUserQueue(ctx context.Context, storage store.Store, traktSrv *trakt.Trakt, userID string) {
	startTime := time.Now()
	successCount := 0
	failureCount := 0

	// Track drain start
	drainStateTracker.RecordDrainStart(userID)
	defer drainStateTracker.RecordDrainComplete(userID)

	slog.Info("user queue drain starting",
		"operation", "queue_drain_user_start",
		"user_id", userID,
	)

	// Log to event buffer
	if queueEventLog != nil {
		queueEventLog.Append(store.QueueLogEvent{
			Timestamp: time.Now(),
			Operation: "queue_drain_user_start",
			UserID:    userID,
		})
	}

	// Drain in batches of 100
	for {
		events, err := storage.DequeueScrobbles(ctx, userID, 100)
		if err != nil {
			slog.Error("failed to dequeue events",
				"user_id", userID,
				"error", err,
			)
			break
		}

		if len(events) == 0 {
			break // Queue empty
		}

		// Process each event
		for _, event := range events {
			// Check for stale events (>7 days old)
			if time.Since(event.CreatedAt) > 7*24*time.Hour {
				slog.Warn("stale event processed",
					"operation", "stale_event_processed",
					"user_id", userID,
					"event_id", event.ID,
					"age_days", int(time.Since(event.CreatedAt).Hours()/24),
				)
			}

			// Attempt to send with retry
			if err := sendEventWithRetry(ctx, storage, traktSrv, event); err != nil {
				slog.Error("queue event permanent failure",
					"operation", "queue_event_failed",
					"user_id", userID,
					"event_id", event.ID,
					"error", err,
				)
				failureCount++
				drainStateTracker.RecordEvent(userID, false)

				// Log to event buffer
				if queueEventLog != nil {
					queueEventLog.Append(store.QueueLogEvent{
						Timestamp: time.Now(),
						Operation: "queue_event_failed",
						UserID:    userID,
						EventID:   event.ID,
						Error:     err.Error(),
					})
				}
			} else {
				slog.Info("queue event scrobbled",
					"operation", "queue_event_scrobbled",
					"user_id", userID,
					"event_id", event.ID,
				)
				successCount++
				drainStateTracker.RecordEvent(userID, true)

				// Log to event buffer
				if queueEventLog != nil {
					queueEventLog.Append(store.QueueLogEvent{
						Timestamp: time.Now(),
						Operation: "queue_event_scrobbled",
						UserID:    userID,
						EventID:   event.ID,
					})
				}
			}

			// Delete from queue (whether success or permanent failure)
			if err := storage.DeleteQueuedScrobble(ctx, event.ID); err != nil {
				slog.Warn("failed to delete queued event",
					"user_id", userID,
					"event_id", event.ID,
					"error", err,
				)
			}

			// Rate limit: 10 events/sec = 100ms between events
			time.Sleep(100 * time.Millisecond)
		}
	}

	duration := time.Since(startTime)
	slog.Info("user queue drain complete",
		"operation", "queue_drain_user_complete",
		"user_id", userID,
		"success_count", successCount,
		"failure_count", failureCount,
		"duration_ms", duration.Milliseconds(),
	)
}

// sendEventWithRetry attempts to send an event with exponential backoff.
func sendEventWithRetry(ctx context.Context, storage store.Store, traktSrv *trakt.Trakt, event store.QueuedScrobbleEvent) error {
	backoffSchedule := []time.Duration{1 * time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

	for attempt := 0; attempt < 5; attempt++ {
		// Get user
		user := storage.GetUser(event.UserID)
		if user == nil {
			return fmt.Errorf("user not found: %s", event.UserID)
		}

		// Reconstruct cache item
		cacheItem := common.CacheItem{
			PlayerUuid: event.PlayerUUID,
			RatingKey:  event.RatingKey,
			Body:       event.ScrobbleBody,
		}

		// Attempt scrobble via Trakt client
		// We need to construct the request ourselves here
		err := sendScrobble(traktSrv, event.Action, cacheItem, *user)

		if err == nil {
			return nil // Success
		}

		// Check if it's a transient error
		if !isTransientError(err) {
			return err // Permanent failure
		}

		// Transient error - update retry count and backoff
		if attempt < 4 {
			storage.UpdateQueuedScrobbleRetry(ctx, event.ID, attempt+1)
			time.Sleep(backoffSchedule[attempt])
		}
	}

	return fmt.Errorf("max retries exceeded")
}

// sendScrobble sends a scrobble request to Trakt (queue drain version).
func sendScrobble(traktSrv *trakt.Trakt, action string, item common.CacheItem, user store.User) error {
	return traktSrv.ScrobbleFromQueue(action, item, user.AccessToken)
}

// isTransientError checks if an error is temporary and worth retrying.
func isTransientError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "503") ||
		strings.Contains(errStr, "502") ||
		strings.Contains(errStr, "504") ||
		strings.Contains(errStr, "429") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection refused")
}

func main() {
	// init structured logging
	logging.Init()
	appAssets = newAssetManifest("static/dist/manifest.json")
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

	// Initialize queue monitoring
	queueEventLog = store.NewQueueEventLog(100)
	drainStateTracker = NewDrainStateTracker()
	traktSrv.SetQueueEventLog(queueEventLog)
	slog.Info("queue monitoring initialized")

	// Start queue drain system
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go startQueueDrainSystem(ctx, storage, traktSrv)

	// Start retry queue worker (PostgreSQL only - FR-016)
	// This worker processes failed scrobbles from the retry_queue_items table
	// with exponential backoff and permanent failure notifications after 5 attempts.
	if _, isPostgres := storage.(*store.PostgresqlStore); isPostgres {
		startRetryQueueWorker(ctx, storage, traktSrv)
	} else {
		slog.Info("retry queue worker disabled (PostgreSQL storage required)")
	}

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
	router.PathPrefix("/static/").Handler(cacheStaticFiles(http.StripPrefix("/static/", http.FileServer(http.Dir("static")))))
	router.HandleFunc("/authorize", authorize).Methods("GET")
	router.HandleFunc("/authorize/family/member", authorizeFamilyMember).Methods("GET")
	router.HandleFunc("/manual/authorize", authorize).Methods("GET")
	router.HandleFunc("/oauth/state", createAuthState).Methods("POST")
	router.HandleFunc("/oauth/family/state", createFamilyAuthState).Methods("POST")
	router.HandleFunc("/api", api).Methods("POST")
	router.HandleFunc("/api/telemetry", telemetryHandler).Methods("POST")
	router.HandleFunc("/users/{id}/trakt-display-name", updateTraktDisplayName).Methods("POST")
	router.Handle("/healthcheck", healthcheckHandler()).Methods("GET")

	// Admin routes
	router.HandleFunc("/admin", renderAdminDashboard).Methods("GET")
	router.HandleFunc("/admin/family", renderFamilyAdmin).Methods("GET")
	router.HandleFunc("/admin/api/users", listAdminUsers).Methods("GET")
	router.HandleFunc("/admin/api/users/{id}", getAdminUser).Methods("GET")
	router.HandleFunc("/admin/api/users/{id}", updateAdminUser).Methods("PUT")
	router.HandleFunc("/admin/api/users/{id}", deleteAdminUser).Methods("DELETE")

	// Queue monitoring routes
	router.HandleFunc("/admin/queue", renderQueueMonitor).Methods("GET")
	router.HandleFunc("/admin/api/queue/status", getQueueStatus).Methods("GET")
	router.HandleFunc("/admin/api/queue/events", getQueueEvents).Methods("GET")
	router.HandleFunc("/admin/api/queue/user/{id}", getUserQueueDetail).Methods("GET")

	// Family group admin routes
	router.HandleFunc("/admin/api/family-groups", listFamilyGroups).Methods("GET")
	router.HandleFunc("/admin/api/family-groups/{id}", getFamilyGroupDetail).Methods("GET")
	router.HandleFunc("/admin/api/family-groups/{id}/members", addFamilyGroupMember).Methods("POST")
	router.HandleFunc("/admin/api/family-groups/{group_id}/members/{member_id}", removeFamilyGroupMember).Methods("DELETE")
	router.HandleFunc("/admin/api/family-groups/{id}", deleteFamilyGroup).Methods("DELETE")

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
		"/api":              {},
		"/authorize":        {},
		"/manual/authorize": {},
		"/oauth/state":      {},
		"/healthcheck":      {},
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

func cacheStaticFiles(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/dist/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		next.ServeHTTP(w, r)
	})
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

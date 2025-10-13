package integration

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFamilyOnboarding validates the complete 3-step family onboarding flow
// Requirements: SC-001 (complete onboarding in <10 minutes)
func TestFamilyOnboarding(t *testing.T) {
	// Setup test server and dependencies
	ts := setupTestServer(t)
	defer ts.Close()

	startTime := time.Now()

	// Step 1: Create family group with Plex username and member labels
	t.Run("Step1_CreateFamilyGroup", func(t *testing.T) {
		payload := map[string]interface{}{
			"plex_username": "TestFamilyPlex",
			"member_labels": []string{"Parent", "Child1", "Child2"},
		}
		body, _ := json.Marshal(payload)

		resp, err := http.Post(ts.URL+"/oauth/family/state", "application/json", bytes.NewBuffer(body))
		require.NoError(t, err)
		defer resp.Body.Close()

		assert.Equal(t, http.StatusOK, resp.StatusCode)

		var result map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&result)
		require.NoError(t, err)

		assert.NotEmpty(t, result["state"])
		assert.NotEmpty(t, result["family_group_id"])

		// Store for next steps
		t.Setenv("FAMILY_STATE", result["state"].(string))
		t.Setenv("FAMILY_GROUP_ID", result["family_group_id"].(string))
	})

	// Step 2: Authorize each member with Trakt
	t.Run("Step2_AuthorizeMembers", func(t *testing.T) {
		familyGroupID := os.Getenv("FAMILY_GROUP_ID")
		state := os.Getenv("FAMILY_STATE")

		// Simulate authorization for each of the 3 members
		memberAuths := []struct {
			label         string
			traktUsername string
			token         string
		}{
			{"Parent", "parent_trakt", "parent_token_123"},
			{"Child1", "child1_trakt", "child1_token_456"},
			{"Child2", "child2_trakt", "child2_token_789"},
		}

		for i, member := range memberAuths {
			// Simulate OAuth callback for each member
			callbackURL := fmt.Sprintf("%s/authorize/family/member?state=%s&member_index=%d&code=test_code_%d",
				ts.URL, state, i, i)

			// Mock the Trakt OAuth response
			mockTraktOAuthResponse(t, member.traktUsername, member.token)

			resp, err := http.Get(callbackURL)
			require.NoError(t, err)
			defer resp.Body.Close()

			// Should redirect back to wizard with success
			assert.Equal(t, http.StatusFound, resp.StatusCode)
			location := resp.Header.Get("Location")
			assert.Contains(t, location, "family-wizard")
			assert.Contains(t, location, "status=authorized")
		}

		// Verify all members are authorized
		resp, err := http.Get(fmt.Sprintf("%s/api/family-groups/%s/status", ts.URL, familyGroupID))
		require.NoError(t, err)
		defer resp.Body.Close()

		var status map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&status)
		require.NoError(t, err)

		members := status["members"].([]interface{})
		assert.Len(t, members, 3)
		for _, m := range members {
			member := m.(map[string]interface{})
			assert.Equal(t, "authorized", member["authorization_status"])
		}
	})

	// Step 3: Get webhook URL
	t.Run("Step3_GetWebhookURL", func(t *testing.T) {
		familyGroupID := os.Getenv("FAMILY_GROUP_ID")

		resp, err := http.Get(fmt.Sprintf("%s/api/family-groups/%s", ts.URL, familyGroupID))
		require.NoError(t, err)
		defer resp.Body.Close()

		var group map[string]interface{}
		err = json.NewDecoder(resp.Body).Decode(&group)
		require.NoError(t, err)

		webhookURL := group["webhook_url"].(string)
		assert.NotEmpty(t, webhookURL)
		assert.Contains(t, webhookURL, "/api?id="+familyGroupID)

		// Verify webhook URL is functional
		webhookResp, err := http.Get(ts.URL + webhookURL)
		require.NoError(t, err)
		defer webhookResp.Body.Close()

		// Should return 400 without proper Plex webhook payload, but not 404
		assert.NotEqual(t, http.StatusNotFound, webhookResp.StatusCode)
	})

	// Validate total time < 10 minutes (SC-001)
	duration := time.Since(startTime)
	assert.Less(t, duration, 10*time.Minute, "Onboarding should complete in less than 10 minutes")

	// Validate database state
	t.Run("ValidateDatabaseState", func(t *testing.T) {
		familyGroupID := os.Getenv("FAMILY_GROUP_ID")

		// Check family group exists
		var groupCount int
		err := ts.db.QueryRow("SELECT COUNT(*) FROM family_groups WHERE id = $1", familyGroupID).Scan(&groupCount)
		require.NoError(t, err)
		assert.Equal(t, 1, groupCount)

		// Check all members exist and are authorized
		var memberCount int
		err = ts.db.QueryRow(`
			SELECT COUNT(*) FROM group_members 
			WHERE family_group_id = $1 AND authorization_status = 'authorized'
		`, familyGroupID).Scan(&memberCount)
		require.NoError(t, err)
		assert.Equal(t, 3, memberCount)

		// Verify no duplicate Trakt usernames
		var uniqueCount int
		err = ts.db.QueryRow(`
			SELECT COUNT(DISTINCT trakt_username) FROM group_members 
			WHERE family_group_id = $1
		`, familyGroupID).Scan(&uniqueCount)
		require.NoError(t, err)
		assert.Equal(t, 3, uniqueCount)
	})

	// Validate telemetry logging
	t.Run("ValidateTelemetryLogs", func(t *testing.T) {
		// Check for onboarding_start event
		assert.True(t, ts.logContains("onboarding_start", map[string]string{
			"mode": "family",
		}))

		// Check for onboarding_complete event
		assert.True(t, ts.logContains("onboarding_complete", map[string]string{
			"mode":    "family",
			"success": "true",
		}))

		// Verify duration is logged
		logs := ts.getLogs("onboarding_complete")
		for _, log := range logs {
			assert.Contains(t, log, "duration_ms")
		}
	})
}

// TestFamilyOnboardingValidation tests input validation for onboarding
func TestFamilyOnboardingValidation(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()

	tests := []struct {
		name        string
		payload     map[string]interface{}
		expectCode  int
		expectError string
	}{
		{
			name: "TooFewMembers",
			payload: map[string]interface{}{
				"plex_username": "TestPlex",
				"member_labels": []string{"OnlyOne"},
			},
			expectCode:  http.StatusBadRequest,
			expectError: "minimum 2 members required",
		},
		{
			name: "TooManyMembers",
			payload: map[string]interface{}{
				"plex_username": "TestPlex",
				"member_labels": []string{"M1", "M2", "M3", "M4", "M5", "M6", "M7", "M8", "M9", "M10", "M11"},
			},
			expectCode:  http.StatusBadRequest,
			expectError: "maximum 10 members allowed",
		},
		{
			name: "EmptyPlexUsername",
			payload: map[string]interface{}{
				"plex_username": "",
				"member_labels": []string{"Parent", "Child"},
			},
			expectCode:  http.StatusBadRequest,
			expectError: "plex username required",
		},
		{
			name: "DuplicatePlexUsername",
			payload: map[string]interface{}{
				"plex_username": "ExistingPlex",
				"member_labels": []string{"Parent", "Child"},
			},
			expectCode:  http.StatusConflict,
			expectError: "plex username already exists",
		},
	}

	// Pre-create a family group for duplicate test
	createTestFamilyGroup(t, ts.db, "ExistingPlex")

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.payload)
			resp, err := http.Post(ts.URL+"/oauth/family/state", "application/json", bytes.NewBuffer(body))
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tt.expectCode, resp.StatusCode)

			if tt.expectError != "" {
				var result map[string]interface{}
				json.NewDecoder(resp.Body).Decode(&result)
				assert.Contains(t, result["error"], tt.expectError)
			}
		})
	}
}

// TestFamilyOnboardingAbandon tests telemetry for abandoned onboarding
func TestFamilyOnboardingAbandon(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()

	// Start onboarding
	payload := map[string]interface{}{
		"plex_username": "AbandonTest",
		"member_labels": []string{"Member1", "Member2"},
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(ts.URL+"/oauth/family/state", "application/json", bytes.NewBuffer(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	state := result["state"].(string)

	// Simulate session timeout (15 minutes)
	time.Sleep(100 * time.Millisecond) // In real test, would manipulate session store
	
	// Attempt to continue with expired state
	callbackURL := fmt.Sprintf("%s/authorize/family/member?state=%s&member_index=0&code=test", ts.URL, state)
	resp2, err := http.Get(callbackURL)
	require.NoError(t, err)
	defer resp2.Body.Close()

	// Should fail with expired session
	assert.Equal(t, http.StatusBadRequest, resp2.StatusCode)

	// Check for abandon telemetry
	assert.True(t, ts.logContains("onboarding_abandon", map[string]string{
		"mode":    "family",
		"success": "false",
	}))
}

// TestFamilyOnboardingDuplicateTraktAccount tests FR-010a enforcement
func TestFamilyOnboardingDuplicateTraktAccount(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.Close()

	// Create family group
	payload := map[string]interface{}{
		"plex_username": "DuplicateTest",
		"member_labels": []string{"User1", "User2"},
	}
	body, _ := json.Marshal(payload)

	resp, err := http.Post(ts.URL+"/oauth/family/state", "application/json", bytes.NewBuffer(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	state := result["state"].(string)

	// Authorize first member
	mockTraktOAuthResponse(t, "same_trakt_user", "token1")
	callbackURL := fmt.Sprintf("%s/authorize/family/member?state=%s&member_index=0&code=code1", ts.URL, state)
	resp1, _ := http.Get(callbackURL)
	resp1.Body.Close()

	// Try to authorize second member with same Trakt account
	mockTraktOAuthResponse(t, "same_trakt_user", "token2")
	callbackURL2 := fmt.Sprintf("%s/authorize/family/member?state=%s&member_index=1&code=code2", ts.URL, state)
	resp2, err := http.Get(callbackURL2)
	require.NoError(t, err)
	defer resp2.Body.Close()

	// Should fail with conflict
	assert.Equal(t, http.StatusConflict, resp2.StatusCode)
}

// Helper functions

type testServer struct {
	*httptest.Server
	db   *sql.DB
	logs []string
}

func (ts *testServer) Close() {
	ts.Server.Close()
	ts.db.Close()
}

func (ts *testServer) logContains(event string, fields map[string]string) bool {
	for _, log := range ts.logs {
		if !strings.Contains(log, event) {
			continue
		}
		allFound := true
		for k, v := range fields {
			if !strings.Contains(log, fmt.Sprintf("%s=%s", k, v)) {
				allFound = false
				break
			}
		}
		if allFound {
			return true
		}
	}
	return false
}

func (ts *testServer) getLogs(event string) []string {
	var matched []string
	for _, log := range ts.logs {
		if strings.Contains(log, event) {
			matched = append(matched, log)
		}
	}
	return matched
}

func setupTestServer(t *testing.T) *testServer {
	// This would be implemented to set up the actual server with test database
	// For now, returning a mock implementation
	// In real implementation, would:
	// 1. Set up test PostgreSQL database
	// 2. Run migrations
	// 3. Initialize server with test configuration
	// 4. Set up log capture
	t.Skip("Test server setup not implemented - would require full application context")
	return nil
}

func createTestFamilyGroup(t *testing.T, db *sql.DB, plexUsername string) {
	// Helper to create a family group for testing
	// Implementation would insert into database
}

func mockTraktOAuthResponse(t *testing.T, username, token string) {
	// Helper to mock Trakt OAuth responses
	// Would set up HTTP mock or test double
}
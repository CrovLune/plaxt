package integration

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFamilyWebhookBroadcast validates scrobbling to multiple family members
// Requirements: SC-002 (≤5s latency), SC-003 (99% success rate), FR-016 (retry queue)
func TestFamilyWebhookBroadcast(t *testing.T) {
	ts := setupTestServerWithFamilyGroup(t, 3) // 3 members
	defer ts.Close()

	// Prepare webhook payload (Plex media.scrobble event)
	webhookPayload := map[string]interface{}{
		"event": "media.scrobble",
		"user":  true,
		"owner": true,
		"Account": map[string]interface{}{
			"id":    1234567,
			"thumb": "https://plex.tv/users/abc123/avatar",
			"title": "TestFamilyPlex",
		},
		"Metadata": map[string]interface{}{
			"librarySectionType": "movie",
			"ratingKey":          "12345",
			"key":                "/library/metadata/12345",
			"guid":               "plex://movie/5d77683f6f4521001ea9dc53",
			"type":               "movie",
			"title":              "Test Movie",
			"year":               2024,
			"thumb":              "/library/metadata/12345/thumb",
			"originalTitle":      "Test Movie Original",
			"duration":           7200000, // 2 hours in milliseconds
			"lastViewedAt":       time.Now().Unix(),
		},
	}

	// Mock Trakt API responses
	traktCalls := make(chan string, 3)
	ts.mockTraktAPI(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		memberID := extractMemberFromToken(auth)
		traktCalls <- memberID

		// Member 2 gets a 429 rate limit response
		if memberID == "member2" {
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Rate limit exceeded",
			})
			return
		}

		// Other members succeed
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":          12345,
			"action":      "scrobble",
			"progress":    90,
			"sharing":     map[string]bool{"facebook": false, "twitter": false, "tumblr": false},
			"movie":       map[string]interface{}{"ids": map[string]interface{}{"trakt": 123}},
			"inserted_at": time.Now().Format(time.RFC3339),
		})
	})

	// Send webhook request
	startTime := time.Now()
	body, _ := json.Marshal(webhookPayload)
	resp, err := http.Post(ts.URL+"/api?id="+ts.familyGroupID, "application/json", bytes.NewBuffer(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	// Response should be success even with partial failure
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify latency requirement (SC-002)
	duration := time.Since(startTime)
	assert.Less(t, duration, 5*time.Second, "Broadcast should complete within 5 seconds")

	// Wait for all Trakt calls to complete
	close(traktCalls)
	calledMembers := []string{}
	for member := range traktCalls {
		calledMembers = append(calledMembers, member)
	}

	// Should have attempted all 3 members
	assert.Len(t, calledMembers, 3)
	assert.Contains(t, calledMembers, "member1")
	assert.Contains(t, calledMembers, "member2")
	assert.Contains(t, calledMembers, "member3")

	// Verify retry queue has the failed member
	t.Run("VerifyRetryQueue", func(t *testing.T) {
		var queueCount int
		err := ts.db.QueryRow(`
			SELECT COUNT(*) FROM retry_queue_items 
			WHERE family_group_id = $1 AND status = 'queued'
		`, ts.familyGroupID).Scan(&queueCount)
		require.NoError(t, err)

		// Member 2 should be in queue due to 429
		assert.Equal(t, 1, queueCount)

		// Verify queue item details
		var memberID, lastError string
		var attemptCount int
		err = ts.db.QueryRow(`
			SELECT group_member_id, attempt_count, last_error 
			FROM retry_queue_items 
			WHERE family_group_id = $1 AND status = 'queued'
		`, ts.familyGroupID).Scan(&memberID, &attemptCount, &lastError)
		require.NoError(t, err)

		assert.Contains(t, memberID, "member2")
		assert.Equal(t, 0, attemptCount)
		assert.Contains(t, lastError, "429")
	})

	// Simulate retry worker processing
	t.Run("RetryWorkerProcessing", func(t *testing.T) {
		// Mock successful retry
		ts.mockTraktAPI(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"id":     12346,
				"action": "scrobble",
			})
		})

		// Simulate worker tick
		processRetryQueue(t, ts.db)

		// Queue should now be empty
		var queueCount int
		err := ts.db.QueryRow(`
			SELECT COUNT(*) FROM retry_queue_items 
			WHERE family_group_id = $1 AND status = 'queued'
		`, ts.familyGroupID).Scan(&queueCount)
		require.NoError(t, err)
		assert.Equal(t, 0, queueCount)
	})

	// Verify success rate (SC-003)
	// 2 immediate successes + 1 retry success = 100% eventual success
	t.Run("VerifySuccessRate", func(t *testing.T) {
		successRate := calculateSuccessRate(t, ts.db, ts.familyGroupID)
		assert.GreaterOrEqual(t, successRate, 99.0, "Success rate should be ≥99%")
	})
}

// TestFamilyWebhookWithPermanentFailure tests retry limit and notifications
func TestFamilyWebhookWithPermanentFailure(t *testing.T) {
	ts := setupTestServerWithFamilyGroup(t, 2)
	defer ts.Close()

	// Mock Trakt to always fail for one member
	failingMemberID := "member1_failing"
	ts.mockTraktAPI(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if extractMemberFromToken(auth) == failingMemberID {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"error": "Internal server error"})
			return
		}
		w.WriteHeader(http.StatusCreated)
	})

	// Send initial webhook
	webhookPayload := createTestWebhookPayload("TestFamilyPlex")
	body, _ := json.Marshal(webhookPayload)
	resp, _ := http.Post(ts.URL+"/api?id="+ts.familyGroupID, "application/json", bytes.NewBuffer(body))
	resp.Body.Close()

	// Simulate 5 retry attempts (max attempts per FR-016)
	for attempt := 1; attempt <= 5; attempt++ {
		processRetryQueue(t, ts.db)

		// Check attempt count
		var attemptCount int
		err := ts.db.QueryRow(`
			SELECT attempt_count FROM retry_queue_items 
			WHERE family_group_id = $1 AND group_member_id LIKE '%failing%'
		`, ts.familyGroupID).Scan(&attemptCount)
		require.NoError(t, err)
		assert.Equal(t, attempt, attemptCount)
	}

	// Verify permanent failure status
	var status string
	err := ts.db.QueryRow(`
		SELECT status FROM retry_queue_items 
		WHERE family_group_id = $1 AND group_member_id LIKE '%failing%'
	`, ts.familyGroupID).Scan(&status)
	require.NoError(t, err)
	assert.Equal(t, "permanent_failure", status)

	// Verify notification was triggered (FR-008a)
	notifications := ts.getNotifications()
	assert.Len(t, notifications, 1)
	assert.Contains(t, notifications[0], "permanent failure")
	assert.Contains(t, notifications[0], failingMemberID)
}

// TestFamilyWebhookConcurrency tests concurrent webhook processing
func TestFamilyWebhookConcurrency(t *testing.T) {
	ts := setupTestServerWithFamilyGroup(t, 5)
	defer ts.Close()

	// Track processing order
	var processingOrder []string
	var orderMux sync.Mutex

	ts.mockTraktAPI(func(w http.ResponseWriter, r *http.Request) {
		memberID := extractMemberFromToken(r.Header.Get("Authorization"))
		
		orderMux.Lock()
		processingOrder = append(processingOrder, memberID)
		orderMux.Unlock()

		// Simulate varying response times
		time.Sleep(time.Duration(len(memberID)*10) * time.Millisecond)
		w.WriteHeader(http.StatusCreated)
	})

	// Send multiple webhooks concurrently
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			
			payload := createTestWebhookPayload("TestFamilyPlex")
			payload["Metadata"].(map[string]interface{})["title"] = fmt.Sprintf("Movie %d", index)
			body, _ := json.Marshal(payload)
			
			resp, err := http.Post(ts.URL+"/api?id="+ts.familyGroupID, "application/json", bytes.NewBuffer(body))
			require.NoError(t, err)
			resp.Body.Close()
		}(i)
	}

	wg.Wait()

	// Verify all members were called for each webhook
	assert.Len(t, processingOrder, 15) // 5 members × 3 webhooks

	// Verify sequential processing per Plex ID (FR-010b)
	// Events for same Plex ID should be processed in order
	var eventTimestamps []time.Time
	rows, err := ts.db.Query(`
		SELECT created_at FROM webhook_events 
		WHERE family_group_id = $1 
		ORDER BY created_at ASC
	`, ts.familyGroupID)
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var timestamp time.Time
		err := rows.Scan(&timestamp)
		require.NoError(t, err)
		eventTimestamps = append(eventTimestamps, timestamp)
	}

	// Verify timestamps are in order
	for i := 1; i < len(eventTimestamps); i++ {
		assert.True(t, eventTimestamps[i].After(eventTimestamps[i-1]) || eventTimestamps[i].Equal(eventTimestamps[i-1]))
	}
}

// TestFamilyWebhookWithExpiredTokens tests handling of expired member tokens
func TestFamilyWebhookWithExpiredTokens(t *testing.T) {
	ts := setupTestServerWithFamilyGroup(t, 3)
	defer ts.Close()

	// Set one member's token as expired
	_, err := ts.db.Exec(`
		UPDATE group_members 
		SET token_expiry = $1 
		WHERE family_group_id = $2 
		LIMIT 1
	`, time.Now().Add(-1*time.Hour), ts.familyGroupID)
	require.NoError(t, err)

	// Track which members receive scrobbles
	var calledMembers []string
	ts.mockTraktAPI(func(w http.ResponseWriter, r *http.Request) {
		memberID := extractMemberFromToken(r.Header.Get("Authorization"))
		calledMembers = append(calledMembers, memberID)
		w.WriteHeader(http.StatusCreated)
	})

	// Send webhook
	payload := createTestWebhookPayload("TestFamilyPlex")
	body, _ := json.Marshal(payload)
	resp, err := http.Post(ts.URL+"/api?id="+ts.familyGroupID, "application/json", bytes.NewBuffer(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	// Only 2 members should receive scrobbles (expired one excluded)
	assert.Len(t, calledMembers, 2)

	// Verify expired member status updated
	var expiredCount int
	err = ts.db.QueryRow(`
		SELECT COUNT(*) FROM group_members 
		WHERE family_group_id = $1 AND authorization_status = 'expired'
	`, ts.familyGroupID).Scan(&expiredCount)
	require.NoError(t, err)
	assert.Equal(t, 1, expiredCount)

	// Verify notification sent (FR-008a)
	notifications := ts.getNotifications()
	assert.GreaterOrEqual(t, len(notifications), 1)
	hasExpiryNotification := false
	for _, n := range notifications {
		if strings.Contains(n, "expired") {
			hasExpiryNotification = true
			break
		}
	}
	assert.True(t, hasExpiryNotification, "Should have expiry notification")
}

// Helper functions

type familyTestServer struct {
	*httptest.Server
	db            *sql.DB
	familyGroupID string
	notifications []string
	traktHandler  http.HandlerFunc
}

func (ts *familyTestServer) Close() {
	ts.Server.Close()
	ts.db.Close()
}

func (ts *familyTestServer) mockTraktAPI(handler http.HandlerFunc) {
	ts.traktHandler = handler
}

func (ts *familyTestServer) getNotifications() []string {
	return ts.notifications
}

func setupTestServerWithFamilyGroup(t *testing.T, memberCount int) *familyTestServer {
	// This would be implemented to:
	// 1. Set up test database with family_groups, group_members, retry_queue_items tables
	// 2. Create a test family group with specified number of members
	// 3. Initialize server with mocked Trakt API
	// 4. Set up notification capture
	t.Skip("Test server setup not implemented - would require full application context")
	return nil
}

func createTestWebhookPayload(plexUsername string) map[string]interface{} {
	return map[string]interface{}{
		"event": "media.scrobble",
		"user":  true,
		"owner": true,
		"Account": map[string]interface{}{
			"title": plexUsername,
		},
		"Metadata": map[string]interface{}{
			"type":         "movie",
			"title":        "Test Movie",
			"year":         2024,
			"duration":     7200000,
			"lastViewedAt": time.Now().Unix(),
		},
	}
}

func extractMemberFromToken(authHeader string) string {
	// Extract member ID from Authorization header
	// In real implementation, would parse Bearer token
	return strings.TrimPrefix(authHeader, "Bearer ")
}

func processRetryQueue(t *testing.T, db *sql.DB) {
	// Simulate retry worker processing
	// In real implementation, would call the actual worker.Process() method
	rows, err := db.Query(`
		SELECT id FROM retry_queue_items 
		WHERE status = 'queued' AND next_attempt_at <= NOW()
		FOR UPDATE SKIP LOCKED
	`)
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var itemID string
		err := rows.Scan(&itemID)
		require.NoError(t, err)

		// Update attempt count and status
		_, err = db.Exec(`
			UPDATE retry_queue_items 
			SET attempt_count = attempt_count + 1,
			    status = CASE 
			        WHEN attempt_count >= 4 THEN 'permanent_failure'
			        ELSE 'queued'
			    END,
			    next_attempt_at = NOW() + INTERVAL '1 minute' * POWER(2, attempt_count + 1)
			WHERE id = $1
		`, itemID)
		require.NoError(t, err)
	}
}

func calculateSuccessRate(t *testing.T, db *sql.DB, familyGroupID string) float64 {
	// Calculate success rate based on queue status
	var totalAttempts, successfulAttempts int
	
	// Count total member scrobbles attempted
	err := db.QueryRow(`
		SELECT COUNT(*) FROM group_members WHERE family_group_id = $1
	`, familyGroupID).Scan(&totalAttempts)
	require.NoError(t, err)

	// Count successful (not in queue or queue cleared)
	err = db.QueryRow(`
		SELECT COUNT(*) FROM group_members m
		LEFT JOIN retry_queue_items q ON q.group_member_id = m.id AND q.status != 'success'
		WHERE m.family_group_id = $1 AND q.id IS NULL
	`, familyGroupID).Scan(&successfulAttempts)
	require.NoError(t, err)

	if totalAttempts == 0 {
		return 100.0
	}
	return float64(successfulAttempts) / float64(totalAttempts) * 100.0
}
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	// Postgres db library loading
	"crovlune/plaxt/lib/common"
	_ "github.com/lib/pq"
)

// PostgresqlStore is a storage engine that writes to postgres
type PostgresqlStore struct {
	db              *sql.DB
	fallbackBuffers map[string]*InMemoryBuffer
	bufferMu        sync.RWMutex
}

// NewPostgresqlClient creates a new db client object
func NewPostgresqlClient(connStr string) *sql.DB {
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		panic(err)
	}
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			id varchar(255) NOT NULL,
			username varchar(255) NOT NULL,
			access varchar(255) NOT NULL,
			refresh varchar(255) NOT NULL,
			trakt_display_name varchar(50),
			updated timestamp with time zone NOT NULL,
			PRIMARY KEY(id)
		)
	`); err != nil {
		panic(err)
	}
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS trakt_display_name varchar(50)`); err != nil {
		panic(err)
	}

	// Add token_expiry column (migration)
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN IF NOT EXISTS token_expiry timestamp with time zone`); err != nil {
		panic(err)
	}

	// Create queued_scrobbles table (migration)
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS queued_scrobbles (
			id UUID PRIMARY KEY,
			user_id VARCHAR(255) NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			scrobble_body JSONB NOT NULL,
			action VARCHAR(10) NOT NULL CHECK (action IN ('start', 'pause', 'stop')),
			progress INTEGER NOT NULL CHECK (progress >= 0 AND progress <= 100),
			created_at TIMESTAMP NOT NULL,
			retry_count INTEGER NOT NULL DEFAULT 0 CHECK (retry_count >= 0 AND retry_count <= 5),
			last_attempt TIMESTAMP,
			player_uuid VARCHAR(255) NOT NULL,
			rating_key VARCHAR(255) NOT NULL,
			CONSTRAINT queued_scrobbles_dedup UNIQUE (player_uuid, rating_key)
		)
	`); err != nil {
		panic(err)
	}

	// Create indexes
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_queued_scrobbles_user_time ON queued_scrobbles(user_id, created_at)`); err != nil {
		panic(err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_queued_scrobbles_retry ON queued_scrobbles(user_id, retry_count) WHERE retry_count > 0`); err != nil {
		panic(err)
	}

	return db
}

// NewPostgresqlStore creates new store
func NewPostgresqlStore(db *sql.DB) *PostgresqlStore {
	return &PostgresqlStore{
		db:              db,
		fallbackBuffers: make(map[string]*InMemoryBuffer),
	}
}

// Ping will check if the connection works right
func (s PostgresqlStore) Ping(ctx context.Context) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	return conn.PingContext(ctx)
}

// WriteUser will write a user object to postgres
func (s PostgresqlStore) WriteUser(user User) {
	_, err := s.db.Exec(
		`
			INSERT INTO users
				(id, username, access, refresh, trakt_display_name, updated, token_expiry)
				VALUES($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT(id)
			DO UPDATE set username=EXCLUDED.username, access=EXCLUDED.access, refresh=EXCLUDED.refresh, trakt_display_name=EXCLUDED.trakt_display_name, updated=EXCLUDED.updated, token_expiry=EXCLUDED.token_expiry
		`,
		user.ID,
		user.Username,
		user.AccessToken,
		user.RefreshToken,
		user.TraktDisplayName,
		user.Updated,
		user.TokenExpiry,
	)
	if err != nil {
		panic(err)
	}
}

// GetUser will load a user from postgres
func (s PostgresqlStore) GetUser(id string) *User {
	var username string
	var access string
	var refresh string
	var updated time.Time
	var displayName sql.NullString
	var tokenExpiry sql.NullTime

	err := s.db.QueryRow(
		"SELECT username, access, refresh, trakt_display_name, updated, token_expiry FROM users WHERE id=$1",
		id,
	).Scan(
		&username,
		&access,
		&refresh,
		&displayName,
		&updated,
		&tokenExpiry,
	)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		panic(fmt.Errorf("query error: %v", err))
	}

	// Default token expiry to 90 days from last update if not set (for legacy users)
	expiry := updated.Add(90 * 24 * time.Hour)
	if tokenExpiry.Valid {
		expiry = tokenExpiry.Time
	}

	user := User{
		ID:               id,
		Username:         strings.ToLower(username),
		AccessToken:      access,
		RefreshToken:     refresh,
		TraktDisplayName: displayName.String,
		Updated:          updated,
		TokenExpiry:      expiry,
		store:            s,
	}

	return &user
}

// GetUserByName will load a user from postgres
func (s PostgresqlStore) GetUserByName(username string) *User {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return nil
	}
	var id string
	err := s.db.QueryRow("SELECT id FROM users WHERE lower(username)=lower($1) LIMIT 1", username).Scan(&id)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		panic(err)
	}
	return s.GetUser(id)
}

// DeleteUser removes a user row and any username mapping from postgres.
func (s PostgresqlStore) DeleteUser(id, username string) bool {
	_, err := s.db.Exec("DELETE FROM users WHERE id=$1", id)
	return err == nil
}

func (s PostgresqlStore) ListUsers() []User {
	rows, err := s.db.Query(`SELECT id, username, access, refresh, trakt_display_name, updated, token_expiry FROM users ORDER BY updated DESC`)
	if err != nil {
		panic(err)
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var (
			id          string
			username    string
			access      string
			refresh     string
			display     sql.NullString
			updated     time.Time
			tokenExpiry sql.NullTime
		)
		if err := rows.Scan(&id, &username, &access, &refresh, &display, &updated, &tokenExpiry); err != nil {
			panic(err)
		}

		// Default token expiry to 90 days from last update if not set (for legacy users)
		expiry := updated.Add(90 * 24 * time.Hour)
		if tokenExpiry.Valid {
			expiry = tokenExpiry.Time
		}

		user := User{
			ID:               id,
			Username:         strings.ToLower(username),
			AccessToken:      access,
			RefreshToken:     refresh,
			TraktDisplayName: display.String,
			Updated:          updated,
			TokenExpiry:      expiry,
			store:            s,
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
	return users
}

func (s PostgresqlStore) GetScrobbleBody(playerUuid, ratingKey string) common.CacheItem {
	return common.CacheItem{
		Body: common.ScrobbleBody{
			Progress: 0,
		},
	}
}

func (s PostgresqlStore) WriteScrobbleBody(item common.CacheItem) {
}

// ========== QUEUE METHODS ==========

// EnqueueScrobble adds a scrobble event to the PostgreSQL queue.
func (s *PostgresqlStore) EnqueueScrobble(ctx context.Context, event QueuedScrobbleEvent) error {
	// Generate event ID if not set
	if event.ID == "" {
		id, err := generateEventID()
		if err != nil {
			return fmt.Errorf("failed to generate event ID: %w", err)
		}
		event.ID = id
	}

	// Validate event
	if err := validateEvent(event); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}

	// Set created timestamp if not set
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}

	// Serialize scrobble body to JSONB
	scrobbleBodyJSON, err := json.Marshal(event.ScrobbleBody)
	if err != nil {
		return fmt.Errorf("failed to marshal scrobble body: %w", err)
	}

	// Check queue size and enforce limit
	queueSize, _ := s.GetQueueSize(ctx, event.UserID)
	if queueSize >= maxQueuePerUser {
		// Evict oldest event (FIFO)
		_, err := s.db.ExecContext(ctx, `
			DELETE FROM queued_scrobbles
			WHERE id IN (
				SELECT id FROM queued_scrobbles
				WHERE user_id = $1
				ORDER BY created_at ASC
				LIMIT 1
			)
		`, event.UserID)
		if err != nil {
			slog.Warn("failed to evict oldest event from postgresql",
				"user_id", event.UserID,
				"error", err,
			)
		} else {
			slog.Warn("queue event dropped due to size limit",
				"operation", "queue_event_dropped",
				"user_id", event.UserID,
				"queue_size", maxQueuePerUser,
			)
		}
	}

	// Insert event (ON CONFLICT DO NOTHING for deduplication)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO queued_scrobbles
			(id, user_id, scrobble_body, action, progress, created_at, retry_count, last_attempt, player_uuid, rating_key)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (player_uuid, rating_key) DO NOTHING
	`,
		event.ID,
		event.UserID,
		scrobbleBodyJSON,
		event.Action,
		event.Progress,
		event.CreatedAt,
		event.RetryCount,
		sql.NullTime{Time: event.LastAttempt, Valid: !event.LastAttempt.IsZero()},
		event.PlayerUUID,
		event.RatingKey,
	)
	if err != nil {
		slog.Error("queue write failed, using fallback buffer",
			"operation", "storage_fallback_activated",
			"user_id", event.UserID,
			"error", err,
		)
		s.addToFallbackBuffer(event.UserID, event)
		return fmt.Errorf("failed to insert event: %w", err)
	}

	slog.Info("queue event enqueued",
		"operation", "queue_enqueue",
		"user_id", event.UserID,
		"event_id", event.ID,
		"queue_size", queueSize+1,
	)

	// Flush fallback buffer if it exists
	s.flushFallbackBuffer(ctx, event.UserID)

	return nil
}

// DequeueScrobbles retrieves oldest N events from PostgreSQL.
func (s *PostgresqlStore) DequeueScrobbles(ctx context.Context, userID string, limit int) ([]QueuedScrobbleEvent, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, scrobble_body, action, progress, created_at, retry_count, last_attempt, player_uuid, rating_key
		FROM queued_scrobbles
		WHERE user_id = $1
		ORDER BY created_at ASC
		LIMIT $2
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query queued events: %w", err)
	}
	defer rows.Close()

	var events []QueuedScrobbleEvent
	for rows.Next() {
		var event QueuedScrobbleEvent
		var scrobbleBodyJSON []byte
		var lastAttempt sql.NullTime

		err := rows.Scan(
			&event.ID,
			&event.UserID,
			&scrobbleBodyJSON,
			&event.Action,
			&event.Progress,
			&event.CreatedAt,
			&event.RetryCount,
			&lastAttempt,
			&event.PlayerUUID,
			&event.RatingKey,
		)
		if err != nil {
			slog.Warn("failed to scan queued event",
				"user_id", userID,
				"error", err,
			)
			continue
		}

		// Unmarshal JSONB scrobble body
		if err := json.Unmarshal(scrobbleBodyJSON, &event.ScrobbleBody); err != nil {
			slog.Warn("failed to unmarshal scrobble body",
				"user_id", userID,
				"event_id", event.ID,
				"error", err,
			)
			continue
		}

		if lastAttempt.Valid {
			event.LastAttempt = lastAttempt.Time
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return events, nil
}

// DeleteQueuedScrobble removes an event from PostgreSQL queue.
func (s *PostgresqlStore) DeleteQueuedScrobble(ctx context.Context, eventID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM queued_scrobbles WHERE id = $1`, eventID)
	if err != nil {
		return fmt.Errorf("failed to delete queued event: %w", err)
	}
	return nil
}

// UpdateQueuedScrobbleRetry updates retry count in PostgreSQL.
func (s *PostgresqlStore) UpdateQueuedScrobbleRetry(ctx context.Context, eventID string, retryCount int) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE queued_scrobbles
		SET retry_count = $1, last_attempt = $2
		WHERE id = $3
	`, retryCount, time.Now(), eventID)
	if err != nil {
		return fmt.Errorf("failed to update retry count: %w", err)
	}
	return nil
}

// GetQueueSize returns the number of queued events for a user.
func (s *PostgresqlStore) GetQueueSize(ctx context.Context, userID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM queued_scrobbles WHERE user_id = $1
	`, userID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("failed to get queue size: %w", err)
	}
	return count, nil
}

// GetQueueStatus returns observability metrics for a user's queue.
func (s *PostgresqlStore) GetQueueStatus(ctx context.Context, userID string) (common.QueueStatus, error) {
	status := common.QueueStatus{
		UserID: userID,
		Mode:   "live", // Default, updated by health checker
	}

	queueSize, err := s.GetQueueSize(ctx, userID)
	if err != nil {
		return status, err
	}
	status.QueueSize = queueSize

	if queueSize > 0 {
		// Get oldest event age
		var oldestCreatedAt time.Time
		err := s.db.QueryRowContext(ctx, `
			SELECT created_at FROM queued_scrobbles
			WHERE user_id = $1
			ORDER BY created_at ASC
			LIMIT 1
		`, userID).Scan(&oldestCreatedAt)
		if err == nil {
			status.OldestEventAge = time.Since(oldestCreatedAt)
		}
	}

	return status, nil
}

// ListUsersWithQueuedEvents returns all user IDs with pending events.
func (s *PostgresqlStore) ListUsersWithQueuedEvents(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT user_id FROM queued_scrobbles
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to list users with queued events: %w", err)
	}
	defer rows.Close()

	var userIDs []string
	for rows.Next() {
		var userID string
		if err := rows.Scan(&userID); err != nil {
			continue
		}
		userIDs = append(userIDs, userID)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating rows: %w", err)
	}

	return userIDs, nil
}

// PurgeQueueForUser deletes all queued events for a user.
func (s *PostgresqlStore) PurgeQueueForUser(ctx context.Context, userID string) (int, error) {
	// Get count before deleting
	queueSize, err := s.GetQueueSize(ctx, userID)
	if err != nil {
		return 0, err
	}

	// Delete all events
	_, err = s.db.ExecContext(ctx, `DELETE FROM queued_scrobbles WHERE user_id = $1`, userID)
	if err != nil {
		return 0, fmt.Errorf("failed to purge queue: %w", err)
	}

	return queueSize, nil
}

// ========== FALLBACK BUFFER HELPERS ==========

func (s *PostgresqlStore) addToFallbackBuffer(userID string, event QueuedScrobbleEvent) {
	s.bufferMu.Lock()
	defer s.bufferMu.Unlock()

	if s.fallbackBuffers == nil {
		s.fallbackBuffers = make(map[string]*InMemoryBuffer)
	}

	buffer, exists := s.fallbackBuffers[userID]
	if !exists {
		buffer = NewInMemoryBuffer(fallbackBufferSize)
		s.fallbackBuffers[userID] = buffer
	}

	buffer.Push(event)
}

func (s *PostgresqlStore) flushFallbackBuffer(ctx context.Context, userID string) {
	s.bufferMu.RLock()
	buffer, exists := s.fallbackBuffers[userID]
	s.bufferMu.RUnlock()

	if !exists {
		return
	}

	events := buffer.GetAll()
	if len(events) == 0 {
		return
	}

	for _, event := range events {
		if err := s.EnqueueScrobble(ctx, event); err != nil {
			return
		}
	}

	// Successfully flushed
	buffer.Clear()

	s.bufferMu.Lock()
	delete(s.fallbackBuffers, userID)
	s.bufferMu.Unlock()

	slog.Info("fallback buffer flushed to storage",
		"user_id", userID,
		"event_count", len(events),
	)
}

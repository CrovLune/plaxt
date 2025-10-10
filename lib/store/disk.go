package store

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"crovlune/plaxt/lib/common"
	"github.com/peterbourgon/diskv"
)

// DiskStore is a storage engine that writes to the disk
type DiskStore struct {
	fallbackBuffers map[string]*InMemoryBuffer
	bufferMu        sync.RWMutex
}

// NewDiskStore will instantiate the disk storage
func NewDiskStore() *DiskStore {
	return &DiskStore{
		fallbackBuffers: make(map[string]*InMemoryBuffer),
	}
}

// Ping will check if the connection works right
func (s DiskStore) Ping(ctx context.Context) error {
	return nil
}

// WriteUser will write a user object to disk
func (s DiskStore) WriteUser(user User) {
	s.writeField(user.ID, "username", user.Username)
	s.writeField(user.ID, "access", user.AccessToken)
	s.writeField(user.ID, "refresh", user.RefreshToken)
	s.writeField(user.ID, "updated", user.Updated.Format("01-02-2006"))
	s.writeField(user.ID, "trakt_display_name", user.TraktDisplayName)
}

// GetUser will load a user from disk
func (s DiskStore) GetUser(id string) *User {
	un, err := s.readField(id, "username")
	if err != nil {
		return nil
	}
	ud, err := s.readField(id, "updated")
	if err != nil {
		return nil
	}
	ac, err := s.readField(id, "access")
	if err != nil {
		return nil
	}
	re, err := s.readField(id, "refresh")
	if err != nil {
		return nil
	}
	displayName, _ := s.readField(id, "trakt_display_name")
	updated, _ := time.Parse("01-02-2006", ud)
	user := User{
		ID:               id,
		Username:         strings.ToLower(un),
		AccessToken:      ac,
		RefreshToken:     re,
		TraktDisplayName: displayName,
		Updated:          updated,
	}

	return &user
}

// GetUserByName will load a user from disk
func (s DiskStore) GetUserByName(username string) *User {
	username = strings.ToLower(strings.TrimSpace(username))
	if username == "" {
		return nil
	}
	// Reuse ListUsers to avoid duplicating disk key iteration logic
	for _, u := range s.ListUsers() {
		if strings.ToLower(u.Username) == username {
			// Return a fresh copy from disk to ensure fields (like Updated) are consistent
			return s.GetUser(u.ID)
		}
	}
	return nil
}

func (s DiskStore) DeleteUser(id, username string) bool {
	s.eraseField(id, "username")
	s.eraseField(id, "updated")
	s.eraseField(id, "access")
	s.eraseField(id, "refresh")
	s.eraseField(id, "trakt_display_name")
	return true
}

func (s DiskStore) GetScrobbleBody(playerUuid, ratingKey string) common.CacheItem {
	return common.CacheItem{
		Body: common.ScrobbleBody{
			Progress: 0,
		},
	}
}

func (s DiskStore) WriteScrobbleBody(item common.CacheItem) {
}

func (s DiskStore) ListUsers() []User {
	d := diskv.New(diskv.Options{
		BasePath:     "keystore",
		Transform:    flatTransform,
		CacheSizeMax: 1024 * 1024,
	})

	ids := map[string]struct{}{}
	for key := range d.Keys(nil) {
		if strings.HasSuffix(key, ".username") {
			id := strings.TrimSuffix(key, ".username")
			ids[id] = struct{}{}
		}
	}

	users := make([]User, 0, len(ids))
	for id := range ids {
		if user := s.GetUser(id); user != nil {
			users = append(users, *user)
		}
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Updated.After(users[j].Updated)
	})

	return users
}

func (s DiskStore) writeField(id, field, value string) {
	err := s.write(fmt.Sprintf("%s.%s", id, field), value)
	if err != nil {
		panic(err)
	}
}

func (s DiskStore) readField(id, field string) (string, error) {
	return s.read(fmt.Sprintf("%s.%s", id, field))
}

func (s DiskStore) eraseField(id, field string) error {
	d := diskv.New(diskv.Options{
		BasePath:     "keystore",
		Transform:    flatTransform,
		CacheSizeMax: 1024 * 1024,
	})
	return d.Erase(fmt.Sprintf("%s.%s", id, field))
}

func (s DiskStore) write(key, value string) error {
	d := diskv.New(diskv.Options{
		BasePath:     "keystore",
		Transform:    flatTransform,
		CacheSizeMax: 1024 * 1024,
	})
	return d.Write(key, []byte(value))
}

func (s DiskStore) read(key string) (string, error) {
	d := diskv.New(diskv.Options{
		BasePath:     "keystore",
		Transform:    flatTransform,
		CacheSizeMax: 1024 * 1024,
	})
	value, err := d.Read(key)
	return string(value), err
}

// ========== QUEUE METHODS ==========

const (
	queueBasePath  = "keystore/queue"
	maxQueuePerUser = 1000
	fallbackBufferSize = 100
)

// EnqueueScrobble adds a scrobble event to the queue.
func (s *DiskStore) EnqueueScrobble(ctx context.Context, event QueuedScrobbleEvent) error {
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

	// Serialize event
	data, err := serializeEvent(event)
	if err != nil {
		return fmt.Errorf("failed to serialize event: %w", err)
	}

	// Create user queue directory
	userQueueDir := filepath.Join(queueBasePath, event.UserID)
	if err := os.MkdirAll(userQueueDir, 0755); err != nil {
		slog.Error("queue directory creation failed, using fallback buffer",
			"operation", "storage_fallback_activated",
			"user_id", event.UserID,
			"error", err,
		)
		s.addToFallbackBuffer(event.UserID, event)
		return fmt.Errorf("storage unavailable: %w", err)
	}

	// Check queue size and enforce limit
	queueSize, _ := s.GetQueueSize(ctx, event.UserID)
	if queueSize >= maxQueuePerUser {
		// Evict oldest event (FIFO)
		if err := s.evictOldestEvent(event.UserID); err != nil {
			slog.Warn("failed to evict oldest event",
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

	// Write event to disk: {timestamp}-{uuid}.json
	filename := fmt.Sprintf("%d-%s.json", event.CreatedAt.Unix(), event.ID)
	filePath := filepath.Join(userQueueDir, filename)

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		slog.Error("queue write failed, using fallback buffer",
			"operation", "storage_fallback_activated",
			"user_id", event.UserID,
			"error", err,
		)
		s.addToFallbackBuffer(event.UserID, event)
		return fmt.Errorf("failed to write event: %w", err)
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

// DequeueScrobbles retrieves oldest N events for a user in chronological order.
func (s *DiskStore) DequeueScrobbles(ctx context.Context, userID string, limit int) ([]QueuedScrobbleEvent, error) {
	userQueueDir := filepath.Join(queueBasePath, userID)

	// Check if directory exists
	if _, err := os.Stat(userQueueDir); os.IsNotExist(err) {
		return []QueuedScrobbleEvent{}, nil
	}

	// Read all files in directory
	files, err := os.ReadDir(userQueueDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read queue directory: %w", err)
	}

	// Filter JSON files and sort by filename (timestamp prefix)
	var jsonFiles []fs.DirEntry
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			jsonFiles = append(jsonFiles, file)
		}
	}

	sort.Slice(jsonFiles, func(i, j int) bool {
		return jsonFiles[i].Name() < jsonFiles[j].Name()
	})

	// Read up to limit events
	var events []QueuedScrobbleEvent
	for i := 0; i < len(jsonFiles) && i < limit; i++ {
		filePath := filepath.Join(userQueueDir, jsonFiles[i].Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			slog.Warn("failed to read queue event file",
				"user_id", userID,
				"file", jsonFiles[i].Name(),
				"error", err,
			)
			continue
		}

		event, err := deserializeEvent(data)
		if err != nil {
			slog.Warn("failed to deserialize queue event",
				"user_id", userID,
				"file", jsonFiles[i].Name(),
				"error", err,
			)
			continue
		}

		events = append(events, event)
	}

	return events, nil
}

// DeleteQueuedScrobble removes an event from the queue.
func (s *DiskStore) DeleteQueuedScrobble(ctx context.Context, eventID string) error {
	// Find the event file by scanning all user directories
	queueDir := queueBasePath
	var foundPath string

	err := filepath.WalkDir(queueDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // Skip errors
		}
		if !d.IsDir() && strings.Contains(d.Name(), eventID) {
			foundPath = path
			return filepath.SkipAll // Found it, stop walking
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return fmt.Errorf("failed to search for event: %w", err)
	}

	if foundPath == "" {
		// Event not found, consider it already deleted (idempotent)
		return nil
	}

	if err := os.Remove(foundPath); err != nil {
		return fmt.Errorf("failed to delete event file: %w", err)
	}

	return nil
}

// UpdateQueuedScrobbleRetry updates retry count for an event.
func (s *DiskStore) UpdateQueuedScrobbleRetry(ctx context.Context, eventID string, retryCount int) error {
	// Find the event file
	queueDir := queueBasePath
	var foundPath string
	var event QueuedScrobbleEvent

	err := filepath.WalkDir(queueDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.Contains(d.Name(), eventID) {
			foundPath = path
			return filepath.SkipAll
		}
		return nil
	})

	if err != nil && err != filepath.SkipAll {
		return fmt.Errorf("failed to search for event: %w", err)
	}

	if foundPath == "" {
		return fmt.Errorf("event not found: %s", eventID)
	}

	// Read event
	data, err := os.ReadFile(foundPath)
	if err != nil {
		return fmt.Errorf("failed to read event file: %w", err)
	}

	event, err = deserializeEvent(data)
	if err != nil {
		return fmt.Errorf("failed to deserialize event: %w", err)
	}

	// Update retry count and last attempt
	event.RetryCount = retryCount
	event.LastAttempt = time.Now()

	// Serialize and write back
	data, err = serializeEvent(event)
	if err != nil {
		return fmt.Errorf("failed to serialize event: %w", err)
	}

	if err := os.WriteFile(foundPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write event file: %w", err)
	}

	return nil
}

// GetQueueSize returns the number of queued events for a user.
func (s *DiskStore) GetQueueSize(ctx context.Context, userID string) (int, error) {
	userQueueDir := filepath.Join(queueBasePath, userID)

	if _, err := os.Stat(userQueueDir); os.IsNotExist(err) {
		return 0, nil
	}

	files, err := os.ReadDir(userQueueDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read queue directory: %w", err)
	}

	count := 0
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".json") {
			count++
		}
	}

	return count, nil
}

// GetQueueStatus returns observability metrics for a user's queue.
func (s *DiskStore) GetQueueStatus(ctx context.Context, userID string) (common.QueueStatus, error) {
	status := common.QueueStatus{
		UserID: userID,
		Mode:   "live", // Default, will be updated by health checker
	}

	queueSize, err := s.GetQueueSize(ctx, userID)
	if err != nil {
		return status, err
	}
	status.QueueSize = queueSize

	if queueSize > 0 {
		// Get oldest event age
		events, err := s.DequeueScrobbles(ctx, userID, 1)
		if err == nil && len(events) > 0 {
			status.OldestEventAge = time.Since(events[0].CreatedAt)
		}
	}

	return status, nil
}

// ListUsersWithQueuedEvents returns all user IDs with pending events.
func (s *DiskStore) ListUsersWithQueuedEvents(ctx context.Context) ([]string, error) {
	if _, err := os.Stat(queueBasePath); os.IsNotExist(err) {
		return []string{}, nil
	}

	entries, err := os.ReadDir(queueBasePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read queue base directory: %w", err)
	}

	var userIDs []string
	for _, entry := range entries {
		if entry.IsDir() && entry.Name() != ".gitkeep" {
			userID := entry.Name()
			queueSize, err := s.GetQueueSize(ctx, userID)
			if err == nil && queueSize > 0 {
				userIDs = append(userIDs, userID)
			}
		}
	}

	return userIDs, nil
}

// PurgeQueueForUser deletes all queued events for a user.
func (s *DiskStore) PurgeQueueForUser(ctx context.Context, userID string) (int, error) {
	userQueueDir := filepath.Join(queueBasePath, userID)

	if _, err := os.Stat(userQueueDir); os.IsNotExist(err) {
		return 0, nil
	}

	// Count events first
	queueSize, err := s.GetQueueSize(ctx, userID)
	if err != nil {
		return 0, err
	}

	// Remove directory and all contents
	if err := os.RemoveAll(userQueueDir); err != nil {
		return 0, fmt.Errorf("failed to purge queue directory: %w", err)
	}

	return queueSize, nil
}

// ========== FALLBACK BUFFER HELPERS ==========

func (s *DiskStore) addToFallbackBuffer(userID string, event QueuedScrobbleEvent) {
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

func (s *DiskStore) flushFallbackBuffer(ctx context.Context, userID string) {
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
			// Failed to flush, stop trying
			return
		}
	}

	// Successfully flushed, clear buffer
	buffer.Clear()

	s.bufferMu.Lock()
	delete(s.fallbackBuffers, userID)
	s.bufferMu.Unlock()

	slog.Info("fallback buffer flushed to storage",
		"user_id", userID,
		"event_count", len(events),
	)
}

func (s *DiskStore) evictOldestEvent(userID string) error {
	events, err := s.DequeueScrobbles(context.Background(), userID, 1)
	if err != nil || len(events) == 0 {
		return err
	}

	return s.DeleteQueuedScrobble(context.Background(), events[0].ID)
}

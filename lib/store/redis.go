package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"crovlune/plaxt/lib/common"
	"github.com/redis/go-redis/v9"
)

const (
	userPrefix         = "goplaxt:user:"
	userMapPrefix      = "goplaxt:usermap:"
	accessTokenTimeout = 75 * 24 * time.Hour
	scrobbleFormat     = "goplaxt:scrobble:%s:%s"
	scrobbleTimeout    = 3 * time.Hour
)

// RedisStore is a storage engine that writes to redis
type RedisStore struct {
	client          *redis.Client
	fallbackBuffers map[string]*InMemoryBuffer
	bufferMu        sync.RWMutex
}

// NewRedisClient creates a new redis client object
func NewRedisClient(addr string, password string) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       0,
	})

	_, err := client.Ping(context.Background()).Result()
	if err != nil {
		panic(err)
	}
	return client
}

// NewRedisClientWithUrl creates a new redis client object
func NewRedisClientWithUrl(url string) *redis.Client {
	option, err := redis.ParseURL(url)
	if err != nil {
		panic(err)
	}

	client := redis.NewClient(option)
	_, err = client.Ping(context.Background()).Result()
	if err != nil {
		panic(err)
	}
	return client
}

// NewRedisStore creates new store
func NewRedisStore(client *redis.Client) *RedisStore {
	return &RedisStore{
		client:          client,
		fallbackBuffers: make(map[string]*InMemoryBuffer),
	}
}

// Ping will check if the connection works right
func (s RedisStore) Ping(ctx context.Context) error {
	_, err := s.client.Ping(ctx).Result()
	return err
}

// WriteUser will write a user object to redis
func (s RedisStore) WriteUser(user User) {
	ctx := context.Background()
	currentUser := s.GetUserByName(user.Username)
	pipe := s.client.Pipeline()
	key := userPrefix + user.ID
	pipe.HSet(ctx, key, "username", user.Username)
	pipe.HSet(ctx, key, "access", user.AccessToken)
	pipe.HSet(ctx, key, "refresh", user.RefreshToken)
	pipe.HSet(ctx, key, "updated", user.Updated.Format("01-02-2006"))
	pipe.HSet(ctx, key, "trakt_display_name", user.TraktDisplayName)
	pipe.Expire(ctx, key, accessTokenTimeout)
	// a username should always be occupied by the first id binded to it unless it's expired
	if currentUser == nil {
		pipe.Set(ctx, userMapPrefix+user.Username, user.ID, accessTokenTimeout)
	} else if currentUser.ID == user.ID {
		// extend the TTL on refresh
		pipe.Expire(ctx, userMapPrefix+user.Username, accessTokenTimeout)
	}
	_, err := pipe.Exec(ctx)
	if err != nil {
		panic(err)
	}
}

// GetUser will load a user from redis
func (s RedisStore) GetUser(id string) *User {
	ctx := context.Background()
	data, err := s.client.HGetAll(ctx, userPrefix+id).Result()
	if err != nil {
		return nil
	}
	updated, err := time.Parse("01-02-2006", data["updated"])
	if err != nil {
		return nil
	}
	user := User{
		ID:               id,
		Username:         strings.ToLower(data["username"]),
		AccessToken:      data["access"],
		RefreshToken:     data["refresh"],
		TraktDisplayName: data["trakt_display_name"],
		Updated:          updated,
		store:            s,
	}

	return &user
}

// GetUserByName will load a user from redis
func (s RedisStore) GetUserByName(username string) *User {
	ctx := context.Background()
	id, err := s.client.Get(ctx, userMapPrefix+username).Result()
	if err != nil {
		return nil
	}
	return s.GetUser(id)
}

// DeleteUser will delete a user from redis
func (s RedisStore) DeleteUser(id, username string) bool {
	ctx := context.Background()
	pipe := s.client.Pipeline()
	pipe.Del(ctx, userPrefix+id)
	pipe.Del(ctx, userMapPrefix+username)
	_, err := pipe.Exec(ctx)
	return err == nil
}

func (s RedisStore) ListUsers() []User {
	ctx := context.Background()
	keys, err := s.client.Keys(ctx, userPrefix+"*").Result()
	if err != nil {
		panic(err)
	}

	users := make([]User, 0, len(keys))
	for _, key := range keys {
		id := strings.TrimPrefix(key, userPrefix)
		if id == "" {
			continue
		}
		user := s.GetUser(id)
		if user == nil {
			continue
		}
		users = append(users, *user)
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].Updated.After(users[j].Updated)
	})

	return users
}

func (s RedisStore) GetScrobbleBody(playerUuid, ratingKey string) (item common.CacheItem) {
	ctx := context.Background()
	item = common.CacheItem{
		Body: common.ScrobbleBody{
			Progress: 0,
		},
	}
	cache, err := s.client.Get(ctx, fmt.Sprintf(scrobbleFormat, playerUuid, ratingKey)).Bytes()
	if err != nil {
		return
	}
	_ = json.Unmarshal(cache, &item)
	return
}

func (s RedisStore) WriteScrobbleBody(item common.CacheItem) {
	ctx := context.Background()
	b, _ := json.Marshal(item)
	s.client.Set(ctx, fmt.Sprintf(scrobbleFormat, item.PlayerUuid, item.RatingKey), b, scrobbleTimeout)
}

// ========== QUEUE METHODS ==========

const (
	queueKeyPrefix = "goplaxt:queue:"
)

// EnqueueScrobble adds a scrobble event to the Redis sorted set queue.
func (s *RedisStore) EnqueueScrobble(ctx context.Context, event QueuedScrobbleEvent) error {
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

	queueKey := queueKeyPrefix + event.UserID

	// Check queue size and enforce limit
	queueSize, _ := s.GetQueueSize(ctx, event.UserID)
	if queueSize >= maxQueuePerUser {
		// Evict oldest event (FIFO) - lowest score in sorted set
		if err := s.client.ZPopMin(ctx, queueKey, 1).Err(); err != nil {
			slog.Warn("failed to evict oldest event from redis",
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

	// Add to sorted set with timestamp as score
	score := float64(event.CreatedAt.Unix())
	if err := s.client.ZAdd(ctx, queueKey, redis.Z{
		Score:  score,
		Member: string(data),
	}).Err(); err != nil {
		slog.Error("queue write failed, using fallback buffer",
			"operation", "storage_fallback_activated",
			"user_id", event.UserID,
			"error", err,
		)
		s.addToFallbackBuffer(event.UserID, event)
		return fmt.Errorf("failed to add event to redis queue: %w", err)
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

// DequeueScrobbles retrieves oldest N events from Redis sorted set.
func (s *RedisStore) DequeueScrobbles(ctx context.Context, userID string, limit int) ([]QueuedScrobbleEvent, error) {
	queueKey := queueKeyPrefix + userID

	// Get oldest N events (lowest scores)
	results, err := s.client.ZRangeWithScores(ctx, queueKey, 0, int64(limit-1)).Result()
	if err != nil {
		if err == redis.Nil {
			return []QueuedScrobbleEvent{}, nil
		}
		return nil, fmt.Errorf("failed to read from redis queue: %w", err)
	}

	var events []QueuedScrobbleEvent
	for _, z := range results {
		member, ok := z.Member.(string)
		if !ok {
			continue
		}

		event, err := deserializeEvent([]byte(member))
		if err != nil {
			slog.Warn("failed to deserialize queue event from redis",
				"user_id", userID,
				"error", err,
			)
			continue
		}

		events = append(events, event)
	}

	return events, nil
}

// DeleteQueuedScrobble removes an event from Redis queue.
func (s *RedisStore) DeleteQueuedScrobble(ctx context.Context, eventID string) error {
	// Need to scan all user queues to find the event
	// Use SCAN to iterate keys matching the pattern
	var cursor uint64
	var keys []string

	for {
		var err error
		var scanKeys []string
		scanKeys, cursor, err = s.client.Scan(ctx, cursor, queueKeyPrefix+"*", 100).Result()
		if err != nil {
			return fmt.Errorf("failed to scan redis keys: %w", err)
		}

		keys = append(keys, scanKeys...)

		if cursor == 0 {
			break
		}
	}

	// Search each queue for the event
	for _, key := range keys {
		members, err := s.client.ZRange(ctx, key, 0, -1).Result()
		if err != nil {
			continue
		}

		for _, member := range members {
			event, err := deserializeEvent([]byte(member))
			if err != nil {
				continue
			}

			if event.ID == eventID {
				// Found it, remove from sorted set
				if err := s.client.ZRem(ctx, key, member).Err(); err != nil {
					return fmt.Errorf("failed to delete event from redis: %w", err)
				}
				return nil
			}
		}
	}

	// Event not found, idempotent
	return nil
}

// UpdateQueuedScrobbleRetry updates retry count in Redis.
func (s *RedisStore) UpdateQueuedScrobbleRetry(ctx context.Context, eventID string, retryCount int) error {
	// Find the event
	var cursor uint64
	var keys []string

	for {
		var err error
		var scanKeys []string
		scanKeys, cursor, err = s.client.Scan(ctx, cursor, queueKeyPrefix+"*", 100).Result()
		if err != nil {
			return fmt.Errorf("failed to scan redis keys: %w", err)
		}

		keys = append(keys, scanKeys...)

		if cursor == 0 {
			break
		}
	}

	// Search each queue for the event
	for _, key := range keys {
		members, err := s.client.ZRangeWithScores(ctx, key, 0, -1).Result()
		if err != nil {
			continue
		}

		for _, z := range members {
			member, ok := z.Member.(string)
			if !ok {
				continue
			}

			event, err := deserializeEvent([]byte(member))
			if err != nil {
				continue
			}

			if event.ID == eventID {
				// Found it, update retry count
				event.RetryCount = retryCount
				event.LastAttempt = time.Now()

				// Serialize updated event
				updatedData, err := serializeEvent(event)
				if err != nil {
					return fmt.Errorf("failed to serialize updated event: %w", err)
				}

				// Remove old member and add updated one (atomic with pipeline)
				pipe := s.client.Pipeline()
				pipe.ZRem(ctx, key, member)
				pipe.ZAdd(ctx, key, redis.Z{
					Score:  z.Score,
					Member: string(updatedData),
				})
				if _, err := pipe.Exec(ctx); err != nil {
					return fmt.Errorf("failed to update event in redis: %w", err)
				}

				return nil
			}
		}
	}

	return fmt.Errorf("event not found: %s", eventID)
}

// GetQueueSize returns the cardinality of the sorted set.
func (s *RedisStore) GetQueueSize(ctx context.Context, userID string) (int, error) {
	queueKey := queueKeyPrefix + userID

	count, err := s.client.ZCard(ctx, queueKey).Result()
	if err != nil {
		if err == redis.Nil {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to get queue size from redis: %w", err)
	}

	return int(count), nil
}

// GetQueueStatus returns observability metrics for a user's queue.
func (s *RedisStore) GetQueueStatus(ctx context.Context, userID string) (common.QueueStatus, error) {
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
		events, err := s.DequeueScrobbles(ctx, userID, 1)
		if err == nil && len(events) > 0 {
			status.OldestEventAge = time.Since(events[0].CreatedAt)
		}
	}

	return status, nil
}

// ListUsersWithQueuedEvents returns all user IDs with pending events.
func (s *RedisStore) ListUsersWithQueuedEvents(ctx context.Context) ([]string, error) {
	var cursor uint64
	var keys []string

	// Scan for all queue keys
	for {
		var err error
		var scanKeys []string
		scanKeys, cursor, err = s.client.Scan(ctx, cursor, queueKeyPrefix+"*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("failed to scan redis keys: %w", err)
		}

		keys = append(keys, scanKeys...)

		if cursor == 0 {
			break
		}
	}

	// Extract user IDs and check if they have events
	var userIDs []string
	for _, key := range keys {
		userID := strings.TrimPrefix(key, queueKeyPrefix)
		if userID == "" {
			continue
		}

		queueSize, err := s.GetQueueSize(ctx, userID)
		if err == nil && queueSize > 0 {
			userIDs = append(userIDs, userID)
		}
	}

	return userIDs, nil
}

// PurgeQueueForUser deletes the entire sorted set for a user.
func (s *RedisStore) PurgeQueueForUser(ctx context.Context, userID string) (int, error) {
	queueKey := queueKeyPrefix + userID

	// Get count before deleting
	queueSize, err := s.GetQueueSize(ctx, userID)
	if err != nil {
		return 0, err
	}

	// Delete the key
	if err := s.client.Del(ctx, queueKey).Err(); err != nil {
		return 0, fmt.Errorf("failed to purge redis queue: %w", err)
	}

	return queueSize, nil
}

// ========== FALLBACK BUFFER HELPERS ==========

func (s *RedisStore) addToFallbackBuffer(userID string, event QueuedScrobbleEvent) {
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

func (s *RedisStore) flushFallbackBuffer(ctx context.Context, userID string) {
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

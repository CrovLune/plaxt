package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
	client *redis.Client
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
func NewRedisStore(client *redis.Client) RedisStore {
	return RedisStore{
		client: client,
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

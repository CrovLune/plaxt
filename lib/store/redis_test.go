package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
)

func TestLoadingUser(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

	s.HSet("goplaxt:user:id123", "username", "halkeye")
	s.HSet("goplaxt:user:id123", "access", "access123")
	s.HSet("goplaxt:user:id123", "refresh", "refresh123")
	s.HSet("goplaxt:user:id123", "updated", "02-25-2019")
	s.HSet("goplaxt:user:id123", "trakt_display_name", "Halkeye")

	// When no token_expiry is set, the system applies 90-day fallback from updated date
	updatedDate := time.Date(2019, 02, 25, 0, 0, 0, 0, time.UTC)
	expectedExpiry := updatedDate.Add(90 * 24 * time.Hour)

	expected, err := json.Marshal(&User{
		ID:               "id123",
		Username:         "halkeye",
		AccessToken:      "access123",
		RefreshToken:     "refresh123",
		TraktDisplayName: "Halkeye",
		Updated:          updatedDate,
		TokenExpiry:      expectedExpiry,
	})
	actual, err := json.Marshal(store.GetUser("id123"))

	assert.EqualValues(t, string(expected), string(actual))
}

func TestSavingUser(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))
	tokenExpiry := time.Date(2019, 05, 25, 0, 0, 0, 0, time.UTC)
	originalUser := &User{
		ID:               "id123",
		Username:         "halkeye",
		AccessToken:      "access123",
		RefreshToken:     "refresh123",
		TraktDisplayName: "Halkeye",
		Updated:          time.Date(2019, 02, 25, 0, 0, 0, 0, time.UTC),
		TokenExpiry:      tokenExpiry,
		store:            store,
	}

	originalUser.save()

	assert.Equal(t, s.HGet("goplaxt:user:id123", "username"), "halkeye")
	assert.Equal(t, s.HGet("goplaxt:user:id123", "access"), "access123")
	assert.Equal(t, s.HGet("goplaxt:user:id123", "refresh"), "refresh123")
	assert.Equal(t, s.HGet("goplaxt:user:id123", "updated"), "02-25-2019")
	assert.Equal(t, s.HGet("goplaxt:user:id123", "trakt_display_name"), "Halkeye")
	assert.Equal(t, s.HGet("goplaxt:user:id123", "token_expiry"), tokenExpiry.Format(time.RFC3339))

	expected, err := json.Marshal(originalUser)
	actual, err := json.Marshal(store.GetUser("id123"))

	assert.EqualValues(t, string(expected), string(actual))
}

func TestPing(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))
	assert.Equal(t, store.Ping(context.TODO()), nil)
}

func TestRedisListUsers(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

	s.HSet("goplaxt:user:newest", "username", "alice")
	s.HSet("goplaxt:user:newest", "access", "access-new")
	s.HSet("goplaxt:user:newest", "refresh", "refresh-new")
	s.HSet("goplaxt:user:newest", "updated", "03-01-2020")
	s.HSet("goplaxt:user:newest", "trakt_display_name", "Alice Smith")

	s.HSet("goplaxt:user:older", "username", "bob")
	s.HSet("goplaxt:user:older", "access", "access-old")
	s.HSet("goplaxt:user:older", "refresh", "refresh-old")
	s.HSet("goplaxt:user:older", "updated", "02-01-2020")

	users := store.ListUsers()
	assert.Len(t, users, 2)
	assert.Equal(t, "newest", users[0].ID)
	assert.Equal(t, "alice", users[0].Username)
	assert.Equal(t, "Alice Smith", users[0].TraktDisplayName)
	assert.Equal(t, "older", users[1].ID)
	assert.Equal(t, "bob", users[1].Username)
	assert.Equal(t, "", users[1].TraktDisplayName)
}

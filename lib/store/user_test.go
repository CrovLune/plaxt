package store

import (
	"strings"
	"testing"
	"time"

	"crovlune/plaxt/lib/common"
	"github.com/stretchr/testify/assert"
)

type captureStore struct {
	lastUser User
}

func (c *captureStore) WriteUser(u User) {
	c.lastUser = u
}

func TestNewUserAppliesDisplayNameLimit(t *testing.T) {
	display := strings.Repeat("x", 60)
	capture := &captureStore{}
	expiry := time.Now().Add(90 * 24 * time.Hour)
	user := NewUser("alice", "atk", "rtk", &display, expiry, capture)

	assert.Equal(t, "alice", user.Username)
	assert.Len(t, user.TraktDisplayName, 50)
	assert.Equal(t, capture.lastUser.TraktDisplayName, user.TraktDisplayName)
}

func TestUpdateUserRespectsOptionalDisplayName(t *testing.T) {
	initialName := "Alice"
	capture := &captureStore{}
	expiry := time.Now().Add(90 * 24 * time.Hour)
	user := NewUser("alice", "atk", "rtk", &initialName, expiry, capture)

	// Nil display name keeps existing value.
	newExpiry := time.Now().Add(90 * 24 * time.Hour)
	user.UpdateUser("atk2", "rtk2", nil, newExpiry)
	assert.Equal(t, "Alice", user.TraktDisplayName)

	// Providing a shorter name replaces it.
	newName := "Bob"
	newExpiry2 := time.Now().Add(90 * 24 * time.Hour)
	user.UpdateUser("atk3", "rtk3", &newName, newExpiry2)
	assert.Equal(t, "Bob", user.TraktDisplayName)
	assert.Equal(t, capture.lastUser.TraktDisplayName, "Bob")
}

func TestUpdateDisplayNameTruncatesAndPreservesTimestamps(t *testing.T) {
	capture := &captureStore{}
	expiry := time.Now().Add(90 * 24 * time.Hour)
	user := NewUser("alice", "atk", "rtk", nil, expiry, capture)
	initialUpdated := user.Updated
	tooLong := strings.Repeat("Z", common.MaxTraktDisplayNameLength+5)

	truncated := user.UpdateDisplayName(&tooLong)
	assert.True(t, truncated)
	assert.Len(t, user.TraktDisplayName, common.MaxTraktDisplayNameLength)
	assert.Equal(t, initialUpdated, user.Updated)
}

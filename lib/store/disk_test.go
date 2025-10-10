package store

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDiskListUsers(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	store.WriteUser(User{
		ID:               "newest",
		Username:         "alice",
		AccessToken:      "access-new",
		RefreshToken:     "refresh-new",
		TraktDisplayName: "Alice Smith",
		Updated:          time.Date(2020, 3, 1, 0, 0, 0, 0, time.UTC),
	})

	store.WriteUser(User{
		ID:               "older",
		Username:         "bob",
		AccessToken:      "access-old",
		RefreshToken:     "refresh-old",
		TraktDisplayName: "",
		Updated:          time.Date(2020, 2, 1, 0, 0, 0, 0, time.UTC),
	})

	users := store.ListUsers()

	assert.Len(t, users, 2)
	assert.Equal(t, "newest", users[0].ID)
	assert.Equal(t, "alice", users[0].Username)
	assert.Equal(t, "Alice Smith", users[0].TraktDisplayName)
	assert.Equal(t, "older", users[1].ID)
	assert.Equal(t, "bob", users[1].Username)
	assert.Equal(t, "", users[1].TraktDisplayName)
}

func TestDiskGetUserLegacyWithoutDisplayName(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	store.writeField("legacy", "username", "carol")
	store.writeField("legacy", "access", "token")
	store.writeField("legacy", "refresh", "refresh-token")
	store.writeField("legacy", "updated", "02-01-2020")

	user := store.GetUser("legacy")
	assert.NotNil(t, user)
	assert.Equal(t, "", user.TraktDisplayName)
}

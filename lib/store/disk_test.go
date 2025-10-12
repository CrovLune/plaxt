package store

import (
	"context"
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

// ========== FAMILY GROUP TESTS ==========

func TestDiskCreateFamilyGroup(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "LivingRoomTV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	err := store.CreateFamilyGroup(context.Background(), group)
	assert.NoError(t, err)

	// Verify group was created
	created, err := store.GetFamilyGroup(context.Background(), "group123")
	assert.NoError(t, err)
	assert.NotNil(t, created)
	assert.Equal(t, "group123", created.ID)
	assert.Equal(t, "LivingRoomTV", created.PlexUsername)

	// Verify plex username mapping
	byPlex, err := store.GetFamilyGroupByPlex(context.Background(), "LivingRoomTV")
	assert.NoError(t, err)
	assert.NotNil(t, byPlex)
	assert.Equal(t, "group123", byPlex.ID)
}

func TestDiskCreateFamilyGroupDuplicatePlexUsername(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	group1 := &FamilyGroup{
		ID:           "group1",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	group2 := &FamilyGroup{
		ID:           "group2",
		PlexUsername: "TV", // Same plex username
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	err := store.CreateFamilyGroup(context.Background(), group1)
	assert.NoError(t, err)

	err = store.CreateFamilyGroup(context.Background(), group2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestDiskListFamilyGroups(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	// Create multiple family groups
	group1 := &FamilyGroup{
		ID:           "group1",
		PlexUsername: "TV1",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	group2 := &FamilyGroup{
		ID:           "group2",
		PlexUsername: "TV2",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	err := store.CreateFamilyGroup(context.Background(), group1)
	assert.NoError(t, err)

	err = store.CreateFamilyGroup(context.Background(), group2)
	assert.NoError(t, err)

	groups, err := store.ListFamilyGroups(context.Background())
	assert.NoError(t, err)
	assert.Len(t, groups, 2)

	// Verify both groups are present
	groupIDs := make(map[string]bool)
	for _, group := range groups {
		groupIDs[group.ID] = true
	}
	assert.True(t, groupIDs["group1"])
	assert.True(t, groupIDs["group2"])
}

func TestDiskAddGroupMember(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	// Create family group first
	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err := store.CreateFamilyGroup(context.Background(), group)
	assert.NoError(t, err)

	// Add member
	member := &GroupMember{
		ID:                  "member1",
		FamilyGroupID:       "group123",
		TempLabel:           "Dad",
		TraktUsername:       "dad_user",
		AccessToken:         "access_token",
		RefreshToken:        "refresh_token",
		AuthorizationStatus: "authorized",
		CreatedAt:           time.Now(),
	}

	err = store.AddGroupMember(context.Background(), member)
	assert.NoError(t, err)

	// Verify member was added
	created, err := store.GetGroupMember(context.Background(), "member1")
	assert.NoError(t, err)
	assert.NotNil(t, created)
	assert.Equal(t, "member1", created.ID)
	assert.Equal(t, "Dad", created.TempLabel)
	assert.Equal(t, "dad_user", created.TraktUsername)
	assert.Equal(t, "authorized", created.AuthorizationStatus)
}

func TestDiskAddGroupMemberDuplicateTraktUsername(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	// Create family group
	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err := store.CreateFamilyGroup(context.Background(), group)
	assert.NoError(t, err)

	// Add first member
	member1 := &GroupMember{
		ID:                  "member1",
		FamilyGroupID:       "group123",
		TempLabel:           "Dad",
		TraktUsername:       "user123",
		AuthorizationStatus: "authorized",
		CreatedAt:           time.Now(),
	}
	err = store.AddGroupMember(context.Background(), member1)
	assert.NoError(t, err)

	// Try to add second member with same Trakt username
	member2 := &GroupMember{
		ID:                  "member2",
		FamilyGroupID:       "group123",
		TempLabel:           "Mom",
		TraktUsername:       "user123", // Same Trakt username
		AuthorizationStatus: "authorized",
		CreatedAt:           time.Now(),
	}
	err = store.AddGroupMember(context.Background(), member2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestDiskListGroupMembers(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	// Create family group
	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err := store.CreateFamilyGroup(context.Background(), group)
	assert.NoError(t, err)

	// Add multiple members
	member1 := &GroupMember{
		ID:                  "member1",
		FamilyGroupID:       "group123",
		TempLabel:           "Dad",
		TraktUsername:       "dad_user",
		AuthorizationStatus: "authorized",
		CreatedAt:           time.Now(),
	}

	member2 := &GroupMember{
		ID:                  "member2",
		FamilyGroupID:       "group123",
		TempLabel:           "Mom",
		TraktUsername:       "mom_user",
		AuthorizationStatus: "authorized",
		CreatedAt:           time.Now(),
	}

	err = store.AddGroupMember(context.Background(), member1)
	assert.NoError(t, err)

	err = store.AddGroupMember(context.Background(), member2)
	assert.NoError(t, err)

	// List members
	members, err := store.ListGroupMembers(context.Background(), "group123")
	assert.NoError(t, err)
	assert.Len(t, members, 2)

	// Verify both members are present
	memberIDs := make(map[string]bool)
	for _, member := range members {
		memberIDs[member.ID] = true
	}
	assert.True(t, memberIDs["member1"])
	assert.True(t, memberIDs["member2"])
}

func TestDiskDeleteFamilyGroup(t *testing.T) {
	_ = os.RemoveAll("keystore")
	defer os.RemoveAll("keystore")

	store := NewDiskStore()

	// Create family group with members
	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err := store.CreateFamilyGroup(context.Background(), group)
	assert.NoError(t, err)

	// Add member
	member := &GroupMember{
		ID:                  "member1",
		FamilyGroupID:       "group123",
		TempLabel:           "Dad",
		TraktUsername:       "dad_user",
		AuthorizationStatus: "authorized",
		CreatedAt:           time.Now(),
	}
	err = store.AddGroupMember(context.Background(), member)
	assert.NoError(t, err)

	// Delete family group
	err = store.DeleteFamilyGroup(context.Background(), "group123")
	assert.NoError(t, err)

	// Verify group is deleted
	deleted, err := store.GetFamilyGroup(context.Background(), "group123")
	assert.NoError(t, err)
	assert.Nil(t, deleted)

	// Verify member is also deleted
	deletedMember, err := store.GetGroupMember(context.Background(), "member1")
	assert.NoError(t, err)
	assert.Nil(t, deletedMember)

	// Verify plex username mapping is deleted
	byPlex, err := store.GetFamilyGroupByPlex(context.Background(), "TV")
	assert.NoError(t, err)
	assert.Nil(t, byPlex)
}

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

// ========== FAMILY GROUP TESTS ==========

func TestCreateFamilyGroup(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "LivingRoomTV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	err = store.CreateFamilyGroup(context.Background(), group)
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

func TestCreateFamilyGroupDuplicatePlexUsername(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

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

	err = store.CreateFamilyGroup(context.Background(), group1)
	assert.NoError(t, err)

	err = store.CreateFamilyGroup(context.Background(), group2)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
}

func TestListFamilyGroups(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

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

	err = store.CreateFamilyGroup(context.Background(), group1)
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

func TestAddGroupMember(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

	// Create family group first
	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err = store.CreateFamilyGroup(context.Background(), group)
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

func TestAddGroupMemberDuplicateTraktUsername(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

	// Create family group
	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err = store.CreateFamilyGroup(context.Background(), group)
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

func TestListGroupMembers(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

	// Create family group
	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err = store.CreateFamilyGroup(context.Background(), group)
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

func TestDeleteFamilyGroup(t *testing.T) {
	s, err := miniredis.Run()
	if err != nil {
		panic(err)
	}
	defer s.Close()

	store := NewRedisStore(NewRedisClient(s.Addr(), ""))

	// Create family group with members
	group := &FamilyGroup{
		ID:           "group123",
		PlexUsername: "TV",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	err = store.CreateFamilyGroup(context.Background(), group)
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

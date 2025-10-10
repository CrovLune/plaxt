package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"crovlune/plaxt/lib/common"
	"github.com/peterbourgon/diskv"
)

// DiskStore is a storage engine that writes to the disk
type DiskStore struct{}

// NewDiskStore will instantiate the disk storage
func NewDiskStore() *DiskStore {
	return &DiskStore{}
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

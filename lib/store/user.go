package store

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"crovlune/plaxt/lib/common"
)

type store interface {
	WriteUser(user User)
}

// User object
type User struct {
	ID               string
	Username         string
	AccessToken      string
	RefreshToken     string
	TraktDisplayName string
	Updated          time.Time
	TokenExpiry      time.Time // When the access token expires
	store            store
}

// uuid returns a random UUIDv4 string.
func uuid() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// extremely unlikely; fall back to timestamp-based hex
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000000")))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return hex.EncodeToString(b)
}

// NewUser creates and persists a new user object with the given tokens.
// If displayName is provided, it is normalized and truncated to the allowed length.
// tokenExpiry is the time when the access token expires.
func NewUser(username, accessToken, refreshToken string, displayName *string, tokenExpiry time.Time, store store) User {
	id := uuid()
	var normalizedName string
	if displayName != nil {
		normalizedName, _ = common.NormalizeDisplayName(*displayName)
	}
	user := User{
		ID:               id,
		Username:         username,
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		TraktDisplayName: normalizedName,
		Updated:          time.Now(),
		TokenExpiry:      tokenExpiry,
		store:            store,
	}
	user.save()
	return user
}

// UpdateUser updates the tokens of an existing user. If displayName is provided,
// it replaces the stored Trakt display name (after normalization/truncation).
// tokenExpiry is the time when the access token expires.
func (user *User) UpdateUser(accessToken, refreshToken string, displayName *string, tokenExpiry time.Time) {
	user.AccessToken = accessToken
	user.RefreshToken = refreshToken
	user.Updated = time.Now()
	user.TokenExpiry = tokenExpiry
	if displayName != nil {
		normalizedName, _ := common.NormalizeDisplayName(*displayName)
		user.TraktDisplayName = normalizedName
	}

	user.save()
}

// UpdateDisplayName updates only the Trakt display name, leaving tokens untouched.
// Returns true if the provided name was truncated.
func (user *User) UpdateDisplayName(displayName *string) bool {
	truncated := false
	if displayName != nil {
		normalizedName, wasTruncated := common.NormalizeDisplayName(*displayName)
		user.TraktDisplayName = normalizedName
		truncated = wasTruncated
	} else {
		user.TraktDisplayName = ""
	}
	user.save()
	return truncated
}

// UpdateUsername updates the Plex username for this user.
// The username is normalized to lowercase for consistency.
func (user *User) UpdateUsername(newUsername string) {
	user.Username = strings.ToLower(strings.TrimSpace(newUsername))
	user.save()
}

func (user User) save() {
	user.store.WriteUser(user)
}

package store

import (
	"fmt"
	"os"
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
	store            store
}

func uuid() string {
	f, _ := os.OpenFile("/dev/urandom", os.O_RDONLY, 0)
	b := make([]byte, 16)
	f.Read(b)
	f.Close()
	uuid := fmt.Sprintf("%x%x%x%x%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])

	return uuid
}

// NewUser creates a new user object
func NewUser(username, accessToken, refreshToken string, displayName *string, store store) User {
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
		store:            store,
	}
	user.save()
	return user
}

// UpdateUser updates an existing user object
func (user *User) UpdateUser(accessToken, refreshToken string, displayName *string) {
	user.AccessToken = accessToken
	user.RefreshToken = refreshToken
	user.Updated = time.Now()
	if displayName != nil {
		normalizedName, _ := common.NormalizeDisplayName(*displayName)
		user.TraktDisplayName = normalizedName
	}

	user.save()
}

// UpdateDisplayName updates only the Trakt display name, leaving tokens untouched.
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

func (user User) save() {
	user.store.WriteUser(user)
}

package store

import (
	"errors"
	"strings"
	"time"
)

var (
	// ErrInvalidFamilyGroup is returned when required fields are missing or invalid.
	ErrInvalidFamilyGroup = errors.New("store: family group is invalid")
	// ErrEmptyPlexUsername signals that PlexUsername cannot be blank.
	ErrEmptyPlexUsername = errors.New("store: plex username cannot be empty")
)

// FamilyGroup represents a shared Plex account mapped to multiple Trakt accounts.
type FamilyGroup struct {
	ID           string    `json:"id"`
	PlexUsername string    `json:"plex_username"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Normalize trims and lowercases the Plex username for consistency.
func (fg *FamilyGroup) Normalize() {
	if fg == nil {
		return
	}
	fg.PlexUsername = strings.ToLower(strings.TrimSpace(fg.PlexUsername))
}

// Validate ensures the family group meets basic invariants before persistence.
func (fg *FamilyGroup) Validate() error {
	if fg == nil {
		return ErrInvalidFamilyGroup
	}
	fg.Normalize()
	if fg.PlexUsername == "" {
		return ErrEmptyPlexUsername
	}
	return nil
}

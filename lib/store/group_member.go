package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// GroupMemberStatusPending indicates the member has not completed OAuth.
	GroupMemberStatusPending = "pending"
	// GroupMemberStatusAuthorized indicates the member has valid Trakt credentials.
	GroupMemberStatusAuthorized = "authorized"
	// GroupMemberStatusExpired indicates stored credentials have expired.
	GroupMemberStatusExpired = "expired"
	// GroupMemberStatusFailed indicates authorization failed and requires action.
	GroupMemberStatusFailed = "failed"
)

var (
	// ErrInvalidGroupMember is returned when validation fails.
	ErrInvalidGroupMember = errors.New("store: group member is invalid")
	// ErrInvalidMemberStatus is returned when the status is not recognized.
	ErrInvalidMemberStatus = errors.New("store: group member status is invalid")
	// ErrEmptyMemberLabel signals missing label input.
	ErrEmptyMemberLabel = errors.New("store: member label cannot be empty")
)

// GroupMember represents a Trakt account linked to a family group.
type GroupMember struct {
	ID                  string     `json:"id"`
	FamilyGroupID       string     `json:"family_group_id"`
	TempLabel           string     `json:"temp_label"`
	TraktUsername       string     `json:"trakt_username,omitempty"`
	AccessToken         string     `json:"-"`
	RefreshToken        string     `json:"-"`
	TokenExpiry         *time.Time `json:"token_expiry,omitempty"`
	AuthorizationStatus string     `json:"authorization_status"`
	CreatedAt           time.Time  `json:"created_at"`
}

// Normalize trims string fields for consistency.
func (gm *GroupMember) Normalize() {
	if gm == nil {
		return
	}
	gm.FamilyGroupID = strings.TrimSpace(gm.FamilyGroupID)
	gm.TempLabel = strings.TrimSpace(gm.TempLabel)
	gm.TraktUsername = strings.TrimSpace(gm.TraktUsername)
	gm.AuthorizationStatus = strings.TrimSpace(strings.ToLower(gm.AuthorizationStatus))
}

// Validate ensures the member satisfies invariants before persistence.
func (gm *GroupMember) Validate() error {
	if gm == nil {
		return ErrInvalidGroupMember
	}
	gm.Normalize()
	if gm.FamilyGroupID == "" {
		return fmt.Errorf("%w: family group id is required", ErrInvalidGroupMember)
	}
	if gm.TempLabel == "" {
		return ErrEmptyMemberLabel
	}
	if len(gm.TempLabel) > 100 {
		return fmt.Errorf("%w: temp label exceeds 100 characters", ErrInvalidGroupMember)
	}
	if !isValidGroupMemberStatus(gm.AuthorizationStatus) {
		return ErrInvalidMemberStatus
	}
	if gm.AuthorizationStatus == GroupMemberStatusAuthorized && gm.TraktUsername == "" {
		return fmt.Errorf("%w: authorized members require trakt username", ErrInvalidGroupMember)
	}
	return nil
}

func isValidGroupMemberStatus(status string) bool {
	switch status {
	case GroupMemberStatusPending,
		GroupMemberStatusAuthorized,
		GroupMemberStatusExpired,
		GroupMemberStatusFailed:
		return true
	default:
		return false
	}
}

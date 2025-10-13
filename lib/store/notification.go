package store

import (
	"encoding/json"
	"time"
)

// NotificationType represents the type of notification
type NotificationType string

const (
	NotificationTypePermanentFailure     NotificationType = "permanent_failure"
	NotificationTypeAuthorizationExpired NotificationType = "authorization_expired"
	NotificationTypeMemberAdded          NotificationType = "member_added"
	NotificationTypeMemberRemoved        NotificationType = "member_removed"
)

// Notification represents a persistent banner notification for a family group
type Notification struct {
	ID             string           `json:"id"`
	FamilyGroupID  string           `json:"family_group_id"`
	GroupMemberID  *string          `json:"group_member_id,omitempty"` // Optional, for member-specific notifications
	Type           NotificationType `json:"type"`
	Message        string           `json:"message"`
	Metadata       json.RawMessage  `json:"metadata,omitempty"` // JSON blob for additional context
	Dismissed      bool             `json:"dismissed"`
	CreatedAt      time.Time        `json:"created_at"`
}

// Validate checks if the notification is valid
func (n *Notification) Validate() error {
	if n.ID == "" {
		return ErrInvalidNotification
	}
	if n.FamilyGroupID == "" {
		return ErrInvalidNotification
	}
	if n.Message == "" {
		return ErrInvalidNotification
	}
	// Validate notification type
	switch n.Type {
	case NotificationTypePermanentFailure,
		NotificationTypeAuthorizationExpired,
		NotificationTypeMemberAdded,
		NotificationTypeMemberRemoved:
		return nil
	default:
		return ErrInvalidNotification
	}
}

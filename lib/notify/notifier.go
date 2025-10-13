package notify

import (
	"context"
	"log/slog"
)

// Notifier provides banner notification functionality for family group events.
// This is a stub implementation that will be expanded in Phase 6 (T047) with
// persistent banner storage and UI integration.
type Notifier struct{}

// NewNotifier creates a new notification service.
func NewNotifier() *Notifier {
	return &Notifier{}
}

// NotifyPermanentFailure logs a permanent scrobble failure.
// TODO (T047): Implement persistent banner storage for admin UI display.
func (n *Notifier) NotifyPermanentFailure(ctx context.Context, groupID, memberID, memberUsername, mediaTitle, errorMsg string) error {
	slog.Error("permanent scrobble failure notification",
		"group_id", groupID,
		"member_id", memberID,
		"member_username", memberUsername,
		"media_title", mediaTitle,
		"error", errorMsg,
		"notification_type", "permanent_failure",
	)
	// TODO: Store notification in database for admin UI retrieval
	return nil
}

// NotifyAuthorizationExpired logs an authorization expiration event.
// TODO (T047): Implement persistent banner storage for admin UI display.
func (n *Notifier) NotifyAuthorizationExpired(ctx context.Context, groupID, memberID, memberUsername string) error {
	slog.Warn("authorization expired notification",
		"group_id", groupID,
		"member_id", memberID,
		"member_username", memberUsername,
		"notification_type", "authorization_expired",
	)
	// TODO: Store notification in database for admin UI retrieval
	return nil
}

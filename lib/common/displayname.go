package common

import "strings"

const MaxTraktDisplayNameLength = 50

// NormalizeDisplayName trims whitespace and enforces the 50 character limit for Trakt display names.
func NormalizeDisplayName(name string) (normalized string, truncated bool) {
	normalized = strings.TrimSpace(name)
	if len(normalized) > MaxTraktDisplayNameLength {
		normalized = normalized[:MaxTraktDisplayNameLength]
		truncated = true
	}
	return normalized, truncated
}

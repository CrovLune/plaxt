package common

import (
	"encoding/json"
	"time"
)

// Ids represent the IDs representing a media item accross the metadata providers
type Ids struct {
	Trakt *int    `json:"trakt,omitempty"`
	Tvdb  *int    `json:"tvdb,omitempty"`
	Imdb  *string `json:"imdb,omitempty"`
	Tmdb  *int    `json:"tmdb,omitempty"`
	Slug  *string `json:"slug,omitempty"`
}

// Show represent a show's IDs
type Show struct {
	Title *string `json:"title,omitempty"`
	Year  *int    `json:"year,omitempty"`
	Ids   Ids     `json:"ids"`
}

// Episode represent an episode
type Episode struct {
	Season *int    `json:"season,omitempty"`
	Number *int    `json:"number,omitempty"`
	Title  *string `json:"title,omitempty"`
	Ids    *Ids    `json:"ids,omitempty"`
}

// Season represent a season
type Season struct {
	Number   int
	Episodes []Episode
}

// Movie represent a movie
type Movie struct {
	Title *string `json:"title,omitempty"`
	Year  *int    `json:"year,omitempty"`
	Ids   Ids     `json:"ids"`
}

// ScrobbleBody represent the scrobbling status for a show or a movie
type ScrobbleBody struct {
	Progress int      `json:"-"` // Handled by custom unmarshaler
	Movie    *Movie   `json:"movie,omitempty"`
	Show     *Show    `json:"show,omitempty"`
	Episode  *Episode `json:"episode,omitempty"`
}

// MarshalJSON implements json.Marshaler for ScrobbleBody.
func (s ScrobbleBody) MarshalJSON() ([]byte, error) {
	type Alias ScrobbleBody
	return json.Marshal(&struct {
		Progress int `json:"progress"`
		*Alias
	}{
		Progress: s.Progress,
		Alias:    (*Alias)(&s),
	})
}

// UnmarshalJSON implements json.Unmarshaler for ScrobbleBody.
// Handles progress as both int and float from Trakt API responses.
func (s *ScrobbleBody) UnmarshalJSON(data []byte) error {
	type Alias ScrobbleBody
	aux := &struct {
		Progress interface{} `json:"progress"`
		*Alias
	}{
		Alias: (*Alias)(s),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	switch v := aux.Progress.(type) {
	case float64:
		s.Progress = int(v)
	case int:
		s.Progress = v
	case nil:
		s.Progress = 0
	}
	return nil
}

// CacheItem represent an item in cache
type CacheItem struct {
	PlayerUuid string       `json:"player_uuid"`
	ServerUuid string       `json:"server_uuid"`
	RatingKey  string       `json:"rating_key"`
	Trigger    string       `json:"trigger"`
	Body       ScrobbleBody `json:"body"`
	LastAction string       `json:"last_action"`
}

// QueueStatus represents current state of the queue system for observability.
type QueueStatus struct {
	// Per-User Metrics
	UserID         string        `json:"user_id"`
	QueueSize      int           `json:"queue_size"`       // Current event count for this user
	OldestEventAge time.Duration `json:"oldest_event_age"` // Age of oldest event in queue

	// System-Wide State
	Mode               string    `json:"mode"`                 // "live" | "queue"
	LastHealthCheck    time.Time `json:"last_health_check"`    // Most recent health check attempt
	LastSuccessfulSync time.Time `json:"last_successful_sync"` // Most recent successful scrobble

	// Drain Metrics
	DrainActive     bool `json:"drain_active"`     // Is drain goroutine running for this user?
	EventsProcessed int  `json:"events_processed"` // Events drained in current session
	EventsFailed    int  `json:"events_failed"`    // Events permanently failed in current session
}

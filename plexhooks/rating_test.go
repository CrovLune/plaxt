package plexhooks

import (
	"testing"
)

func TestRatingArrayHandling(t *testing.T) {
	// This is a simplified version of the actual Plex webhook payload
	// that was causing the error
	payload := `{
		"event":"media.stop",
		"user":true,
		"owner":true,
		"Account":{"id":32868327,"title":"crovlune"},
		"Server":{"title":"ZEUS","uuid":"dd9ab58d08da4375d63f56c115bfc07401775b25"},
		"Player":{"local":true,"publicAddress":"1.2.3.4","title":"Test","uuid":"test-uuid"},
		"Metadata":{
			"librarySectionType":"show",
			"ratingKey":"799",
			"key":"/library/metadata/799",
			"type":"episode",
			"title":"Test Episode",
			"audienceRating":7.9,
			"Rating":[{"image":"themoviedb://image.rating","value":7.9,"type":"audience"}],
			"duration":1320000
		}
	}`

	hook, err := ParseWebhook([]byte(payload))
	if err != nil {
		t.Fatalf("Failed to parse webhook: %v", err)
	}

	if hook.Event != "media.stop" {
		t.Errorf("Expected event 'media.stop', got '%s'", hook.Event)
	}

	if hook.Metadata.Title != "Test Episode" {
		t.Errorf("Expected title 'Test Episode', got '%s'", hook.Metadata.Title)
	}

	if hook.Metadata.AudienceRating != 7.9 {
		t.Errorf("Expected audienceRating 7.9, got %f", hook.Metadata.AudienceRating)
	}

	if hook.Metadata.Type != "episode" {
		t.Errorf("Expected type 'episode', got '%s'", hook.Metadata.Type)
	}

	t.Logf("Successfully parsed webhook with Rating array!")
}

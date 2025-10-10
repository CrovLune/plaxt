package common

import (
	"encoding/json"
	"testing"
)

func TestScrobbleBodyProgressUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		json     string
		expected int
	}{
		{
			name:     "progress as integer",
			json:     `{"progress": 42}`,
			expected: 42,
		},
		{
			name:     "progress as float",
			json:     `{"progress": 0.0}`,
			expected: 0,
		},
		{
			name:     "progress as float with decimals",
			json:     `{"progress": 75.8}`,
			expected: 75,
		},
		{
			name:     "progress missing",
			json:     `{}`,
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body ScrobbleBody
			err := json.Unmarshal([]byte(tt.json), &body)
			if err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if body.Progress != tt.expected {
				t.Errorf("Expected progress %d, got %d", tt.expected, body.Progress)
			}
		})
	}
}

func TestScrobbleBodyProgressMarshal(t *testing.T) {
	body := ScrobbleBody{Progress: 50}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("Failed to marshal: %v", err)
	}

	// Verify it marshals as integer
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Failed to unmarshal result: %v", err)
	}

	progress, ok := result["progress"]
	if !ok {
		t.Fatal("progress field missing in marshaled JSON")
	}

	// JSON numbers unmarshal as float64, but we want to verify it's a whole number
	progressFloat, ok := progress.(float64)
	if !ok {
		t.Fatalf("progress is not a number, got type %T", progress)
	}

	if int(progressFloat) != 50 {
		t.Errorf("Expected progress 50, got %v", progressFloat)
	}
}

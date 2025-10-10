package plexhooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParserParsesFixtures(t *testing.T) {
	t.Parallel()

	parser := NewParser()

	cases := []struct {
		name     string
		fixture  string
		validate func(t *testing.T, hook *Webhook)
	}{
		{
			name:    "music playback",
			fixture: "music.json",
			validate: func(t *testing.T, hook *Webhook) {
				t.Helper()
				assert.Equal(t, "media.play", hook.Event)
				assert.Equal(t, 1, hook.Account.ID)
				assert.Equal(t, "Office", hook.Server.Title)
				assert.Equal(t, "Plex Web (Safari)", hook.Player.Title)
				assert.Equal(t, "track", hook.Metadata.Type)
				assert.Equal(t, "Love The One You're With", hook.Metadata.Title)
			},
		},
		{
			name:    "tv playback",
			fixture: "tv.json",
			validate: func(t *testing.T, hook *Webhook) {
				t.Helper()
				assert.True(t, hook.User)
				assert.Equal(t, "testyboi", hook.Account.Title)
				assert.Equal(t, "nice", hook.Server.Title)
				assert.Equal(t, "200.200.200.200", hook.Player.PublicAddress)
				require.NotEmpty(t, hook.Metadata.Directors)
				assert.Equal(t, "Rich Moore", hook.Metadata.Directors[0].Tag)
				require.NotEmpty(t, hook.Metadata.Writers)
				assert.Equal(t, "writer=49503", hook.Metadata.Writers[0].Filter)
			},
		},
		{
			name:    "movie playback",
			fixture: "movie.json",
			validate: func(t *testing.T, hook *Webhook) {
				t.Helper()
				assert.True(t, hook.User)
				assert.Equal(t, "testyboi", hook.Account.Title)
				assert.Equal(t, "what", hook.Server.Title)
				assert.Equal(t, "200.200.200.200", hook.Player.PublicAddress)
				assert.Equal(t, "movie", hook.Metadata.Type)
				assert.Equal(t, "Hawk Films", hook.Metadata.Studio)
				assert.Equal(t, "PG", hook.Metadata.ContentRating)
				assert.InDelta(t, 9.2, hook.Metadata.AudienceRating, 0.0001)

				require.NotEmpty(t, hook.Metadata.Directors)
				assert.Equal(t, "Stanley Kubrick", hook.Metadata.Directors[0].Tag)

				require.NotEmpty(t, hook.Metadata.Writers)
				assert.Equal(t, "writer=7", hook.Metadata.Writers[0].Filter)

				require.NotEmpty(t, hook.Metadata.Producers)
				assert.Equal(t, 42, hook.Metadata.Producers[0].ID)

				require.NotEmpty(t, hook.Metadata.Countries)
				assert.Equal(t, "United Kingdom", hook.Metadata.Countries[0].Tag)

				require.Len(t, hook.Metadata.Roles, 43)
				assert.Equal(t, "Anthony Herrick", hook.Metadata.Roles[25].Tag)

				require.Len(t, hook.Metadata.Similar, 20)
				assert.Equal(t, "Touch of Evil", hook.Metadata.Similar[8].Tag)

				require.Len(t, hook.Metadata.Genres, 4)
				assert.Equal(t, "War", hook.Metadata.Genres[3].Tag)
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := loadFixture(t, tc.fixture)
			hook, err := parser.Parse(payload)
			require.NoError(t, err)
			require.NotNil(t, hook)

			roundTrip, err := parser.Parse(mustMarshal(t, hook))
			require.NoError(t, err)

			assert.Equal(t, hook, roundTrip)
			tc.validate(t, hook)
		})
	}
}

func TestParseWebhookUsesDefaultParser(t *testing.T) {
	payload := loadFixture(t, "music.json")

	hook, err := ParseWebhook(payload)
	require.NoError(t, err)
	require.NotNil(t, hook)
	assert.Equal(t, "media.play", hook.Event)
}

func TestParserRejectsEmptyPayloads(t *testing.T) {
	parser := NewParser()
	_, err := parser.Parse(nil)
	require.ErrorIs(t, err, ErrEmptyPayload)

	_, err = parser.Parse([]byte("   "))
	require.ErrorIs(t, err, ErrEmptyPayload)
}

func TestParserSurfaceDecoderErrors(t *testing.T) {
	expected := errors.New("boom")
	parser := NewParser(WithDecoder(decoderFunc(func([]byte, interface{}) error {
		return expected
	})))

	_, err := parser.Parse([]byte(`{}`))
	require.ErrorIs(t, err, expected)
}

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()

	path := filepath.Join("test-fixtures", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read fixture %s", path)
	return data
}

func mustMarshal(t *testing.T, hook *Webhook) []byte {
	t.Helper()

	data, err := json.Marshal(hook)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(data), "{"))
	return data
}

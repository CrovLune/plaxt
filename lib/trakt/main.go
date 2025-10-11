package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/store"
	"crovlune/plaxt/plexhooks"
)

const (
	TheTVDBService    = "tvdb"
	TheMovieDbService = "tmdb"
	IMDBService       = "imdb"

	ProgressThreshold = 90

	actionStart = "start"
	actionPause = "pause"
	actionStop  = "stop"
)

// New constructs a Trakt client with sane defaults (10s timeout) and a
// concurrency lock to prevent duplicate scrobble processing.
func New(clientId, clientSecret string, storage store.Store) *Trakt {
	return &Trakt{
		ClientId:     clientId,
		clientSecret: clientSecret,
		storage:      storage,
		httpClient:   &http.Client{Timeout: time.Second * 10},
		ml:           common.NewMultipleLock(),
	}
}

type userSettingsResponse struct {
	User struct {
		Name     string `json:"name"`
		Display  string `json:"display"`
		Username string `json:"username"`
	} `json:"user"`
}

// FetchDisplayName retrieves the Trakt display name for the authenticated user.
func (t *Trakt) FetchDisplayName(ctx context.Context, accessToken string) (string, bool, error) {
	if strings.TrimSpace(accessToken) == "" {
		return "", false, errors.New("missing access token for display name lookup")
	}

	req, err := http.NewRequest(http.MethodGet, "https://api.trakt.tv/users/settings", nil)
	if err != nil {
		return "", false, err
	}
	req = req.WithContext(ctx)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", t.ClientId)

resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		bodySummary := strings.TrimSpace(string(body))
		if bodySummary == "" {
			bodySummary = resp.Status
		}
		return "", false, fmt.Errorf("trakt users/settings http %d: %s", resp.StatusCode, bodySummary)
	}

	var payload userSettingsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", false, err
	}

	displayName := strings.TrimSpace(payload.User.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(payload.User.Display)
	}
	if displayName == "" {
		displayName = strings.TrimSpace(payload.User.Username)
	}

	normalized, truncated := common.NormalizeDisplayName(displayName)
	return normalized, truncated, nil
}

// AuthRequest authorize the connection with Trakt
func (t *Trakt) AuthRequest(redirectURI, username, code, refreshToken, grantType string) (map[string]interface{}, bool) {
	values := map[string]string{
		"code":          code,
		"refresh_token": refreshToken,
		"client_id":     t.ClientId,
		"client_secret": t.clientSecret,
		"redirect_uri":  redirectURI,
		"grant_type":    grantType,
	}
	jsonValue, err := json.Marshal(values)
	if err != nil {
		slog.Error("trakt oauth marshal error", "error", err)
		return map[string]interface{}{"error": "marshal_error", "error_description": err.Error()}, false
	}

	resp, err := t.httpClient.Post("https://api.trakt.tv/oauth/token", "application/json", bytes.NewBuffer(jsonValue))
	if err != nil {
		slog.Error("trakt oauth request error", "error", err)
		return map[string]interface{}{"error": "http_error", "error_description": err.Error()}, false
	}
	defer resp.Body.Close()

	var result map[string]interface{}

	if resp.StatusCode != http.StatusOK {
		// Read error response body for detailed error information
		bodyBytes, readErr := io.ReadAll(resp.Body)
		errorDetail := "Unknown error"
		errorDescription := ""

		if readErr == nil && len(bodyBytes) > 0 {
			var errorResponse map[string]interface{}
			if jsonErr := json.Unmarshal(bodyBytes, &errorResponse); jsonErr == nil {
				// Trakt typically returns {"error": "invalid_grant", "error_description": "..."}
				if errMsg, ok := errorResponse["error"].(string); ok {
					errorDetail = errMsg
				}
				if errDesc, ok := errorResponse["error_description"].(string); ok {
					errorDescription = errDesc
				}
			} else {
				// If JSON parsing fails, use raw body as error detail
				errorDetail = string(bodyBytes)
			}
		}

		slog.Error("trakt oauth error", "http_status", resp.StatusCode, "http_status_text", resp.Status, "error", errorDetail, "error_description", errorDescription)

		// Include error details in result for caller to use
		result = map[string]interface{}{
			"http_status":       resp.StatusCode,
			"http_status_text":  resp.Status,
			"error":             errorDetail,
			"error_description": errorDescription,
		}
		return result, false
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		slog.Error("trakt oauth decode error", "error", err)
		return map[string]interface{}{"error": "decode_error", "error_description": err.Error()}, false
	}

	// Log the expires_in value if present for debugging
	if expiresIn, ok := result["expires_in"].(float64); ok {
		slog.Debug("trakt oauth token received", "expires_in_seconds", expiresIn)
	}

	return result, true
}

// Handle determine if an item is a show or a movie
func (t *Trakt) Handle(hook *plexhooks.Webhook, user store.User) {
	if hook == nil {
		slog.Error("webhook missing payload")
		return
	}
	if hook.Player.UUID == "" || hook.Metadata.RatingKey == "" {
		slog.Warn("webhook ignored: missing fields", "event", hook.Event)
		return
	}

	lockKey := fmt.Sprintf("%s:%s", hook.Player.UUID, hook.Metadata.RatingKey)
	t.ml.Lock(lockKey)
	defer t.ml.Unlock(lockKey)

	event, cache, progress := t.getAction(hook)
	itemChanged := true
	if event == "" {
		slog.Info("webhook ignored: no action", "event", hook.Event)
		return
	} else if cache.ServerUuid == hook.Server.UUID {
		itemChanged = false
		if cache.LastAction == actionStop || (cache.LastAction == event && progress == cache.Body.Progress) {
			slog.Info("webhook duplicate event ignored", "username", user.Username, "plaxt_id", user.ID, "event", hook.Event)
			return
		}
	}

	if itemChanged {
		var body *common.ScrobbleBody
		switch hook.Metadata.LibrarySectionType {
		case "show":
			body = t.handleShow(hook)
			if body == nil {
				slog.Warn("episode not found")
				return
			}
		case "movie":
			body = t.handleMovie(hook)
			if body == nil {
				slog.Warn("movie not found")
				return
			}
		default:
			slog.Info("webhook ignored: unsupported library section type")
			return
		}
		cache.Body = *body
	}

	cache.PlayerUuid = hook.Player.UUID
	cache.ServerUuid = hook.Server.UUID
	cache.RatingKey = hook.Metadata.RatingKey
	cache.Trigger = hook.Event
	cache.Body.Progress = progress
	// Log intent with best-effort media description based on hook metadata
	mediaHint := hook.Metadata.Title
	if strings.ToLower(hook.Metadata.Type) == "episode" && hook.Metadata.GrandparentTitle != "" {
		mediaHint = fmt.Sprintf("%s - S%02dE%02d %s", hook.Metadata.GrandparentTitle, hook.Metadata.ParentIndex, hook.Metadata.Index, hook.Metadata.Title)
	}
	finished := event == actionStop && progress >= ProgressThreshold
		slog.Info("webhook handle", "username", user.Username, "plaxt_id", user.ID, "action", event, "media", mediaHint, "progress", progress, "finished", finished)
	t.scrobbleRequest(event, cache, user)
}

func (t *Trakt) handleShow(hook *plexhooks.Webhook) *common.ScrobbleBody {
	if len(hook.Metadata.ExternalGUIDs) > 0 {
		isValid := false
		ids := common.Ids{}
		for _, guid := range hook.Metadata.ExternalGUIDs {
			if len(guid.ID) < 8 {
				continue
			}
			switch guid.ID[:4] {
			case TheMovieDbService:
				id, err := strconv.Atoi(guid.ID[7:])
				if err != nil {
					continue
				}
				ids.Tmdb = &id
				isValid = true
			case TheTVDBService:
				id, err := strconv.Atoi(guid.ID[7:])
				if err != nil {
					continue
				}
				ids.Tvdb = &id
				isValid = true
			case IMDBService:
				id := guid.ID[7:]
				ids.Imdb = &id
				isValid = true
			}
		}
		if isValid {
			return &common.ScrobbleBody{
				Episode: &common.Episode{
					Ids: &ids,
				},
			}
		}
	}
	return t.findEpisode(hook)
}

func (t *Trakt) handleMovie(hook *plexhooks.Webhook) *common.ScrobbleBody {
	if len(hook.Metadata.ExternalGUIDs) > 0 {
		isValid := false
		movie := common.Movie{}
		for _, guid := range hook.Metadata.ExternalGUIDs {
			if len(guid.ID) < 8 {
				continue
			}
			switch guid.ID[:4] {
			case TheMovieDbService:
				id, err := strconv.Atoi(guid.ID[7:])
				if err != nil {
					continue
				}
				movie.Ids.Tmdb = &id
				isValid = true
			case TheTVDBService:
				id, err := strconv.Atoi(guid.ID[7:])
				if err != nil {
					continue
				}
				movie.Ids.Tvdb = &id
				isValid = true
			case IMDBService:
				id := guid.ID[7:]
				movie.Ids.Imdb = &id
				isValid = true
			}
		}
		if isValid {
			return &common.ScrobbleBody{
				Movie: &movie,
			}
		}
	}
	return t.findMovie(hook)
}

var episodeRegex = regexp.MustCompile(`([0-9]+)/([0-9]+)/([0-9]+)`)

func (t *Trakt) findEpisode(hook *plexhooks.Webhook) *common.ScrobbleBody {
	u, err := url.Parse(hook.Metadata.GUID)
	if err != nil {
		slog.Warn("invalid guid", "guid", hook.Metadata.GUID)
		return nil
	}
	var srv string
	if strings.HasSuffix(u.Scheme, "tvdb") {
		srv = TheTVDBService
	} else if strings.HasSuffix(u.Scheme, "themoviedb") {
		srv = TheMovieDbService
	} else if strings.HasSuffix(u.Scheme, "hama") {
		if strings.HasPrefix(u.Host, "tvdb-") || strings.HasPrefix(u.Host, "tvdb2-") {
			srv = TheTVDBService
		}
	}
	if srv == "" {
		slog.Warn("unidentified guid", "guid", hook.Metadata.GUID)
		return nil
	}
	showID := episodeRegex.FindStringSubmatch(hook.Metadata.GUID)
	if showID == nil {
		slog.Warn("unmatched guid", "guid", hook.Metadata.GUID)
		return nil
	}
	show := common.Show{}
	id, _ := strconv.Atoi(showID[1])
	if srv == TheTVDBService {
		show.Ids.Tvdb = &id
	} else {
		show.Ids.Tmdb = &id
	}
	season, _ := strconv.Atoi(showID[2])
	number, _ := strconv.Atoi(showID[3])
	episode := common.Episode{
		Season: &season,
		Number: &number,
	}
	return &common.ScrobbleBody{
		Show:    &show,
		Episode: &episode,
	}
}

func (t *Trakt) findMovie(hook *plexhooks.Webhook) *common.ScrobbleBody {
	if hook.Metadata.Title == "" || hook.Metadata.Year == 0 {
		return nil
	}
	return &common.ScrobbleBody{
		Movie: &common.Movie{
			Title: &hook.Metadata.Title,
			Year:  &hook.Metadata.Year,
		},
	}
}

func (t *Trakt) makeRequest(url string) ([]map[string]interface{}, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil { return nil, err }

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("trakt-api-version", "2")
	req.Header.Add("trakt-api-key", t.ClientId)

	resp, err := t.httpClient.Do(req)
	if err != nil { return nil, err }
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("trakt GET %s: %s", url, strings.TrimSpace(string(b)))
	}

	var results []map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&results); err != nil {
		return nil, err
	}
	return results, nil
}

func (t *Trakt) scrobbleRequest(action string, item common.CacheItem, user store.User) {
	URL := fmt.Sprintf("https://api.trakt.tv/scrobble/%s", action)

	body, _ := json.Marshal(item.Body)
	req, err := http.NewRequest("POST", URL, bytes.NewBuffer(body))
	if err != nil {
		slog.Error("scrobble build request error", "username", user.Username, "plaxt_id", user.ID, "action", action, "error", err)
		return
	}

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", user.AccessToken))
	req.Header.Add("trakt-api-version", "2")
	req.Header.Add("trakt-api-key", t.ClientId)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		slog.Error("scrobble http error", "username", user.Username, "plaxt_id", user.ID, "action", action, "error", err)
		// Network error - queue the event
		t.enqueueScrobbleEvent(user, item, action)
		return
	}
	defer resp.Body.Close()

	// Check for service unavailability or rate limiting
	if resp.StatusCode == http.StatusServiceUnavailable ||
	   resp.StatusCode == http.StatusBadGateway ||
	   resp.StatusCode == http.StatusGatewayTimeout ||
	   resp.StatusCode == http.StatusTooManyRequests {
		slog.Warn("scrobble failure, queueing event",
			"username", user.Username,
			"plaxt_id", user.ID,
			"action", action,
			"status", resp.StatusCode,
			"trigger", item.Trigger,
		)
		t.enqueueScrobbleEvent(user, item, action)
		return
	}

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		item.LastAction = action
		if err := json.NewDecoder(resp.Body).Decode(&item.Body); err != nil {
			slog.Error("scrobble decode error", "username", user.Username, "plaxt_id", user.ID, "action", action, "error", err)
			return
		}
		t.storage.WriteScrobbleBody(item)
		// Compose human-friendly media label from returned body
		media := "unknown"
		if item.Body.Movie != nil && item.Body.Movie.Title != nil && item.Body.Movie.Year != nil {
			media = fmt.Sprintf("%s (%d)", *item.Body.Movie.Title, *item.Body.Movie.Year)
		} else if item.Body.Show != nil {
			title := "Unknown Show"
			if item.Body.Show.Title != nil { title = *item.Body.Show.Title }
			if item.Body.Episode != nil && item.Body.Episode.Season != nil && item.Body.Episode.Number != nil {
				media = fmt.Sprintf("%s - S%02dE%02d", title, *item.Body.Episode.Season, *item.Body.Episode.Number)
			} else {
				media = title
			}
		}
		finished := action == actionStop && item.Body.Progress >= ProgressThreshold
		slog.Info("scrobble success", "username", user.Username, "plaxt_id", user.ID, "action", action, "media", media, "progress", item.Body.Progress, "finished", finished, "trigger", item.Trigger)
	} else {
		slog.Error("scrobble failure", "username", user.Username, "plaxt_id", user.ID, "action", action, "status", resp.StatusCode, "trigger", item.Trigger)
	}
}

// enqueueScrobbleEvent queues a scrobble event when Trakt is unavailable.
func (t *Trakt) enqueueScrobbleEvent(user store.User, item common.CacheItem, action string) {
	event := store.QueuedScrobbleEvent{
		UserID:       user.ID,
		ScrobbleBody: item.Body,
		Action:       action,
		Progress:     item.Body.Progress,
		PlayerUUID:   item.PlayerUuid,
		RatingKey:    item.RatingKey,
	}

	ctx := context.Background()
	if err := t.storage.EnqueueScrobble(ctx, event); err != nil {
		slog.Error("failed to enqueue scrobble event",
			"username", user.Username,
			"plaxt_id", user.ID,
			"action", action,
			"error", err,
		)
		return
	}

	// Log the enqueue event for monitoring
	if t.queueEventLog != nil {
		queueSize, _ := t.storage.GetQueueSize(ctx, user.ID)
		t.queueEventLog.Append(store.QueueLogEvent{
			Timestamp:  time.Now(),
			Operation:  "queue_enqueue",
			UserID:     user.ID,
			Username:   user.Username,
			EventID:    event.ID,
			QueueSize:  queueSize,
			RetryCount: 0,
		})
	}

	slog.Info("scrobble event queued",
		"operation", "queue_enqueue",
		"username", user.Username,
		"plaxt_id", user.ID,
		"action", action,
	)
}

func (t *Trakt) getAction(hook *plexhooks.Webhook) (action string, item common.CacheItem, progress int) {
	item = t.storage.GetScrobbleBody(hook.Player.UUID, hook.Metadata.RatingKey)
	if hook.Metadata.Duration > 0 {
		progress = int(math.Round(float64(hook.Metadata.ViewOffset) / float64(hook.Metadata.Duration) * 100.0))
	} else {
		progress = item.Body.Progress
	}
	switch hook.Event {
	case "media.play", "media.resume", "playback.started":
		action = actionStart
	case "media.pause", "media.stop":
		if progress >= ProgressThreshold {
			action = actionStop
		} else {
			action = actionPause
		}
	case "media.scrobble":
		action = actionStop
		if progress < ProgressThreshold {
			progress = ProgressThreshold
		}
	}
	return
}


func (e HttpError) Error() string {
	return e.Message
}

func NewHttpError(code int, message string) HttpError {
	return HttpError{
		Code:    code,
		Message: message,
	}
}

// HealthCheck performs a lightweight health check against Trakt API.
// Returns nil if Trakt is available, error otherwise.
func (t *Trakt) HealthCheck(ctx context.Context) error {
	// Use GET /users/settings as health check endpoint
	// This is a lightweight endpoint that confirms API availability
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.trakt.tv/", nil)
	if err != nil {
		return fmt.Errorf("failed to create health check request: %w", err)
	}

	req.Header.Set("trakt-api-version", "2")
	req.Header.Set("trakt-api-key", t.ClientId)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	defer resp.Body.Close()

	// Any 2xx or 3xx status is considered healthy
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return nil
	}

	return fmt.Errorf("trakt API returned status %d", resp.StatusCode)
}

// ScrobbleFromQueue sends a queued scrobble event to Trakt.
// Returns nil on success, error otherwise.
func (t *Trakt) ScrobbleFromQueue(action string, item common.CacheItem, accessToken string) error {
	URL := fmt.Sprintf("https://api.trakt.tv/scrobble/%s", action)

	body, _ := json.Marshal(item.Body)
	req, err := http.NewRequest("POST", URL, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to build scrobble request: %w", err)
	}

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Add("trakt-api-version", "2")
	req.Header.Add("trakt-api-key", t.ClientId)

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("scrobble http error: %w", err)
	}
	defer resp.Body.Close()

	// Check for service unavailability or rate limiting
	if resp.StatusCode == http.StatusServiceUnavailable ||
	   resp.StatusCode == http.StatusBadGateway ||
	   resp.StatusCode == http.StatusGatewayTimeout ||
	   resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("transient error: status %d", resp.StatusCode)
	}

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		// Success - update cache
		if err := json.NewDecoder(resp.Body).Decode(&item.Body); err == nil {
			item.LastAction = action
			t.storage.WriteScrobbleBody(item)
		}
		return nil
	}

	return fmt.Errorf("scrobble failed with status %d", resp.StatusCode)
}

package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
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
		body, _ := ioutil.ReadAll(resp.Body)
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
	jsonValue, _ := json.Marshal(values)

	resp, err := t.httpClient.Post("https://api.trakt.tv/oauth/token", "application/json", bytes.NewBuffer(jsonValue))
	handleErr(err)
	defer resp.Body.Close()

	var result map[string]interface{}

	if resp.StatusCode != http.StatusOK {
		// Read error response body for detailed error information
		bodyBytes, readErr := ioutil.ReadAll(resp.Body)
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

		log.Printf("Trakt OAuth error: HTTP %d (%s) - error=%s, description=%s",
			resp.StatusCode, resp.Status, errorDetail, errorDescription)

		// Include error details in result for caller to use
		result = map[string]interface{}{
			"http_status":       resp.StatusCode,
			"http_status_text":  resp.Status,
			"error":             errorDetail,
			"error_description": errorDescription,
		}
		return result, false
	}

	err = json.NewDecoder(resp.Body).Decode(&result)
	handleErr(err)

	return result, true
}

// Handle determine if an item is a show or a movie
func (t *Trakt) Handle(hook *plexhooks.Webhook, user store.User) {
	if hook == nil {
		log.Print("Webhook payload missing")
		return
	}
	if hook.Player.UUID == "" || hook.Metadata.RatingKey == "" {
		log.Printf("Event %s ignored", hook.Event)
		return
	}
	lockKey := fmt.Sprintf("%s:%s", hook.Player.UUID, hook.Metadata.RatingKey)
	t.ml.Lock(lockKey)
	defer t.ml.Unlock(lockKey)

	event, cache, progress := t.getAction(hook)
	itemChanged := true
	if event == "" {
		log.Printf("Event %s ignored", hook.Event)
		return
	} else if cache.ServerUuid == hook.Server.UUID {
		itemChanged = false
		if cache.LastAction == actionStop ||
			(cache.LastAction == event && progress == cache.Body.Progress) {
			log.Print("Event already scrobbled")
			return
		}
	}

	if itemChanged {
		var body *common.ScrobbleBody
		switch hook.Metadata.LibrarySectionType {
		case "show":
			body = t.handleShow(hook)
			if body == nil {
				log.Print("Cannot find episode")
				return
			}
		case "movie":
			body = t.handleMovie(hook)
			if body == nil {
				log.Print("Cannot find movie")
				return
			}
		default:
			log.Print("Event ignored")
			return
		}
		cache.Body = *body
	}

	cache.PlayerUuid = hook.Player.UUID
	cache.ServerUuid = hook.Server.UUID
	cache.RatingKey = hook.Metadata.RatingKey
	cache.Trigger = hook.Event
	cache.Body.Progress = progress
	t.scrobbleRequest(event, cache, user.AccessToken)
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
		log.Printf("Invalid guid: %s", hook.Metadata.GUID)
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
		log.Printf("Unidentified guid: %s", hook.Metadata.GUID)
		return nil
	}
	showID := episodeRegex.FindStringSubmatch(hook.Metadata.GUID)
	if showID == nil {
		log.Printf("Unmatched guid: %s", hook.Metadata.GUID)
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

func (t *Trakt) makeRequest(url string) []map[string]interface{} {
	req, err := http.NewRequest("GET", url, nil)
	handleErr(err)

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("trakt-api-version", "2")
	req.Header.Add("trakt-api-key", t.ClientId)

	resp, err := t.httpClient.Do(req)
	handleErr(err)
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	respBody, err := ioutil.ReadAll(resp.Body)
	handleErr(err)

	var results []map[string]interface{}
	err = json.Unmarshal(respBody, &results)
	handleErr(err)

	return results
}

func (t *Trakt) scrobbleRequest(action string, item common.CacheItem, accessToken string) {
	URL := fmt.Sprintf("https://api.trakt.tv/scrobble/%s", action)

	body, _ := json.Marshal(item.Body)
	req, err := http.NewRequest("POST", URL, bytes.NewBuffer(body))
	handleErr(err)

	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Add("trakt-api-version", "2")
	req.Header.Add("trakt-api-key", t.ClientId)

	resp, _ := t.httpClient.Do(req)
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		item.LastAction = action
		respBody, _ := ioutil.ReadAll(resp.Body)
		_ = json.Unmarshal(respBody, &item.Body)
		t.storage.WriteScrobbleBody(item)
		switch action {
		case actionStart:
			log.Printf("%s started (triggered by: %s)", item.Body, item.Trigger)
		case actionPause:
			log.Printf("%s paused (triggered by: %s)", item.Body, item.Trigger)
		case actionStop:
			log.Printf("%s stopped (triggered by: %s)", item.Body, item.Trigger)
		}
	} else {
		log.Printf("%s failed (triggered by: %s, status code: %d)", string(body), item.Trigger, resp.StatusCode)
	}
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

func handleErr(err error) {
	if err != nil {
		panic(err)
	}
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

package plexhooks

// Webhook models the top-level Plex webhook payload.
type Webhook struct {
	Event    string   `json:"event"`
	User     bool     `json:"user"`
	Owner    bool     `json:"owner"`
	Account  Account  `json:"Account"`
	Metadata Metadata `json:"Metadata"`
	Server   Server   `json:"Server"`
	Player   Player   `json:"Player"`
}

// Account represents the Plex account that triggered the webhook.
type Account struct {
	ID    int    `json:"id"`
	Thumb string `json:"thumb"`
	Title string `json:"title"`
}

// Server carries details about the Plex server that emitted the hook.
type Server struct {
	Title string `json:"title"`
	UUID  string `json:"uuid"`
}

// Player describes the playback client responsible for the event.
type Player struct {
	Local         bool   `json:"local"`
	PublicAddress string `json:"publicAddress"`
	Title         string `json:"title"`
	UUID          string `json:"uuid"`
}

// Tag captures repeating metadata such as directors, writers, and genres.
type Tag struct {
	ID     int    `json:"id"`
	Filter string `json:"filter"`
	Tag    string `json:"tag"`
	Role   string `json:"role,omitempty"`
	Thumb  string `json:"thumb,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// ExternalGUID represents an external identifier associated with media.
type ExternalGUID struct {
	ID string `json:"id"`
}

// Metadata contains the majority of information about the played media item.
type Metadata struct {
	LibrarySectionType   string         `json:"librarySectionType"`
	RatingKey            string         `json:"ratingKey"`
	Key                  string         `json:"key"`
	ParentRatingKey      string         `json:"parentRatingKey,omitempty"`
	GrandparentRatingKey string         `json:"grandparentRatingKey,omitempty"`
	ExternalGUIDs        []ExternalGUID `json:"Guid,omitempty"`
	GUID                 string         `json:"guid,omitempty"`

	LibrarySectionTitle string `json:"librarySectionTitle,omitempty"`
	LibrarySectionID    int    `json:"librarySectionID,omitempty"`
	LibrarySectionKey   string `json:"librarySectionKey,omitempty"`

	Studio           string `json:"studio,omitempty"`
	Type             string `json:"type,omitempty"`
	Title            string `json:"title,omitempty"`
	TitleSort        string `json:"titleSort,omitempty"`
	GrandparentKey   string `json:"grandparentKey,omitempty"`
	ParentKey        string `json:"parentKey,omitempty"`
	GrandparentTitle string `json:"grandparentTitle,omitempty"`
	ParentTitle      string `json:"parentTitle,omitempty"`
	ContentRating    string `json:"contentRating,omitempty"`
	Summary          string `json:"summary,omitempty"`
	Tagline          string `json:"tagline,omitempty"`

	Index       int     `json:"index,omitempty"`
	ParentIndex int     `json:"parentIndex,omitempty"`
	Rating      float32 `json:"rating,omitempty"`
	RatingCount int     `json:"ratingCount,omitempty"`

	AudienceRating float32 `json:"audienceRating,omitempty"`
	ViewOffset     int     `json:"viewOffset,omitempty"`
	ViewCount      int     `json:"viewCount,omitempty"`
	LastViewedAt   int     `json:"lastViewedAt,omitempty"`
	Year           int     `json:"year,omitempty"`
	Duration       int     `json:"duration,omitempty"`

	Thumb            string `json:"thumb,omitempty"`
	Art              string `json:"art,omitempty"`
	ParentThumb      string `json:"parentThumb,omitempty"`
	GrandparentThumb string `json:"grandparentThumb,omitempty"`
	GrandparentArt   string `json:"grandparentArt,omitempty"`
	GrandparentTheme string `json:"grandparentTheme,omitempty"`

	OriginallyAvailableAt string `json:"originallyAvailableAt,omitempty"`
	AddedAt               int    `json:"addedAt,omitempty"`
	UpdatedAt             int    `json:"updatedAt,omitempty"`
	AudienceRatingImage   string `json:"audienceRatingImage,omitempty"`
	PrimaryExtraKey       string `json:"primaryExtraKey,omitempty"`
	RatingImage           string `json:"ratingImage,omitempty"`

	Genres    []Tag `json:"Genre,omitempty"`
	Directors []Tag `json:"Director,omitempty"`
	Writers   []Tag `json:"Writer,omitempty"`
	Producers []Tag `json:"Producer,omitempty"`
	Countries []Tag `json:"Country,omitempty"`
	Similar   []Tag `json:"Similar,omitempty"`
	Roles     []Tag `json:"Role,omitempty"`
}

package groupme

import "encoding/json"

// Reaction is the provider's saved reaction projection. UserIDs and Code are
// provider data; they are never interpreted as paths or HTTP headers.
type Reaction struct {
	Type      string   `json:"type"`
	Code      string   `json:"code,omitempty"`
	PackID    *string  `json:"pack_id,omitempty"`
	PackIndex *string  `json:"pack_index,omitempty"`
	UserIDs   []string `json:"user_ids,omitempty"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type MessageEvent struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type ReactionInput struct {
	Type      string  `json:"type"`
	Code      string  `json:"code,omitempty"`
	PackID    *string `json:"pack_id,omitempty"`
	PackIndex *string `json:"pack_index,omitempty"`
}

type EditInput struct {
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type PollOption struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Votes    int      `json:"votes"`
	VoterIDs []string `json:"voter_ids,omitempty"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type Poll struct {
	ID             string       `json:"id"`
	Subject        string       `json:"subject"`
	OwnerID        string       `json:"owner_id"`
	ConversationID string       `json:"conversation_id"`
	CreatedAt      int64        `json:"created_at"`
	Expiration     int64        `json:"expiration"`
	Status         string       `json:"status"`
	Options        []PollOption `json:"options"`
	LastModified   int64        `json:"last_modified"`
	Type           string       `json:"type"`
	Visibility     string       `json:"visibility"`
	UserVotes      []string     `json:"user_votes,omitempty"`
}

// PollList is one observed list page. A nonempty token does not authorize an
// implicit backfill; a continuation request has not yet been verified.
// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type PollList struct {
	Items             []Poll `json:"items"`
	ContinuationToken string `json:"continuation_token,omitempty"`
}

type PollOptionInput struct {
	Title string `json:"title"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type CreatePollInput struct {
	Subject    string            `json:"subject"`
	Options    []PollOptionInput `json:"options"`
	Expiration int64             `json:"expiration"`
	Visibility string            `json:"visibility"`
	Type       string            `json:"type"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type PollResult struct {
	Poll    Poll    `json:"poll"`
	Message Message `json:"message"`
}

type EventLocation struct {
	Name string `json:"name"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type CalendarEvent struct {
	Location       *EventLocation    `json:"location,omitempty"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	StartAt        string            `json:"start_at"`
	EndAt          string            `json:"end_at"`
	IsAllDay       bool              `json:"is_all_day"`
	Timezone       string            `json:"timezone"`
	ScheduledCall  bool              `json:"scheduled_call"`
	Reminders      []int             `json:"reminders"`
	ConversationID string            `json:"conversation_id"`
	EventID        string            `json:"event_id"`
	CreatorID      string            `json:"creator_id"`
	Going          []string          `json:"going"`
	NotGoing       []string          `json:"not_going"`
	MaybeGoing     []string          `json:"maybe_going"`
	GoingCount     int               `json:"going_count"`
	CreatedAt      string            `json:"created_at"`
	UpdatedAt      string            `json:"updated_at"`
	RSVPList       map[string]string `json:"rsvp_list,omitempty"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type CreateEventInput struct {
	Location      *EventLocation `json:"location,omitempty"`
	Name          string         `json:"name"`
	IsAllDay      bool           `json:"is_all_day"`
	StartAt       string         `json:"start_at"`
	EndAt         string         `json:"end_at"`
	Timezone      string         `json:"timezone"`
	ScheduledCall bool           `json:"scheduled_call"`
	Reminders     []int          `json:"reminders"`
	Description   string         `json:"description"`
}

type EventResult struct {
	Event   CalendarEvent `json:"event"`
	Message Message       `json:"message"`
}

type Album struct {
	ConversationID string `json:"conversation_id"`
	AlbumID        string `json:"album_id"`
	Title          string `json:"title"`
	CoverImageURL  string `json:"cover_image_url"`
	EventID        string `json:"event_id"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	CreatorID      string `json:"creator_id"`
	TotalViews     int    `json:"total_views"`
	TotalVideos    int    `json:"total_videos"`
	TotalImages    int    `json:"total_images"`
}

type CreateAlbumInput struct {
	Title             string `json:"title"`
	CoverAttachmentID string `json:"cover_attachment_id"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type AlbumList struct {
	Items      []Album `json:"items"`
	NextCursor string  `json:"next_cursor,omitempty"`
}

type EventWindow struct {
	EndAt string `json:"end_at"`
	Limit int    `json:"limit"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type AlbumMedia struct {
	MediaID          string     `json:"media_id"`
	MediaType        string     `json:"media_type"`
	MediaURL         string     `json:"media_url"`
	PreviewURL       string     `json:"preview_url,omitempty"`
	MediaSource      string     `json:"media_source,omitempty"`
	ConversationID   string     `json:"conversation_id"`
	AlbumID          string     `json:"album_id"`
	CreatorID        string     `json:"creator_id"`
	CreatorName      string     `json:"creator_name,omitempty"`
	CreatorAvatarURL string     `json:"creator_avatar_url,omitempty"`
	CreatedAt        string     `json:"created_at"`
	UpdatedAt        string     `json:"updated_at"`
	TotalViews       int        `json:"total_views"`
	Reactions        []Reaction `json:"reactions,omitempty"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type AlbumMediaPage struct {
	Album      Album        `json:"album"`
	Items      []AlbumMedia `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

type AlbumMediaInput struct {
	MediaURL    string `json:"media_url"`
	MediaType   string `json:"media_type"`
	MediaSource string `json:"media_source"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type InitializeUploadInput struct {
	Extension string `json:"extension"`
	SenderID  string `json:"senderId"`
	FileSize  int64  `json:"fileSize"`
	GroupID   string `json:"groupId"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// UploadTarget contains a short-lived upload capability. Never log or persist
// the capability; only render/thumbnail URLs belong in a sent Attachment.
// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type UploadTarget struct {
	RenderURL     string `json:"renderUrl"`
	ThumbnailURL  string `json:"thumbnailUrl"`
	TranscriptURL string `json:"transcriptUrl"`
	uploadURL     string
	owner         [32]byte
	size          int64
	contentType   string
}

func (UploadTarget) String() string   { return "GroupMe upload capability" }
func (UploadTarget) GoString() string { return "GroupMe upload capability" }

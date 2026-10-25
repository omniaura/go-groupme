package groupme

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
)

// Reaction is the provider's saved reaction projection. UserIDs and Code are
// provider data; they are never interpreted as paths or HTTP headers.
type Reaction struct {
	Type      string   `json:"type"`
	Code      string   `json:"code,omitempty"`
	PackID    *string  `json:"pack_id,omitempty"`
	PackIndex *string  `json:"pack_index,omitempty"`
	UserIDs   []string `json:"user_ids,omitempty"`
}

// UnmarshalJSON accepts both provider representations without changing the
// public string identifiers or the stricter outgoing ReactionInput contract.
func (r *Reaction) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	type reactionWire Reaction
	decoded := reactionWire(*r)
	wire := struct {
		*reactionWire
		PackID    json.RawMessage `json:"pack_id"`
		PackIndex json.RawMessage `json:"pack_index"`
	}{reactionWire: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire.PackID) != 0 {
		value, err := reactionIdentifier(wire.PackID)
		if err != nil {
			return err
		}
		decoded.PackID = value
	}
	if len(wire.PackIndex) != 0 {
		value, err := reactionIdentifier(wire.PackIndex)
		if err != nil {
			return err
		}
		decoded.PackIndex = value
	}
	*r = Reaction(decoded)
	return nil
}

func reactionIdentifier(raw json.RawMessage) (*string, error) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var value string
	if len(raw) != 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		return &value, nil
	}
	// Literal decimal integers avoid floating-point precision loss. Existing
	// string values remain untouched, including provider extensions we do not
	// yet understand; numeric values must fit the uint64 representation.
	number, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		return nil, errors.New("GroupMe reaction identifier must be a string or unsigned integer")
	}
	value = strconv.FormatUint(number, 10)
	return &value, nil
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
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Lat     string `json:"lat,omitempty"`
	Lng     string `json:"lng,omitempty"`
}

// UnmarshalJSON preserves GroupMe's numeric and numeric-string coordinates as
// strings, matching the event creation wire format without losing precision.
func (l *EventLocation) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	type locationWire EventLocation
	decoded := locationWire(*l)
	decoded.Lat, decoded.Lng = "", ""
	wire := struct {
		*locationWire
		Lat json.RawMessage `json:"lat"`
		Lng json.RawMessage `json:"lng"`
	}{locationWire: &decoded}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var err error
	if decoded.Lat, err = eventCoordinate(wire.Lat); err != nil {
		return err
	}
	if decoded.Lng, err = eventCoordinate(wire.Lng); err != nil {
		return err
	}
	*l = EventLocation(decoded)
	return nil
}

func eventCoordinate(raw json.RawMessage) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", nil
	}
	var value string
	if raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", err
		}
		if value == "" {
			return "", nil
		}
	} else {
		var number json.Number
		if err := json.Unmarshal(raw, &number); err != nil {
			return "", errors.New("GroupMe event coordinate must be a numeric value or string")
		}
		value = number.String()
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return "", errors.New("GroupMe event coordinate must be finite")
	}
	return value, nil
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type CalendarEvent struct {
	Location       *EventLocation    `json:"location,omitempty"`
	ImageURL       string            `json:"image_url,omitempty"`
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

package groupme

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

// GalleryOptions selects one gallery page. Before and After are RFC3339
// timestamps, not message IDs. They may be combined to select a bounded window.
// Limit defaults to 100 when zero; the SDK permits 1 through 100 items per call.
type GalleryOptions struct {
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// GalleryPageResult is one explicit gallery response. HasMore means the page
// was full, not that another page is guaranteed. Pass NextBeforeTimestamp as a
// new call's Before to request older media. No message-ID cursor is synthesized.
// Timestamp pagination cannot guarantee exhaustive traversal across equal
// gallery_ts values: a strict Before boundary may omit unseen rows at that time.
type GalleryPageResult struct {
	Messages            []Message `json:"messages"`
	Count               int       `json:"count"`
	HasMore             bool      `json:"has_more"`
	NextBeforeTimestamp string    `json:"next_before_timestamp,omitempty"`
	// ContinuationMaySkipTimestampTies is true whenever a timestamp-only
	// continuation is emitted, even without visible ties. Uniqueness is not
	// guaranteed, so that hint does not promise exhaustive traversal.
	ContinuationMaySkipTimestampTies bool `json:"continuation_may_skip_timestamp_ties,omitempty"`
}

// Fractional precision is bounded to nanoseconds so validation cannot silently
// truncate a provider cursor when comparing it with the requested window.
var galleryTimestampPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

func parseGalleryTimestamp(raw string) (time.Time, bool) {
	if !galleryTimestampPattern.MatchString(raw) {
		return time.Time{}, false
	}
	value, err := time.Parse(time.RFC3339Nano, raw)
	return value, err == nil
}

// GalleryPage follows the community-documented before/after timestamp contract
// with one request. Unlike the legacy Gallery wrapper, it requires gallery_ts
// on returned rows so cursor bounds and continuation can be checked. This is a
// source-backed contract, not a claim of complete live continuation acceptance.
func (c *Client) GalleryPage(ctx context.Context, cid string, options GalleryOptions) (GalleryPageResult, error) {
	if !ValidConversationID(cid) {
		return GalleryPageResult{}, ErrInvalid.New(ctx)
	}
	if options.Limit == 0 {
		options.Limit = 100
	}
	if options.Limit < 1 || options.Limit > 100 {
		return GalleryPageResult{}, ErrInvalid.New(ctx)
	}
	var before, after time.Time
	var valid bool
	if options.Before != "" {
		before, valid = parseGalleryTimestamp(options.Before)
		if !valid {
			return GalleryPageResult{}, ErrInvalid.New(ctx)
		}
	}
	if options.After != "" {
		after, valid = parseGalleryTimestamp(options.After)
		if !valid || options.Before != "" && !after.Before(before) {
			return GalleryPageResult{}, ErrInvalid.New(ctx)
		}
	}
	query := map[string]string{"acceptFiles": "1", "limit": strconv.Itoa(options.Limit)}
	if options.Before != "" {
		query["before"] = options.Before
	}
	if options.After != "" {
		query["after"] = options.After
	}
	var out struct {
		Messages []Message `json:"messages"`
	}
	if err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/conversations/" + cid + "/gallery", Query: query, Result: &out}); err != nil {
		return GalleryPageResult{}, err
	}
	if len(out.Messages) > options.Limit {
		return GalleryPageResult{}, ErrUnavailable.New(ctx)
	}
	seen := make(map[string]bool, len(out.Messages))
	var oldest time.Time
	var nextBefore string
	for _, message := range out.Messages {
		timestamp, valid := parseGalleryTimestamp(message.GalleryTS)
		// Native gallery rows may identify the conversation through either field.
		// A matching field must never mask a conflicting nonempty identity.
		sameConversation := (message.GroupID == cid || message.ConversationID == cid) &&
			(message.GroupID == "" || message.GroupID == cid) &&
			(message.ConversationID == "" || message.ConversationID == cid)
		if !ValidID(message.ID) || seen[message.ID] || !sameConversation || !valid || options.Before != "" && !timestamp.Before(before) || options.After != "" && !timestamp.After(after) {
			return GalleryPageResult{}, ErrUnavailable.New(ctx)
		}
		seen[message.ID] = true
		if nextBefore == "" || timestamp.Before(oldest) {
			oldest, nextBefore = timestamp, message.GalleryTS
		}
	}
	page := GalleryPageResult{Messages: out.Messages, Count: len(out.Messages), HasMore: len(out.Messages) == options.Limit}
	if page.Messages == nil {
		page.Messages = []Message{}
	}
	if page.HasMore {
		page.NextBeforeTimestamp = nextBefore
		page.ContinuationMaySkipTimestampTies = true
	}
	return page, nil
}

package groupme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omniaura/go-kit/errs"
	"github.com/omniaura/go-kit/net/hit"
)

var ErrUnsupported = errs.NewFactory(422, "This GroupMe action is not available yet.", errs.WithCode("groupme_unsupported_action"))

// WithBrowserUserAgent returns an independent client configuration. Only the
// observed browser-style feature methods use it; account/catalog/send calls do
// not forward a caller-supplied header. No other header or origin is configurable.
func (c *Client) WithBrowserUserAgent(ua string) (*Client, error) {
	if len(ua) == 0 || len(ua) > 512 {
		return nil, ErrInvalid.New(context.Background())
	}
	for _, ch := range ua {
		if ch < 0x20 || ch > 0x7e {
			return nil, ErrInvalid.New(context.Background())
		}
	}
	clone := *c
	clone.browserUserAgent = ua
	return &clone, nil
}

func ValidConversationID(id string) bool {
	if ValidID(id) {
		return true
	}
	parts := strings.Split(id, "+")
	return len(parts) == 2 && ValidID(parts[0]) && ValidID(parts[1])
}

func (c *Client) featureAPI(version int) *hit.Client[envelope] {
	base := c.baseURL
	if version == 4 {
		base = strings.TrimSuffix(base, "/v3") + "/v4"
	}
	api := hit.NewClient[envelope](base).Service("groupme").HTTPClient(c.httpClient).Timeout(20*time.Second).Header("Accept", "application/json").Retry(errs.NeverRetry())
	if c.browserUserAgent != "" {
		api = api.Header("User-Agent", c.browserUserAgent)
	}
	return api
}

type featureRequestOptions struct {
	Query            map[string]string
	Body             any
	Result           any
	Method           string
	Endpoint         string
	Version          int
	NullOK           bool
	SelectedNotFound bool
}

// Every caller supplies a fixed route and validates IDs before this boundary.
// SelectedNotFound is enabled only for optional selected-message GETs. Their
// 404 allows the known-page fallback; missing never proves deletion.
func (c *Client) featureRequest(ctx context.Context, options featureRequestOptions) error {
	version, method, endpoint := options.Version, options.Method, options.Endpoint
	q, in, out := options.Query, options.Body, options.Result
	nullOK, selectedNotFound := options.NullOK, options.SelectedNotFound
	if selectedNotFound && method != http.MethodGet {
		return ErrInvalid.New(ctx)
	}
	if !ValidToken(c.token) {
		return ErrAuthentication.New(ctx)
	}
	api := c.featureAPI(version)
	var req *hit.Request[envelope]
	switch method {
	case http.MethodGet:
		req = api.GET(endpoint)
	case http.MethodPost:
		req = api.POST(endpoint)
	case http.MethodPut:
		req = api.PUT(endpoint)
	case http.MethodDelete:
		req = api.DELETE(endpoint)
	default:
		return ErrInvalid.New(ctx)
	}
	req = req.Header("X-Access-Token", c.token)
	for k, v := range q {
		req = req.Query(k, v)
	}
	if in != nil {
		req = req.Body(&in)
	}
	var doc envelope
	mutation := method != http.MethodGet
	if err := req.Do(ctx, &doc); err != nil {
		if selectedNotFound {
			if problem, ok := errors.AsType[*errs.Error](err); ok {
				if upstream, ok := problem.UpstreamInfo(); ok && upstream.Status == http.StatusNotFound {
					return ErrUnavailable.New(ctx)
				}
			}
		}
		return failure(ctx, err, mutation)
	}
	if selectedNotFound && doc.Meta.Code == http.StatusNotFound {
		return ErrUnavailable.New(ctx)
	}
	codeOK := doc.Meta.Code == 200 || mutation && doc.Meta.Code == 201
	if method == http.MethodDelete {
		codeOK = doc.Meta.Code == 200
	}
	if pollPath, isPoll := strings.CutPrefix(endpoint, "/poll/"); isPoll {
		create := method == http.MethodPost && strings.Count(pollPath, "/") == 0
		codeOK = codeOK || doc.Meta.Code == 20000 && !create || doc.Meta.Code == 20100 && create
	}
	if !codeOK {
		return statusFailure(ctx, doc.Meta.Code, mutation)
	}
	if len(doc.Response) == 0 || bytes.Equal(bytes.TrimSpace(doc.Response), []byte("null")) {
		if nullOK {
			return nil
		}
		return statusFailure(ctx, 0, mutation)
	}
	if out != nil && json.Unmarshal(doc.Response, out) != nil {
		return statusFailure(ctx, 0, mutation)
	}
	return nil
}

type reactionIconWrite struct {
	PackID    *json.Number `json:"pack_id,omitempty"`
	PackIndex *json.Number `json:"pack_index,omitempty"`
	Type      string       `json:"type"`
	Code      string       `json:"code,omitempty"`
}

func (c *Client) React(ctx context.Context, cid, mid string, in ReactionInput) error {
	if !ValidConversationID(cid) || !ValidID(mid) || !validReactionInput(in) {
		return ErrInvalid.New(ctx)
	}
	icon := reactionIconWrite{Type: in.Type, Code: in.Code}
	if in.Type == "emoji" {
		// Read models and durable intents retain strings; the captured native
		// write uses numbers. Validation above admits only bounded decimal IDs.
		pack, index := json.Number(*in.PackID), json.Number(*in.PackIndex)
		icon.PackID, icon.PackIndex = &pack, &index
	}
	return c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/messages/" + cid + "/" + mid + "/like", Body: struct {
		LikeIcon reactionIconWrite `json:"like_icon"`
	}{icon}, NullOK: true})
}

// Custom intent IDs use canonical decimal strings; React converts only the
// outbound pack IDs to the native write's numeric JSON representation.
func validReactionInput(in ReactionInput) bool {
	switch in.Type {
	case "unicode":
		return in.PackID == nil && in.PackIndex == nil && utf8.ValidString(in.Code) && strings.TrimSpace(in.Code) != "" && utf8.RuneCountInString(in.Code) <= 32
	case "emoji":
		return in.Code == "" && validReactionDecimal(in.PackID, 1000000) && validReactionDecimal(in.PackIndex, 4095)
	}
	return false
}

func validReactionDecimal(value *string, maximum uint64) bool {
	if value == nil || len(*value) == 0 || len(*value) > 7 {
		return false
	}
	for _, ch := range *value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	number, err := strconv.ParseUint(*value, 10, 32)
	return err == nil && number <= maximum && strconv.FormatUint(number, 10) == *value
}

func (c *Client) Unlike(ctx context.Context, cid, mid string) error {
	if !ValidConversationID(cid) || !ValidID(mid) {
		return ErrInvalid.New(ctx)
	}
	return c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/messages/" + cid + "/" + mid + "/unlike", NullOK: true})
}

func (c *Client) PinMessage(ctx context.Context, cid, mid string) error {
	if !ValidConversationID(cid) || !ValidID(mid) {
		return ErrInvalid.New(ctx)
	}
	return c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/conversations/" + cid + "/messages/" + mid + "/pin", NullOK: true})
}

func (c *Client) UnpinMessage(ctx context.Context, cid, mid string) error {
	if !ValidConversationID(cid) || !ValidID(mid) {
		return ErrInvalid.New(ctx)
	}
	return c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/conversations/" + cid + "/messages/" + mid + "/unpin", NullOK: true})
}

func (c *Client) PinnedMessages(ctx context.Context, cid string) ([]Message, error) {
	if !ValidID(cid) {
		return nil, ErrInvalid.New(ctx)
	}

	var out struct {
		Messages []Message `json:"messages"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/pinned/groups/" + cid + "/messages", Result: &out})
	for _, m := range out.Messages {
		if !ValidID(m.ID) || m.GroupID != cid {
			return nil, ErrUnavailable.New(ctx)
		}
	}
	return out.Messages, err
}

func (c *Client) EditMessage(ctx context.Context, gid, mid string, in EditInput) (Message, error) {
	if !ValidID(gid) || !ValidID(mid) || !validateEdit(in) {
		return Message{}, ErrInvalid.New(ctx)
	}
	if in.Attachments == nil {
		in.Attachments = []Attachment{}
	}

	var out struct {
		Message Message `json:"message"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 4, Method: http.MethodPut, Endpoint: "/groups/" + gid + "/messages/" + mid, Body: in, Result: &out})
	if err == nil && (out.Message.ID != mid || out.Message.GroupID != gid) {
		err = ErrUncertain.New(ctx)
	}
	return out.Message, err
}

// SendDirect uses the documented recipient_id API. It never derives or guesses
// a DM conversation ID from a group or another participant's connection.
func (c *Client) SendDirect(ctx context.Context, recipientID string, in SendInput) (Message, error) {
	if !ValidID(recipientID) || !ValidateSend(in) {
		return Message{}, ErrInvalid.New(ctx)
	}
	if !ValidToken(c.token) {
		return Message{}, ErrAuthentication.New(ctx)
	}
	var body struct {
		DirectMessage struct {
			RecipientID string       `json:"recipient_id"`
			SourceGUID  string       `json:"source_guid"`
			Text        string       `json:"text"`
			Attachments []Attachment `json:"attachments,omitempty"`
		} `json:"direct_message"`
	}
	body.DirectMessage.RecipientID = recipientID
	body.DirectMessage.SourceGUID = in.SourceGUID
	body.DirectMessage.Text = in.Text
	body.DirectMessage.Attachments = in.Attachments
	var doc envelope
	if err := c.api.POST("/direct_messages").Header("X-Access-Token", c.token).Retry(errs.NeverRetry()).Body(&body).Do(ctx, &doc); err != nil {
		return Message{}, failure(ctx, err, true)
	}

	var out struct {
		Message Message `json:"direct_message"`
	}
	if doc.Meta.Code != 201 || json.Unmarshal(doc.Response, &out) != nil || !ValidID(out.Message.ID) || out.Message.RecipientID != recipientID || out.Message.SourceGUID != in.SourceGUID {
		return Message{}, ErrUncertain.New(ctx)
	}
	return out.Message, nil
}

type pollWire struct {
	Data      Poll     `json:"data"`
	UserVote  string   `json:"user_vote"`
	UserVotes []string `json:"user_votes"`
}

func (p pollWire) value() Poll {
	p.Data.UserVotes = p.UserVotes
	// Older single-choice responses use a scalar. An explicitly supplied array,
	// including an empty selection, remains authoritative over that scalar.
	if p.Data.UserVotes == nil && p.UserVote != "" {
		p.Data.UserVotes = []string{p.UserVote}
	}
	return p.Data
}

func (c *Client) Polls(ctx context.Context, cid string) (PollList, error) {
	if !ValidConversationID(cid) {
		return PollList{}, ErrInvalid.New(ctx)
	}

	var out struct {
		Polls []pollWire `json:"polls"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/poll/" + cid, Result: &out})
	// The native client reads one list; no continuation request is verified.
	page := PollList{Items: []Poll{}}
	for _, p := range out.Polls {
		if !ValidID(p.Data.ID) || p.Data.ConversationID != cid {
			return PollList{}, ErrUnavailable.New(ctx)
		}
		page.Items = append(page.Items, p.value())
	}
	return page, err
}

func (c *Client) Poll(ctx context.Context, cid, pid string) (Poll, error) {
	if !ValidConversationID(cid) || !ValidID(pid) {
		return Poll{}, ErrInvalid.New(ctx)
	}

	var out struct {
		Poll pollWire `json:"poll"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/poll/" + cid + "/" + pid, Result: &out})
	if err == nil && (out.Poll.Data.ID != pid || out.Poll.Data.ConversationID != cid) {
		err = ErrUnavailable.New(ctx)
	}
	return out.Poll.value(), err
}

func (c *Client) CreatePoll(ctx context.Context, cid string, in CreatePollInput) (PollResult, error) {
	if !ValidID(cid) || !validText(in.Subject, 1, 500) || len(in.Options) < 2 || len(in.Options) > 10 || in.Expiration <= 0 || (in.Type != "multi" && in.Type != "single") || (in.Visibility != "public" && in.Visibility != "anonymous") {
		return PollResult{}, ErrInvalid.New(ctx)
	}
	for _, v := range in.Options {
		if !validText(v.Title, 1, 500) {
			return PollResult{}, ErrInvalid.New(ctx)
		}
	}

	var out struct {
		Message Message  `json:"message"`
		Poll    pollWire `json:"poll"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/poll/" + cid, Body: in, Result: &out})
	if err == nil && (!ValidID(out.Poll.Data.ID) || out.Poll.Data.ConversationID != cid || !ValidID(out.Message.ID) || out.Message.GroupID != cid) {
		err = ErrUncertain.New(ctx)
	}
	return PollResult{Poll: out.Poll.value(), Message: out.Message}, err
}

func (c *Client) VotePoll(ctx context.Context, cid, pid string, votes []string) (Poll, error) {
	if !ValidConversationID(cid) || !ValidID(pid) || len(votes) > 10 {
		return Poll{}, ErrInvalid.New(ctx)
	}
	seen := map[string]bool{}
	for _, id := range votes {
		if !ValidID(id) || seen[id] {
			return Poll{}, ErrInvalid.New(ctx)
		}
		seen[id] = true
	}
	if votes == nil {
		votes = []string{}
	}

	var out struct {
		Poll pollWire `json:"poll"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/poll/" + cid + "/" + pid, Body: struct {
		Votes []string `json:"votes"`
	}{votes}, Result: &out})
	if err == nil && (out.Poll.Data.ID != pid || out.Poll.Data.ConversationID != cid) {
		err = ErrUncertain.New(ctx)
	}
	return out.Poll.value(), err
}

func validText(s string, min, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(strings.TrimSpace(s)) >= min && utf8.RuneCountInString(s) <= max
}

func (c *Client) Events(ctx context.Context, cid string) ([]CalendarEvent, error) {
	// Match the native calendar's bounded one-year lookback and one list page.
	return c.EventsWithWindow(ctx, cid, EventWindow{EndAt: time.Now().UTC().AddDate(-1, 0, 0).Format(time.RFC3339Nano), Limit: 100})
}

func (c *Client) EventsWithWindow(ctx context.Context, cid string, in EventWindow) ([]CalendarEvent, error) {
	if !ValidConversationID(cid) {
		return nil, ErrInvalid.New(ctx)
	}
	if _, err := time.Parse(time.RFC3339Nano, in.EndAt); err != nil || in.Limit < 1 || in.Limit > 100 {
		return nil, ErrInvalid.New(ctx)
	}

	var out struct {
		Events []CalendarEvent `json:"events"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/conversations/" + cid + "/events/list", Query: map[string]string{"limit": strconv.Itoa(in.Limit), "end_at": in.EndAt}, Result: &out})
	for _, event := range out.Events {
		if !ValidID(event.EventID) || event.ConversationID != cid {
			return nil, ErrUnavailable.New(ctx)
		}
	}
	return out.Events, err
}

// RSVPAcceptedReadbackError distinguishes a successful RSVP POST from a failed
// canonical read. It never authorizes replaying the mutation.
type RSVPAcceptedReadbackError struct {
	cause                   error
	conversationID, eventID string
}

func (e *RSVPAcceptedReadbackError) Error() string {
	return "GroupMe accepted the RSVP; readback is unavailable"
}
func (e *RSVPAcceptedReadbackError) Unwrap() error { return e.cause }
func (e *RSVPAcceptedReadbackError) Matches(conversationID, eventID string) bool {
	return e != nil && e.cause != nil && e.conversationID == conversationID && e.eventID == eventID
}

func (c *Client) RSVP(ctx context.Context, cid, eid string, going bool) (CalendarEvent, error) {
	if !ValidConversationID(cid) || !ValidID(eid) {
		return CalendarEvent{}, ErrInvalid.New(ctx)
	}
	if err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/conversations/" + cid + "/events/rsvp", Query: map[string]string{"event_id": eid, "going": strconv.FormatBool(going)}, NullOK: true}); err != nil {
		return CalendarEvent{}, err
	}
	event, err := c.Event(ctx, cid, eid)
	if err != nil {
		return CalendarEvent{}, &RSVPAcceptedReadbackError{cause: ErrUncertain.New(ctx).AddError(err), conversationID: cid, eventID: eid}
	}
	return event, nil
}

func (c *Client) Event(ctx context.Context, cid, eid string) (CalendarEvent, error) {
	if !ValidConversationID(cid) || !ValidID(eid) {
		return CalendarEvent{}, ErrInvalid.New(ctx)
	}

	var out struct {
		Event CalendarEvent `json:"event"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/conversations/" + cid + "/events/show", Query: map[string]string{"event_id": eid}, Result: &out})
	if err == nil && (out.Event.EventID != eid || out.Event.ConversationID != cid) {
		err = ErrUnavailable.New(ctx)
	}
	return out.Event, err
}

func (c *Client) CreateEvent(ctx context.Context, cid string, in CreateEventInput) (EventResult, error) {
	start, e1 := time.Parse(time.RFC3339Nano, in.StartAt)
	end, e2 := time.Parse(time.RFC3339Nano, in.EndAt)
	_, zoneErr := time.LoadLocation(in.Timezone)
	if !ValidID(cid) || !validText(in.Name, 1, 500) || !validText(in.Description, 0, 4000) || e1 != nil || e2 != nil || !end.After(start) || zoneErr != nil || !validEventReminders(in.Reminders) || (in.Location != nil && !validText(in.Location.Name, 1, 500)) {
		return EventResult{}, ErrInvalid.New(ctx)
	}
	if in.Reminders == nil {
		in.Reminders = []int{}
	}
	var out EventResult
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/conversations/" + cid + "/events/create", Body: in, Result: &out})
	if err == nil && (!ValidID(out.Event.EventID) || out.Event.ConversationID != cid || !ValidID(out.Message.ID) || out.Message.GroupID != cid) {
		err = ErrUncertain.New(ctx)
	}
	return out, err
}

func validEventReminders(values []int) bool {
	if len(values) > 2 {
		return false
	}
	seen := map[int]bool{}
	for _, v := range values {
		switch v {
		case 0, 300, 900, 1800, 3600, 7200, 86400, 172800, 604800:
		default:
			return false
		}
		if seen[v] {
			return false
		}
		seen[v] = true
	}
	return true
}

func (c *Client) CreateAlbum(ctx context.Context, cid string, in CreateAlbumInput) (Album, error) {
	if !ValidConversationID(cid) || !validText(in.Title, 1, 500) || in.CoverAttachmentID != "" {
		return Album{}, ErrInvalid.New(ctx)
	}
	var out Album
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/conversations/" + cid + "/albums/create", Body: in, Result: &out})
	if err == nil && (!ValidID(out.AlbumID) || out.ConversationID != cid) {
		err = ErrUncertain.New(ctx)
	}
	return out, err
}

func (c *Client) Albums(ctx context.Context, cid string) (AlbumList, error) {
	if !ValidConversationID(cid) {
		return AlbumList{}, ErrInvalid.New(ctx)
	}

	var out struct {
		Albums []Album `json:"albums"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/conversations/" + cid + "/albums", Query: map[string]string{"per_page": "50"}, Result: &out})
	for _, a := range out.Albums {
		if !ValidID(a.AlbumID) || a.ConversationID != cid {
			return AlbumList{}, ErrUnavailable.New(ctx)
		}
	}
	// The native list is bounded at 50; only album media has verified paging.
	return AlbumList{Items: out.Albums}, err
}

func (c *Client) Gallery(ctx context.Context, cid string) (MessagePage, error) {
	if !ValidConversationID(cid) {
		return MessagePage{}, ErrInvalid.New(ctx)
	}
	var out MessagePage
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/conversations/" + cid + "/gallery", Query: map[string]string{"acceptFiles": "1", "limit": "100"}, Result: &out})
	for _, m := range out.Messages {
		if !ValidID(m.ID) || (!strings.Contains(cid, "+") && m.GroupID != cid) || (strings.Contains(cid, "+") && m.ConversationID != cid) {
			return MessagePage{}, ErrUnavailable.New(ctx)
		}
	}
	out.Count = len(out.Messages)
	out.HasMore = len(out.Messages) == 100
	// This legacy first-page result has no timestamp cursor. GalleryPage exposes
	// explicit timestamp continuation without overloading NextBeforeID.
	return out, err
}

// AlbumMedia reads one explicitly requested page. Continuation uses the loaded
// native client source contract; a nonempty live cursor is not yet QA-verified.
func (c *Client) AlbumMedia(ctx context.Context, cid, aid, cursor string, limit int) (AlbumMediaPage, error) {
	if !ValidConversationID(cid) || !ValidID(aid) || limit < 1 || limit > 100 || !validAlbumCursor(cursor) {
		return AlbumMediaPage{}, ErrInvalid.New(ctx)
	}
	q := map[string]string{"per_page": strconv.Itoa(limit)}
	if cursor != "" {
		q["page"] = cursor
	}

	var out struct {
		NextCursor string `json:"next_cursor"`
		Album      struct {
			Attachments []AlbumMedia `json:"attachments"`
			Album
		} `json:"album"`
	}
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/conversations/" + cid + "/albums/" + aid + "/media", Query: q, Result: &out})
	if err != nil {
		return AlbumMediaPage{}, err
	}
	if out.Album.AlbumID != aid || out.Album.ConversationID != cid || !validAlbumCursor(out.NextCursor) || cursor != "" && out.NextCursor == cursor {
		return AlbumMediaPage{}, ErrUnavailable.New(ctx)
	}
	for _, item := range out.Album.Attachments {
		if !validAlbumMedia(item, cid, aid) {
			return AlbumMediaPage{}, ErrUnavailable.New(ctx)
		}
	}
	return AlbumMediaPage{Album: out.Album.Album, Items: out.Album.Attachments, NextCursor: out.NextCursor}, err
}

// Album cursors are opaque query data, never paths. This conservative bound
// admits URL-safe/base64 token characters and rejects controls, Unicode and
// request delimiters. The loaded native client passes next_cursor as page.
func validAlbumCursor(cursor string) bool {
	if len(cursor) > 1024 || strings.Contains(cursor, "..") {
		return false
	}
	for _, ch := range cursor {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || strings.ContainsRune("-_.~+=", ch) {
			continue
		}
		return false
	}
	return true
}

func (c *Client) AddAlbumMedia(ctx context.Context, cid, aid string, in []AlbumMediaInput) ([]AlbumMedia, error) {
	if !ValidConversationID(cid) || !ValidID(aid) || len(in) < 1 || len(in) > 10 {
		return nil, ErrInvalid.New(ctx)
	}
	for _, item := range in {
		if item.MediaType != "image" || item.MediaSource != "album" || !validImageURL(item.MediaURL) {
			return nil, ErrInvalid.New(ctx)
		}
	}
	var out []AlbumMedia
	err := c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodPost, Endpoint: "/conversations/" + cid + "/albums/media", Query: map[string]string{"album_id": aid}, Body: in, Result: &out})
	if err == nil {
		if len(out) != len(in) {
			return nil, ErrUncertain.New(ctx)
		}
		type mediaKey struct{ url, kind string }
		requested := make(map[mediaKey]int, len(in))
		for _, item := range in {
			requested[mediaKey{item.MediaURL, item.MediaType}]++
		}
		seen := make(map[string]bool, len(out))
		for _, item := range out {
			key := mediaKey{item.MediaURL, item.MediaType}
			if !validAlbumMedia(item, cid, aid) || seen[item.MediaID] || requested[key] == 0 {
				return nil, ErrUncertain.New(ctx)
			}
			requested[key]--
			seen[item.MediaID] = true
		}
	}
	return out, err
}

func validAlbumMedia(item AlbumMedia, cid, aid string) bool {
	return ValidID(item.MediaID) && item.ConversationID == cid && item.AlbumID == aid && MediaURL(item.MediaURL) != "" && (item.PreviewURL == "" || MediaURL(item.PreviewURL) != "")
}

// DeleteMessage implements the APK-research route. Its provider success behavior
// remains unverified live because the owner has explicitly held all deletions.
// The engine must enforce authorship and the exact durable approval intent.
func (c *Client) DeleteMessage(ctx context.Context, cid, mid string) error {
	if !ValidConversationID(cid) || !ValidID(mid) {
		return ErrInvalid.New(ctx)
	}
	return c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodDelete, Endpoint: "/conversations/" + cid + "/messages/" + mid, NullOK: true})
}

// GroupMessage implements the APK-research selected-message route and typed
// SingleMessageResponse.message model. The shape remains pending paired live
// read QA; an unknown wrapper fails closed rather than trying alternate shapes.
func (c *Client) GroupMessage(ctx context.Context, gid, mid string) (Message, error) {
	if !ValidID(gid) || !ValidID(mid) {
		return Message{}, ErrInvalid.New(ctx)
	}

	var out struct {
		Message Message `json:"message"`
	}
	if err := c.featureRequest(ctx, featureRequestOptions{Version: 4, Method: http.MethodGet, Endpoint: "/groups/" + gid + "/messages/" + mid, Result: &out, SelectedNotFound: true}); err != nil {
		return Message{}, err
	}
	if out.Message.ID != mid || out.Message.GroupID != gid {
		return Message{}, ErrUnavailable.New(ctx)
	}
	return out.Message, nil
}

// DirectMessage requires the actual provider conversation ID already observed
// in an owned page and verifies both participants against current Me. It never
// constructs a DM ID. The research Message response model is deliberately
// strict; its live wrapper still needs paired read-only QA.
func (c *Client) DirectMessage(ctx context.Context, cid, accountID, otherUserID, mid string) (Message, error) {
	if !ValidID(accountID) || !ValidID(otherUserID) || accountID == otherUserID || !ValidID(mid) || !ValidConversationID(cid) {
		return Message{}, ErrInvalid.New(ctx)
	}
	pair := strings.Split(cid, "+")
	if len(pair) != 2 || (pair[0] != accountID || pair[1] != otherUserID) && (pair[0] != otherUserID || pair[1] != accountID) {
		return Message{}, ErrInvalid.New(ctx)
	}
	account, err := c.Me(ctx)
	if err != nil {
		return Message{}, err
	}
	if account.ID != accountID {
		return Message{}, ErrForbidden.New(ctx)
	}
	var out Message
	if err = c.featureRequest(ctx, featureRequestOptions{Version: 3, Method: http.MethodGet, Endpoint: "/direct_messages/" + mid, Query: map[string]string{"other_user_id": otherUserID}, Result: &out, SelectedNotFound: true}); err != nil {
		return Message{}, err
	}
	correctPair := out.SenderID == accountID && out.RecipientID == otherUserID || out.SenderID == otherUserID && out.RecipientID == accountID
	if out.ID != mid || out.ConversationID != cid || out.GroupID != "" || !correctPair || out.UserID != "" && out.UserID != out.SenderID {
		return Message{}, ErrUnavailable.New(ctx)
	}
	return out, nil
}

// Package groupme implements personal-account GroupMe APIs: documented v3 calls
// and narrowly typed browser features verified against native requests.
// It never calls bot endpoints, follows provider links, or retries a mutation.
package groupme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omniaura/go-kit/errs"
	"github.com/omniaura/go-kit/net/hit"
)

const BaseURL = "https://api.groupme.com/v3"

var (
	ErrAuthentication = errs.NewFactory(503, "Reconnect your GroupMe account to continue.", errs.WithCode("groupme_reconnect"), errs.WithAction(errs.ActionReconnect))
	ErrUnavailable    = errs.NewFactory(503, "GroupMe could not be reached. Try again later.", errs.WithCode("groupme_unavailable"))
	ErrInvalid        = errs.NewFactory(400, "Check the GroupMe account, group and message options.", errs.WithCode("groupme_invalid_input"), errs.WithAction(errs.ActionFixInput))
	ErrForbidden      = errs.NewFactory(403, "Your GroupMe account cannot access this group.", errs.WithCode("groupme_forbidden"))
	ErrUncertain      = errs.NewFactory(503, "This action may have completed. Check its saved outcome and GroupMe before trying again.", errs.WithCode("groupme_send_uncertain"))
	ErrRejected       = errs.NewFactory(400, "GroupMe did not accept this request. Check the group and try again.", errs.WithCode("groupme_rejected"))
	ErrRateLimited    = errs.NewFactory(503, "GroupMe is busy. Wait before trying again.", errs.WithCode("groupme_rate_limited"), errs.WithAction(errs.ActionWait))
	idPattern         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

func ValidID(s string) bool { return idPattern.MatchString(s) }
func ValidToken(s string) bool {
	return len(s) >= 8 && len(s) <= 4096 && !strings.ContainsAny(s, " \t\r\n")
}

type Account struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ImageURL accepts only the public GroupMe image service, never credentials or redirects.
func ImageURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host != "i.groupme.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(u.Path) < 2 {
		return ""
	}
	return raw
}

type Member struct {
	ImageURL string `json:"image_url,omitempty"`
	UserID   string `json:"user_id"`
	Nickname string `json:"nickname"`
	Muted    bool   `json:"muted"`
}
type Group struct {
	ImageURL string   `json:"image_url,omitempty"`
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Members  []Member `json:"members,omitempty"`
	Messages struct {
		LastCreatedAt int64 `json:"last_message_created_at"`
	} `json:"messages"`
	CreatedAt int64 `json:"created_at"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type Attachment struct {
	Type        string   `json:"type"`
	URL         string   `json:"url,omitempty"`
	PreviewURL  string   `json:"preview_url,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	Name        string   `json:"name,omitempty"`
	Lat         string   `json:"lat,omitempty"`
	Lng         string   `json:"lng,omitempty"`
	ReplyID     string   `json:"reply_id,omitempty"`
	BaseReplyID string   `json:"base_reply_id,omitempty"`
	UserID      string   `json:"user_id,omitempty"`
	UserIDs     []string `json:"user_ids,omitempty"`
	Loci        [][]int  `json:"loci,omitempty"`
	Charmap     [][]int  `json:"charmap,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	FileID      string   `json:"file_id,omitempty"`
	PollID      string   `json:"poll_id,omitempty"`
	EventID     string   `json:"event_id,omitempty"`
	View        string   `json:"view,omitempty"`
}

// fieldalign:ignore Preserve provider JSON and durable receipt/intent serialization order.
type Message struct {
	// FileMetadata is enriched only for an explicitly requested owned page. It is
	// separate from outgoing attachments and never forwarded by send/edit inputs.
	FileMetadata   map[string]FileMetadata `json:"file_metadata,omitempty"`
	AvatarURL      string                  `json:"avatar_url,omitempty"`
	SenderID       string                  `json:"sender_id,omitempty"`
	RecipientID    string                  `json:"recipient_id,omitempty"`
	ConversationID string                  `json:"conversation_id,omitempty"`
	ID             string                  `json:"id"`
	SourceGUID     string                  `json:"source_guid"`
	GroupID        string                  `json:"group_id"`
	UserID         string                  `json:"user_id"`
	Name           string                  `json:"name"`
	Text           string                  `json:"text"`
	Attachments    []Attachment            `json:"attachments,omitempty"`
	CreatedAt      int64                   `json:"created_at"`
	UpdatedAt      int64                   `json:"updated_at,omitempty"`
	GalleryTS      string                  `json:"gallery_ts,omitempty"`
	PinnedAt       *int64                  `json:"pinned_at,omitzero"`
	PinnedBy       string                  `json:"pinned_by,omitempty"`
	DeletedAt      string                  `json:"deleted_at,omitempty"`
	Hidden         bool                    `json:"hidden"`
	System         bool                    `json:"system"`
	SenderType     string                  `json:"sender_type,omitempty"`
	FavoritedBy    []string                `json:"favorited_by,omitempty"`
	Reactions      []Reaction              `json:"reactions,omitempty"`
	Event          *MessageEvent           `json:"event,omitzero"`
}

// IsDeleted recognizes both history tombstone shapes. deleted_at is an
// explicit provider deletion marker even when hidden is absent.
func (m Message) IsDeleted() bool { return m.Hidden || m.DeletedAt != "" }

// UnmarshalJSON keeps the source-compatible deletion marker while accepting
// null, string, or numeric timestamp wire shapes.
func (m *Message) UnmarshalJSON(data []byte) error {
	type messageWire Message
	wire := struct {
		*messageWire
		DeletedAt json.RawMessage `json:"deleted_at"`
	}{messageWire: (*messageWire)(m)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	raw := bytes.TrimSpace(wire.DeletedAt)
	m.DeletedAt = ""
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] == '"' {
		return json.Unmarshal(raw, &m.DeletedAt)
	}
	var timestamp json.Number
	if err := json.Unmarshal(raw, &timestamp); err != nil {
		return err
	}
	m.DeletedAt = timestamp.String()
	return nil
}

type Chat struct {
	LastMessage Message `json:"last_message"`
	OtherUser   struct {
		AvatarURL string          `json:"avatar_url,omitempty"`
		Name      string          `json:"name"`
		ID        json.RawMessage `json:"id"`
	} `json:"other_user"`
	UpdatedAt     int64 `json:"updated_at"`
	CreatedAt     int64 `json:"created_at"`
	MessagesCount int   `json:"messages_count"`
}
type MessagePage struct {
	Conversation *Group    `json:"conversation,omitempty"`
	NextBeforeID string    `json:"next_before_id,omitempty"`
	Messages     []Message `json:"messages"`
	Count        int       `json:"count"`
	HasMore      bool      `json:"has_more"`
}
type ListPage[T any] struct {
	Items   []T  `json:"items"`
	Page    int  `json:"page"`
	HasMore bool `json:"has_more"`
}
type SendInput struct {
	SourceGUID  string       `json:"source_guid"`
	Text        string       `json:"text"`
	Attachments []Attachment `json:"attachments,omitempty"`
}
type envelope struct {
	Response json.RawMessage `json:"response"`
	Meta     struct {
		Code int `json:"code"`
	} `json:"meta"`
}

type Client struct {
	api              *hit.Client[envelope]
	token            string
	baseURL          string
	httpClient       *http.Client
	browserUserAgent string
}
type boundedTransport struct{ base http.RoundTripper }
type boundedBody struct {
	io.Reader
	io.Closer
}

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	v, e := t.base.RoundTrip(r)
	if e == nil && v != nil && v.Body != nil {
		v.Body = boundedBody{io.LimitReader(v.Body, 2<<20), v.Body}
	}
	return v, e
}
func New(token string) *Client { return NewWithTransport(token, nil) }

// NewWithTransport creates an independent client using the supplied transport.
// A nil transport uses http.DefaultTransport. Configure the transport before
// sharing it, and ensure it supports concurrent requests. Fixed GroupMe URLs,
// timeout, response limits and redirect refusal remain controlled by the SDK.
func NewWithTransport(token string, transport http.RoundTripper) *Client {
	return newClientWithTransport(BaseURL, token, transport)
}

func newClient(base, token string) *Client {
	return newClientWithTransport(base, token, nil)
}

func newClientWithTransport(base, token string, transport http.RoundTripper) *Client {
	if transport == nil {
		transport = http.DefaultTransport
	}
	h := &http.Client{Timeout: 20 * time.Second, Transport: boundedTransport{transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{api: hit.NewClient[envelope](base).Service("groupme").HTTPClient(h).Timeout(20*time.Second).Retry(errs.NeverRetry()).Header("Accept", "application/json"), token: token, baseURL: strings.TrimRight(base, "/"), httpClient: h}
}

// Replace hit errors instead of wrapping them: provider bodies can echo tokens
// or private messages. No body, URL, headers or underlying error escapes here.
func failure(ctx context.Context, err error, mutation bool) error {
	status := 0
	if e, ok := errors.AsType[*errs.Error](err); ok {
		if u, ok := e.UpstreamInfo(); ok {
			status = u.Status
		}
	}
	if status == http.StatusTooManyRequests {
		limited := ErrRateLimited.New(ctx)
		if after, ok := errs.RetryAfterOf(err); ok {
			limited = limited.RetryAfter(after)
		}
		return limited
	}
	return statusFailure(ctx, status, mutation)
}
func statusFailure(ctx context.Context, status int, mutation bool) error {
	switch status {
	case 401:
		return ErrAuthentication.New(ctx)
	case 403, 404:
		return ErrForbidden.New(ctx)
	case 400, 422:
		return ErrRejected.New(ctx)
	case 429:
		return ErrRateLimited.New(ctx)
	}
	if mutation {
		return ErrUncertain.New(ctx)
	}
	return ErrUnavailable.New(ctx)
}
func (c *Client) get(ctx context.Context, endpoint string, q map[string]string, out any) error {
	if !ValidToken(c.token) {
		return ErrAuthentication.New(ctx)
	}
	r := c.api.GET(endpoint).Header("X-Access-Token", c.token)
	if c.browserUserAgent != "" && (strings.HasSuffix(endpoint, "/messages") || endpoint == "/direct_messages") {
		r = r.Header("User-Agent", c.browserUserAgent)
	}
	for k, v := range q {
		r = r.Query(k, v)
	}
	var doc envelope
	if e := r.Do(ctx, &doc); e != nil {
		// GroupMe returns 304 for an exhausted message cursor.
		if x, ok := errors.AsType[*errs.Error](e); ok {
			if u, ok := x.UpstreamInfo(); ok && u.Status == 304 && (strings.HasSuffix(endpoint, "/messages") || endpoint == "/direct_messages") {
				return nil
			}
		}
		return failure(ctx, e, false)
	}
	if doc.Meta.Code == 304 && (strings.HasSuffix(endpoint, "/messages") || endpoint == "/direct_messages") {
		return nil
	}
	if doc.Meta.Code != 200 || len(doc.Response) == 0 || bytes.Equal(bytes.TrimSpace(doc.Response), []byte("null")) || json.Unmarshal(doc.Response, out) != nil {
		return statusFailure(ctx, doc.Meta.Code, false)
	}
	return nil
}
func (c *Client) Me(ctx context.Context) (Account, error) {
	var a Account
	e := c.get(ctx, "/users/me", nil, &a)
	if e == nil && !ValidID(a.ID) {
		e = ErrUnavailable.New(ctx)
	}
	return a, e
}
func pageQuery(ctx context.Context, page, size int) (map[string]string, error) {
	if page < 1 || page > 10000 || size < 1 || size > 100 {
		return nil, ErrInvalid.New(ctx)
	}
	return map[string]string{"page": strconv.Itoa(page), "per_page": strconv.Itoa(size)}, nil
}
func (c *Client) Groups(ctx context.Context, page, size int) (ListPage[Group], error) {
	q, e := pageQuery(ctx, page, size)
	if e != nil {
		return ListPage[Group]{}, e
	}
	q["omit"] = "memberships"
	items := []Group{}
	e = c.get(ctx, "/groups", q, &items)
	return ListPage[Group]{Items: items, Page: page, HasMore: len(items) == size}, e
}
func (c *Client) Chats(ctx context.Context, page, size int) (ListPage[Chat], error) {
	q, e := pageQuery(ctx, page, size)
	if e != nil {
		return ListPage[Chat]{}, e
	}
	items := []Chat{}
	e = c.get(ctx, "/chats", q, &items)
	return ListPage[Chat]{Items: items, Page: page, HasMore: len(items) == size}, e
}
func (c *Client) Group(ctx context.Context, id string) (Group, error) {
	if !ValidID(id) {
		return Group{}, ErrInvalid.New(ctx)
	}
	var g Group
	e := c.get(ctx, "/groups/"+id, nil, &g)
	if e == nil && g.ID != id {
		e = ErrUnavailable.New(ctx)
	}
	return g, e
}
func (c *Client) Messages(ctx context.Context, id, before string, limit int) (MessagePage, error) {
	return c.messages(ctx, id, "before_id", before, limit)
}

// DirectMessages uses the DM counterpart, never a group endpoint.
func (c *Client) DirectMessages(ctx context.Context, id, before string, limit int) (MessagePage, error) {
	if !ValidID(id) || before != "" && !ValidID(before) || limit < 1 || limit > 100 {
		return MessagePage{}, ErrInvalid.New(ctx)
	}
	q := map[string]string{"other_user_id": id, "limit": strconv.Itoa(limit), "acceptFiles": "1"}
	if before != "" {
		q["before_id"] = before
	}
	var body struct {
		Count    int       `json:"count"`
		Messages []Message `json:"direct_messages"`
	}
	err := c.get(ctx, "/direct_messages", q, &body)
	if err != nil {
		return MessagePage{}, err
	}
	if !validMessagePage(body.Messages, before, limit, func(m Message) bool {
		return m.UserID == id || m.RecipientID == id || m.SenderID == id
	}) {
		return MessagePage{}, ErrUnavailable.New(ctx)
	}
	page := MessagePage{Messages: body.Messages, Count: body.Count, HasMore: len(body.Messages) > 0}
	if page.HasMore {
		page.NextBeforeID = page.Messages[len(page.Messages)-1].ID
	}
	return page, err
}

// MessagesAfter uses ascending after_id pagination. since_id returns the newest
// page and can skip messages during a busy interval, so sync never uses it.
func (c *Client) MessagesAfter(ctx context.Context, id, after string, limit int) (MessagePage, error) {
	return c.messages(ctx, id, "after_id", after, limit)
}

func (c *Client) messages(ctx context.Context, id, key, before string, limit int) (MessagePage, error) {
	if !ValidID(id) || before != "" && !ValidID(before) || limit < 1 || limit > 100 {
		return MessagePage{}, ErrInvalid.New(ctx)
	}
	q := map[string]string{"limit": strconv.Itoa(limit), "acceptFiles": "1"}
	if before != "" {
		q[key] = before
	}
	p := MessagePage{Messages: []Message{}}
	e := c.get(ctx, "/groups/"+id+"/messages", q, &p)
	if e != nil {
		return MessagePage{}, e
	}
	if !validMessagePage(p.Messages, before, limit, func(m Message) bool { return m.GroupID == id }) {
		return MessagePage{}, ErrUnavailable.New(ctx)
	}
	// These fields describe this SDK page, never provider-supplied cursor hints.
	p.HasMore, p.NextBeforeID = len(p.Messages) == limit, ""
	if p.HasMore {
		p.NextBeforeID = p.Messages[len(p.Messages)-1].ID
	}
	return p, nil
}

// A page must fit its requested window and exclude its exclusive input cursor.
// IDs are opaque; no numeric or lexical ordering is assumed.
func validMessagePage(messages []Message, cursor string, limit int, belongs func(Message) bool) bool {
	if len(messages) > limit {
		return false
	}
	seen := make(map[string]bool, len(messages))
	for _, message := range messages {
		if !ValidID(message.ID) || message.ID == cursor || seen[message.ID] || !belongs(message) {
			return false
		}
		seen[message.ID] = true
	}
	return true
}

// Search scans a bounded history window. A partial result is never an assertion
// that no matching message exists elsewhere in the group.
type SearchResult struct {
	NextBeforeID string    `json:"next_before_id,omitempty"`
	Messages     []Message `json:"messages"`
	ReadMessages []Message `json:"-"` // Already-fetched pages for the owner's activity cache.
	Inspected    int       `json:"inspected"`
	Complete     bool      `json:"complete"`
}

// Search examines one selected history page. pages must be 1; an older page
// requires a separate explicit call with the returned NextBeforeID.
func (c *Client) Search(ctx context.Context, id, before, query string, pages int) (SearchResult, error) {
	if strings.TrimSpace(query) == "" || len(query) > 256 || pages < 1 || pages > 1 {
		return SearchResult{}, ErrInvalid.New(ctx)
	}
	matches := []Message{}
	read := []Message{}
	inspected := 0
	next := before
	complete := false
	for range pages {
		p, e := c.Messages(ctx, id, next, 100)
		if e != nil {
			return SearchResult{}, e
		}
		inspected += len(p.Messages)
		read = append(read, p.Messages...)
		for _, m := range p.Messages {
			if strings.Contains(strings.ToLower(m.Text), strings.ToLower(query)) {
				matches = append(matches, m)
			}
		}
		next = p.NextBeforeID
		if !p.HasMore {
			complete = true
			break
		}
		if next == before {
			return SearchResult{}, ErrUnavailable.New(ctx)
		}
		before = next
	}
	return SearchResult{Messages: matches, ReadMessages: read, Inspected: inspected, Complete: complete, NextBeforeID: next}, nil
}

// MaxMessageCharacters matches the personal-account API's live validation.
const MaxMessageCharacters = 4000

func ValidateSend(in SendInput) bool {
	if !ValidID(in.SourceGUID) || !utf8.ValidString(in.Text) || utf8.RuneCountInString(in.Text) > MaxMessageCharacters || len(in.Attachments) > 4 || strings.TrimSpace(in.Text) == "" && len(in.Attachments) == 0 {
		return false
	}
	for _, a := range in.Attachments {
		switch a.Type {
		case "image":
			if !validImageURL(a.URL) || !attachmentFields(a, "url") {
				return false
			}
		case "video":
			if !validVideoURL(a.URL) || !validImageURL(a.PreviewURL) || !attachmentFields(a, "url", "preview_url") {
				return false
			}
		case "location":
			lat, e := strconv.ParseFloat(a.Lat, 64)
			lng, e2 := strconv.ParseFloat(a.Lng, 64)
			if e != nil || e2 != nil || !(lat >= -90 && lat <= 90) || !(lng >= -180 && lng <= 180) || strings.TrimSpace(a.Name) == "" || len(a.Name) > 200 || !attachmentFields(a, "lat", "lng", "name") {
				return false
			}
		case "reply":
			if !ValidID(a.ReplyID) || !ValidID(a.BaseReplyID) || !ValidID(a.UserID) || !attachmentFields(a, "reply_id", "base_reply_id", "user_id") {
				return false
			}
		case "file":
			if !ValidID(a.FileID) || !attachmentFields(a, "file_id") {
				return false
			}
		case "mentions":
			boundaries := map[int]bool{0: true}
			textLength := 0
			for _, r := range in.Text {
				textLength++
				if r > 0xFFFF {
					textLength++
				}
				boundaries[textLength] = true
			}
			if len(a.UserIDs) == 0 || len(a.UserIDs) > 100 || len(a.UserIDs) != len(a.Loci) || !attachmentFields(a, "user_ids", "loci") {
				return false
			}
			for i, id := range a.UserIDs {
				if !ValidID(id) || len(a.Loci[i]) != 2 || a.Loci[i][0] < 0 || a.Loci[i][1] < 1 || a.Loci[i][0] > textLength || a.Loci[i][1] > textLength-a.Loci[i][0] || !boundaries[a.Loci[i][0]] || !boundaries[a.Loci[i][0]+a.Loci[i][1]] {
					return false
				}
			}
		default:
			return false
		}
	}
	return utf8.RuneCountInString(CanonicalSendText(in)) <= MaxMessageCharacters
}

// CanonicalSendText models only the observed GroupMe video transform: each
// validated outgoing video URL is appended with one ASCII space, in order.
// An empty caption becomes the URL alone, as the native receipt confirms.
// Callers must ValidateSend before using this exact receipt comparison.
func CanonicalSendText(in SendInput) string {
	text := in.Text
	for _, a := range in.Attachments {
		if a.Type == "video" && validVideoURL(a.URL) {
			if text == "" {
				text = a.URL
			} else {
				text += " " + a.URL
			}
		}
	}
	return text
}

func validVideoURL(raw string) bool {
	return MediaURL(raw) != "" && (strings.HasPrefix(raw, "https://m.groupme.com/uploads/") || strings.HasPrefix(raw, "https://v.groupme.com/")) && strings.HasSuffix(raw, ".mp4")
}

// Legacy i.groupme.com images can be extensionless. Upload and legacy video
// origins require an image extension so a video cannot become its own preview.
func validImageURL(raw string) bool {
	if MediaURL(raw) == "" {
		return false
	}
	if strings.HasPrefix(raw, "https://i.groupme.com/") {
		imageURL, err := url.Parse(raw)
		return err == nil && !strings.HasSuffix(strings.ToLower(imageURL.Path), ".mp4")
	}
	if !strings.HasPrefix(raw, "https://m.groupme.com/uploads/") && !strings.HasPrefix(raw, "https://v.groupme.com/") {
		return false
	}
	for _, extension := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		if strings.HasSuffix(strings.ToLower(raw), extension) {
			return true
		}
	}
	return false
}

func attachmentFields(a Attachment, allowed ...string) bool {
	data, err := json.Marshal(a)
	if err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return false
	}
	keys := map[string]bool{"type": true}
	for _, k := range allowed {
		keys[k] = true
	}
	for k := range fields {
		if !keys[k] {
			return false
		}
	}
	return true
}
func (c *Client) Send(ctx context.Context, id string, in SendInput) (Message, error) {
	if !ValidID(id) || !ValidateSend(in) {
		return Message{}, ErrInvalid.New(ctx)
	}
	if !ValidToken(c.token) {
		return Message{}, ErrAuthentication.New(ctx)
	}
	var doc envelope
	body := struct {
		Message SendInput `json:"message"`
	}{in}
	if e := c.api.POST("/groups/"+id+"/messages").Header("X-Access-Token", c.token).Body(&body).Do(ctx, &doc); e != nil {
		return Message{}, failure(ctx, e, true)
	}
	var out struct {
		Message Message `json:"message"`
	}
	if doc.Meta.Code != 201 || json.Unmarshal(doc.Response, &out) != nil || !ValidID(out.Message.ID) || out.Message.GroupID != id || out.Message.SourceGUID != in.SourceGUID {
		return Message{}, ErrUncertain.New(ctx)
	}
	return out.Message, nil
}

package groupme

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFeatureMutationWireContracts(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
		assert.Equal(t, "SyntheticBrowser/1.0", r.Header.Get("User-Agent"))
		assert.Empty(t, r.URL.RawQuery)
		methods = append(methods, r.Method+" "+r.URL.Path)
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		switch r.URL.Path {
		case "/messages/g1/m1/like":
			assert.JSONEq(t, `{"like_icon":{"type":"unicode","code":"👍"}}`, string(body))
		case "/messages/g1/m1/unlike", "/conversations/g1/messages/m1/pin", "/conversations/g1/messages/m1/unpin":
			assert.Empty(t, body)
		case "/v4/groups/g1/messages/m1":
			assert.Equal(t, http.MethodPut, r.Method)
			assert.JSONEq(t, `{"text":"edited","attachments":[]}`, string(body))
			writeFixture(t, w, `{"meta":{"code":200},"response":{"message":{"id":"m1","group_id":"g1","text":"edited"}}}`)
			return
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		writeFixture(t, w, `{"meta":{"code":200},"response":null}`)
	}))
	defer server.Close()
	client, err := newClient(server.URL, "fixture-token").WithBrowserUserAgent("SyntheticBrowser/1.0")
	require.NoError(t, err)
	require.NoError(t, client.React(t.Context(), "g1", "m1", ReactionInput{Type: "unicode", Code: "👍"}))
	require.NoError(t, client.Unlike(t.Context(), "g1", "m1"))
	require.NoError(t, client.PinMessage(t.Context(), "g1", "m1"))
	require.NoError(t, client.UnpinMessage(t.Context(), "g1", "m1"))
	message, err := client.EditMessage(t.Context(), "g1", "m1", EditInput{Text: "edited"})
	require.NoError(t, err)
	require.Equal(t, "m1", message.ID)
	require.Len(t, methods, 5)
}

func TestPollActualMetaCodesAndCanonicalWrappers(t *testing.T) {
	var created, voted bool
	poll := `{"data":{"id":"p1","conversation_id":"g1","type":"multi","subject":"fixture","options":[{"id":"1","title":"yes","votes":1,"voter_ids":["u1"]}]},"user_votes":["1"]}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/poll/g1" && r.Method == http.MethodGet:
			writeFixture(t, w, `{"meta":{"code":20000},"response":{"polls":[`+poll+`],"continuation_token":"more"}}`)
		case r.URL.Path == "/poll/g1" && r.Method == http.MethodPost:
			created = true
			var in CreatePollInput
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&in))
			assert.Equal(t, "multi", in.Type)
			assert.Equal(t, "anonymous", in.Visibility)
			writeFixture(t, w, `{"meta":{"code":20100},"response":{"poll":`+poll+`,"message":{"id":"m1","group_id":"g1"}}}`)
		case r.URL.Path == "/poll/g1/p1":
			if r.Method == http.MethodPost {
				voted = true
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.JSONEq(t, `{"votes":["1"]}`, string(body))
			}
			writeFixture(t, w, `{"meta":{"code":20000},"response":{"poll":`+poll+`}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := newClient(server.URL, "fixture-token")
	list, err := c.Polls(t.Context(), "g1")
	require.NoError(t, err)
	require.Empty(t, list.ContinuationToken)
	require.Len(t, list.Items, 1)
	require.Equal(t, []string{"1"}, list.Items[0].UserVotes)
	require.Equal(t, 1, list.Items[0].Options[0].Votes)
	require.Equal(t, []string{"u1"}, list.Items[0].Options[0].VoterIDs)
	_, err = c.Poll(t.Context(), "g1", "p1")
	require.NoError(t, err)
	result, err := c.CreatePoll(t.Context(), "g1", CreatePollInput{Subject: "fixture", Options: []PollOptionInput{{Title: "yes"}, {Title: "no"}}, Expiration: 2000000000, Visibility: "anonymous", Type: "multi"})
	require.NoError(t, err)
	require.Equal(t, "p1", result.Poll.ID)
	require.Equal(t, "m1", result.Message.ID)
	value, err := c.VotePoll(t.Context(), "g1", "p1", []string{"1"})
	require.NoError(t, err)
	require.Equal(t, []string{"1"}, value.UserVotes)
	require.True(t, created)
	require.True(t, voted)
}

func TestUnknownPollMetaCodeIsNotSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeFixture(t, w, `{"meta":{"code":20001},"response":{"poll":{"data":{"id":"p1","conversation_id":"g1"}}}}`)
	}))
	defer server.Close()
	_, err := newClient(server.URL, "fixture-token").Poll(t.Context(), "g1", "p1")
	require.Error(t, err)
}

func TestFeatureMutationsNeverRetryOrExposeProviderData(t *testing.T) {
	for _, operation := range []string{"react", "unlike", "pin", "unpin", "edit", "poll", "vote", "event", "album"} {
		t.Run(operation, func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if operation == "vote" && r.Method == http.MethodGet {
					writeFixture(t, w, `{"meta":{"code":20000},"response":{"poll":{"data":{"id":"p1","conversation_id":"g1","type":"multi","options":[{"id":"1"}]}}}}`)
					return
				}
				calls.Add(1)
				w.Header().Set("Location", "https://external.invalid/SECRET")
				w.WriteHeader(503)
				writeFixture(t, w, `{"private":"SECRET fixture-token private message"}`)
			}))
			defer s.Close()
			c := newClient(s.URL, "fixture-token")
			var err error
			switch operation {
			case "react":
				err = c.React(t.Context(), "g1", "m1", ReactionInput{Type: "unicode", Code: "👍"})
			case "unlike":
				err = c.Unlike(t.Context(), "g1", "m1")
			case "pin":
				err = c.PinMessage(t.Context(), "g1", "m1")
			case "unpin":
				err = c.UnpinMessage(t.Context(), "g1", "m1")
			case "edit":
				_, err = c.EditMessage(t.Context(), "g1", "m1", EditInput{Text: "fixture"})
			case "poll":
				_, err = c.CreatePoll(t.Context(), "g1", CreatePollInput{Subject: "fixture", Options: []PollOptionInput{{Title: "a"}, {Title: "b"}}, Expiration: 2000000000, Visibility: "public", Type: "single"})
			case "vote":
				_, err = c.VotePoll(t.Context(), "g1", "p1", []string{"1"})
			case "event":
				_, err = c.CreateEvent(t.Context(), "g1", CreateEventInput{Name: "fixture", StartAt: "2030-01-01T12:00:00Z", EndAt: "2030-01-01T13:00:00Z", Timezone: "UTC"})
			case "album":
				_, err = c.CreateAlbum(t.Context(), "g1", CreateAlbumInput{Title: "fixture"})
			}
			require.Error(t, err)
			require.True(t, ErrUncertain.Is(err))
			require.EqualValues(t, 1, calls.Load())
			require.NotContains(t, err.Error(), "SECRET")
			require.NotContains(t, err.Error(), "fixture-token")
			require.NotContains(t, err.Error(), "private message")
		})
	}
}

func TestAlbumsGalleryAndExplicitPaginationBoundary(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/conversations/g1/albums":
			assert.Equal(t, "50", r.URL.Query().Get("per_page"))
			writeFixture(t, w, `{"meta":{"code":200},"response":{"albums":[{"album_id":"a1","conversation_id":"g1"}],"next_cursor":"more-albums"}}`)
		case "/conversations/g1/albums/a1/media":
			assert.Equal(t, "6", r.URL.Query().Get("per_page"))
			assert.Empty(t, r.URL.Query().Get("cursor"))
			if page := r.URL.Query().Get("page"); page != "" {
				assert.Equal(t, "more-media", page)
				writeFixture(t, w, `{"meta":{"code":200},"response":{"album":{"album_id":"a1","conversation_id":"g1","attachments":[]},"next_cursor":""}}`)
				return
			}
			writeFixture(t, w, `{"meta":{"code":200},"response":{"album":{"album_id":"a1","conversation_id":"g1","attachments":[{"media_id":"media1","media_type":"image","media_url":"https://m.groupme.com/uploads/fixture.png","conversation_id":"g1","album_id":"a1"}]},"next_cursor":"more-media"}}`)
		case "/conversations/g1/gallery":
			assert.Equal(t, "1", r.URL.Query().Get("acceptFiles"))
			assert.Equal(t, "100", r.URL.Query().Get("limit"))
			writeFixture(t, w, `{"meta":{"code":200},"response":{"messages":[{"id":"m1","group_id":"g1","attachments":[{"type":"file","file_id":"f1"}]}]}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer s.Close()
	c := newClient(s.URL, "fixture-token")
	albums, err := c.Albums(t.Context(), "g1")
	require.NoError(t, err)
	require.Empty(t, albums.NextCursor)
	media, err := c.AlbumMedia(t.Context(), "g1", "a1", "", 6)
	require.NoError(t, err)
	require.Equal(t, "more-media", media.NextCursor)
	require.Equal(t, "media1", media.Items[0].MediaID)
	older, err := c.AlbumMedia(t.Context(), "g1", "a1", "more-media", 6)
	require.NoError(t, err)
	require.Empty(t, older.NextCursor)
	gallery, err := c.Gallery(t.Context(), "g1")
	require.NoError(t, err)
	require.Equal(t, "f1", gallery.Messages[0].Attachments[0].FileID)
	require.EqualValues(t, 4, calls.Load())
}

func TestInvalidFeatureInputDoesNotReachProvider(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer s.Close()
	c := newClient(s.URL, "fixture-token")
	for _, id := range []string{"../g1", "g1/path", "g1?token=secret", "g1+g2+g3", ""} {
		require.Error(t, c.PinMessage(t.Context(), id, "m1"))
		_, err := c.Poll(t.Context(), id, "p1")
		require.Error(t, err)
	}
	for _, ua := range []string{"", strings.Repeat("a", 513), "browser\r\nX-Access-Token: secret", "浏览器"} {
		_, err := c.WithBrowserUserAgent(ua)
		require.Error(t, err)
	}
	_, err := c.CreateEvent(t.Context(), "g1", CreateEventInput{Name: "fixture", StartAt: "bad", EndAt: "bad", Timezone: "UTC"})
	require.Error(t, err)
	_, err = c.CreateAlbum(t.Context(), "g1", CreateAlbumInput{Title: "fixture", CoverAttachmentID: "unverified"})
	require.Error(t, err)
	require.Zero(t, calls.Load())
}

type featureTransport func(*http.Request) (*http.Response, error)

func (f featureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func featureResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestPNGUploadUsesBoundCapabilityAndNeverForwardsCredentials(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))))
	payload := append([]byte(nil), encoded.Bytes()...)
	c := New("fixture-token")
	var calls int
	c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		switch r.URL.Host {
		case "m.groupme.com":
			require.Equal(t, "/uploads", r.URL.Path)
			require.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
			var in InitializeUploadInput
			require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
			require.Equal(t, "g1", in.GroupID)
			require.Equal(t, "u1", in.SenderID)
			require.Equal(t, 2, in.Width)
			require.Equal(t, 2, in.Height)
			require.Equal(t, int64(len(payload)), in.FileSize)
			return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/original.png?sig=synthetic&sp=w","renderUrl":"https://m.groupme.com/uploads/fixture/original.png","thumbnailUrl":"https://m.groupme.com/uploads/fixture/thumb.png","transcriptUrl":""}`), nil
		case "cdn2.groupme.com":
			require.Equal(t, http.MethodPut, r.Method)
			require.Empty(t, r.Header.Get("X-Access-Token"))
			require.Empty(t, r.Header.Get("Authorization"))
			require.Equal(t, "BlockBlob", r.Header.Get("x-ms-blob-type"))
			require.Equal(t, "image/png", r.Header.Get("Content-Type"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, payload, body)
			return featureResponse(201, ""), nil
		default:
			t.Fatalf("unexpected upload host %s", r.URL.Host)
			return nil, nil
		}
	})
	attachment, err := c.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: bytes.NewReader(payload), Name: "fixture.png", ContentType: "image/png", Size: int64(len(payload))})
	require.NoError(t, err)
	require.Equal(t, "image", attachment.Type)
	require.Equal(t, "https://m.groupme.com/uploads/fixture/original.png", attachment.URL)
	require.Equal(t, 2, calls)
	err = c.Upload(t.Context(), UploadTarget{uploadURL: "https://cdn2.groupme.com/uploads/fixture/original.png?sig=synthetic"}, "image/png", bytes.NewReader(payload), int64(len(payload)))
	require.Error(t, err)
	require.Equal(t, 2, calls, "an arbitrary constructed target is not an initialized capability")
}

func TestMediaAndCompositionValidation(t *testing.T) {
	for _, raw := range []string{"https://i.groupme.com/fixture.png", "https://v.groupme.com/g1/fixture.mp4", "https://m.groupme.com/uploads/fixture/original.png"} {
		require.Equal(t, raw, MediaURL(raw))
	}
	for _, raw := range []string{"http://i.groupme.com/x", "https://m.groupme.com/private/x", "https://m.groupme.com:443/uploads/x", "https://m.groupme.com.evil.invalid/uploads/x", "https://u@i.groupme.com/x", "https://i.groupme.com/x?sig=secret", "https://m.groupme.com/uploads/%2e%2e/x"} {
		require.Empty(t, MediaURL(raw))
	}
	require.Empty(t, ImageURL("https://m.groupme.com/uploads/fixture.png"))
	for _, a := range []Attachment{{Type: "reply", ReplyID: "m1", BaseReplyID: "m0", UserID: "u1"}, {Type: "file", FileID: "file1"}, {Type: "image", URL: "https://m.groupme.com/uploads/fixture.png"}, {Type: "mentions", UserIDs: []string{"u1"}, Loci: [][]int{{0, 2}}}} {
		require.True(t, ValidateSend(SendInput{SourceGUID: "op1", Text: "🙂", Attachments: []Attachment{a}}))
	}
	require.False(t, ValidateSend(SendInput{SourceGUID: "op1", Text: "🙂", Attachments: []Attachment{{Type: "mentions", UserIDs: []string{"u1"}, Loci: [][]int{{0, 3}}}}}))
	require.False(t, ValidateSend(SendInput{SourceGUID: "op1", Text: "fixture", Attachments: []Attachment{{Type: "reply", ReplyID: "m1", BaseReplyID: "m0", UserID: "u1", URL: "https://i.groupme.com/extra.png"}}}))
}

func TestEventsWindowCreateAndRSVPReadback(t *testing.T) {
	var mutations int
	going := true
	event := `{"event_id":"event1","conversation_id":"g1","name":"fixture","start_at":"2030-01-01T12:00:00Z","end_at":"2030-01-01T13:00:00Z","timezone":"UTC","going":["u1"],"going_count":1}`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations/g1/events/list":
			assert.Equal(t, "100", r.URL.Query().Get("limit"))
			assert.NotEmpty(t, r.URL.Query().Get("end_at"))
			writeFixture(t, w, `{"meta":{"code":200},"response":{"events":[`+event+`]}}`)
		case "/conversations/g1/events/create":
			mutations++
			var in CreateEventInput
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&in))
			assert.NotNil(t, in.Reminders)
			assert.Empty(t, in.Reminders)
			writeFixture(t, w, `{"meta":{"code":200},"response":{"event":`+event+`,"message":{"id":"m1","group_id":"g1"}}}`)
		case "/conversations/g1/events/rsvp":
			mutations++
			assert.Equal(t, "event1", r.URL.Query().Get("event_id"))
			going = r.URL.Query().Get("going") == "true"
			assert.Contains(t, []string{"true", "false"}, r.URL.Query().Get("going"))
			body, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Empty(t, body)
			writeFixture(t, w, `{"meta":{"code":200},"response":null}`)
		case "/conversations/g1/events/show":
			assert.Equal(t, "event1", r.URL.Query().Get("event_id"))
			canonical := event
			if !going {
				canonical = strings.Replace(canonical, `"going":["u1"],"going_count":1`, `"going":[],"not_going":["u1"],"going_count":0`, 1)
			}
			writeFixture(t, w, `{"meta":{"code":200},"response":{"event":`+canonical+`}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer s.Close()
	c := newClient(s.URL, "fixture-token")
	events, err := c.Events(t.Context(), "g1")
	require.NoError(t, err)
	require.Len(t, events, 1)
	result, err := c.CreateEvent(t.Context(), "g1", CreateEventInput{Name: "fixture", StartAt: "2030-01-01T12:00:00Z", EndAt: "2030-01-01T13:00:00Z", Timezone: "UTC"})
	require.NoError(t, err)
	require.Equal(t, "event1", result.Event.EventID)
	canonical, err := c.RSVP(t.Context(), "g1", "event1", true)
	require.NoError(t, err)
	require.Equal(t, []string{"u1"}, canonical.Going)
	canonical, err = c.RSVP(t.Context(), "g1", "event1", false)
	require.NoError(t, err)
	require.Equal(t, []string{"u1"}, canonical.NotGoing)
	require.Equal(t, 3, mutations)
}

func TestNativeMessageMetadataDecodesWithoutLosingActions(t *testing.T) {
	var message Message
	require.NoError(t, json.Unmarshal([]byte(`{"id":"m1","conversation_id":"u1+u2","updated_at":100,"pinned_at":90,"pinned_by":"u1","hidden":true,"favorited_by":["u1"],"reactions":[{"type":"unicode","code":"👍","user_ids":["u1"]}],"event":{"type":"poll.created","data":{"poll":{"id":"p1"}}},"attachments":[{"type":"reply","reply_id":"m0","base_reply_id":"base0","user_id":"u2"},{"type":"file","file_id":"f1"},{"type":"video","url":"https://v.groupme.com/g1/fixture.mp4","preview_url":"https://v.groupme.com/g1/preview.jpeg"},{"type":"emoji","placeholder":"☃","charmap":[[0,1]]}]}`), &message))
	require.Equal(t, "u1+u2", message.ConversationID)
	require.EqualValues(t, 100, message.UpdatedAt)
	require.EqualValues(t, 90, *message.PinnedAt)
	require.True(t, message.Hidden)
	require.Equal(t, "👍", message.Reactions[0].Code)
	require.Equal(t, "poll.created", message.Event.Type)
	require.Equal(t, "base0", message.Attachments[0].BaseReplyID)
	require.Equal(t, "f1", message.Attachments[1].FileID)
	require.NotEmpty(t, message.Attachments[2].PreviewURL)
	require.Equal(t, [][]int{{0, 1}}, message.Attachments[3].Charmap)
}

func TestHistoryRequestsAdmitFilesAndScopeBrowserUserAgent(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/me":
			assert.NotEqual(t, "SyntheticBrowser/1.0", r.Header.Get("User-Agent"))
			writeFixture(t, w, `{"meta":{"code":200},"response":{"id":"u1"}}`)
		case "/groups/g1/messages":
			assert.Equal(t, "1", r.URL.Query().Get("acceptFiles"))
			assert.Equal(t, "SyntheticBrowser/1.0", r.Header.Get("User-Agent"))
			writeFixture(t, w, `{"meta":{"code":200},"response":{"messages":[{"id":"m1","group_id":"g1","attachments":[{"type":"file","file_id":"f1"}]}]}}`)
		case "/direct_messages":
			assert.Equal(t, "1", r.URL.Query().Get("acceptFiles"))
			assert.Equal(t, "SyntheticBrowser/1.0", r.Header.Get("User-Agent"))
			writeFixture(t, w, `{"meta":{"code":200},"response":{"direct_messages":[{"id":"m2","recipient_id":"u2","conversation_id":"u1+u2"}]}}`)
		default:
			t.Errorf("unexpected route %s", r.URL.Path)
		}
	}))
	defer s.Close()
	c, err := newClient(s.URL, "fixture-token").WithBrowserUserAgent("SyntheticBrowser/1.0")
	require.NoError(t, err)
	_, err = c.Me(t.Context())
	require.NoError(t, err)
	page, err := c.Messages(t.Context(), "g1", "", 100)
	require.NoError(t, err)
	require.Equal(t, "f1", page.Messages[0].Attachments[0].FileID)
	page, err = c.DirectMessages(t.Context(), "u2", "", 100)
	require.NoError(t, err)
	require.Equal(t, "u1+u2", page.Messages[0].ConversationID)
}

func TestUploadCapabilityDoesNotSerializeOrRedirect(t *testing.T) {
	c := New("fixture-token")
	var calls int
	c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "m.groupme.com" {
			return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/original.png?sig=SECRET","renderUrl":"https://m.groupme.com/uploads/fixture/original.png","thumbnailUrl":"","transcriptUrl":""}`), nil
		}
		require.Equal(t, "cdn2.groupme.com", r.URL.Host)
		require.Empty(t, r.Header.Get("X-Access-Token"))
		res := featureResponse(302, `private SECRET`)
		res.Header.Set("Location", "https://external.invalid/SECRET")
		return res, nil
	})
	target, err := c.InitializeUpload(t.Context(), InitializeUploadInput{Extension: "png", SenderID: "u1", GroupID: "g1", FileSize: 3, Width: 1, Height: 1})
	require.NoError(t, err)
	encoded, err := json.Marshal(target)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "SECRET")
	require.NotContains(t, fmt.Sprint(target), "SECRET")
	for _, format := range []string{"%v", "%+v", "%#v"} {
		t.Run("format/"+format, func(t *testing.T) {
			for _, value := range []any{target, &target} {
				require.NotContains(t, fmt.Sprintf(format, value), "SECRET")
				require.NotContains(t, fmt.Sprintf(format, value), "cdn2.groupme.com")
			}
		})
	}
	t.Run("gob", func(t *testing.T) {
		var encoded bytes.Buffer
		require.NoError(t, gob.NewEncoder(&encoded).Encode(target))
		require.NotContains(t, encoded.String(), "SECRET")
		require.NotContains(t, encoded.String(), "cdn2.groupme.com")
		var restored UploadTarget
		require.NoError(t, gob.NewDecoder(&encoded).Decode(&restored))
		require.Equal(t, target.RenderURL, restored.RenderURL)
		uploadErr := c.Upload(t.Context(), restored, "image/png", strings.NewReader("abc"), 3)
		require.True(t, ErrInvalid.Is(uploadErr), "serialized public metadata must not recreate an upload capability")
		require.Equal(t, 1, calls, "a deserialized target must fail before transport")
	})
	err = c.Upload(t.Context(), target, "image/png", strings.NewReader("abc"), 3)
	require.True(t, ErrUncertain.Is(err))
	require.NotContains(t, err.Error(), "SECRET")
	require.Equal(t, 2, calls, "redirect must not contact an external host")
}

func TestAddAlbumMediaUsesVerifiedArrayContract(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/conversations/g1/albums/media", r.URL.Path)
		assert.Equal(t, "a1", r.URL.Query().Get("album_id"))
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.JSONEq(t, `[{"media_url":"https://m.groupme.com/uploads/fixture/original.png","media_type":"image","media_source":"album"}]`, string(body))
		writeFixture(t, w, `{"meta":{"code":200},"response":[{"media_id":"media1","media_type":"image","media_url":"https://m.groupme.com/uploads/fixture/original.png","conversation_id":"g1","album_id":"a1"}]}`)
	}))
	defer s.Close()
	out, err := newClient(s.URL, "fixture-token").AddAlbumMedia(t.Context(), "g1", "a1", []AlbumMediaInput{{MediaURL: "https://m.groupme.com/uploads/fixture/original.png", MediaType: "image", MediaSource: "album"}})
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "media1", out[0].MediaID)
}

func TestAlbumMediaRejectsForeignScopeAndUnsafeURLs(t *testing.T) {
	for _, mutate := range []func(*AlbumMedia){
		func(v *AlbumMedia) { v.ConversationID = "other" }, func(v *AlbumMedia) { v.AlbumID = "other" },
		func(v *AlbumMedia) { v.MediaID = "../bad" }, func(v *AlbumMedia) { v.MediaURL = "https://evil.example/file" },
		func(v *AlbumMedia) { v.PreviewURL = "https://evil.example/preview" },
	} {
		item := AlbumMedia{MediaID: "media1", ConversationID: "g1", AlbumID: "a1", MediaURL: "https://m.groupme.com/uploads/fixture.png"}
		mutate(&item)
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			response := any(map[string]any{"album": map[string]any{"album_id": "a1", "conversation_id": "g1", "attachments": []AlbumMedia{item}}})
			if r.Method == http.MethodPost {
				response = []AlbumMedia{item}
			}
			assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"meta": map[string]int{"code": 200}, "response": response}))
		}))
		c := newClient(s.URL, "fixture-token")
		page, err := c.AlbumMedia(t.Context(), "g1", "a1", "", 6)
		require.True(t, ErrUnavailable.Is(err))
		require.Empty(t, page.Items)
		items, err := c.AddAlbumMedia(t.Context(), "g1", "a1", []AlbumMediaInput{{MediaURL: "https://m.groupme.com/uploads/fixture.png", MediaType: "image", MediaSource: "album"}})
		require.True(t, ErrUncertain.Is(err))
		require.Empty(t, items)
		s.Close()
	}
}

func TestEventLocationAndReminderSeconds(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in CreateEventInput
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		assert.Equal(t, []int{300, 3600}, in.Reminders)
		assert.Equal(t, "Public place", in.Location.Name)
		writeFixture(t, w, `{"meta":{"code":201},"response":{"event":{"event_id":"e1","conversation_id":"g1","location":{"name":"Public place"},"reminders":[300,3600]},"message":{"id":"m1","group_id":"g1"}}}`)
	}))
	defer s.Close()
	c := newClient(s.URL, "fixture-token")
	in := CreateEventInput{Name: "fixture", StartAt: "2030-01-01T12:00:00Z", EndAt: "2030-01-01T13:00:00Z", Timezone: "UTC", Location: &EventLocation{Name: "Public place"}, Reminders: []int{300, 3600}}
	out, err := c.CreateEvent(t.Context(), "g1", in)
	require.NoError(t, err)
	require.Equal(t, []int{300, 3600}, out.Event.Reminders)
	for _, reminders := range [][]int{{5}, {300, 300}, {0, 300, 900}, {-300}} {
		in.Reminders = reminders
		_, err = c.CreateEvent(t.Context(), "g1", in)
		require.True(t, ErrInvalid.Is(err))
	}
	require.EqualValues(t, 1, calls.Load())
}

func TestRawFileUploadStatusAndHeaderDownload(t *testing.T) {
	c := New("fixture-token")
	var calls int
	c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "file.groupme.com", r.URL.Host)
		require.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
		require.Empty(t, r.URL.Query().Get("access_token"))
		switch {
		case r.Method == http.MethodPost:
			require.Equal(t, "/v1/g1/files", r.URL.Path)
			require.Equal(t, "fixture.txt", r.URL.Query().Get("name"))
			require.Equal(t, "text/plain", r.Header.Get("Content-Type"))
			require.EqualValues(t, 7, r.ContentLength)
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, "fixture", string(body))
			return featureResponse(201, `{"status_url":"https://file.groupme.com/v1/g1/uploadStatus?job=job1"}`), nil
		case strings.HasSuffix(r.URL.Path, "uploadStatus"):
			require.Equal(t, "job1", r.URL.Query().Get("job"))
			require.Equal(t, "0", r.URL.Query().Get("cnt"))
			return featureResponse(200, `{"status":"completed","file_id":"file1"}`), nil
		default:
			require.Equal(t, "/v1/g1/files/file1", r.URL.Path)
			require.Empty(t, r.URL.RawQuery)
			resp := featureResponse(200, "fixture")
			resp.ContentLength = 7
			resp.Header.Set("Content-Type", "text/plain; charset=utf-8")
			resp.Header.Set("Content-Disposition", `attachment; filename="fixture.txt"`)
			return resp, nil
		}
	})
	job, err := c.UploadFile(t.Context(), "g1", UploadInput{Data: strings.NewReader("fixture"), Name: "fixture.txt", ContentType: "text/plain", Size: 7})
	require.NoError(t, err)
	require.Equal(t, "job1", job.JobID)
	completed, err := c.FileUploadStatus(t.Context(), "g1", job.JobID)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.Status)
	require.Equal(t, "file1", completed.FileID)
	var dst bytes.Buffer
	info, err := c.DownloadFile(t.Context(), "g1", "file1", &dst)
	require.NoError(t, err)
	require.Equal(t, "fixture", dst.String())
	require.Equal(t, FileInfo{FileID: "file1", Name: "fixture.txt", ContentType: "text/plain", Size: 7}, info)
	require.Equal(t, 3, calls)
}

func TestFileStatusReceiptCannotForwardCredentialsOrChangeScope(t *testing.T) {
	for _, raw := range []string{
		"http://file.groupme.com/v1/g1/uploadStatus?job=j1", "https://evil.example/v1/g1/uploadStatus?job=j1",
		"https://file.groupme.com/v1/other/uploadStatus?job=j1", "https://file.groupme.com/v1/g1/uploadStatus?job=j1&token=private",
		"https://file.groupme.com/v1/g1/uploadStatus?job=j1&job=j2", "https://file.groupme.com/v1/g1/uploadStatus?job=../bad",
		"https://user:secret@file.groupme.com/v1/g1/uploadStatus?job=j1", "https://file.groupme.com/v1/g1/uploadStatus?job=j1#frag",
	} {
		require.Empty(t, fileStatusJob(raw, "g1"))
	}
	c := New("fixture-token")
	var calls int
	c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		resp := featureResponse(302, "")
		resp.Header.Set("Location", "https://evil.example/leak")
		return resp, nil
	})
	_, err := c.UploadFile(t.Context(), "g1", UploadInput{Data: strings.NewReader("fixture"), Name: "fixture.txt", ContentType: "text/plain", Size: 7})
	require.True(t, ErrUncertain.Is(err))
	require.Equal(t, 1, calls)
	_, err = c.DownloadFile(t.Context(), "g1", "file1", io.Discard)
	require.True(t, ErrUnavailable.Is(err))
	require.Equal(t, 2, calls)
}

func TestFileDownloadRejectsOversizeAndShortRead(t *testing.T) {
	for _, size := range []int64{MaxUploadBytes + 1, 10} {
		c := New("fixture-token")
		c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
			resp := featureResponse(200, "fixture")
			resp.ContentLength = size
			return resp, nil
		})
		var dst bytes.Buffer
		_, err := c.DownloadFile(t.Context(), "g1", "file1", &dst)
		require.True(t, ErrUnavailable.Is(err))
		if size > MaxUploadBytes {
			require.Empty(t, dst.String())
		}
	}
}

func TestStandardImageUploadsPreserveBytesAndBindMIME(t *testing.T) {
	for _, kind := range []string{"image/jpeg", "image/gif", "image/webp"} {
		t.Run(kind, func(t *testing.T) {
			var data bytes.Buffer
			fixture := image.NewRGBA(image.Rect(0, 0, 2, 3))
			switch kind {
			case "image/jpeg":
				require.NoError(t, jpeg.Encode(&data, fixture, nil))
			case "image/gif":
				require.NoError(t, gif.Encode(&data, fixture, nil))
			case "image/webp":
				header := make([]byte, 30)
				copy(header, "RIFF")
				copy(header[8:], "WEBPVP8X")
				header[16] = 10
				header[24] = 1
				header[27] = 2
				data.Write(header)
			}
			payload := append([]byte{}, data.Bytes()...)
			c := New("fixture-token")
			calls := 0
			c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host == "m.groupme.com" {
					var in InitializeUploadInput
					require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
					require.Equal(t, imageExtension(kind), in.Extension)
					require.Equal(t, 2, in.Width)
					require.Equal(t, 3, in.Height)
					return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/original?sig=synthetic","renderUrl":"https://m.groupme.com/uploads/fixture/original.`+in.Extension+`"}`), nil
				}
				require.Equal(t, kind, r.Header.Get("Content-Type"))
				require.Empty(t, r.Header.Get("X-Access-Token"))
				b, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, payload, b)
				return featureResponse(201, ""), nil
			})
			out, err := c.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: bytes.NewReader(payload), Name: "fixture", ContentType: kind, Size: int64(len(payload))})
			require.NoError(t, err)
			require.Equal(t, "image", out.Type)
			require.True(t, ValidateSend(SendInput{SourceGUID: "intent", Attachments: []Attachment{out}}))
			require.Equal(t, 2, calls)
		})
	}
}

func TestFileHeaderSniffingKeepsActiveContentDownloadable(t *testing.T) {
	require.Equal(t, "application/pdf", safeFileContentType("application/pdf", []byte("%PDF-1.7\nfixture")))
	require.Equal(t, "application/octet-stream", safeFileContentType("text/html", []byte("<!DOCTYPE html><html>fixture</html>")))
	require.Equal(t, "application/octet-stream", safeFileContentType("text/plain", []byte("<!DOCTYPE html><html>fixture</html>")))
	require.Empty(t, safeFileContentType("text/plain\r\nX-Injected: yes", []byte("fixture")))
	require.Empty(t, safeFileContentType("image/jpeg", []byte("fixture")))
	for _, b := range [][]byte{nil, []byte("RIFF"), make([]byte, 30)} {
		w, h := webpDimensions(b)
		require.Zero(t, w)
		require.Zero(t, h)
	}
}

func TestVideoUploadAndExactCanonicalCaptionBoundary(t *testing.T) {
	box := func(kind string, payload []byte) []byte {
		out := make([]byte, 8+len(payload))
		length := len(out)
		if length > math.MaxUint32 {
			t.Fatal("MP4 test fixture exceeds its32-bit box size")
			return nil
		}
		binary.BigEndian.PutUint32(out, uint32(length))
		copy(out[4:], kind)
		copy(out[8:], payload)
		return out
	}
	header := make([]byte, 84)
	binary.BigEndian.PutUint32(header[76:], 320<<16)
	binary.BigEndian.PutUint32(header[80:], 180<<16)
	payload := append(box("ftyp", []byte("isom\x00\x00\x00\x00isom")), box("moov", box("trak", box("tkhd", header)))...)
	c := New("fixture-token")
	calls := 0
	c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "m.groupme.com" {
			var in InitializeUploadInput
			require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
			require.Equal(t, "mp4", in.Extension)
			require.Equal(t, 320, in.Width)
			require.Equal(t, 180, in.Height)
			require.Equal(t, "u1+u2", in.GroupID)
			return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/video.mp4?sig=synthetic","renderUrl":"https://m.groupme.com/uploads/fixture/320x180.original.mp4","thumbnailUrl":"https://m.groupme.com/uploads/fixture/320x180.thumbnail.jpg","transcriptUrl":null}`), nil
		}
		require.Equal(t, "video/mp4", r.Header.Get("Content-Type"))
		require.Empty(t, r.Header.Get("X-Access-Token"))
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, payload, data)
		return featureResponse(201, ""), nil
	})
	out, err := c.UploadAttachment(t.Context(), "u1+u2", "u1", UploadInput{Data: bytes.NewReader(payload), Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(payload))})
	require.NoError(t, err)
	require.Equal(t, "video", out.Type)
	require.Equal(t, 2, calls)
	in := SendInput{SourceGUID: "fixture-guid", Text: "caption", Attachments: []Attachment{out}}
	require.Equal(t, "caption "+out.URL, CanonicalSendText(in))
	require.True(t, ValidateSend(in))
	in.Text = ""
	require.Equal(t, out.URL, CanonicalSendText(in))
	require.True(t, ValidateSend(in))
	overhead := 1 + len(out.URL)
	in.Text = strings.Repeat("x", MaxMessageCharacters-overhead)
	require.True(t, ValidateSend(in))
	in.Text += "x"
	require.False(t, ValidateSend(in))
	in.Text = "caption"
	in.Attachments[0].URL = "https://evil.example/video.mp4"
	require.False(t, ValidateSend(in))
	require.Equal(t, "caption", CanonicalSendText(in))
	for _, raw := range [][]byte{[]byte("not mp4"), payload[:30]} {
		_, err = c.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: bytes.NewReader(raw), Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(raw))})
		require.True(t, ErrInvalid.Is(err))
	}
	require.Equal(t, 2, calls)
}

func TestDownloadStreamUsesFileBoundInsteadOfJSONBound(t *testing.T) {
	c := New("fixture-token")
	payload := strings.Repeat("x", 3<<20)
	c.httpClient.Transport = boundedTransport{featureTransport(func(r *http.Request) (*http.Response, error) {
		resp := featureResponse(200, payload)
		resp.ContentLength = int64(len(payload))
		return resp, nil
	})}
	info, err := c.DownloadFile(t.Context(), "g1", "file1", io.Discard)
	require.NoError(t, err)
	require.EqualValues(t, len(payload), info.Size)
}

func TestObservedFileMetadataBatchReadScopesEveryID(t *testing.T) {
	c := New("fixture-token")
	calls := 0
	c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "https://file.groupme.com/v1/g1/fileData", r.URL.String())
		require.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.JSONEq(t, `{"file_ids":["file1","file2"]}`, string(body))
		return featureResponse(200, `[{"file_id":"file1","meta":200,"file_data":{"file_name":"fixture.txt","file_size":89,"mime_type":""}},{"file_id":"file2","meta":200,"file_data":{"file_name":"unsafe.svg","file_size":12,"mime_type":"image/svg+xml"}}]`), nil
	})
	items, err := c.FileMetadata(t.Context(), "g1", []string{"file1", "file2"})
	require.NoError(t, err)
	require.Len(t, items, 2)
	require.Equal(t, FileMetadata{FileID: "file1", Name: "fixture.txt", Size: 89, Status: 200}, items[0])
	require.Equal(t, "application/octet-stream", items[1].MIMEType)
	_, err = c.FileMetadata(t.Context(), "g1", []string{"file1", "file1"})
	require.True(t, ErrInvalid.Is(err))
	empty, err := c.FileMetadata(t.Context(), "g1", nil)
	require.NoError(t, err)
	require.Empty(t, empty)
	require.Equal(t, 1, calls)
	c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		return featureResponse(200, `[{"file_id":"foreign","meta":200,"file_data":{"file_name":"private.txt","file_size":89}}]`), nil
	})
	items, err = c.FileMetadata(t.Context(), "g1", []string{"file1"})
	require.True(t, ErrUnavailable.Is(err))
	require.Empty(t, items)
}

func TestActiveDownloadMIMEIsNeverInline(t *testing.T) {
	for _, kind := range []string{"text/html", "application/xhtml+xml", "image/svg+xml", "application/javascript", "text/javascript", "text/xml", "application/xml"} {
		c := New("fixture-token")
		c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
			resp := featureResponse(200, "fixture")
			resp.ContentLength = 7
			resp.Header.Set("Content-Type", kind)
			return resp, nil
		})
		info, err := c.DownloadFile(t.Context(), "g1", "file1", io.Discard)
		require.NoError(t, err)
		require.Equal(t, "application/octet-stream", info.ContentType)
	}
}

func TestResearchDeleteContractIsBoundedAndNeverRetries(t *testing.T) {
	for _, status := range []int{200, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				assert.Equal(t, http.MethodDelete, r.Method)
				assert.Equal(t, "/conversations/g1/messages/m1", r.URL.Path)
				assert.Empty(t, r.URL.RawQuery)
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.Empty(t, body)
				if status == 503 {
					w.WriteHeader(503)
					if _, writeErr := w.Write([]byte("private-provider-sentinel")); writeErr != nil {
						t.Errorf("write provider fixture: %v", writeErr)
					}
					return
				}
				writeFixture(t, w, `{"meta":{"code":200},"response":null}`)
			}))
			defer s.Close()
			err := newClient(s.URL, "fixture-token").DeleteMessage(t.Context(), "g1", "m1")
			if status == 200 {
				require.NoError(t, err)
			} else {
				require.True(t, ErrUncertain.Is(err))
				require.NotContains(t, err.Error(), "private-provider-sentinel")
			}
			require.Equal(t, 1, calls)
		})
	}
}

func TestNativeCustomReactionPackIdentityIsPreserved(t *testing.T) {
	var message Message
	require.NoError(t, json.Unmarshal([]byte(`{"id":"m1","reactions":[{"type":"emoji","pack_id":"1","pack_index":"0","user_ids":["u1"]},{"type":"emoji","pack_id":"1","pack_index":"52","user_ids":["u2"]},{"type":"unicode","code":"👍","user_ids":["u1"]}]}`), &message))
	require.Len(t, message.Reactions, 3)
	require.NotNil(t, message.Reactions[0].PackIndex)
	require.Equal(t, "0", *message.Reactions[0].PackIndex)
	require.Equal(t, "52", *message.Reactions[1].PackIndex)
	require.NotEqual(t, *message.Reactions[0].PackIndex, *message.Reactions[1].PackIndex)
	require.Nil(t, message.Reactions[2].PackID)
	require.Equal(t, "👍", message.Reactions[2].Code)
	out, err := json.Marshal(message.Reactions[0])
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"emoji","pack_id":"1","pack_index":"0","user_ids":["u1"]}`, string(out))
}

func TestCustomReactionInputExactNativeWriteShape(t *testing.T) {
	text := func(value string) *string { return &value }
	for _, test := range []struct {
		name string
		in   ReactionInput
		want string
	}{
		{"captured-native", ReactionInput{Type: "emoji", PackID: text("1"), PackIndex: text("52")}, `{"like_icon":{"type":"emoji","pack_id":1,"pack_index":52}}`},
		{"zero-index", ReactionInput{Type: "emoji", PackID: text("1"), PackIndex: text("0")}, `{"like_icon":{"type":"emoji","pack_id":1,"pack_index":0}}`},
		{"unicode", ReactionInput{Type: "unicode", Code: "👍"}, `{"like_icon":{"type":"unicode","code":"👍"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/messages/g1/m1/like", r.URL.Path)
				assert.Empty(t, r.URL.RawQuery)
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.JSONEq(t, test.want, string(body))
				writeFixture(t, w, `{"meta":{"code":200},"response":null}`)
			}))
			defer server.Close()
			require.NoError(t, newClient(server.URL, "fixture-token").React(t.Context(), "g1", "m1", test.in))
			require.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestCustomReactionRejectsMixedMissingAndUnboundedInputBeforeHTTP(t *testing.T) {
	text := func(s string) *string { return &s }
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer server.Close()
	c := newClient(server.URL, "fixture-token")
	for _, in := range []ReactionInput{
		{Type: "emoji", PackID: text("1")}, {Type: "emoji", PackIndex: text("52")},
		{Type: "emoji", PackID: text("1"), PackIndex: text("")}, {Type: "emoji", PackID: text("1"), PackIndex: text("4096")},
		{Type: "emoji", PackID: text("1000001"), PackIndex: text("52")}, {Type: "emoji", PackID: text("-1"), PackIndex: text("52")},
		{Type: "emoji", PackID: text("1"), PackIndex: text("+52")}, {Type: "emoji", PackID: text("1/other"), PackIndex: text("52")},
		{Type: "emoji", PackID: text("1"), PackIndex: text("52?token=private")}, {Type: "emoji", PackID: text("01"), PackIndex: text("52")},
		{Type: "emoji", Code: "👍", PackID: text("1"), PackIndex: text("52")},
		{Type: "unicode", Code: "👍", PackID: text("1"), PackIndex: text("52")},
	} {
		require.True(t, ErrInvalid.Is(c.React(t.Context(), "g1", "m1", in)))
	}
	require.Zero(t, calls.Load())
	require.True(t, validReactionInput(ReactionInput{Type: "emoji", PackID: text("0"), PackIndex: text("0")}))
	require.True(t, validReactionInput(ReactionInput{Type: "emoji", PackID: text("1000000"), PackIndex: text("4095")}))
}

func TestCustomReactionMutationNeverRetriesOrLeaksProviderBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, writeErr := w.Write([]byte("private-provider-sentinel")); writeErr != nil {
			t.Errorf("write provider fixture: %v", writeErr)
		}
	}))
	defer server.Close()
	pack, index := "1", "52"
	err := newClient(server.URL, "fixture-token").React(t.Context(), "g1", "m1", ReactionInput{Type: "emoji", PackID: &pack, PackIndex: &index})
	require.True(t, ErrUncertain.Is(err))
	require.NotContains(t, err.Error(), "private-provider-sentinel")
	require.EqualValues(t, 1, calls.Load())
}

func TestResearchSelectedMessageGettersAreScopedAndBounded(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
		seen = append(seen, r.URL.RequestURI())
		switch r.URL.Path {
		case "/v4/groups/g1/messages/m1":
			writeFixture(t, w, `{"meta":{"code":200},"response":{"message":{"id":"m1","group_id":"g1"}}}`)
		case "/v3/users/me":
			writeFixture(t, w, `{"meta":{"code":200},"response":{"id":"u1"}}`)
		case "/v3/direct_messages/m2":
			assert.Equal(t, "u2", r.URL.Query().Get("other_user_id"))
			assert.Len(t, r.URL.Query(), 1)
			writeFixture(t, w, `{"meta":{"code":200},"response":{"id":"m2","conversation_id":"u1+u2","sender_id":"u2","user_id":"u2","recipient_id":"u1"}}`)
		default:
			t.Fatalf("unexpected selected-message route %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c := newClient(server.URL+"/v3", "fixture-token")
	group, err := c.GroupMessage(t.Context(), "g1", "m1")
	require.NoError(t, err)
	require.Equal(t, "m1", group.ID)
	direct, err := c.DirectMessage(t.Context(), "u1+u2", "u1", "u2", "m2")
	require.NoError(t, err)
	require.Equal(t, "m2", direct.ID)
	require.Equal(t, []string{"/v4/groups/g1/messages/m1", "/v3/users/me", "/v3/direct_messages/m2?other_user_id=u2"}, seen)
}

func TestSelectedMessageGetterRejectsWrongIdentityAndUnknownWrapper(t *testing.T) {
	for _, test := range []struct {
		name, body string
		direct     bool
	}{
		{"group-wrong-id", `{"message":{"id":"other","group_id":"g1"}}`, false},
		{"group-wrong-scope", `{"message":{"id":"m1","group_id":"foreign"}}`, false},
		{"group-unknown-wrapper", `{"id":"m1","group_id":"g1"}`, false},
		{"dm-wrong-account-pair", `{"id":"m1","conversation_id":"u1+u2","sender_id":"foreign","recipient_id":"u1"}`, true},
		{"dm-wrong-conversation", `{"id":"m1","conversation_id":"u1+foreign","sender_id":"u2","recipient_id":"u1"}`, true},
		{"dm-unknown-wrapper", `{"direct_message":{"id":"m1","conversation_id":"u1+u2","sender_id":"u2","recipient_id":"u1"}}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v3/users/me" {
					writeFixture(t, w, `{"meta":{"code":200},"response":{"id":"u1"}}`)
					return
				}
				writeFixture(t, w, `{"meta":{"code":200},"response":`+test.body+`}`)
			}))
			defer s.Close()
			c := newClient(s.URL+"/v3", "fixture-token")
			var got Message
			var err error
			if test.direct {
				got, err = c.DirectMessage(t.Context(), "u1+u2", "u1", "u2", "m1")
			} else {
				got, err = c.GroupMessage(t.Context(), "g1", "m1")
			}
			require.True(t, ErrUnavailable.Is(err))
			require.Empty(t, got.ID)
		})
	}
}

func TestSelectedMessageGettersDoNotRetryOrAcceptForgedAccount(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/v3/users/me" {
			writeFixture(t, w, `{"meta":{"code":200},"response":{"id":"different-account"}}`)
			return
		}
		w.WriteHeader(503)
		if _, writeErr := w.Write([]byte("private-provider-sentinel")); writeErr != nil {
			t.Errorf("write provider fixture: %v", writeErr)
		}
	}))
	defer s.Close()
	c := newClient(s.URL+"/v3", "fixture-token")
	_, err := c.GroupMessage(t.Context(), "../bad", "m1")
	require.True(t, ErrInvalid.Is(err))
	require.Zero(t, calls.Load())
	_, err = c.DirectMessage(t.Context(), "u1+foreign", "u1", "u2", "m1")
	require.True(t, ErrInvalid.Is(err))
	require.Zero(t, calls.Load())
	_, err = c.DirectMessage(t.Context(), "u1+u2", "u1", "u2", "m1")
	require.True(t, ErrForbidden.Is(err))
	require.EqualValues(t, 1, calls.Load())
	_, err = c.GroupMessage(t.Context(), "g1", "m1")
	require.True(t, ErrUnavailable.Is(err))
	require.NotContains(t, err.Error(), "private-provider-sentinel")
	require.EqualValues(t, 2, calls.Load())
}

func TestSelectedGettersDistinguishNotFoundFromForbidden(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, test := range []struct {
			name             string
			httpStatus, meta int
			notFound         bool
		}{
			{"HTTP404", 404, 404, true}, {"HTTP403", 403, 403, false}, {"meta404", 200, 404, true}, {"meta403", 200, 403, false},
		} {
			t.Run("direct="+strconv.FormatBool(direct)+"/"+test.name, func(t *testing.T) {
				var selectedCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v3/users/me" {
						writeFixture(t, w, `{"meta":{"code":200},"response":{"id":"u1"}}`)
						return
					}
					selectedCalls.Add(1)
					w.WriteHeader(test.httpStatus)
					writeFixture(t, w, `{"meta":{"code":`+strconv.Itoa(test.meta)+`},"response":null,"private":"provider-sentinel"}`)
				}))
				defer server.Close()
				c := newClient(server.URL+"/v3", "fixture-token")
				var got Message
				var err error
				if direct {
					got, err = c.DirectMessage(t.Context(), "u1+u2", "u1", "u2", "m1")
				} else {
					got, err = c.GroupMessage(t.Context(), "g1", "m1")
				}
				if test.notFound {
					require.True(t, ErrUnavailable.Is(err))
					require.False(t, ErrForbidden.Is(err))
				} else {
					require.True(t, ErrForbidden.Is(err))
					require.False(t, ErrUnavailable.Is(err))
				}
				require.Empty(t, got.ID)
				require.NotContains(t, err.Error(), "provider-sentinel")
				require.EqualValues(t, 1, selectedCalls.Load())
			})
		}
	}
}

func TestSelectedGetter404PolicyDoesNotBroadenOtherReads(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeFixture(t, w, `{"meta":{"code":404},"response":null}`)
	}))
	defer server.Close()
	c := newClient(server.URL+"/v3", "fixture-token")
	_, err := c.PinnedMessages(t.Context(), "g1")
	require.True(t, ErrForbidden.Is(err))
	require.False(t, ErrUnavailable.Is(err))
	_, err = c.Me(t.Context())
	require.True(t, ErrForbidden.Is(err))
	require.False(t, ErrUnavailable.Is(err))
}

func TestSourceSupportedAlbumMediaPageQueryIsOneBoundedRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/conversations/g1/albums/a1/media", r.URL.Path)
		assert.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
		assert.Equal(t, "next+page==", r.URL.Query().Get("page"))
		assert.Equal(t, "100", r.URL.Query().Get("per_page"))
		assert.Len(t, r.URL.Query(), 2)
		writeFixture(t, w, `{"meta":{"code":200},"response":{"album":{"album_id":"a1","conversation_id":"g1","attachments":[]},"next_cursor":"last-page"}}`)
	}))
	defer server.Close()
	page, err := newClient(server.URL, "fixture-token").AlbumMedia(t.Context(), "g1", "a1", "next+page==", 100)
	require.NoError(t, err)
	require.Equal(t, "last-page", page.NextCursor)
	require.EqualValues(t, 1, calls.Load())
}

func TestAlbumMediaRejectsUnsafeAndRepeatedCursors(t *testing.T) {
	var calls atomic.Int32
	next := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		encodeFixture(t, w, map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"album": map[string]any{"album_id": "a1", "conversation_id": "g1", "attachments": []AlbumMedia{}}, "next_cursor": next}})
	}))
	defer server.Close()
	c := newClient(server.URL, "fixture-token")
	for _, cursor := range []string{strings.Repeat("x", 1025), "../next", "next?page=other", "next&access_token=private", "https://evil.example/next", "next\r\nX-Token: private", "next page", "next\tpage", "page🔥", string([]byte{0xff})} {
		page, err := c.AlbumMedia(t.Context(), "g1", "a1", cursor, 100)
		require.True(t, ErrInvalid.Is(err))
		require.Empty(t, page.Items)
	}
	require.Zero(t, calls.Load())
	for _, badNext := range []string{"same-page", "bad🔥"} {
		next = badNext
		page, err := c.AlbumMedia(t.Context(), "g1", "a1", "same-page", 100)
		require.True(t, ErrUnavailable.Is(err))
		require.Empty(t, page.Items)
		require.Empty(t, page.NextCursor)
	}
	require.EqualValues(t, 2, calls.Load())
}

func TestSupportsMediaUploadMatchesExactDirectUploadFormats(t *testing.T) {
	for _, contentType := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "video/mp4"} {
		require.True(t, SupportsMediaUpload(contentType), contentType)
	}
	for _, contentType := range []string{"image/svg+xml", "image/heic", "image/heif", "video/webm", "image/tiff", "application/octet-stream", "text/plain", "image/PNG", "image/png; charset=utf-8", "video/mp4; codecs=avc1", " image/png", ""} {
		require.False(t, SupportsMediaUpload(contentType), contentType)
	}
}

func TestUploadCapabilityProtocolRestriction(t *testing.T) {
	for _, test := range []struct {
		name, query string
		valid       bool
	}{
		{"https", "spr=https", true},
		{"http", "spr=http", false},
		{"mixed", "spr=https,http", false},
		{"empty", "spr=", false},
		{"repeated", "spr=https&spr=https", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := New("fixture-token")
			puts := 0
			c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "m.groupme.com" {
					body, err := json.Marshal(map[string]string{"uploadUrl": "https://cdn2.groupme.com/uploads/fixture/original.png?sig=synthetic&" + test.query, "renderUrl": "https://m.groupme.com/uploads/fixture/original.png"})
					if err != nil {
						return nil, err
					}
					return featureResponse(200, string(body)), nil
				}
				puts++
				assert.Equal(t, "cdn2.groupme.com", r.URL.Host)
				assert.Equal(t, http.MethodPut, r.Method)
				assert.Equal(t, "https", r.URL.Query().Get("spr"))
				assert.Empty(t, r.Header.Get("X-Access-Token"))
				return featureResponse(201, ""), nil
			})
			target, err := c.InitializeUpload(t.Context(), InitializeUploadInput{Extension: "png", SenderID: "u1", GroupID: "g1", FileSize: 3, Width: 1, Height: 1})
			if !test.valid {
				require.True(t, ErrUncertain.Is(err))
				require.Zero(t, puts)
				return
			}
			require.NoError(t, err)
			require.NoError(t, c.Upload(t.Context(), target, "image/png", strings.NewReader("png"), 3))
			require.Equal(t, 1, puts)
		})
	}
}

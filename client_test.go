package groupme

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omniaura/go-kit/errs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimitPreservesOnlySafeRetryDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
		writeFixture(t, w, `{"meta":{"code":429,"errors":["private provider detail"]}}`)
	}))
	defer server.Close()
	_, err := newClient(server.URL, "synthetic-private-token").Me(t.Context())
	require.True(t, ErrRateLimited.Is(err))
	delay, ok := errs.RetryAfterOf(err)
	require.True(t, ok)
	require.Equal(t, 2*time.Hour, delay)
	require.NotContains(t, err.Error(), "private provider detail")
	require.NotContains(t, err.Error(), "synthetic-private-token")
}

func writeFixture(t *testing.T, w io.Writer, text string) {
	t.Helper()
	if _, err := io.WriteString(w, text); err != nil {
		t.Error(err)
	}
}
func encodeFixture(t *testing.T, w io.Writer, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func TestPersonalAccountAPI(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "private-fixture-token", r.Header.Get("X-Access-Token"))
		assert.NotContains(t, r.URL.String(), "token")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users/me":
			writeFixture(t, w, `{"meta":{"code":200},"response":{"id":"u1","name":"Synthetic coach","phone_number":"must-not-project"}}`)
		case "/groups":
			assert.Equal(t, "memberships", r.URL.Query().Get("omit"))
			writeFixture(t, w, `{"meta":{"code":200},"response":[{"id":"g1","name":"Synthetic team"}]}`)
		case "/groups/g1":
			writeFixture(t, w, `{"meta":{"code":200},"response":{"id":"g1","name":"Synthetic team","members":[{"user_id":"u1","nickname":"Coach","phone_number":"must-not-project"}]}}`)
		case "/groups/g1/messages":
			if r.URL.Query().Get("after_id") != "" {
				assert.Equal(t, "older", r.URL.Query().Get("after_id"))
				assert.Empty(t, r.URL.Query().Get("since_id"))
				assert.Empty(t, r.URL.Query().Get("before_id"))
				writeFixture(t, w, `{"meta":{"code":200},"response":{"count":1,"messages":[{"id":"newer","group_id":"g1","text":"next"}]}}`)
				return
			}
			if r.Method == http.MethodPost {
				sends.Add(1)
				var body struct {
					Message SendInput `json:"message"`
				}
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					return
				}
				w.WriteHeader(201)
				encodeFixture(t, w, map[string]any{"meta": map[string]int{"code": 201}, "response": map[string]any{"message": Message{ID: "opaque-message-A", GroupID: "g1", UserID: "u1", SourceGUID: body.Message.SourceGUID, Text: body.Message.Text, Attachments: body.Message.Attachments}}})
				return
			}
			if r.URL.Query().Get("before_id") == "older" {
				w.WriteHeader(304)
				return
			}
			writeFixture(t, w, `{"meta":{"code":200},"response":{"count":2,"messages":[{"id":"newer","group_id":"g1","text":"AVAILABILITY"},{"id":"older","group_id":"g1","text":"hello"}]}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cl := newClient(server.URL, "private-fixture-token")
	after, afterErr := cl.MessagesAfter(t.Context(), "g1", "older", 2)
	require.NoError(t, afterErr)
	require.Len(t, after.Messages, 1)
	require.Equal(t, "newer", after.Messages[0].ID)
	me, e := cl.Me(t.Context())
	require.NoError(t, e)
	require.Equal(t, "u1", me.ID)
	b, e := json.Marshal(me)
	require.NoError(t, e)
	require.NotContains(t, string(b), "phone_number")
	groups, e := cl.Groups(t.Context(), 1, 1)
	require.NoError(t, e)
	require.True(t, groups.HasMore)
	g, e := cl.Group(t.Context(), "g1")
	require.NoError(t, e)
	b, e = json.Marshal(g)
	require.NoError(t, e)
	require.NotContains(t, string(b), "phone_number")
	p, e := cl.Messages(t.Context(), "g1", "", 2)
	require.NoError(t, e)
	require.True(t, p.HasMore)
	require.Equal(t, "older", p.NextBeforeID)
	p, e = cl.Messages(t.Context(), "g1", "older", 2)
	require.NoError(t, e)
	require.Empty(t, p.Messages)
	require.False(t, p.HasMore)
	in := SendInput{SourceGUID: "operation-1", Text: "Owned synthetic message", Attachments: []Attachment{{Type: "image", URL: "https://i.groupme.com/fixture.png"}}}
	m, e := cl.Send(t.Context(), "g1", in)
	require.NoError(t, e)
	require.Equal(t, "opaque-message-A", m.ID)
	require.EqualValues(t, 1, sends.Load())
}

func TestErrorsAreSanitizedAndSendsNeverRetry(t *testing.T) {
	for _, status := range []int{301, 400, 401, 403, 409, 429, 500, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "https://must-not-follow.invalid/token")
				w.WriteHeader(status)
				writeFixture(t, w, `{"private":"SECRET-TOKEN private message"}`)
			}))
			defer s.Close()
			cl := newClient(s.URL, "SECRET-TOKEN")
			_, e := cl.Send(t.Context(), "g1", SendInput{SourceGUID: "operation-1", Text: "private message"})
			require.Error(t, e)
			require.NotContains(t, e.Error(), "SECRET-TOKEN")
			require.NotContains(t, e.Error(), "private message")
			require.EqualValues(t, 1, calls.Load())
			if status == 409 || status >= 500 || status == 301 {
				require.True(t, ErrUncertain.Is(e))
			}
		})
	}
}

func TestInvalidInputNeverReachesProvider(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer s.Close()
	cl := newClient(s.URL, "fixture-token")
	for _, id := range []string{"../users/me", "g?token=stolen", "g/other", ""} {
		_, e := cl.Messages(t.Context(), id, "", 25)
		require.Error(t, e)
	}
	for _, in := range []SendInput{{SourceGUID: "op", Text: strings.Repeat("x", 4001)}, {SourceGUID: "op", Attachments: []Attachment{{Type: "image", URL: "https://i.groupme.com.evil.invalid/x"}}}, {SourceGUID: "op", Attachments: []Attachment{{Type: "location", Name: "x", Lat: "NaN", Lng: "1"}}}, {SourceGUID: "op", Attachments: []Attachment{{Type: "reply"}}}} {
		_, e := cl.Send(t.Context(), "g1", in)
		require.Error(t, e)
	}
	require.Zero(t, calls.Load())
}

func TestPersonalMessageCharacterBoundary(t *testing.T) {
	var sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		var body struct {
			Message SendInput `json:"message"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusCreated)
		encodeFixture(t, w, map[string]any{"meta": map[string]int{"code": 201}, "response": map[string]any{"message": Message{ID: "boundary-message", GroupID: "g1", SourceGUID: body.Message.SourceGUID, Text: body.Message.Text}}})
	}))
	defer server.Close()
	client := newClient(server.URL, "fixture-token")
	for _, text := range []string{strings.Repeat("x", 1001), strings.Repeat("x", 4000), strings.Repeat("🙂", 4000)} {
		message, err := client.Send(t.Context(), "g1", SendInput{SourceGUID: "boundary-operation", Text: text})
		require.NoError(t, err)
		require.Equal(t, text, message.Text)
	}
	_, err := client.Send(t.Context(), "g1", SendInput{SourceGUID: "boundary-operation", Text: strings.Repeat("🙂", 4001)})
	require.Error(t, err)
	require.EqualValues(t, 3, sends.Load(), "oversized text must not reach the provider")
}

func TestBoundedSearchAndMalformedResponses(t *testing.T) {
	var calls atomic.Int32
	mode := "search"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if mode == "bad" {
			writeFixture(t, w, `{"meta":{"code":200},"response":null}`)
			return
		}
		messages := make([]Message, 100)
		for i := range messages {
			messages[i] = Message{ID: "m" + strconv.Itoa(i), GroupID: "g1", Text: "availability"}
		}
		encodeFixture(t, w, map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": messages, "count": 5000}})
	}))
	defer s.Close()
	cl := newClient(s.URL, "fixture-token")
	v, e := cl.Search(t.Context(), "g1", "", "availability", 1)
	require.NoError(t, e)
	raw, e := json.Marshal(v)
	require.NoError(t, e)
	require.Contains(t, string(raw), `"complete":false`)
	require.Contains(t, string(raw), `"inspected":100`)
	require.EqualValues(t, 1, calls.Load())
	mode = "bad"
	_, e = cl.Me(t.Context())
	require.Error(t, e)
}

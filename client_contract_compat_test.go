package groupme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDirectMessagePagePreservesProviderCount(t *testing.T) {
	calls := 0
	client := newClient("https://api.example.invalid/v3", "synthetic-token")
	client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "peer", r.URL.Query().Get("other_user_id"))
		return featureResponse(200, `{"meta":{"code":200},"response":{"count":41,"direct_messages":[{"id":"message-1","user_id":"peer","recipient_id":"self"}]}}`), nil
	})
	page, err := client.DirectMessages(t.Context(), "peer", "", 100)
	require.NoError(t, err)
	require.Equal(t, 41, page.Count)
	require.True(t, page.HasMore, "a short DM page does not prove exhaustion")
	require.Equal(t, "message-1", page.NextBeforeID)
	require.Equal(t, 1, calls, "provider count must not cause implicit history fetches")
}

func TestHistoryRejectsInvalidProviderPageWindows(t *testing.T) {
	for _, method := range []string{"group-before", "group-after", "direct"} {
		for _, defect := range []string{"oversized", "duplicate", "cursor-last", "cursor-first", "missing-id", "foreign-scope"} {
			t.Run(method+"/"+defect, func(t *testing.T) {
				rows := []Message{{ID: "message-1", GroupID: "group", UserID: "peer", RecipientID: "self"}, {ID: "message-2", GroupID: "group", UserID: "peer", RecipientID: "self"}}
				switch defect {
				case "oversized":
					rows = append(rows, Message{ID: "message-3", GroupID: "group", UserID: "peer"})
				case "duplicate":
					rows[1].ID = rows[0].ID
				case "cursor-last":
					rows[1].ID = "exclusive-cursor"
				case "cursor-first":
					rows[0].ID = "exclusive-cursor"
				case "missing-id":
					rows[0].ID = ""
				case "foreign-scope":
					rows[0].GroupID, rows[0].UserID, rows[0].RecipientID = "other-group", "other-user", "someone-else"
				}
				field := "messages"
				if method == "direct" {
					field = "direct_messages"
				}
				body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"count": 90, field: rows}})
				require.NoError(t, err)
				calls := 0
				client := newClient("https://api.example.invalid/v3", "synthetic-token")
				client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					require.Equal(t, "2", r.URL.Query().Get("limit"))
					return featureResponse(200, string(body)), nil
				})
				var page MessagePage
				switch method {
				case "group-before":
					page, err = client.Messages(t.Context(), "group", "exclusive-cursor", 2)
				case "group-after":
					page, err = client.MessagesAfter(t.Context(), "group", "exclusive-cursor", 2)
				case "direct":
					page, err = client.DirectMessages(t.Context(), "peer", "exclusive-cursor", 2)
				}
				require.True(t, ErrUnavailable.Is(err), "malformed history must fail rather than publish a cursor")
				require.Empty(t, page.Messages)
				require.Empty(t, page.NextBeforeID)
				require.False(t, page.HasMore)
				require.Equal(t, 1, calls)
			})
		}
	}
}

func TestGroupHistoryDerivesItsOwnContinuation(t *testing.T) {
	for _, rows := range [][]Message{nil, {{ID: "message-1", GroupID: "group"}}, {{ID: "message-1", GroupID: "group"}, {ID: "message-2", GroupID: "group"}}} {
		t.Run(string(rune('0'+len(rows))), func(t *testing.T) {
			body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": rows, "has_more": true, "next_before_id": "untrusted-hint"}})
			require.NoError(t, err)
			calls := 0
			client := newClient("https://api.example.invalid/v3", "synthetic-token")
			client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return featureResponse(200, string(body)), nil
			})
			page, err := client.Messages(t.Context(), "group", "", 2)
			require.NoError(t, err)
			require.Equal(t, len(rows) == 2, page.HasMore)
			if len(rows) == 2 {
				require.Equal(t, "message-2", page.NextBeforeID)
			} else {
				require.Empty(t, page.NextBeforeID)
			}
			require.Equal(t, 1, calls)
		})
	}
}

func TestMessageRetainsGalleryTimestamp(t *testing.T) {
	var message Message
	require.NoError(t, json.Unmarshal([]byte(`{"id":"message","gallery_ts":"1720628538.123"}`), &message))
	encoded, err := json.Marshal(message)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"gallery_ts":"1720628538.123"`)
	encoded, err = json.Marshal(Message{})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "gallery_ts")
}

func TestLegacyVideoAttachmentsKeepNarrowOriginAndFormatRules(t *testing.T) {
	for _, preview := range []string{"https://v.groupme.com/group/preview.jpeg", "https://v.groupme.com/group/preview.png", "https://i.groupme.com/legacy-preview", "https://m.groupme.com/uploads/group/preview.jpg"} {
		attachment := Attachment{Type: "video", URL: "https://v.groupme.com/group/legacy.mp4", PreviewURL: preview}
		require.True(t, ValidateSend(SendInput{SourceGUID: "synthetic-intent", Attachments: []Attachment{attachment}}), preview)
		require.Equal(t, attachment.URL, CanonicalSendText(SendInput{Attachments: []Attachment{attachment}}))
	}
	for _, raw := range []string{
		"http://v.groupme.com/group/video.mp4", "https://v.groupme.com.evil.invalid/group/video.mp4", "https://user@v.groupme.com/group/video.mp4",
		"https://v.groupme.com:443/group/video.mp4", "https://v.groupme.com/group/video.mp4?token=synthetic", "https://v.groupme.com/group/video.mp4#fragment",
		"https://v.groupme.com/group/../video.mp4", "https://v.groupme.com/group/%2E%2E/video.mp4", "https://v.groupme.com/group/video", "https://v.groupme.com/group/video.jpeg",
	} {
		require.False(t, ValidateSend(SendInput{SourceGUID: "synthetic-intent", Attachments: []Attachment{{Type: "video", URL: raw, PreviewURL: "https://v.groupme.com/group/preview.jpeg"}}}), raw)
	}
	for _, raw := range []string{"https://v.groupme.com/group/video.mp4", "https://v.groupme.com/group/preview", "https://v.groupme.com/group/preview.svg", "https://v.groupme.com/group/preview.jpeg?token=synthetic"} {
		require.False(t, ValidateSend(SendInput{SourceGUID: "synthetic-intent", Attachments: []Attachment{{Type: "video", URL: "https://v.groupme.com/group/video.mp4", PreviewURL: raw}}}), raw)
	}
	require.Empty(t, ImageURL("https://v.groupme.com/group/preview.jpeg"), "avatar origin policy does not widen with attachment support")
}

func TestNewWithTransportUsesIsolatedAPIAndMediaClients(t *testing.T) {
	defaultTransport := http.DefaultTransport
	var calls []string
	transport := featureTransport(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, r.Method+" "+r.URL.Host+r.URL.Path)
		require.Equal(t, "https", r.URL.Scheme)
		switch r.URL.Host {
		case "api.groupme.com":
			require.Equal(t, "/v3/users/me", r.URL.Path)
			require.Equal(t, "synthetic-token", r.Header.Get("X-Access-Token"))
			return featureResponse(200, `{"meta":{"code":200},"response":{"id":"self"}}`), nil
		case "m.groupme.com":
			require.Equal(t, "/uploads", r.URL.Path)
			require.Equal(t, "synthetic-token", r.Header.Get("X-Access-Token"))
			return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/group/image.png?sig=synthetic&spr=https","renderUrl":"https://m.groupme.com/uploads/group/image.png"}`), nil
		case "cdn2.groupme.com":
			require.Equal(t, http.MethodPut, r.Method)
			require.Empty(t, r.Header.Get("X-Access-Token"))
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Equal(t, "png", string(body))
			return featureResponse(201, ""), nil
		case "file.groupme.com":
			require.Equal(t, "/v1/group/files", r.URL.Path)
			require.Equal(t, "synthetic-token", r.Header.Get("X-Access-Token"))
			return featureResponse(201, `{"status_url":"https://file.groupme.com/v1/group/uploadStatus?job=synthetic-job"}`), nil
		default:
			t.Fatalf("unexpected transport destination: %s", r.URL.Host)
			return nil, errors.New("unexpected destination")
		}
	})
	client := NewWithTransport("synthetic-token", transport)
	_, err := client.Me(t.Context())
	require.NoError(t, err)
	target, err := client.InitializeUpload(t.Context(), InitializeUploadInput{Extension: "png", GroupID: "group", SenderID: "self", FileSize: 3, Width: 1, Height: 1})
	require.NoError(t, err)
	require.NoError(t, client.Upload(t.Context(), target, "image/png", strings.NewReader("png"), 3))
	file, err := client.UploadFile(t.Context(), "group", UploadInput{Data: strings.NewReader("fixture"), Name: "fixture.txt", ContentType: "text/plain", Size: 7})
	require.NoError(t, err)
	require.Equal(t, "synthetic-job", file.JobID)
	require.Equal(t, []string{"GET api.groupme.com/v3/users/me", "POST m.groupme.com/uploads", "PUT cdn2.groupme.com/uploads/group/image.png", "POST file.groupme.com/v1/group/files"}, calls)
	otherCalls := 0
	other := NewWithTransport("other-synthetic-token", featureTransport(func(*http.Request) (*http.Response, error) {
		otherCalls++
		return featureResponse(200, `{"meta":{"code":200},"response":{"id":"other"}}`), nil
	}))
	account, err := other.Me(t.Context())
	require.NoError(t, err)
	require.Equal(t, "other", account.ID)
	require.Equal(t, 1, otherCalls)
	require.Len(t, calls, 4)
	require.Same(t, defaultTransport, http.DefaultTransport)
	require.NotSame(t, client.httpClient, other.httpClient)
	require.Equal(t, 20*time.Second, client.httpClient.Timeout)
	require.Equal(t, 20*time.Second, client.fileHTTPClient().Timeout)
	require.Same(t, defaultTransport, NewWithTransport("synthetic-token", nil).httpClient.Transport.(boundedTransport).base)
}

func TestNewWithTransportPreservesSafetyBoundaries(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		calls := 0
		client := NewWithTransport("synthetic-token", featureTransport(func(*http.Request) (*http.Response, error) {
			calls++
			response := featureResponse(302, "")
			response.Header.Set("Location", "https://external.invalid/target")
			return response, nil
		}))
		_, err := client.Me(t.Context())
		require.True(t, ErrUnavailable.Is(err))
		require.Equal(t, 1, calls)
	})
	t.Run("body cap", func(t *testing.T) {
		body := `{"meta":{"code":200},"response":{"id":"self","name":"` + strings.Repeat("a", 2<<20) + `"}}`
		client := NewWithTransport("synthetic-token", featureTransport(func(*http.Request) (*http.Response, error) {
			return featureResponse(200, body), nil
		}))
		_, err := client.Me(t.Context())
		require.True(t, ErrUnavailable.Is(err))
	})
	t.Run("mutation has one attempt", func(t *testing.T) {
		calls := 0
		client := NewWithTransport("synthetic-token", featureTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("synthetic private provider detail")
		}))
		_, err := client.Send(t.Context(), "group", SendInput{SourceGUID: "synthetic-intent", Text: "Synthetic text"})
		require.True(t, ErrUncertain.Is(err))
		require.NotContains(t, err.Error(), "private provider detail")
		require.Equal(t, 1, calls)
	})
	t.Run("caller cancellation", func(t *testing.T) {
		client := NewWithTransport("synthetic-token", featureTransport(func(r *http.Request) (*http.Response, error) {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}))
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer cancel()
		_, err := client.Me(ctx)
		require.True(t, ErrUnavailable.Is(err))
		require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	})
}

func TestNewWithTransportFileTransfersKeepTheirOwnBodyBudget(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), (2<<20)+1)
	client := NewWithTransport("synthetic-token", featureTransport(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "file.groupme.com", r.URL.Host)
		response := featureResponse(200, string(payload))
		response.ContentLength = int64(len(payload))
		response.Header.Set("Content-Type", "text/plain")
		response.Header.Set("Content-Disposition", `attachment; filename="synthetic.txt"`)
		return response, nil
	}))
	var destination bytes.Buffer
	_, err := client.DownloadFile(t.Context(), "group", "file", &destination)
	require.NoError(t, err)
	require.Equal(t, payload, destination.Bytes(), "binary/file transport retains its separate response policy")
}

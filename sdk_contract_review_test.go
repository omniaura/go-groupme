package groupme

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewFileRedirectPolicyCannotForwardCredentials(t *testing.T) {
	for _, policy := range []string{"constructor", "nil", "permissive"} {
		t.Run(policy, func(t *testing.T) {
			client := New("synthetic-private-token")
			switch policy {
			case "nil":
				client.httpClient.CheckRedirect = nil
			case "permissive":
				client.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
			}
			calls, outside, leaked := 0, 0, 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "file.groupme.com" {
					outside++
					if r.Header.Get("X-Access-Token") != "" {
						leaked++
					}
					return featureResponse(200, `{"status":"completed","file_id":"file1"}`), nil
				}
				require.Equal(t, "synthetic-private-token", r.Header.Get("X-Access-Token"))
				response := featureResponse(302, "")
				response.Header.Set("Location", "https://untrusted.invalid/redirect")
				return response, nil
			})
			_, err := client.FileUploadStatus(t.Context(), "g1", "job1")
			assert.True(t, ErrUnavailable.Is(err))
			assert.Equal(t, 1, calls)
			assert.Zero(t, outside)
			assert.Zero(t, leaked)
			if policy == "nil" {
				require.Nil(t, client.httpClient.CheckRedirect, "the original client is not mutated")
			}
		})
	}
}

func reviewMP4Fixture(t *testing.T, brand, compatible string) []byte {
	t.Helper()
	require.Len(t, brand, 4)
	box := func(kind string, payload []byte) []byte {
		out := make([]byte, 8+len(payload))
		length := len(out)
		if length > math.MaxUint32 {
			t.Fatal("MP4 fixture is too large")
			return nil
		}
		binary.BigEndian.PutUint32(out, uint32(length))
		copy(out[4:8], kind)
		copy(out[8:], payload)
		return out
	}
	track := make([]byte, 84)
	binary.BigEndian.PutUint32(track[76:], 320<<16)
	binary.BigEndian.PutUint32(track[80:], 180<<16)
	return append(box("ftyp", []byte(brand+"\x00\x00\x00\x00"+compatible)), box("moov", box("trak", box("tkhd", track)))...)
}

func TestReviewMP4OrdinaryReaderSharesBoundedUploadSpool(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("TMPDIR", directory)
	payload := reviewMP4Fixture(t, "isom", "isom")
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "m.groupme.com" {
			var input InitializeUploadInput
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			require.Equal(t, 320, input.Width)
			require.Equal(t, 180, input.Height)
			require.EqualValues(t, len(payload), input.FileSize)
			return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/video.mp4?sig=synthetic","renderUrl":"https://m.groupme.com/uploads/fixture/original.mp4","thumbnailUrl":"https://m.groupme.com/uploads/fixture/thumbnail.jpg"}`), nil
		}
		require.Equal(t, "cdn2.groupme.com", r.URL.Host)
		require.Empty(t, r.Header.Get("X-Access-Token"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.Equal(t, payload, body)
		return featureResponse(201, ""), nil
	})
	// Hiding ReaderAt/Seeker exercises the public io.Reader contract.
	ordinary := func() io.Reader { return struct{ io.Reader }{bytes.NewReader(payload)} }
	attachment, err := client.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: ordinary(), Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(payload))})
	require.NoError(t, err)
	require.Equal(t, "video", attachment.Type)
	require.Equal(t, 2, calls)
	positioned := bytes.NewReader(append([]byte("prefix"), payload...))
	_, err = positioned.Seek(6, io.SeekStart)
	require.NoError(t, err)
	_, err = client.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: positioned, Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(payload))})
	require.NoError(t, err, "io.Reader input starts at its current position")
	require.Equal(t, 4, calls)
	for _, delta := range []int64{-1, 1} {
		_, err = client.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: ordinary(), Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(payload)) + delta})
		require.True(t, ErrInvalid.Is(err))
	}
	require.Equal(t, 4, calls, "size mismatches are rejected before HTTP")
	files, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, files, "owned spools are removed on success and failure")
}

func TestReviewMP4RegisteredAndCompatibleBrands(t *testing.T) {
	for _, test := range []struct{ brand, compatible string }{
		{"iso3", ""}, {"iso4", ""}, {"iso5", ""}, {"iso6", ""}, {"dash", ""},
		// avc3 is a sample-entry code, not a registered brand. A file with this
		// major still declares an independently supported isom compatibility.
		{"avc3", "isom"}, {"zzzz", "iso6"},
	} {
		payload := reviewMP4Fixture(t, test.brand, test.compatible)
		width, height := mp4Dimensions(bytes.NewReader(payload), int64(len(payload)))
		require.Equal(t, 320, width, "%v", test)
		require.Equal(t, 180, height, "%v", test)
	}
	for _, payload := range [][]byte{reviewMP4Fixture(t, "zzzz", ""), reviewMP4Fixture(t, "isom", "bad")} {
		width, height := mp4Dimensions(bytes.NewReader(payload), int64(len(payload)))
		require.Zero(t, width)
		require.Zero(t, height)
	}
}

func TestReviewPinnedMessagesRejectsDMBeforeHTTP(t *testing.T) {
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, "/v3/pinned/groups/g1/messages", r.URL.Path)
		return featureResponse(200, `{"meta":{"code":200},"response":{"messages":[]}}`), nil
	})
	_, err := client.PinnedMessages(t.Context(), "u1+u2")
	require.True(t, ErrInvalid.Is(err))
	require.Zero(t, calls)
	_, err = client.PinnedMessages(t.Context(), "g1")
	require.NoError(t, err)
	require.Equal(t, 1, calls)
}

func TestReviewDeletionTimestampShapesPreserveHistoryPage(t *testing.T) {
	for _, raw := range []string{`null`, `""`, `"2026-10-23T00:00:00Z"`, `1700000000`, `1700000000.25`} {
		client := New("fixture-token")
		client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
			return featureResponse(200, `{"meta":{"code":200},"response":{"messages":[{"id":"m1","group_id":"g1","created_at":100,"text":"fixture","deleted_at":`+raw+`},{"id":"m2","group_id":"g1","created_at":99}],"count":2}}`), nil
		})
		page, err := client.Messages(t.Context(), "g1", "", 20)
		require.NoError(t, err, raw)
		require.Len(t, page.Messages, 2)
		require.Equal(t, "fixture", page.Messages[0].Text)
		require.Equal(t, raw != `null` && raw != `""`, page.Messages[0].IsDeleted())
		require.False(t, page.Messages[1].IsDeleted())
	}
	for _, raw := range []string{`true`, `{}`, `[]`} {
		var message Message
		require.Error(t, json.Unmarshal([]byte(`{"deleted_at":`+raw+`}`), &message))
	}
}

func TestReviewGalleryMatchesNativePotentialContinuationHeuristic(t *testing.T) {
	for _, count := range []int{0, 1, 100} {
		client := New("fixture-token")
		client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
			require.Equal(t, "100", r.URL.Query().Get("limit"))
			messages := make([]Message, count)
			for index := range messages {
				messages[index] = Message{ID: "m" + strconv.Itoa(index), GroupID: "g1"}
			}
			body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": messages}})
			require.NoError(t, err)
			return featureResponse(200, string(body)), nil
		})
		page, err := client.Gallery(t.Context(), "g1")
		require.NoError(t, err)
		require.Equal(t, count == 100, page.HasMore)
	}
}

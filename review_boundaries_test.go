package groupme

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/stretchr/testify/require"
)

func TestReviewXMLUploadContentTypesStayDownloadable(t *testing.T) {
	plain := []byte(`<?xml version="1.0"?><fixture>owned synthetic XML</fixture>`)
	utf16LE, utf16BE := []byte{}, []byte{}
	for _, unit := range utf16.Encode([]rune(string(plain))) {
		utf16LE = binary.LittleEndian.AppendUint16(utf16LE, unit)
		utf16BE = binary.BigEndian.AppendUint16(utf16BE, unit)
	}
	require.Equal(t, "application/octet-stream", http.DetectContentType(utf16LE))
	require.Equal(t, "application/octet-stream", http.DetectContentType(utf16BE))
	for _, kind := range []string{"text/xml", "application/xml", "application/xml; charset=utf-16"} {
		for _, data := range [][]byte{plain, utf16LE, utf16BE, append([]byte{0xff, 0xfe}, utf16LE...)} {
			t.Run(kind+"/"+http.DetectContentType(data), func(t *testing.T) {
				client := New("fixture-token")
				calls := 0
				client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					require.Equal(t, http.MethodPost, r.Method)
					require.Equal(t, "https", r.URL.Scheme)
					require.Equal(t, "file.groupme.com", r.URL.Host)
					require.Equal(t, "/v1/g1/files", r.URL.Path)
					require.Equal(t, "application/octet-stream", r.Header.Get("Content-Type"))
					require.Empty(t, r.URL.Query().Get("access_token"))
					body, err := io.ReadAll(r.Body)
					require.NoError(t, err)
					require.Equal(t, data, body)
					return featureResponse(201, `{"status_url":"https://file.groupme.com/v1/g1/uploadStatus?job=job1"}`), nil
				})
				job, err := client.UploadFile(t.Context(), "g1", UploadInput{Data: bytes.NewReader(data), Name: "fixture.xml", ContentType: kind, Size: int64(len(data))})
				require.NoError(t, err)
				require.Equal(t, "job1", job.JobID)
				require.Equal(t, 1, calls)
			})
		}
	}
}

func TestReviewImageAttachmentsRequireImageUploadFormat(t *testing.T) {
	for _, raw := range []string{
		"https://i.groupme.com/legacy-image", "https://i.groupme.com/fixture.png",
		"https://m.groupme.com/uploads/fixture/512x512.original.png",
		"https://m.groupme.com/uploads/fixture/original.jpg", "https://m.groupme.com/uploads/fixture/original.jpeg",
		"https://m.groupme.com/uploads/fixture/original.gif", "https://m.groupme.com/uploads/fixture/original.webp",
	} {
		require.True(t, ValidateSend(SendInput{SourceGUID: "op1", Attachments: []Attachment{{Type: "image", URL: raw}}}), raw)
	}
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return featureResponse(500, ""), nil
	})
	for _, raw := range []string{
		"https://m.groupme.com/uploads/fixture/320x180.original.mp4", "https://m.groupme.com/uploads/fixture/original.MP4",
		"https://m.groupme.com/uploads/fixture/original%2emp4", "https://m.groupme.com/uploads/fixture/original",
		"https://v.groupme.com/fixture.mp4", "https://m.groupme.com/other/fixture.png",
		"https://m.groupme.com.evil.invalid/uploads/fixture.png", "https://m.groupme.com/uploads/../fixture.png",
		"https://m.groupme.com/uploads/fixture.png?secret=x", "https://m.groupme.com/uploads/fixture.png#fragment",
	} {
		_, err := client.Send(t.Context(), "g1", SendInput{SourceGUID: "op1", Attachments: []Attachment{{Type: "image", URL: raw}}})
		require.True(t, ErrInvalid.Is(err), raw)
		_, err = client.AddAlbumMedia(t.Context(), "g1", "a1", []AlbumMediaInput{{MediaType: "image", MediaSource: "album", MediaURL: raw}})
		require.True(t, ErrInvalid.Is(err), raw)
	}
	require.Zero(t, calls, "invalid media types must not cause a provider mutation")
	require.True(t, ValidateSend(SendInput{SourceGUID: "op1", Attachments: []Attachment{{Type: "video", URL: "https://m.groupme.com/uploads/fixture/original.mp4", PreviewURL: "https://m.groupme.com/uploads/fixture/thumbnail.jpg"}}}))
}

func TestReviewNativeListsDoNotAdvertiseUnsupportedContinuation(t *testing.T) {
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "api.groupme.com", r.URL.Host)
		if strings.HasSuffix(r.URL.Path, "/poll/g1") {
			require.Empty(t, r.URL.RawQuery)
			return featureResponse(200, `{"meta":{"code":20000},"response":{"polls":[{"data":{"id":"p1","conversation_id":"g1"}}],"continuation_token":"unsupported-poll-cursor"}}`), nil
		}
		require.Equal(t, "/v3/conversations/g1/albums", r.URL.Path)
		require.Equal(t, "per_page=50", r.URL.RawQuery)
		return featureResponse(200, `{"meta":{"code":200},"response":{"albums":[{"album_id":"a1","conversation_id":"g1"}],"next_cursor":"unsupported-album-cursor"}}`), nil
	})
	t.Run("polls", func(t *testing.T) {
		polls, err := client.Polls(t.Context(), "g1")
		require.NoError(t, err)
		require.Len(t, polls.Items, 1)
		require.Empty(t, polls.ContinuationToken)
	})
	t.Run("albums", func(t *testing.T) {
		albums, err := client.Albums(t.Context(), "g1")
		require.NoError(t, err)
		require.Len(t, albums.Items, 1)
		require.Empty(t, albums.NextCursor)
	})
	require.Equal(t, 2, calls, "native lists perform exactly one bounded call each")
}

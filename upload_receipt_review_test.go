package groupme

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type failingMP4Seeker struct {
	*bytes.Reader
	fail string
}

func (r *failingMP4Seeker) Seek(offset int64, whence int) (int64, error) {
	if r.fail == "current" && whence == io.SeekCurrent {
		return 0, errors.New("synthetic current seek failure")
	}
	if r.fail == "end" && whence == io.SeekEnd {
		end, err := r.Reader.Seek(offset, whence)
		if err != nil {
			return end, err
		}
		return end, errors.New("synthetic end seek failure")
	}
	if r.fail == "reset" && whence == io.SeekStart {
		return 0, errors.New("synthetic reset failure")
	}
	if r.fail == "wrong reset" && whence == io.SeekStart {
		return r.Reader.Seek(1, io.SeekStart)
	}
	return r.Reader.Seek(offset, whence)
}

func TestMP4SeekableUploadProvesExactLengthBeforeHTTP(t *testing.T) {
	payload := reviewMP4Fixture(t, "isom", "isom")
	for _, test := range []struct {
		name, failure string
		sizeDelta     int64
		extra         bool
	}{
		{name: "exact"}, {name: "reader shorter than declared", sizeDelta: 1}, {name: "reader longer than declared", extra: true},
		{name: "current seek failure", failure: "current"}, {name: "end seek failure", failure: "end"},
		{name: "reset failure", failure: "reset"}, {name: "incorrect reset position", failure: "wrong reset"},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := append([]byte(nil), payload...)
			if test.extra {
				data = append(data, []byte("extra bytes")...)
			}
			reader := &failingMP4Seeker{Reader: bytes.NewReader(data), fail: test.failure}
			client := New("fixture-token")
			calls := 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host == "m.groupme.com" {
					assert.Equal(t, http.MethodPost, r.Method)
					assert.Equal(t, "/uploads", r.URL.Path)
					return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/video.mp4?sig=synthetic","renderUrl":"https://m.groupme.com/uploads/fixture/original.mp4","thumbnailUrl":"https://m.groupme.com/uploads/fixture/thumbnail.jpg"}`), nil
				}
				assert.Equal(t, "cdn2.groupme.com", r.URL.Host)
				assert.Equal(t, http.MethodPut, r.Method)
				assert.Empty(t, r.Header.Get("X-Access-Token"))
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				if test.name == "exact" {
					assert.Equal(t, payload, body)
				}
				return featureResponse(201, ""), nil
			})
			attachment, err := client.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: reader, Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(payload)) + test.sizeDelta})
			if test.name == "exact" {
				require.NoError(t, err)
				require.Equal(t, "video", attachment.Type)
				require.Equal(t, 2, calls)
			} else {
				require.True(t, ErrInvalid.Is(err))
				require.Zero(t, calls, "invalid owned readers must fail before init POST or upload PUT")
				require.Equal(t, Attachment{}, attachment)
				if test.failure == "" || test.failure == "end" {
					position, seekErr := reader.Reader.Seek(0, io.SeekCurrent)
					require.NoError(t, seekErr)
					require.Zero(t, position, "validation restores the reader whenever its reset works")
				}
			}
		})
	}
}

func TestAlbumAddReceiptMatchesRequestedMediaMultiset(t *testing.T) {
	const firstURL = "https://m.groupme.com/uploads/fixture/first.png"
	const secondURL = "https://m.groupme.com/uploads/fixture/second.png"
	input := []AlbumMediaInput{{MediaURL: firstURL, MediaType: "image", MediaSource: "album"}, {MediaURL: secondURL, MediaType: "image", MediaSource: "album"}}
	first := AlbumMedia{MediaID: "first", MediaType: "image", MediaURL: firstURL, ConversationID: "g1", AlbumID: "a1"}
	second := AlbumMedia{MediaID: "second", MediaType: "image", MediaURL: secondURL, ConversationID: "g1", AlbumID: "a1"}
	for _, name := range []string{"exact", "reordered", "partial", "substituted URL", "wrong type", "duplicated media", "duplicated ID", "foreign scope", "unsafe origin"} {
		t.Run(name, func(t *testing.T) {
			receipt := []AlbumMedia{first, second}
			switch name {
			case "reordered":
				receipt[0], receipt[1] = receipt[1], receipt[0]
			case "partial":
				receipt = receipt[:1]
			case "substituted URL":
				receipt[1].MediaURL = "https://m.groupme.com/uploads/fixture/other.png"
			case "wrong type":
				receipt[1].MediaType = "video"
			case "duplicated media":
				receipt[1] = first
				receipt[1].MediaID = "duplicate"
			case "duplicated ID":
				receipt[1].MediaID = first.MediaID
			case "foreign scope":
				receipt[1].AlbumID = "other"
			case "unsafe origin":
				receipt[1].MediaURL = "https://untrusted.invalid/second.png"
			}
			client := New("fixture-token")
			calls := 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, "api.groupme.com", r.URL.Host)
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "/v3/conversations/g1/albums/media", r.URL.Path)
				assert.Equal(t, "a1", r.URL.Query().Get("album_id"))
				assert.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
				var sent []AlbumMediaInput
				if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
					return nil, err
				}
				assert.Equal(t, input, sent)
				body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": receipt})
				if err != nil {
					return nil, err
				}
				return featureResponse(200, string(body)), nil
			})
			out, err := client.AddAlbumMedia(t.Context(), "g1", "a1", input)
			require.Equal(t, 1, calls, "an ambiguous accepted response must not cause another mutation")
			if name == "exact" || name == "reordered" {
				require.NoError(t, err)
				require.Equal(t, receipt, out)
			} else {
				require.True(t, ErrUncertain.Is(err))
				require.Empty(t, out)
			}
		})
	}
}

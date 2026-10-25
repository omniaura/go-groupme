package groupme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPreparedVideoValidatesImagePreviewAndConsumesOnce(t *testing.T) {
	for _, preview := range []string{"https://m.groupme.com/uploads/fixture/thumbnail.jpg", "https://i.groupme.com/synthetic-preview", "https://m.groupme.com/uploads/fixture/preview.mp4", "https://m.groupme.com/uploads/fixture/arbitrary"} {
		t.Run(preview, func(t *testing.T) {
			valid := validImageURL(preview)
			input := SendInput{SourceGUID: "intent", Text: "caption", Attachments: []Attachment{{Type: "video", URL: "https://m.groupme.com/uploads/fixture/original.mp4", PreviewURL: preview}}}
			require.Equal(t, valid, ValidateSend(input))
			payload := reviewMP4Fixture(t, "isom", "isom")
			client := New("fixture-token")
			initializations, puts := 0, 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "m.groupme.com" {
					initializations++
					return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/video.mp4?sig=synthetic-secret&spr=https","renderUrl":"https://m.groupme.com/uploads/fixture/original.mp4","thumbnailUrl":"`+preview+`"}`), nil
				}
				require.Equal(t, "cdn2.groupme.com", r.URL.Host)
				puts++
				got, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, payload, got)
				require.Empty(t, r.Header.Get("X-Access-Token"))
				return featureResponse(201, ""), nil
			})
			video, err := client.PrepareVideoUpload(t.Context(), "g1", "self", UploadInput{Data: bytes.NewReader(payload), Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(payload))})
			require.Equal(t, 1, initializations)
			require.Zero(t, puts)
			if !valid {
				require.True(t, ErrUncertain.Is(err))
				require.Nil(t, video)
				return
			}
			require.NoError(t, err)
			defer video.Close()
			require.Equal(t, input.Attachments[0], video.Attachment())
			encoded, err := json.Marshal(video)
			require.NoError(t, err)
			copied := *video
			encodedCopy, err := json.Marshal(copied)
			require.NoError(t, err)
			for _, text := range []string{string(encoded), string(encodedCopy), fmt.Sprintf("%+v", video), fmt.Sprintf("%#v", video), fmt.Sprintf("%+v", copied), fmt.Sprintf("%#v", copied)} {
				require.NotContains(t, text, "synthetic-secret")
				require.NotContains(t, text, "cdn2.groupme.com")
			}
			attachment, err := client.UploadPreparedVideo(t.Context(), video)
			require.NoError(t, err)
			require.Equal(t, input.Attachments[0], attachment)
			_, err = client.UploadPreparedVideo(t.Context(), video)
			require.True(t, ErrInvalid.Is(err))
			_, err = client.UploadPreparedVideo(t.Context(), &copied)
			require.True(t, ErrInvalid.Is(err), "a copied handle must share its consumed state")
			require.Equal(t, 1, puts)
			video.Close()
			video.Close()
		})
	}
}

func TestPreparedVideoCannotTransferWithAnotherCredential(t *testing.T) {
	payload := reviewMP4Fixture(t, "isom", "isom")
	owner := New("owner-token")
	other := New("other-token")
	puts := 0
	transport := featureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "m.groupme.com" {
			return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/video.mp4?sig=synthetic&spr=https","renderUrl":"https://m.groupme.com/uploads/fixture/original.mp4","thumbnailUrl":"https://m.groupme.com/uploads/fixture/thumbnail.jpg"}`), nil
		}
		puts++
		return featureResponse(201, ""), nil
	})
	owner.httpClient.Transport, other.httpClient.Transport = transport, transport
	video, err := owner.PrepareVideoUpload(t.Context(), "g1", "self", UploadInput{Data: bytes.NewReader(payload), Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(payload))})
	require.NoError(t, err)
	defer video.Close()
	_, err = other.UploadPreparedVideo(t.Context(), video)
	require.Error(t, err)
	require.Zero(t, puts)
	_, err = owner.UploadPreparedVideo(t.Context(), video)
	require.Error(t, err)
	require.Zero(t, puts, "an attempted capability is single-use even on rejection")
}

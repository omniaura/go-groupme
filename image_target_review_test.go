package groupme

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageInitializationRejectsNonImageRenderTargetsBeforeTransfer(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	for _, test := range []struct {
		render string
		valid  bool
	}{
		{"https://i.groupme.com/synthetic-image", true},
		{"https://m.groupme.com/uploads/fixture/original.png", true},
		{"https://v.groupme.com/a", false},
		{"https://m.groupme.com/uploads/fixture/original.mp4", false},
		{"https://m.groupme.com/uploads/fixture/unsupported", false},
		{"https://external.invalid/image.png", false},
		{"https://user@i.groupme.com/image", false},
		{"https://i.groupme.com:443/image", false},
		{"https://i.groupme.com/image?query=value", false},
	} {
		t.Run(test.render, func(t *testing.T) {
			client := New("synthetic-token")
			initializations, puts := 0, 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == "m.groupme.com" {
					initializations++
					return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/image.png?sig=synthetic&spr=https","renderUrl":"`+test.render+`"}`), nil
				}
				require.Equal(t, "cdn2.groupme.com", r.URL.Host)
				puts++
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, encoded.Bytes(), body)
				return featureResponse(201, ""), nil
			})
			attachment, err := client.UploadAttachment(t.Context(), "g1", "self", UploadInput{Data: bytes.NewReader(encoded.Bytes()), Name: "fixture.png", ContentType: "image/png", Size: int64(encoded.Len())})
			require.Equal(t, 1, initializations)
			if test.valid {
				require.NoError(t, err)
				require.Equal(t, 1, puts)
				require.True(t, ValidateSend(SendInput{SourceGUID: "intent", Attachments: []Attachment{attachment}}))
			} else {
				require.True(t, ErrUncertain.Is(err))
				require.Zero(t, puts, "typed initialization must reject before any file bytes reach SAS storage")
			}
		})
	}
}

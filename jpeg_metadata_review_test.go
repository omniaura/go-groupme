package groupme

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"testing"

	"github.com/omniaura/xgo/runtime/mock"
	"github.com/stretchr/testify/require"
)

func TestJPEGUploadReadsMetadataFromBoundedSpool(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil))
	metadata := []byte{0xff, 0xd8}
	for range 2 {
		// An APP segment's length includes its two length bytes. Two valid
		// segments put SOF beyond 64KiB without changing the encoded image.
		metadata = append(metadata, 0xff, 0xe1, 0xff, 0xff)
		metadata = append(metadata, make([]byte, 65533)...)
	}
	metadata = append(metadata, encoded.Bytes()[2:]...)
	for _, name := range []string{"ordinary", "large APP metadata", "truncated APP", "format mismatch"} {
		t.Run(name, func(t *testing.T) {
			body, kind := encoded.Bytes(), "image/jpeg"
			switch name {
			case "large APP metadata":
				body = metadata
			case "truncated APP":
				body = metadata[:70<<10]
			case "format mismatch":
				kind = "image/png"
			}
			initializations, puts := 0, 0
			client := New("synthetic-token")
			client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Host {
				case "m.groupme.com":
					initializations++
					require.Equal(t, http.MethodPost, request.Method)
					var input InitializeUploadInput
					require.NoError(t, json.NewDecoder(request.Body).Decode(&input))
					require.Equal(t, 2, input.Width)
					require.Equal(t, 3, input.Height)
					require.Equal(t, int64(len(body)), input.FileSize)
					return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/image.jpg?sig=synthetic&spr=https","renderUrl":"https://m.groupme.com/uploads/fixture/image.jpg"}`), nil
				case "cdn2.groupme.com":
					puts++
					require.Equal(t, http.MethodPut, request.Method)
					require.Empty(t, request.Header.Get("X-Access-Token"))
					received, err := io.ReadAll(request.Body)
					require.NoError(t, err)
					require.Equal(t, body, received, "config parsing must reset the complete exact spool before PUT")
					return featureResponse(201, ""), nil
				default:
					t.Fatalf("unexpected synthetic upload host %s", request.URL.Host)
					return nil, nil
				}
			})
			reader := struct{ io.Reader }{bytes.NewReader(body)}
			attachment, err := client.UploadAttachment(t.Context(), "group", "self", UploadInput{Data: reader, Name: "fixture.jpg", ContentType: kind, Size: int64(len(body))})
			if name == "ordinary" || name == "large APP metadata" {
				require.NoError(t, err)
				require.Equal(t, "image", attachment.Type)
				require.Equal(t, 1, initializations)
				require.Equal(t, 1, puts)
			} else {
				require.True(t, ErrInvalid.Is(err), "%v", err)
				require.Zero(t, initializations)
				require.Zero(t, puts)
			}
		})
	}
}

type imageResetFailure struct{ *bytes.Reader }

func (*imageResetFailure) Seek(int64, int) (int64, error) {
	return 0, errors.New("synthetic image spool reset failure")
}

func TestImageSpoolResetFailurePreventsInitialization(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, jpeg.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3)), nil))
	cleanups := 0
	mock.Patch(spoolUploadReader, func(context.Context, io.Reader, int64, string) (io.Reader, func(), error) {
		return &imageResetFailure{bytes.NewReader(encoded.Bytes())}, func() { cleanups++ }, nil
	})
	calls := 0
	client := New("synthetic-token")
	client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected HTTP before reset")
	})
	_, err := client.UploadAttachment(t.Context(), "group", "self", UploadInput{Data: bytes.NewReader(encoded.Bytes()), Name: "fixture.jpg", ContentType: "image/jpeg", Size: int64(encoded.Len())})
	require.True(t, ErrInvalid.Is(err), "%v", err)
	require.Zero(t, calls)
	require.Equal(t, 1, cleanups)
}

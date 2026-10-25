package groupme

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/omniaura/go-kit/errs"
	"github.com/stretchr/testify/require"
)

func TestPaginationErrorsRetainCallerRef(t *testing.T) {
	for _, method := range []string{"groups", "chats"} {
		for _, page := range []int{0, 10001} {
			t.Run(fmt.Sprintf("%s/%d", method, page), func(t *testing.T) {
				ctx, ref := errs.WithNewRef(t.Context())
				client := New("fixture-token")
				var err error
				if method == "groups" {
					_, err = client.Groups(ctx, page, 10)
				} else {
					_, err = client.Chats(ctx, page, 10)
				}
				var typed *errs.Error
				require.ErrorAs(t, err, &typed)
				require.True(t, ErrInvalid.Is(err))
				require.Equal(t, ref, typed.Ref())
				require.Equal(t, errs.ActionFixInput, typed.ActionHint())
			})
		}
	}
}

func TestPreparedVideoAttachmentDoesNotWaitForUpload(t *testing.T) {
	client := New("fixture-token")
	payload := reviewMP4Fixture(t, "isom", "isom")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "m.groupme.com" {
			return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/video.mp4?sig=synthetic&spr=https","renderUrl":"https://m.groupme.com/uploads/fixture/original.mp4","thumbnailUrl":"https://m.groupme.com/uploads/fixture/thumbnail.jpg"}`), nil
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return nil, err
		}
		close(entered)
		<-release
		return featureResponse(201, ""), nil
	})
	video, err := client.PrepareVideoUpload(t.Context(), "g1", "self", UploadInput{Data: bytes.NewReader(payload), Name: "fixture.mp4", ContentType: "video/mp4", Size: int64(len(payload))})
	require.NoError(t, err)
	var uploadErr error
	go func() {
		_, uploadErr = client.UploadPreparedVideo(t.Context(), video)
		close(done)
	}()
	defer func() { unblock(); <-done; video.Close() }()
	select {
	case <-entered:
	case <-done:
		t.Fatalf("upload did not reach transport: %v", uploadErr)
	case <-time.After(2 * time.Second):
		t.Fatal("upload did not start")
	}
	metadata := make(chan Attachment, 1)
	go func() { metadata <- video.Attachment() }()
	select {
	case attachment := <-metadata:
		require.Equal(t, "https://m.groupme.com/uploads/fixture/original.mp4", attachment.URL)
		require.Equal(t, "https://m.groupme.com/uploads/fixture/thumbnail.jpg", attachment.PreviewURL)
	case <-time.After(time.Second):
		t.Fatal("immutable attachment metadata waited for the network upload")
	}
	unblock()
	<-done
	require.NoError(t, uploadErr)
}

func TestFileUploadUsesNativeWebPMIMESniffing(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			payload := make([]byte, 30)
			copy(payload, "RIFF")
			copy(payload[8:], "WEBPVP8X")
			if !valid {
				copy(payload[8:], "NOPEVP8X")
			}
			client := New("fixture-token")
			calls := 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				require.Equal(t, "image/webp", r.Header.Get("Content-Type"))
				return featureResponse(201, `{"status_url":"https://file.groupme.com/v1/g1/uploadStatus?job=fixture-job"}`), nil
			})
			_, err := client.UploadFile(t.Context(), "g1", UploadInput{Data: bytes.NewReader(payload), Name: "fixture.webp", ContentType: "image/webp", Size: int64(len(payload))})
			if valid {
				require.Equal(t, "image/webp", http.DetectContentType(payload))
				require.NoError(t, err)
				require.Equal(t, 1, calls)
			} else {
				require.True(t, ErrInvalid.Is(err))
				require.Zero(t, calls)
			}
		})
	}
}

func TestSearchRequiresExplicitSinglePageContinuation(t *testing.T) {
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
		calls++
		messages := make([]Message, 100)
		for i := range messages {
			messages[i] = Message{ID: fmt.Sprintf("m-%d", i), GroupID: "g1", Text: "needle"}
		}
		raw, err := json.Marshal(MessagePage{Messages: messages})
		if err != nil {
			return nil, err
		}
		return featureResponse(200, `{"meta":{"code":200},"response":`+string(raw)+`}`), nil
	})
	for _, budget := range []int{0, 2, 20} {
		_, err := client.Search(t.Context(), "g1", "", "needle", budget)
		require.True(t, ErrInvalid.Is(err))
	}
	require.Zero(t, calls)
	page, err := client.Search(t.Context(), "g1", "", "needle", 1)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, 100, page.Inspected)
	require.False(t, page.Complete)
	require.Equal(t, "m-99", page.NextBeforeID)
}

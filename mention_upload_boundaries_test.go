package groupme

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReviewMentionLociRequireCompleteUTF16Runes(t *testing.T) {
	for _, test := range []struct {
		text  string
		locus []int
		valid bool
	}{
		{"😀", []int{0, 2}, true},
		{"😀", []int{0, 1}, false},
		{"😀", []int{1, 1}, false},
		{"A😀B", []int{0, 1}, true},
		{"A😀B", []int{1, 2}, true},
		{"A😀B", []int{3, 1}, true},
		{"A😀B", []int{0, 4}, true},
		{"A😀B", []int{1, 1}, false},
		{"A😀B", []int{2, 1}, false},
		{"A😀B", []int{0, 2}, false},
		{"😀😀", []int{0, 4}, true},
		{"😀😀", []int{2, 2}, true},
		{"😀😀", []int{1, 2}, false},
		{"café @Amy", []int{5, 4}, true},
		{"😀 @Amy", []int{3, 4}, true},
		{"@Amy 😀 @Bo", []int{8, 3}, true},
		{"e\u0301", []int{1, 1}, true},
		{"😀", []int{2, 1}, false},
		{"😀", []int{0, 3}, false},
		{"😀", []int{-1, 1}, false},
		{"😀", []int{0, 0}, false},
		{"😀", []int{0}, false},
	} {
		in := SendInput{SourceGUID: "op1", Text: test.text, Attachments: []Attachment{{Type: "mentions", UserIDs: []string{"u1"}, Loci: [][]int{test.locus}}}}
		require.Equal(t, test.valid, ValidateSend(in), "text=%q locus=%v", test.text, test.locus)
		if !test.valid {
			client := New("fixture-token")
			calls := 0
			client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return featureResponse(500, ""), nil
			})
			_, err := client.Send(t.Context(), "g1", in)
			require.True(t, ErrInvalid.Is(err))
			require.Zero(t, calls)
		}
	}
}

func TestReviewGenericUploadHelperCannotDiscardPendingJob(t *testing.T) {
	for _, kind := range []string{"text/plain", "application/pdf", "image/svg+xml", "image/heic", "video/webm"} {
		t.Run(kind, func(t *testing.T) {
			client := New("fixture-token")
			calls := 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method == http.MethodGet {
					return featureResponse(200, `{"status":"pending"}`), nil
				}
				return featureResponse(201, `{"status_url":"https://file.groupme.com/v1/g1/uploadStatus?job=job1"}`), nil
			})
			attachment, err := client.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: strings.NewReader("fixture"), Name: "fixture.txt", ContentType: kind, Size: 7})
			require.True(t, ErrUnsupported.Is(err))
			require.Equal(t, Attachment{}, attachment)
			require.Zero(t, calls, "generic helper must reject before creating an asynchronous job")
		})
	}
}

func TestReviewSplitFileUploadRetainsJobUntilCompletion(t *testing.T) {
	for _, initial := range []string{"pending", "processing"} {
		t.Run(initial, func(t *testing.T) {
			client := New("fixture-token")
			posts, reads := 0, 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "file.groupme.com", r.URL.Host)
				require.Empty(t, r.URL.Query().Get("access_token"))
				if r.Method == http.MethodPost {
					posts++
					require.Equal(t, "/v1/g1/files", r.URL.Path)
					return featureResponse(201, `{"status_url":"https://file.groupme.com/v1/g1/uploadStatus?job=job1"}`), nil
				}
				reads++
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/v1/g1/uploadStatus", r.URL.Path)
				require.Equal(t, "job1", r.URL.Query().Get("job"))
				if reads == 1 {
					return featureResponse(200, `{"status":"`+initial+`"}`), nil
				}
				return featureResponse(200, `{"status":"completed","file_id":"file1"}`), nil
			})
			job, err := client.UploadFile(t.Context(), "g1", UploadInput{Data: strings.NewReader("fixture"), Name: "fixture.txt", ContentType: "text/plain", Size: 7})
			require.NoError(t, err)
			require.Equal(t, "job1", job.JobID)
			pending, err := client.FileUploadStatus(t.Context(), "g1", job.JobID)
			require.NoError(t, err)
			require.Equal(t, initial, pending.Status)
			require.Equal(t, job.JobID, pending.JobID)
			completed, err := client.FileUploadStatus(t.Context(), "g1", pending.JobID)
			require.NoError(t, err)
			require.Equal(t, job.JobID, completed.JobID)
			require.Equal(t, "file1", completed.FileID)
			require.Equal(t, 1, posts)
			require.Equal(t, 2, reads)
		})
	}
}

package groupme

import (
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileUploadStatusRecognizesBoundedPendingResponses(t *testing.T) {
	for _, status := range []string{"pending", "processing", "completed"} {
		t.Run(status, func(t *testing.T) {
			c := New("fixture-token")
			calls := 0
			c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, "file.groupme.com", r.URL.Host)
				assert.Equal(t, "/v1/u1+u2/uploadStatus", r.URL.Path)
				assert.Equal(t, "job1", r.URL.Query().Get("job"))
				assert.Equal(t, "0", r.URL.Query().Get("cnt"))
				assert.Len(t, r.URL.Query(), 2)
				assert.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
				assert.Empty(t, r.URL.Query().Get("access_token"))
				body := `{"status":"` + status + `"}`
				if status == "completed" {
					body = `{"status":"completed","file_id":"file1"}`
				}
				return featureResponse(200, body), nil
			})
			out, err := c.FileUploadStatus(t.Context(), "u1+u2", "job1")
			require.NoError(t, err)
			require.Equal(t, "job1", out.JobID)
			require.Equal(t, status, out.Status)
			if status == "completed" {
				require.Equal(t, "file1", out.FileID)
			} else {
				require.Empty(t, out.FileID)
			}
			require.Equal(t, 1, calls)
		})
	}
	for _, body := range []string{`{}`, `null`, `{"status":"queued"}`, `{"status":false}`, `{"status":"Pending"}`, `{"status":"pending","file_id":"file1"}`, `{"status":"completed"}`, `{"status":"completed","file_id":"../escape"}`, `{"status":"processing","job":"different"}`, `{"status":"pending","conversation_id":"other"}`, `{"status":"pending"} {}`, strings.Repeat("x", 4097)} {
		t.Run("invalid-"+body[:min(len(body), 24)], func(t *testing.T) {
			c := New("fixture-token")
			calls := 0
			c.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) { calls++; return featureResponse(200, body), nil })
			out, err := c.FileUploadStatus(t.Context(), "g1", "job1")
			require.True(t, ErrUnavailable.Is(err))
			require.Equal(t, FileUpload{}, out)
			require.Equal(t, 1, calls)
		})
	}
}

func TestRawFileUploadRequiresExactSizeBeforeHTTP(t *testing.T) {
	for _, test := range []struct {
		name, body string
		valid      bool
	}{
		{"exact", "fixture", true}, {"short", "fixtur", false}, {"extra bytes", "fixture-extra", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			t.Setenv("TMPDIR", directory)
			client := New("fixture-token")
			posts := 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				posts++
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "file.groupme.com", r.URL.Host)
				assert.Equal(t, "/v1/g1/files", r.URL.Path)
				assert.EqualValues(t, 7, r.ContentLength)
				data, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				assert.Equal(t, "fixture", string(data))
				return featureResponse(201, `{"status_url":"https://file.groupme.com/v1/g1/uploadStatus?job=job1"}`), nil
			})
			reader := strings.NewReader(test.body)
			job, err := client.UploadFile(t.Context(), "g1", UploadInput{Data: reader, Name: "fixture.txt", ContentType: "text/plain", Size: 7})
			if test.valid {
				require.NoError(t, err)
				require.Equal(t, "job1", job.JobID)
				require.Equal(t, 1, posts)
			} else {
				require.True(t, ErrInvalid.Is(err))
				require.Zero(t, posts, "a mismatched body must be rejected before the first POST")
				require.Equal(t, FileUpload{}, job)
				if len(test.body) > 7 {
					require.Equal(t, len(test.body)-8, reader.Len(), "reject after at most one extra byte")
				}
			}
			remaining, err := os.ReadDir(directory)
			require.NoError(t, err)
			require.Empty(t, remaining, "both rejected and successful reads release the private spool")
		})
	}
}

func TestRawFileUploadAcceptsBenignTextFormatsWithoutWeakeningMIMEGuards(t *testing.T) {
	for _, test := range []struct {
		name, kind, body, wantType string
	}{
		{"plain control", "text/plain; charset=utf-8", "hello world\n", "text/plain"},
		{"markdown artifact", "text/markdown; charset=utf-8", "# Trip plan\nMeet at noon.\n", "text/markdown"},
		{"CSV artifact", "text/csv; charset=utf-8", "name,count\nAlice,3\n", "text/csv"},
		{"JSON file", "application/json; charset=utf-8", `{"hello":"world"}`, "application/json"},
		{"active HTML", "text/html", "<!DOCTYPE html><html>fixture</html>", "application/octet-stream"},
		{"active script", "application/javascript", "alert('fixture')", "application/octet-stream"},
		{"mislabeled HTML", "text/plain", "<!DOCTYPE html><html>fixture</html>", "application/octet-stream"},
		{"HTML as markdown", "text/markdown", "<!DOCTYPE html><html>fixture</html>", ""},
		{"binary as CSV", "text/csv", "\x00\x01\x02", ""},
		{"text as image", "image/jpeg", "fixture", ""},
		{"header injection", "text/markdown\r\nX-Injected: yes", "fixture", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New("fixture-token")
			posts := 0
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				posts++
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, "https", r.URL.Scheme)
				assert.Equal(t, "file.groupme.com", r.URL.Host)
				assert.Equal(t, "/v1/g1/files", r.URL.Path)
				assert.Equal(t, test.wantType, r.Header.Get("Content-Type"))
				assert.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
				assert.Equal(t, "fixture.txt", r.URL.Query().Get("name"))
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				assert.Equal(t, test.body, string(body))
				assert.EqualValues(t, len(test.body), r.ContentLength)
				return featureResponse(201, `{"status_url":"https://file.groupme.com/v1/g1/uploadStatus?job=job1"}`), nil
			})
			job, err := client.UploadFile(t.Context(), "g1", UploadInput{Data: strings.NewReader(test.body), Name: "fixture.txt", ContentType: test.kind, Size: int64(len(test.body))})
			if test.wantType == "" {
				require.True(t, ErrInvalid.Is(err))
				require.Zero(t, posts, "mismatches must be rejected before HTTP")
				require.Equal(t, FileUpload{}, job)
			} else {
				require.NoError(t, err)
				require.Equal(t, "job1", job.JobID)
				require.Equal(t, 1, posts)
			}
		})
	}
}

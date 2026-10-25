package groupme

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImageUploadValidatesExactBodyBeforeInitialization(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))))
	payload := encoded.Bytes()
	for _, ordinary := range []bool{false, true} {
		for _, sizeCase := range []string{"exact", "extra", "short"} {
			name := sizeCase + "/seekable"
			if ordinary {
				name = sizeCase + "/ordinary"
			}
			t.Run(name, func(t *testing.T) {
				directory := t.TempDir()
				t.Setenv("TMPDIR", directory)
				body := append([]byte(nil), payload...)
				if sizeCase == "extra" {
					body = append(body, 'x')
				}
				if sizeCase == "short" {
					body = body[:len(body)-1]
				}
				var reader io.Reader = bytes.NewReader(body)
				if ordinary {
					reader = struct{ io.Reader }{reader}
				}
				initializations, puts := 0, 0
				server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.Method {
					case http.MethodPost:
						initializations++
						assert.Equal(t, "m.groupme.com", r.Host)
						assert.Equal(t, "/uploads", r.URL.Path)
						writeFixture(t, w, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/image.png?sig=synthetic&spr=https","renderUrl":"https://m.groupme.com/uploads/fixture/image.png"}`)
					case http.MethodPut:
						puts++
						assert.Equal(t, "cdn2.groupme.com", r.Host)
						assert.Empty(t, r.Header.Get("X-Access-Token"))
						received, err := io.ReadAll(r.Body)
						assert.NoError(t, err)
						assert.Equal(t, payload, received)
						w.WriteHeader(http.StatusCreated)
					default:
						t.Errorf("unexpected method %s", r.Method)
						w.WriteHeader(http.StatusBadRequest)
					}
				}))
				defer server.Close()
				local, err := url.Parse(server.URL)
				require.NoError(t, err)
				transport := server.Client().Transport
				c := New("synthetic-token")
				c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
					assert.Contains(t, []string{"m.groupme.com", "cdn2.groupme.com"}, r.URL.Host)
					request := r.Clone(r.Context())
					request.Host = r.URL.Host
					request.URL.Scheme, request.URL.Host = local.Scheme, local.Host
					// Only destination routing is replaced. The real Go HTTP transport
					// still enforces ContentLength and reads the actual request body.
					return transport.RoundTrip(request)
				})
				attachment, err := c.UploadAttachment(t.Context(), "g1", "u1", UploadInput{Data: reader, Name: "fixture.png", ContentType: "image/png", Size: int64(len(payload))})
				if sizeCase == "exact" {
					require.NoError(t, err)
					require.Equal(t, "image", attachment.Type)
					require.Equal(t, 1, initializations)
					require.Equal(t, 1, puts)
				} else {
					require.True(t, ErrInvalid.Is(err))
					require.Zero(t, initializations)
					require.Zero(t, puts)
				}
				files, err := os.ReadDir(directory)
				require.NoError(t, err)
				require.Empty(t, files, "all bounded temporary spools must be cleaned")
			})
		}
	}
}

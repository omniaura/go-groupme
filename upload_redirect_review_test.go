package groupme

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSASUploadIndependentlyDeniesRedirects(t *testing.T) {
	for _, policy := range []string{"constructor", "nil", "permissive"} {
		t.Run(policy, func(t *testing.T) {
			c := New("private-groupme-token")
			policyCalls := 0
			switch policy {
			case "nil":
				c.httpClient.CheckRedirect = nil
			case "permissive":
				c.httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { policyCalls++; return nil }
			}
			c.httpClient.Timeout = 7 * time.Second
			calls, outside, leaked := 0, 0, 0
			c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				switch r.URL.Host {
				case "m.groupme.com":
					assert.Equal(t, "private-groupme-token", r.Header.Get("X-Access-Token"))
					return featureResponse(200, `{"uploadUrl":"https://cdn2.groupme.com/uploads/fixture/image.png?sig=PRIVATE-SAS&spr=https","renderUrl":"https://m.groupme.com/uploads/fixture/image.png"}`), nil
				case "cdn2.groupme.com":
					assert.Equal(t, http.MethodPut, r.Method)
					assert.Empty(t, r.Header.Get("X-Access-Token"))
					response := featureResponse(302, "")
					response.Header.Set("Location", "https://external.invalid/redirect")
					return response, nil
				default:
					outside++
					if strings.Contains(r.Header.Get("Referer"), "PRIVATE-SAS") || r.Header.Get("X-Access-Token") != "" {
						leaked++
					}
					return featureResponse(200, ""), nil
				}
			})
			target, err := c.InitializeUpload(t.Context(), InitializeUploadInput{Extension: "png", SenderID: "self", GroupID: "group", FileSize: 3, Width: 1, Height: 1})
			require.NoError(t, err)
			err = c.Upload(t.Context(), target, "image/png", strings.NewReader("abc"), 3)
			assert.True(t, ErrUncertain.Is(err))
			assert.Equal(t, 2, calls)
			assert.Zero(t, outside, "SAS redirect destination must receive no request")
			assert.Zero(t, leaked)
			assert.Zero(t, policyCalls)
			assert.Equal(t, 7*time.Second, c.httpClient.Timeout)
			if policy == "nil" {
				assert.Nil(t, c.httpClient.CheckRedirect)
			}
			if policy == "permissive" {
				require.NoError(t, c.httpClient.CheckRedirect(nil, nil))
				assert.Equal(t, 1, policyCalls)
			}
		})
	}
}

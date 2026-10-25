package groupme

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateGroupResourcesRejectsDMBeforeHTTP(t *testing.T) {
	for _, kind := range []string{"poll", "event"} {
		t.Run(kind, func(t *testing.T) {
			client := New("fixture-token")
			calls := 0
			var requestPath string
			client.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				requestPath = r.URL.Path
				require.Equal(t, http.MethodPost, r.Method)
				if kind == "poll" {
					return featureResponse(200, `{"meta":{"code":20100},"response":{"poll":{"data":{"id":"p1","conversation_id":"g1"}},"message":{"id":"m1","group_id":"g1"}}}`), nil
				}
				return featureResponse(200, `{"meta":{"code":200},"response":{"event":{"event_id":"e1","conversation_id":"g1"},"message":{"id":"m1","group_id":"g1"}}}`), nil
			})
			create := func(cid string) error {
				if kind == "poll" {
					_, err := client.CreatePoll(t.Context(), cid, CreatePollInput{Subject: "fixture", Options: []PollOptionInput{{Title: "yes"}, {Title: "no"}}, Expiration: 2000000000, Visibility: "public", Type: "single"})
					return err
				}
				_, err := client.CreateEvent(t.Context(), cid, CreateEventInput{Name: "fixture", StartAt: "2030-01-01T12:00:00Z", EndAt: "2030-01-01T13:00:00Z", Timezone: "UTC"})
				return err
			}
			err := create("u1+u2")
			require.Zero(t, calls, "unverified DM feature writes must fail before posting")
			require.True(t, ErrInvalid.Is(err))
			require.NoError(t, create("g1"))
			require.Equal(t, 1, calls, "verified group creation remains available")
			if kind == "poll" {
				require.Equal(t, "/v3/poll/g1", requestPath)
			} else {
				require.Equal(t, "/v3/conversations/g1/events/create", requestPath)
			}
		})
	}
}

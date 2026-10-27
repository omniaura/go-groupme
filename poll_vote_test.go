package groupme

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func pollVoteFixture(t *testing.T, mode string, votes []string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"meta": map[string]int{"code": 20000},
		"response": map[string]any{"poll": map[string]any{
			"data":       Poll{ID: "p1", ConversationID: "g1", Type: mode, Options: []PollOption{{ID: "1"}, {ID: "2"}}},
			"user_votes": votes,
		}},
	})
	require.NoError(t, err)
	return string(body)
}

func TestVotePollCanonicalModeRoutes(t *testing.T) {
	for _, test := range []struct {
		name, mode, path string
		votes            []string
	}{
		{"single", "single", "/v3/poll/g1/p1/1", []string{"1"}},
		{"multi one selection", "multi", "/v3/poll/g1/p1", []string{"1"}},
		{"multi two selections", "multi", "/v3/poll/g1/p1", []string{"1", "2"}},
		{"multi clear", "multi", "/v3/poll/g1/p1", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New("fixture-token")
			reads, writes := 0, 0
			client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
				require.Equal(t, "api.groupme.com", request.URL.Host)
				if request.Method == http.MethodGet {
					reads++
					require.Equal(t, "/v3/poll/g1/p1", request.URL.Path)
					return featureResponse(200, pollVoteFixture(t, test.mode, nil)), nil
				}
				writes++
				require.Equal(t, 1, reads, "mode must be read before the only POST")
				require.Equal(t, http.MethodPost, request.Method)
				require.Equal(t, test.path, request.URL.Path)
				if test.mode == "single" {
					if request.Body != nil {
						body, err := io.ReadAll(request.Body)
						require.NoError(t, err)
						require.Empty(t, body, "native single-choice vote has no JSON body")
					}
					return featureResponse(200, `{"meta":{"code":20000},"response":{"poll":{"data":{"id":"p1","conversation_id":"g1","type":"single"},"user_vote":"1"}}}`), nil
				}
				var body struct {
					Votes []string `json:"votes"`
				}
				require.NoError(t, json.NewDecoder(request.Body).Decode(&body))
				require.NotNil(t, body.Votes, "clear is an explicit JSON array")
				require.Equal(t, append([]string{}, test.votes...), body.Votes)
				return featureResponse(200, pollVoteFixture(t, test.mode, body.Votes)), nil
			})
			poll, err := client.VotePoll(t.Context(), "g1", "p1", test.votes)
			require.NoError(t, err)
			require.Equal(t, 1, reads)
			require.Equal(t, 1, writes)
			require.Equal(t, append([]string{}, test.votes...), poll.UserVotes)
		})
	}
}

func TestVotePollRejectsBeforeMutation(t *testing.T) {
	for _, test := range []struct {
		name, mode, readBody string
		votes                []string
		readStatus, reads    int
		invalid              bool
	}{
		{name: "duplicate", votes: []string{"1", "1"}, invalid: true},
		{name: "invalid option syntax", votes: []string{"../1"}, invalid: true},
		{name: "too many", votes: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, invalid: true},
		{name: "single empty", mode: "single", reads: 1, invalid: true},
		{name: "single multiple", mode: "single", votes: []string{"1", "2"}, reads: 1, invalid: true},
		{name: "foreign option", mode: "multi", votes: []string{"3"}, reads: 1, invalid: true},
		{name: "unknown mode", mode: "future", votes: []string{"1"}, reads: 1},
		{name: "read failure", readStatus: 503, reads: 1},
		{name: "wrong poll", readBody: `{"meta":{"code":20000},"response":{"poll":{"data":{"id":"p2","conversation_id":"g1","type":"single"}}}}`, votes: []string{"1"}, reads: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New("fixture-token")
			reads, writes := 0, 0
			client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet {
					writes++
					return featureResponse(503, `{"private":"not returned"}`), nil
				}
				reads++
				status := test.readStatus
				if status == 0 {
					status = 200
				}
				body := test.readBody
				if body == "" {
					body = pollVoteFixture(t, test.mode, nil)
				}
				return featureResponse(status, body), nil
			})
			_, err := client.VotePoll(t.Context(), "g1", "p1", test.votes)
			require.Error(t, err)
			if test.invalid {
				require.True(t, ErrInvalid.Is(err))
			} else {
				require.True(t, ErrUnavailable.Is(err))
			}
			require.Equal(t, test.reads, reads)
			require.Zero(t, writes)
		})
	}
}

func TestVotePollUncertainWriteNeverFallsBack(t *testing.T) {
	for _, mode := range []string{"single", "multi"} {
		t.Run(mode, func(t *testing.T) {
			client := New("fixture-token")
			reads, writes := 0, 0
			client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
				if request.Method == http.MethodGet {
					reads++
					return featureResponse(200, pollVoteFixture(t, mode, nil)), nil
				}
				writes++
				return featureResponse(503, `{"private":"fixture-token private content"}`), nil
			})
			_, err := client.VotePoll(t.Context(), "g1", "p1", []string{"1"})
			require.True(t, ErrUncertain.Is(err))
			require.NotContains(t, err.Error(), "fixture-token")
			require.Equal(t, 1, reads)
			require.Equal(t, 1, writes)
		})
	}
}

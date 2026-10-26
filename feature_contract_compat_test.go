package groupme

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPollScalarSelectionKeepsExplicitArrayPrecedence(t *testing.T) {
	for _, test := range []struct {
		name      string
		selection string
		want      []string
	}{
		{"scalar", `"user_vote":"o1"`, []string{"o1"}},
		{"array", `"user_vote":"stale","user_votes":["o2"]`, []string{"o2"}},
		{"cleared array", `"user_vote":"stale","user_votes":[]`, []string{}},
		{"null array", `"user_vote":"o1","user_votes":null`, []string{"o1"}},
		{"no selection", `"user_vote":""`, nil},
	} {
		for _, operation := range []string{"read", "list"} {
			t.Run(test.name+"/"+operation, func(t *testing.T) {
				client := New("fixture-token")
				calls := 0
				client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
					calls++
					require.Equal(t, http.MethodGet, request.Method)
					require.Equal(t, "fixture-token", request.Header.Get("X-Access-Token"))
					wire := `{"data":{"id":"p1","conversation_id":"g1"},` + test.selection + `}`
					response := `{"poll":` + wire + `}`
					if operation == "list" {
						require.Equal(t, "/v3/poll/g1", request.URL.Path)
						response = `{"polls":[` + wire + `]}`
					} else {
						require.Equal(t, "/v3/poll/g1/p1", request.URL.Path)
					}
					return featureResponse(200, `{"meta":{"code":20000},"response":`+response+`}`), nil
				})
				var got Poll
				if operation == "list" {
					page, err := client.Polls(t.Context(), "g1")
					require.NoError(t, err)
					require.Len(t, page.Items, 1)
					got = page.Items[0]
				} else {
					var err error
					got, err = client.Poll(t.Context(), "g1", "p1")
					require.NoError(t, err)
				}
				require.Equal(t, test.want, got.UserVotes)
				require.Equal(t, 1, calls)
			})
		}
	}
}

func TestGalleryPageUsesExplicitTimestampWindowAndOneRequest(t *testing.T) {
	const before = "2030-01-03T12:00:00.9999Z"
	const after = "2030-01-01T12:00:00Z"
	const oldest = "2030-01-02T12:00:00.1234Z"
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/v3/conversations/g1/gallery", request.URL.Path)
		require.Equal(t, "fixture-token", request.Header.Get("X-Access-Token"))
		wantBefore := before
		messages := []Message{
			{ID: "newer", GroupID: "g1", GalleryTS: "2030-01-03T12:00:00.1Z"},
			{ID: "older", GroupID: "g1", GalleryTS: oldest},
		}
		if calls == 2 {
			wantBefore = oldest
			messages = []Message{{ID: "last", GroupID: "g1", GalleryTS: "2030-01-02T12:00:00.1233Z"}}
		}
		require.Equal(t, url.Values{"acceptFiles": {"1"}, "limit": {"2"}, "before": {wantBefore}, "after": {after}}, request.URL.Query())
		body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": messages}})
		require.NoError(t, err)
		return featureResponse(200, string(body)), nil
	})
	page, err := client.GalleryPage(t.Context(), "g1", GalleryOptions{Before: before, After: after, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, 1, calls, "a full result must not trigger an implicit second page")
	require.Equal(t, 2, page.Count)
	require.True(t, page.HasMore)
	require.Equal(t, oldest, page.NextBeforeTimestamp)
	require.True(t, page.ContinuationMaySkipTimestampTies, "distinct visible timestamps cannot prove boundary uniqueness")
	wire, err := json.Marshal(page)
	require.NoError(t, err)
	require.Contains(t, string(wire), `"continuation_may_skip_timestamp_ties":true`)
	require.Equal(t, oldest, page.Messages[1].GalleryTS)
	last, err := client.GalleryPage(t.Context(), "g1", GalleryOptions{Before: page.NextBeforeTimestamp, After: after, Limit: 2})
	require.NoError(t, err)
	require.Equal(t, 2, calls, "older media needs a separate explicit call")
	require.False(t, last.HasMore)
	require.Empty(t, last.NextBeforeTimestamp)
	require.False(t, last.ContinuationMaySkipTimestampTies)
}

func TestGalleryPageRetainsDistinctMessagesWithEqualTimestamps(t *testing.T) {
	const timestamp = "2030-01-02T12:00:00.1234Z"
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/v3/conversations/g1/gallery", request.URL.Path)
		require.Equal(t, url.Values{"acceptFiles": {"1"}, "limit": {"2"}}, request.URL.Query())
		messages := []Message{
			{ID: "m1", GroupID: "g1", GalleryTS: timestamp},
			{ID: "m2", GroupID: "g1", GalleryTS: timestamp},
		}
		body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": messages}})
		require.NoError(t, err)
		return featureResponse(200, string(body)), nil
	})
	page, err := client.GalleryPage(t.Context(), "g1", GalleryOptions{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, 1, calls, "timestamp ties must not trigger implicit requests")
	require.Equal(t, 2, page.Count)
	require.Equal(t, []Message{
		{ID: "m1", GroupID: "g1", GalleryTS: timestamp},
		{ID: "m2", GroupID: "g1", GalleryTS: timestamp},
	}, page.Messages)
	require.True(t, page.HasMore)
	require.Equal(t, timestamp, page.NextBeforeTimestamp, "preserve the native timestamp without fabricated tie breakers")
	require.True(t, page.ContinuationMaySkipTimestampTies)
}

func TestGalleryPageSurfacesSyntheticBoundarySplitUncertainty(t *testing.T) {
	const boundary = "2030-01-02T12:00:00.1234Z"
	// A timestamp-only provider can split equal-time rows across a full-page
	// boundary. This fixture demonstrates the limitation, not live behavior.
	dataset := []Message{
		{ID: "newer", GroupID: "g1", GalleryTS: "2030-01-03T00:00:00Z"},
		{ID: "visible-tie", GroupID: "g1", GalleryTS: boundary},
		{ID: "unseen-tie", GroupID: "g1", GalleryTS: boundary},
		{ID: "older", GroupID: "g1", GalleryTS: "2030-01-01T00:00:00Z"},
	}
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, http.MethodGet, request.Method)
		require.Equal(t, "/v3/conversations/g1/gallery", request.URL.Path)
		messages := dataset[:2]
		query := url.Values{"acceptFiles": {"1"}, "limit": {"2"}}
		if calls == 2 {
			query.Set("before", boundary)
			messages = dataset[3:]
		}
		require.Equal(t, query, request.URL.Query())
		body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": messages}})
		require.NoError(t, err)
		return featureResponse(200, string(body)), nil
	})
	first, err := client.GalleryPage(t.Context(), "g1", GalleryOptions{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, dataset[:2], first.Messages)
	require.Equal(t, boundary, first.NextBeforeTimestamp)
	require.True(t, first.ContinuationMaySkipTimestampTies)
	last, err := client.GalleryPage(t.Context(), "g1", GalleryOptions{Limit: 2, Before: first.NextBeforeTimestamp})
	require.NoError(t, err)
	require.Equal(t, 2, calls, "each explicit call makes exactly one request")
	require.Equal(t, dataset[3:], last.Messages, "strict timestamp continuation can omit the unseen boundary tie")
	require.Empty(t, last.NextBeforeTimestamp)
	require.False(t, last.ContinuationMaySkipTimestampTies)
}

func TestGalleryPagePartialAndEmptyResultsHaveNoContinuationUncertainty(t *testing.T) {
	for _, count := range []int{0, 1} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			client := New("fixture-token")
			calls := 0
			client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
				calls++
				messages := []Message{}
				if count == 1 {
					messages = append(messages, Message{ID: "m1", GroupID: "g1", GalleryTS: "2030-01-01T00:00:00Z"})
				}
				body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": messages}})
				require.NoError(t, err)
				return featureResponse(200, string(body)), nil
			})
			page, err := client.GalleryPage(t.Context(), "g1", GalleryOptions{Limit: 2})
			require.NoError(t, err)
			require.Equal(t, 1, calls)
			require.Equal(t, count, page.Count)
			require.False(t, page.HasMore)
			require.Empty(t, page.NextBeforeTimestamp)
			require.False(t, page.ContinuationMaySkipTimestampTies)
			wire, err := json.Marshal(page)
			require.NoError(t, err)
			require.NotContains(t, string(wire), "continuation_may_skip_timestamp_ties")
		})
	}
}

func TestGalleryPageDefaultLimitAndConversationRepresentations(t *testing.T) {
	for _, cid := range []string{"g1", "u1+u2"} {
		for _, identity := range []string{"group_id", "conversation_id", "both"} {
			t.Run(cid+"/"+identity, func(t *testing.T) {
				client := New("fixture-token")
				calls := 0
				client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
					calls++
					require.Equal(t, "/v3/conversations/"+cid+"/gallery", request.URL.Path)
					require.Equal(t, url.Values{"acceptFiles": {"1"}, "limit": {"100"}}, request.URL.Query())
					message := Message{ID: "m1", GalleryTS: "2030-01-02T12:00:00.1234Z"}
					if identity == "group_id" || identity == "both" {
						message.GroupID = cid
					}
					if identity == "conversation_id" || identity == "both" {
						message.ConversationID = cid
					}
					body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": []Message{message}}})
					require.NoError(t, err)
					return featureResponse(200, string(body)), nil
				})
				page, err := client.GalleryPage(t.Context(), cid, GalleryOptions{})
				require.NoError(t, err)
				require.Equal(t, 1, calls)
				require.Equal(t, 1, page.Count)
				require.False(t, page.HasMore)
				require.Empty(t, page.NextBeforeTimestamp)
			})
		}
	}
}

func TestGalleryPageRejectsInvalidOptionsBeforeHTTP(t *testing.T) {
	for _, options := range []GalleryOptions{
		{Limit: -1}, {Limit: 101}, {Before: "message-id"}, {After: "2030-01-01"},
		{Before: "2030-02-30T00:00:00Z"}, {Before: "2030-01-01T00:00:00.1234567891Z"},
		{After: "2030-01-01T00:00:00+24:00"},
		{Before: "2030-01-01T00:00:00Z", After: "2030-01-01T00:00:00Z"},
		{Before: "2030-01-01T00:00:00Z", After: "2030-01-02T00:00:00Z"},
	} {
		t.Run(fmt.Sprintf("%+v", options), func(t *testing.T) {
			client := New("fixture-token")
			calls := 0
			client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return featureResponse(200, `{"meta":{"code":200},"response":{"messages":[]}}`), nil
			})
			_, err := client.GalleryPage(t.Context(), "g1", options)
			require.True(t, ErrInvalid.Is(err))
			require.Zero(t, calls)
		})
	}
}

func TestGalleryPageRejectsUnsafeProviderCursorOrIdentity(t *testing.T) {
	valid := Message{ID: "m1", GroupID: "g1", GalleryTS: "2030-01-02T00:00:00Z"}
	for _, test := range []struct {
		name     string
		cid      string
		options  GalleryOptions
		messages []Message
	}{
		{"missing timestamp", "g1", GalleryOptions{Limit: 1}, []Message{{ID: "m1", GroupID: "g1"}}},
		{"malformed timestamp", "g1", GalleryOptions{Limit: 1}, []Message{{ID: "m1", GroupID: "g1", GalleryTS: "message-id"}}},
		{"foreign group", "g2", GalleryOptions{Limit: 1}, []Message{valid}},
		{"contradictory group identity", "g1", GalleryOptions{Limit: 1}, []Message{{ID: "m1", GroupID: "g1", ConversationID: "g2", GalleryTS: valid.GalleryTS}}},
		{"foreign direct conversation", "u1+u2", GalleryOptions{Limit: 1}, []Message{{ID: "m1", ConversationID: "u1+u3", GalleryTS: valid.GalleryTS}}},
		{"contradictory direct identity", "u1+u2", GalleryOptions{Limit: 1}, []Message{{ID: "m1", GroupID: "u1+u2", ConversationID: "u1+u3", GalleryTS: valid.GalleryTS}}},
		{"missing conversation identity", "g1", GalleryOptions{Limit: 1}, []Message{{ID: "m1", GalleryTS: valid.GalleryTS}}},
		{"group row in direct conversation", "u1+u2", GalleryOptions{Limit: 1}, []Message{{ID: "m1", GroupID: "g1", ConversationID: "u1+u2", GalleryTS: valid.GalleryTS}}},
		{"duplicate messages", "g1", GalleryOptions{Limit: 2}, []Message{valid, valid}},
		{"oversized page", "g1", GalleryOptions{Limit: 1}, []Message{valid, {ID: "m2", GroupID: "g1", GalleryTS: "2030-01-01T00:00:00Z"}}},
		{"before boundary did not advance", "g1", GalleryOptions{Limit: 1, Before: valid.GalleryTS}, []Message{valid}},
		{"after boundary did not advance", "g1", GalleryOptions{Limit: 1, After: valid.GalleryTS}, []Message{valid}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := New("fixture-token")
			calls := 0
			client.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
				calls++
				body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": test.messages}})
				require.NoError(t, err)
				return featureResponse(200, string(body)), nil
			})
			page, err := client.GalleryPage(t.Context(), test.cid, test.options)
			require.True(t, ErrUnavailable.Is(err))
			require.Empty(t, page.Messages, "invalid responses must not expose a usable partial page")
			require.Empty(t, page.NextBeforeTimestamp)
			require.Equal(t, 1, calls)
		})
	}
}

func TestGalleryLegacyWrapperKeepsTimestampLessFirstPageCompatibility(t *testing.T) {
	client := New("fixture-token")
	calls := 0
	client.httpClient.Transport = featureTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		require.Equal(t, url.Values{"acceptFiles": {"1"}, "limit": {"100"}}, request.URL.Query())
		messages := make([]Message, 100)
		for index := range messages {
			messages[index] = Message{ID: "m" + strconv.Itoa(index), GroupID: "g1"}
		}
		body, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"messages": messages}})
		require.NoError(t, err)
		return featureResponse(200, string(body)), nil
	})
	page, err := client.Gallery(t.Context(), "g1")
	require.NoError(t, err)
	require.Equal(t, 100, page.Count)
	require.True(t, page.HasMore)
	require.Empty(t, page.NextBeforeID, "a gallery timestamp is not a message ID")
	require.Equal(t, 1, calls)
	_, err = client.GalleryPage(t.Context(), "g1", GalleryOptions{})
	require.True(t, ErrUnavailable.Is(err), "explicit paging cannot fabricate a cursor for timestamp-less rows")
	require.Equal(t, 2, calls)
}

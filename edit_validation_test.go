package groupme

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEditRetainsNativeLegacyEmojiAndMovedMentionsHTTP(t *testing.T) {
	text := "Before 😀 " + strings.Repeat("x", 64) + "�Avery Robinson after"
	attachments := []Attachment{{Type: "mentions", UserIDs: []string{"peer-001"}, Loci: [][]int{{75, 14}}}, {Type: "emoji", Charmap: [][]int{{1, 52}}, Placeholder: "�"}}
	require.False(t, ValidateSend(SendInput{SourceGUID: "fresh-send", Text: text, Attachments: attachments}), "this contract must remain edit-only")
	c := New("fixture-token")
	calls := 0
	c.httpClient.Transport = featureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "/v4/groups/g1/messages/m1", r.URL.Path)
		assert.Equal(t, "fixture-token", r.Header.Get("X-Access-Token"))
		var in EditInput
		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		assert.Equal(t, text, in.Text)
		assert.Equal(t, attachments, in.Attachments)
		raw, err := json.Marshal(map[string]any{"meta": map[string]int{"code": 200}, "response": map[string]any{"message": Message{ID: "m1", GroupID: "g1", UserID: "u1", Text: in.Text, Attachments: in.Attachments, UpdatedAt: 123}}})
		require.NoError(t, err)
		return featureResponse(200, string(raw)), nil
	})
	message, err := c.EditMessage(t.Context(), "g1", "m1", EditInput{Text: text, Attachments: attachments})
	require.NoError(t, err)
	require.Equal(t, text, message.Text)
	require.Equal(t, attachments, message.Attachments)
	require.EqualValues(t, 123, message.UpdatedAt)
	require.Equal(t, 1, calls)
}
func TestEditRejectsMalformedLegacyEmojiBeforeHTTP(t *testing.T) {
	for _, attachment := range []Attachment{{Type: "emoji", Placeholder: "�", Charmap: [][]int{{1}}}, {Type: "emoji", Placeholder: "�", Charmap: [][]int{{-1, 52}}}, {Type: "emoji", Placeholder: "�", Charmap: [][]int{{1, 4096}}}, {Type: "emoji", Placeholder: "�", Charmap: [][]int{{1000001, 1}}}, {Type: "emoji", Placeholder: "�x", Charmap: [][]int{{1, 52}}}, {Type: "emoji", Placeholder: " ", Charmap: [][]int{{1, 52}}}, {Type: "emoji", Placeholder: "�", Charmap: [][]int{{1, 52}}, URL: "https://i.groupme.com/unapproved"}, {Type: "emoji", Placeholder: "�", Charmap: [][]int{{1, 52}, {1, 53}}}} {
		c := New("fixture-token")
		c.httpClient.Transport = featureTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("invalid retained attachment reached HTTP")
			return nil, nil
		})
		_, err := c.EditMessage(t.Context(), "g1", "m1", EditInput{Text: "one �", Attachments: []Attachment{attachment}})
		require.True(t, ErrInvalid.Is(err))
	}
}

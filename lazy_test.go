package groupme

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDirectHistoryUsesCounterpartAndExplicitCursor(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		assert.Equal(t, "/direct_messages", r.URL.Path)
		assert.Equal(t, "peer", r.URL.Query().Get("other_user_id"))
		assert.Equal(t, "100", r.URL.Query().Get("limit"))
		if r.URL.Query().Get("before_id") == "old" {
			w.WriteHeader(304)
			return
		}
		writeFixture(t, w, `{"meta":{"code":200},"response":{"direct_messages":[{"id":"old","user_id":"peer","recipient_id":"self","created_at":1,"avatar_url":"https://i.groupme.com/peer","name":"Synthetic","text":"Synthetic old DM"}]}}`)
	}))
	defer server.Close()
	cl := newClient(server.URL, "synthetic-token")
	page, err := cl.DirectMessages(t.Context(), "peer", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Messages, 1)
	require.True(t, page.HasMore, "DM endpoints can return fewer than the requested limit; only an empty page proves the end")
	require.Equal(t, "old", page.NextBeforeID)
	require.Equal(t, "https://i.groupme.com/peer", page.Messages[0].AvatarURL)
	require.Equal(t, 1, calls, "the SDK never follows its own cursor")
	empty, err := cl.DirectMessages(t.Context(), "peer", page.NextBeforeID, 100)
	require.NoError(t, err)
	require.Empty(t, empty.Messages)
	require.False(t, empty.HasMore)
	require.Equal(t, 2, calls)
}

func TestGroupMeImageOriginIsStrict(t *testing.T) {
	require.Equal(t, "https://i.groupme.com/photo", ImageURL("https://i.groupme.com/photo"))
	for _, source := range []string{"http://i.groupme.com/photo", "https://i.groupme.com.evil.invalid/photo", "https://i.groupme.com:443/photo", "https://user@i.groupme.com/photo", "https://i.groupme.com/photo?token=secret", "https://i.groupme.com/photo#fragment", "https://127.0.0.1/photo", "data:image/svg+xml,<svg/>", "//i.groupme.com/photo"} {
		require.Empty(t, ImageURL(source), source)
	}
}

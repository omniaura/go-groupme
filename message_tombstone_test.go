package groupme

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessageRecognizesProviderDeletionMarkers(t *testing.T) {
	for _, payload := range []string{
		`{"id":"message","hidden":true}`,
		`{"id":"message","deleted_at":"2026-09-16T00:28:08.0039Z","deletion_actor":"sender","text":"This message was deleted"}`,
	} {
		var message Message
		require.NoError(t, json.Unmarshal([]byte(payload), &message))
		require.True(t, message.IsDeleted())
	}
	for _, payload := range []string{
		`{"id":"message","hidden":false,"deleted_at":null}`,
		`{"id":"message","text":"This message was deleted"}`,
	} {
		var message Message
		require.NoError(t, json.Unmarshal([]byte(payload), &message))
		require.False(t, message.IsDeleted(), "text alone never proves a deletion")
	}
}

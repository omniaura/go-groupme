package groupme

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompatibilityReactionWireIdentifiers(t *testing.T) {
	for _, test := range []struct {
		name, raw, pack, index string
	}{
		{"numbers", `{"pack_id":12,"pack_index":0}`, "12", "0"},
		{"mixed", `{"pack_id":"12","pack_index":3}`, "12", "3"},
		{"existing-strings", `{"pack_id":"0012","pack_index":"03"}`, "0012", "03"},
		{"provider-string-extension", `{"pack_id":"future-pack","pack_index":"future-index"}`, "future-pack", "future-index"},
		{"uint64-boundary", `{"pack_id":18446744073709551615,"pack_index":18446744073709551615}`, "18446744073709551615", "18446744073709551615"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reaction Reaction
			require.NoError(t, json.Unmarshal([]byte(test.raw), &reaction))
			require.NotNil(t, reaction.PackID)
			require.NotNil(t, reaction.PackIndex)
			require.Equal(t, test.pack, *reaction.PackID)
			require.Equal(t, test.index, *reaction.PackIndex)
			encoded, err := json.Marshal(reaction)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(encoded, &fields))
			require.Equal(t, test.pack, fields["pack_id"], "marshalling keeps the public string representation")
			require.Equal(t, test.index, fields["pack_index"])
		})
	}
	for _, raw := range []string{`{}`, `{"pack_id":null,"pack_index":null}`} {
		var reaction Reaction
		require.NoError(t, json.Unmarshal([]byte(raw), &reaction))
		require.Nil(t, reaction.PackID)
		require.Nil(t, reaction.PackIndex)
	}
}

func TestCompatibilityReactionRejectsNonIntegralNumericIdentifiers(t *testing.T) {
	for _, field := range []string{"pack_id", "pack_index"} {
		for _, raw := range []string{"-1", "-0", "1.5", "1.0", "1e2", "18446744073709551616", "{}", "[]", "true"} {
			t.Run(field+"/"+raw, func(t *testing.T) {
				var reaction Reaction
				require.Error(t, json.Unmarshal([]byte(`{"`+field+`":`+raw+`}`), &reaction))
			})
		}
	}
}

func TestCompatibilityNumericReactionInMessagePreservesWriteBounds(t *testing.T) {
	var message Message
	require.NoError(t, json.Unmarshal([]byte(`{"id":"synthetic-message","reactions":[{"type":"emoji","pack_id":12,"pack_index":0,"user_ids":["synthetic-peer"]}]}`), &message))
	require.Len(t, message.Reactions, 1)
	require.Equal(t, []string{"synthetic-peer"}, message.Reactions[0].UserIDs)
	require.Equal(t, "12", *message.Reactions[0].PackID)
	require.Equal(t, "0", *message.Reactions[0].PackIndex)
	var large Reaction
	require.NoError(t, json.Unmarshal([]byte(`{"pack_id":18446744073709551615,"pack_index":0}`), &large))
	require.False(t, validReactionInput(ReactionInput{Type: "emoji", PackID: large.PackID, PackIndex: large.PackIndex}), "tolerant reads do not relax write bounds")
}

func TestCompatibilityEventLocationCoordinateShapes(t *testing.T) {
	for _, test := range []struct {
		name, raw, expected string
	}{
		{"numeric", `{"name":"Synthetic place","address":"123 Synthetic Lane","lat":11.25,"lng":-22.5}`, `{"name":"Synthetic place","address":"123 Synthetic Lane","lat":"11.25","lng":"-22.5"}`},
		{"numeric-string", `{"name":"Synthetic place","lat":"11.2500","lng":"-22.5000"}`, `{"name":"Synthetic place","lat":"11.2500","lng":"-22.5000"}`},
		{"mixed-zero", `{"name":"Synthetic place","lat":0,"lng":"0.00"}`, `{"name":"Synthetic place","lat":"0","lng":"0.00"}`},
		{"omitted", `{"name":"Synthetic place"}`, `{"name":"Synthetic place"}`},
		{"null", `{"name":"Synthetic place","lat":null,"lng":null}`, `{"name":"Synthetic place"}`},
		{"empty", `{"name":"","lat":"","lng":""}`, `{"name":""}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var location EventLocation
			require.NoError(t, json.Unmarshal([]byte(test.raw), &location))
			encoded, err := json.Marshal(location)
			require.NoError(t, err)
			require.JSONEq(t, test.expected, string(encoded))
		})
	}
}

func TestCompatibilityEventLocationRejectsInvalidCoordinates(t *testing.T) {
	for _, field := range []string{"lat", "lng"} {
		for _, raw := range []string{`{}`, `[]`, `true`, `"not-a-number"`, `"NaN"`, `"+Inf"`, `1e1000`} {
			t.Run(field+"/"+raw, func(t *testing.T) {
				var location EventLocation
				require.Error(t, json.Unmarshal([]byte(`{"name":"Synthetic place","`+field+`":`+raw+`}`), &location))
			})
		}
	}
}

func TestCompatibilityEventLocationReusedDestination(t *testing.T) {
	const initial = `{"name":"Synthetic place","address":"123 Synthetic Lane","lat":11.25,"lng":-22.5}`
	for _, test := range []struct {
		name, raw, expected string
	}{
		{"omitted-members", `{"name":"Renamed place"}`, `{"name":"Renamed place","address":"123 Synthetic Lane","lat":"11.25","lng":"-22.5"}`},
		{"latitude-only", `{"lat":12}`, `{"name":"Synthetic place","address":"123 Synthetic Lane","lat":"12","lng":"-22.5"}`},
		{"longitude-only", `{"lng":"-23.500"}`, `{"name":"Synthetic place","address":"123 Synthetic Lane","lat":"11.25","lng":"-23.500"}`},
		{"null-latitude", `{"lat":null}`, `{"name":"Synthetic place","address":"123 Synthetic Lane","lng":"-22.5"}`},
		{"null-longitude", `{"lng":null}`, `{"name":"Synthetic place","address":"123 Synthetic Lane","lat":"11.25"}`},
		{"null-coordinates", `{"lat":null,"lng":null}`, `{"name":"Synthetic place","address":"123 Synthetic Lane"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var location EventLocation
			require.NoError(t, json.Unmarshal([]byte(initial), &location))
			require.NoError(t, json.Unmarshal([]byte(test.raw), &location))
			encoded, err := json.Marshal(location)
			require.NoError(t, err)
			require.JSONEq(t, test.expected, string(encoded))
		})
	}
	for _, raw := range []string{
		`{"name":"Renamed place","lat":{}}`,
		`{"name":"Renamed place","lat":12,"lng":[]}`,
		`{"name":"Renamed place","lat":null,"lng":true}`,
	} {
		t.Run("invalid/"+raw, func(t *testing.T) {
			var location EventLocation
			require.NoError(t, json.Unmarshal([]byte(initial), &location))
			before := location
			require.Error(t, json.Unmarshal([]byte(raw), &location))
			require.Equal(t, before, location, "an invalid update must preserve the complete prior object")
		})
	}
}

func TestCompatibilityEventMetadataRoundTrip(t *testing.T) {
	var event CalendarEvent
	require.NoError(t, json.Unmarshal([]byte(`{"event_id":"synthetic-event","image_url":"https://i.groupme.com/synthetic-image","location":{"name":"Synthetic place","address":"123 Synthetic Lane","lat":11.25,"lng":-22.5}}`), &event))
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.JSONEq(t, `"https://i.groupme.com/synthetic-image"`, string(fields["image_url"]))
	require.JSONEq(t, `{"name":"Synthetic place","address":"123 Synthetic Lane","lat":"11.25","lng":"-22.5"}`, string(fields["location"]))
	var input CreateEventInput
	require.NoError(t, json.Unmarshal([]byte(`{"name":"Synthetic event","location":{"name":"Synthetic place"}}`), &input))
	encoded, err = json.Marshal(input.Location)
	require.NoError(t, err)
	require.JSONEq(t, `{"name":"Synthetic place"}`, string(encoded), "existing name-only write shape is unchanged")
	require.NoError(t, json.Unmarshal([]byte(`{"location":null}`), &event))
	require.Nil(t, event.Location)
}

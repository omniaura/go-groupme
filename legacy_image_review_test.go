package groupme

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyImageRejectsMP4ForImagesAndPreviews(t *testing.T) {
	for _, item := range []struct {
		url   string
		valid bool
	}{
		{"https://i.groupme.com/legacy.mp4", false},
		{"https://i.groupme.com/legacy.MP4", false},
		{"https://i.groupme.com/legacy%2Emp4", false},
		{"https://i.groupme.com/legacy.%6d%70%34", false},
		{"https://i.groupme.com/legacy", true},
		{"https://i.groupme.com/legacy.png", true},
		{"https://m.groupme.com/uploads/fixture/IMAGE.PNG", true},
		{"https://m.groupme.com/uploads/fixture/IMAGE.JpEg", true},
		{"https://m.groupme.com/uploads/fixture/IMAGE.GIF", true},
		{"https://m.groupme.com/uploads/fixture/IMAGE.WEBP", true},
		{"https://m.groupme.com/uploads/fixture/VIDEO.MP4", false},
	} {
		for _, kind := range []string{"image", "video preview"} {
			t.Run(item.url+"/"+kind, func(t *testing.T) {
				attachment := Attachment{Type: "image", URL: item.url}
				if kind == "video preview" {
					attachment = Attachment{Type: "video", URL: "https://m.groupme.com/uploads/fixture/video.mp4", PreviewURL: item.url}
				}
				require.Equal(t, item.valid, ValidateSend(SendInput{SourceGUID: "intent", Attachments: []Attachment{attachment}}))
			})
		}
	}
}

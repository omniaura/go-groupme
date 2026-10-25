package groupme

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// validateEdit admits the observed retained legacy emoji attachment only for
// editing. Fresh sends retain their existing stricter attachment contract.
func validateEdit(in EditInput) bool {
	if len(in.Attachments) > 4 {
		return false
	}
	ordinary := make([]Attachment, 0, len(in.Attachments))
	for _, attachment := range in.Attachments {
		if attachment.Type != "emoji" {
			ordinary = append(ordinary, attachment)
			continue
		}
		if !attachmentFields(attachment, "charmap", "placeholder") || !utf8.ValidString(attachment.Placeholder) || utf8.RuneCountInString(attachment.Placeholder) != 1 || len(attachment.Charmap) < 1 || len(attachment.Charmap) > MaxMessageCharacters || strings.Count(in.Text, attachment.Placeholder) != len(attachment.Charmap) {
			return false
		}
		for _, char := range attachment.Placeholder {
			if unicode.IsControl(char) || unicode.IsSpace(char) {
				return false
			}
		}
		for _, pair := range attachment.Charmap {
			if len(pair) != 2 || pair[0] < 0 || pair[0] > 1000000 || pair[1] < 0 || pair[1] > 4095 {
				return false
			}
		}
	}
	return ValidateSend(SendInput{SourceGUID: "edit-validation", Text: in.Text, Attachments: ordinary})
}

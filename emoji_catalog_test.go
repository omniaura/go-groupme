package groupme

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomEmojiCatalogMatchesNativeCountsRangesAndTitles(t *testing.T) {
	expected := []CustomEmojiPack{
		{PackID: "1", MinIndex: 0, MaxIndex: 83, Count: 84},
		{PackID: "2", MinIndex: 0, MaxIndex: 84, Count: 85},
		{PackID: "3", MinIndex: 0, MaxIndex: 100, Count: 101},
		{PackID: "4", MinIndex: 0, MaxIndex: 36, Count: 37},
		{PackID: "5", MinIndex: 0, MaxIndex: 33, Count: 34},
		{PackID: "9", MinIndex: 0, MaxIndex: 44, Count: 45},
		{PackID: "10", MinIndex: 0, MaxIndex: 41, Count: 42},
		{PackID: "18", MinIndex: 0, MaxIndex: 46, Count: 47},
		{PackID: "19", MinIndex: 0, MaxIndex: 28, Count: 29},
		{PackID: "20", MinIndex: 0, MaxIndex: 44, Count: 45},
	}
	require.Equal(t, expected, CustomEmojiPacks())
	catalog := CustomEmojiCatalog()
	require.Len(t, catalog, 549)
	position := 0
	seen := map[string]bool{}
	var captured strings.Builder
	for _, pack := range expected {
		for index := pack.MinIndex; index <= pack.MaxIndex; index++ {
			item := catalog[position]
			position++
			require.Equal(t, pack.PackID, item.PackID)
			require.Equal(t, strconv.Itoa(index), item.PackIndex)
			key := item.PackID + ":" + item.PackIndex
			require.False(t, seen[key])
			seen[key] = true
			require.NotEmpty(t, item.Title)
			captured.WriteString(item.PackID + "\x00" + item.PackIndex + "\x00" + item.Title + "\n")
		}
	}
	// Fingerprint derives from all 549 captured native titles, not from generated
	// Go source. It protects every exact label, identity and ordering.
	digest := sha256.Sum256([]byte(captured.String()))
	require.Equal(t, "e0f3c0f9effc0e32cf175a37d9d4c8dedb65e521bb7903bc531d7818a2da0e4b", hex.EncodeToString(digest[:]))
	require.Equal(t, CustomEmoji{PackID: "1", PackIndex: "0", Title: "smiley face"}, catalog[0])
	require.Equal(t, CustomEmoji{PackID: "1", PackIndex: "52", Title: "brown bottle"}, catalog[52])
	require.Equal(t, CustomEmoji{PackID: "20", PackIndex: "44", Title: "toilet paper"}, catalog[len(catalog)-1])
}

func TestCustomEmojiCatalogAndPackResultsAreIndependentCopies(t *testing.T) {
	original := append([]CustomEmoji(nil), CustomEmojiCatalog()...)
	changed := CustomEmojiCatalog()
	changed[0] = CustomEmoji{PackID: "changed", PackIndex: "changed", Title: "changed"}
	changed = append(changed, CustomEmoji{Title: "extra"})
	require.Len(t, changed, 550)
	require.Equal(t, original, CustomEmojiCatalog())
	require.Len(t, CustomEmojiCatalog(), 549)
	packs := append([]CustomEmojiPack(nil), CustomEmojiPacks()...)
	changedPacks := CustomEmojiPacks()
	changedPacks[0].PackID = "changed"
	changedPacks[0].MaxIndex = 999
	changedPacks[0].Count = 999
	changedPacks = append(changedPacks, CustomEmojiPack{PackID: "extra"})
	require.Len(t, changedPacks, 11)
	require.Equal(t, packs, CustomEmojiPacks())
	require.Len(t, CustomEmojiPacks(), 10)
}

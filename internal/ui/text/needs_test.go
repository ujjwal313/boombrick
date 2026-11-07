package text

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/ujjwal313/boombrick/assets"
)

// The fonts a text needs are the ones the engine would draw it with: none for
// what the bundled fonts draw, the script's weights for the rest, and for
// Chinese characters the form on the card.
func TestNeeds(t *testing.T) {
	n, err := NewNeeds(assets.Fonts(), fixtureCatalog(t, filepath.Join("testdata", "fonts")))
	if err != nil {
		t.Fatal(err)
	}
	none := func(string) bool { return false }
	tc := func(name string) bool { return name == "tc.ttf" }
	for _, c := range []struct {
		s    string
		has  func(string) bool
		want []string
	}{
		{"Low Tide Radio", none, nil},
		{"Кино · Mỹ Tâm", none, nil}, // Noto Sans
		{"東京事変の新しいアルバム", none, []string{"jp.ttf"}},
		{"Odalys · مرحبا", none, []string{"arabic-semibold.ttf", "arabic.ttf"}},
		{"Love❤️", none, []string{"emoji.ttf"}},
		{"大地", none, []string{"sc.ttf"}},
		{"大地", tc, []string{"tc.ttf"}},
		{"한국어 東京事変", none, []string{"jp.ttf", "kr.ttf"}},
	} {
		got := n.Of(c.s, c.has)
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("%q needs %v, want %v", c.s, got, c.want)
		}
	}
}

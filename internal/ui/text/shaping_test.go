package text

import (
	"slices"
	"strconv"
	"testing"

	"github.com/go-text/typesetting/di"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/shaping"
)

// inked counts l's glyphs that take up room: a variation selector is shaped
// to one that doesn't.
func inked(l Line) int {
	n := 0
	for _, run := range l.runs {
		for _, g := range run.Glyphs {
			if g.Advance > 0 {
				n++
			}
		}
	}
	return n
}

func TestDirection(t *testing.T) {
	for _, c := range []struct {
		s   string
		rtl bool
	}{
		{"", false}, {"Rust", false}, {"مرحبا", true}, {"שלום", true}, {"123 مرحبا", true},
		{"Rust مرحبا", false}, {"(مرحبا) Rust", true}, {"東京", false},
	} {
		if rtl := direction([]rune(c.s)) == di.DirectionRTL; rtl != c.rtl {
			t.Errorf("%q: right to left %v", c.s, rtl)
		}
		if RightToLeft(c.s) != c.rtl {
			t.Errorf("RightToLeft(%q) = %v", c.s, !c.rtl)
		}
	}
}

// Arabic is shaped right to left with its letters joined: the word's first
// letter is drawn last, in its initial form.
func TestArabicJoinsRightToLeft(t *testing.T) {
	e, _ := downloadsEngine(t, "arabic.ttf")
	alone := e.Shape("م", Body).runs[0].Glyphs[0].GlyphID
	l := e.Shape("مرحبا", Body)
	if len(l.runs) != 1 || l.runs[0].Direction != di.DirectionRTL {
		t.Fatalf("%d runs", len(l.runs))
	}
	g := l.runs[0].Glyphs
	if last := g[len(g)-1]; last.ClusterIndex != 0 || last.GlyphID == alone {
		t.Fatalf("the last glyph drawn is rune %d, glyph %d (alone %d)", last.ClusterIndex, last.GlyphID, alone)
	}
}

// A line mixing directions is drawn in the order of the bidirectional
// algorithm: Arabic inside an English line stays where it is, and English
// in an Arabic line is drawn left of the Arabic before it.
func TestMixedDirectionsOrder(t *testing.T) {
	e, _ := downloadsEngine(t, "arabic.ttf")
	l := e.Shape("Hello مرحبا World", Body)
	ar, fig := e.loaded["arabic.ttf"], e.faces[Figtree]
	if !slices.Equal(faces(l), []*font.Face{fig, ar, fig}) {
		t.Fatalf("an English line: %d faces", len(faces(l)))
	}
	l = e.Shape("مرحبا Hello", Body)
	if !slices.Equal(faces(l), []*font.Face{fig, ar}) {
		t.Fatalf("an Arabic line: %d faces, first in Figtree %v", len(faces(l)), l.runs[0].Face == fig)
	}
}

func TestVisualOrder(t *testing.T) {
	runs := func(dirs string) []shaping.Output {
		var out []shaping.Output
		for i, d := range dirs {
			o := shaping.Output{Runes: shaping.Range{Offset: i}, Direction: di.DirectionLTR}
			if d == 'R' {
				o.Direction = di.DirectionRTL
			}
			out = append(out, o)
		}
		return out
	}
	order := func(out []shaping.Output) (s string) {
		for _, o := range out {
			s += strconv.Itoa(o.Runes.Offset)
		}
		return s
	}
	for _, c := range []struct {
		dirs string
		para di.Direction
		want string
	}{
		{"LRRL", di.DirectionLTR, "0213"},
		{"RLLR", di.DirectionRTL, "3120"},
		{"L", di.DirectionLTR, "0"},
		{"RR", di.DirectionRTL, "10"},
	} {
		if got := order(visual(runs(c.dirs), c.para)); got != c.want {
			t.Errorf("%s in a %v text: %s, want %s", c.dirs, c.para, got, c.want)
		}
	}
}

// Devanagari is shaped by its own rules: consonants joined by a virama form
// one conjunct, so there are fewer glyphs than characters.
func TestDevanagariFormsConjuncts(t *testing.T) {
	e, _ := downloadsEngine(t, "devanagari.ttf")
	l := e.Shape("हिन्दी", Body)
	if !drawsAll(l) || len(l.runs) != 1 || len(l.runs[0].Glyphs) >= len([]rune("हिन्दी")) {
		t.Fatalf("every glyph drawn %v, %d glyphs", drawsAll(l), len(l.runs[0].Glyphs))
	}
}

// An emoji sequence is drawn as one glyph, and an emoji beside a word by the
// emoji font alone.
func TestEmoji(t *testing.T) {
	e, _ := downloadsEngine(t, "emoji.ttf")
	for _, s := range []string{"👨‍👩‍👧", "❤️", "🇯🇵"} {
		l := e.Shape(s, Body)
		if fs := faces(l); !drawsAll(l) || len(fs) != 1 || fs[0] != e.loaded["emoji.ttf"] || inked(l) != 1 {
			t.Errorf("%s: every glyph drawn %v, %d faces, %d glyphs", s, drawsAll(l), len(fs), inked(l))
		}
	}
	l := e.Shape("Love❤️", Body)
	emoji, fig := e.loaded["emoji.ttf"], e.faces[Figtree]
	if !drawsAll(l) || !slices.Equal(faces(l), []*font.Face{fig, emoji}) {
		t.Fatalf("Love❤️: every glyph drawn %v, %d faces", drawsAll(l), len(faces(l)))
	}
	// The emoji font has no space: one between emoji is the text's.
	l = e.Shape("❤️ 🇯🇵", Body)
	if !drawsAll(l) || !slices.Equal(faces(l), []*font.Face{emoji, fig, emoji}) {
		t.Fatalf("❤️ 🇯🇵: every glyph drawn %v, %d faces", drawsAll(l), len(faces(l)))
	}
}

package text

import (
	"slices"
	"strings"
	"testing"

	"github.com/go-text/typesetting/segmenter"
)

// wrapsWithin checks that lines fit w and hold the words of s in order.
func wrapsWithin(t *testing.T, e *Engine, s string, lines []string, w int) {
	t.Helper()
	for _, l := range lines {
		if got := e.Shape(l, Body).Width; got > w {
			t.Errorf("%q is %d wide, more than %d", l, got, w)
		}
		if l != strings.TrimSpace(l) {
			t.Errorf("%q starts or ends with a space", l)
		}
	}
	if got, want := strings.Join(strings.Fields(strings.Join(lines, " ")), ""), strings.Join(strings.Fields(s), ""); got != want {
		t.Errorf("lines %q lose or reorder text", lines)
	}
}

func TestWrapLatinAtSpaces(t *testing.T) {
	e := engine(t)
	s := "Paper Lanterns at the Hollow Pines, an extended session"
	lines := e.Wrap(s, Body, 300)
	if len(lines) < 2 {
		t.Fatalf("lines %q", lines)
	}
	wrapsWithin(t, e, s, lines, 300)
	for _, l := range lines {
		for _, w := range strings.Fields(l) {
			if !strings.Contains(s, w) {
				t.Errorf("a word was broken: %q", w)
			}
		}
	}
	// A word wider than the line gets a line of its own.
	if lines := e.Wrap("a Supercalifragilisticexpialidocious b", Body, 150); !slices.Equal(lines, []string{"a", "Supercalifragilisticexpialidocious", "b"}) {
		t.Fatalf("a long word: %q", lines)
	}
	for _, s := range []string{"", "   "} {
		if lines := e.Wrap(s, Body, 300); lines != nil {
			t.Errorf("%q wrapped to %q", s, lines)
		}
	}
}

// Japanese, which has no spaces, breaks between characters, never before a
// full stop.
func TestWrapJapaneseBetweenCharacters(t *testing.T) {
	e, _ := downloadsEngine(t, "jp.ttf")
	s := "東京事変の新しいアルバム。東京事変の新しいアルバム"
	lines := e.Wrap(s, Body, 200)
	if len(lines) < 3 {
		t.Fatalf("lines %q", lines)
	}
	wrapsWithin(t, e, s, lines, 200)
	for _, l := range lines {
		if strings.HasPrefix(l, "。") {
			t.Errorf("%q starts with a full stop", l)
		}
	}
}

// bounds lists where s's characters, as a reader sees them, end.
func bounds(s string) []int {
	var seg segmenter.Segmenter
	seg.Init([]rune(s))
	var ends []int
	for it := seg.GraphemeIterator(); it.Next(); {
		g := it.Grapheme()
		ends = append(ends, g.Offset+len(g.Text))
	}
	return ends
}

// Fit cuts between whole characters: never inside an emoji sequence or a
// Devanagari cluster.
func TestFitKeepsCharactersWhole(t *testing.T) {
	e, _ := downloadsEngine(t, "emoji.ttf", "devanagari.ttf")
	for _, s := range []string{strings.Repeat("👨‍👩‍👧", 12), strings.Repeat("हिन्दी ", 12)} {
		got := e.Fit(s, Body, 240)
		if !strings.HasSuffix(got, "…") || e.Shape(got, Body).Width > 240 {
			t.Fatalf("Fit = %q", got)
		}
		// Trimming the spaces before the ellipsis leaves the cut between
		// characters too, as a space is one.
		kept := strings.TrimSuffix(got, "…")
		if !strings.HasPrefix(s, kept) || !slices.Contains(bounds(s), len([]rune(kept))) {
			t.Fatalf("%q isn't cut between characters", kept)
		}
	}
}

// The ellipsis follows the text, so a right-to-left one shows it on the left.
func TestFitRightToLeft(t *testing.T) {
	e, _ := downloadsEngine(t, "arabic.ttf")
	got := e.Fit(strings.Repeat("مرحبا بالعالم ", 8), Body, 240)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("Fit = %q", got)
	}
	l := e.Shape(got, Body)
	first := l.runs[0]
	if first.Face != e.faces[Figtree] || first.Runes.Offset != len([]rune(got))-1 {
		t.Fatalf("the first run drawn starts at rune %d of %d", first.Runes.Offset, len([]rune(got)))
	}
}

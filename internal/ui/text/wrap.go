package text

import (
	"strings"

	"github.com/go-text/typesetting/shaping"
)

// Wrap breaks s into lines no wider than maxWidth where Unicode's line
// breaking rules (UAX #14) allow: at spaces, and between Chinese or Japanese
// characters, though not before their closing punctuation. A word wider than
// maxWidth gets a line of its own. The lines come without the spaces they
// start or end with; a text of only spaces has none.
func (e *Engine) Wrap(s string, st Style, maxWidth int) []string {
	runes := []rune(s)
	if strings.TrimSpace(s) == "" {
		return nil
	}
	runs, dir := e.shapeRuns(runes, st)
	cfg := shaping.WrapConfig{Direction: dir, BreakPolicy: shaping.Never}
	lines, _ := e.wrapper.WrapParagraph(cfg, maxWidth, runes, shaping.NewSliceIterator(runs))
	var out []string
	for _, l := range lines {
		start, end := len(runes), 0
		for _, run := range l {
			start = min(start, run.Runes.Offset)
			end = max(end, run.Runes.Offset+run.Runes.Count)
		}
		if line := strings.TrimSpace(string(runes[start:end])); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// Fit returns s if it fits in maxWidth, otherwise the longest beginning of it
// that fits with an ellipsis after. It cuts between the characters a reader
// sees (UAX #29), never inside an emoji, a conjunct or a letter with its
// marks. The ellipsis follows the text, so a right-to-left one shows it on
// the left.
func (e *Engine) Fit(s string, st Style, maxWidth int) string {
	if e.Shape(s, st).Width <= maxWidth {
		return s
	}
	runes := []rune(s)
	// ends holds where each character ends, but the last.
	var ends []int
	e.graphemes.Init(runes)
	for it := e.graphemes.GraphemeIterator(); it.Next(); {
		g := it.Grapheme()
		ends = append(ends, g.Offset+len(g.Text))
	}
	ends = ends[:max(len(ends)-1, 0)]
	cut := func(k int) string { // the first k characters
		n := 0
		if k > 0 {
			n = ends[k-1]
		}
		return strings.TrimRight(string(runes[:n]), " ") + ellipsis
	}
	lo, hi := 0, len(ends)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if e.Shape(cut(mid), st).Width <= maxWidth {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return cut(lo)
}

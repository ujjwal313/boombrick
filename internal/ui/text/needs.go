package text

import (
	"fmt"
	"io/fs"
	"slices"
)

// Needs works out which downloaded fonts text needs away from the UI thread,
// so that they are fetched before the text is shown: the library's titles
// after a scan, lyrics as they arrive. It doesn't change once made, so any
// goroutine may use it.
type Needs struct {
	bundled   Coverage // what the bundled fonts draw between them
	downloads []download
}

// NewNeeds reads what the bundled fonts in fonts draw, for the downloads in
// list.
func NewNeeds(fonts fs.FS, list []Download) (*Needs, error) {
	var rs []rune
	for _, name := range append(files[:], fallbackFiles[:]...) {
		face, err := parse(fonts, name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		rs = append(rs, CoverageOf(face)...)
	}
	n := &Needs{bundled: decodeCoverage(EncodeCoverage(rs))}
	for _, d := range list {
		n.downloads = append(n.downloads, download{d, decodeCoverage(d.Covers)})
	}
	return n, nil
}

// Of lists by name the downloads s needs, as the engine would choose them a
// word at a time, has saying which are on the card. A script's semibold font
// comes with its regular one, for headings.
func (n *Needs) Of(s string, has func(string) bool) []string {
	runes := []rune(s)
	hint := hanHint(runes)
	var out []string
	add := func(i int) {
		if i < 0 {
			return
		}
		for _, d := range n.downloads {
			if d.Group == n.downloads[i].Group && !slices.Contains(out, d.Name) {
				out = append(out, d.Name)
			}
		}
	}
	for i := 0; i < len(runes); {
		if invisible(runes[i]) {
			i++
			continue
		}
		j := i + 1
		for j < len(runes) && !invisible(runes[j]) {
			j++
		}
		if word := runes[i:j]; !draws(n.bundled, word) {
			if k := choose(n.downloads, word, hint, has); k >= 0 {
				add(k)
			} else {
				for k := i; k < j; {
					m := k + 1
					for m < j && joins(runes[m]) {
						m++
					}
					if c := runes[k:m]; !draws(n.bundled, c) {
						add(choose(n.downloads, c, hint, has))
					}
					k = m
				}
			}
		}
		i = j
	}
	return out
}

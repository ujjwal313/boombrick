package text

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"unicode"

	"github.com/go-text/typesetting/font"
)

// Weight is a downloadable font's weight.
type Weight uint8

const (
	Regular Weight = iota
	SemiBold
)

// Han is the regional form a font draws Chinese characters in. On a tie, the
// first of Simplified, Traditional and Japanese draws them: Japanese text has
// kana, which picks the Japanese font, and Japanese draws many traditional
// characters too.
type Han uint8

const (
	NoHan Han = iota // draws no Chinese characters
	Simplified
	Traditional
	Japanese
)

// A Download is a font Boombrick fetches the first time text needs it.
type Download struct {
	Name   string // its file in the fonts folder, "NotoSansJP-Regular.ttf"
	Group  string // shared by a script's weights: "arabic"
	Weight Weight
	Han    Han
	Size   int64  // the bytes fetched: the file, gzipped
	Sum    string // SHA-256 of what is fetched, in hex
	Covers string // the characters it draws, as EncodeCoverage writes them
}

// NewDownload describes the font file data, kept as name, for a catalog. Its
// Size and Sum are left for whoever compresses it.
func NewDownload(name, group string, w Weight, han Han, data []byte) (Download, error) {
	face, err := font.ParseTTF(bytes.NewReader(data))
	if err != nil {
		return Download{}, err
	}
	return Download{Name: name, Group: group, Weight: w, Han: han, Covers: EncodeCoverage(CoverageOf(face))}, nil
}

// Source holds the downloaded fonts. Has reports whether one is on the card
// and must be cheap; Open opens it; Want asks for one that isn't there,
// without waiting; Bad says one on the card wouldn't parse. The engine calls
// them from the UI thread.
type Source interface {
	Has(name string) bool
	Open(name string) (File, error)
	Want(name string)
	Bad(name string)
}

// File is a downloaded font, open. *os.File is one.
type File interface {
	font.Resource
	io.Closer
}

// Dir is a Source of the fonts in a folder, which fetches nothing.
type Dir string

func (d Dir) Has(name string) bool {
	_, err := os.Stat(filepath.Join(string(d), name))
	return err == nil
}

func (d Dir) Open(name string) (File, error) { return os.Open(filepath.Join(string(d), name)) }
func (Dir) Want(string)                      {}
func (Dir) Bad(string)                       {}

// download is a catalog entry with its coverage read.
type download struct {
	Download
	cover Coverage
}

// UseDownloads gives the engine the fonts it may download, list, kept in src.
func (e *Engine) UseDownloads(src Source, list []Download) {
	e.source = src
	e.downloads = make([]download, len(list))
	for i, d := range list {
		e.downloads[i] = download{d, decodeCoverage(d.Covers)}
	}
	e.loaded, e.broken, e.asked = map[string]*font.Face{}, map[string]bool{}, map[string]bool{}
	e.gen++
}

// Generation changes when the fonts the engine draws with may have: text
// shaped before it changed is to be shaped again.
func (e *Engine) Generation() uint64 { return e.gen }

// Refresh tells the engine that fonts have arrived: what it asked for may be
// on the card now, and a file that wouldn't parse may have been replaced.
func (e *Engine) Refresh() {
	clear(e.asked)
	clear(e.broken)
	e.gen++
}

// downloaded is the downloaded face that draws all of runes, in f's weight
// where its script has it, or nil. A font that isn't on the card is asked for
// and nil returned, so the runes draw as missing until it arrives. hint is
// the form Chinese characters take in the text the runes are from.
func (e *Engine) downloaded(runes []rune, f Face, hint Han) *font.Face {
	i := e.pick(runes, hint)
	if i < 0 {
		return nil
	}
	if f == OutfitSemiBold || f == FigtreeSemiBold {
		for _, d := range e.downloads {
			if d.Group == e.downloads[i].Group && d.Weight == SemiBold {
				if face := e.load(d.Name); face != nil {
					return face
				}
			}
		}
	}
	return e.load(e.downloads[i].Name)
}

// pick is the index of the regular-weight download that draws all of runes,
// or -1, as choose finds it. An engine with no fonts to download has none.
func (e *Engine) pick(runes []rune, hint Han) int {
	if e.source == nil {
		return -1
	}
	return choose(e.downloads, runes, hint, e.source.Has)
}

// choose is the index in downloads of the regular-weight font that draws all
// of runes, or -1. For Chinese characters, which several draw, it takes the
// form hint names, then one has says is on the card, then the first of
// Simplified, Traditional and Japanese. Kana in Japanese text, which the
// Simplified font draws too, goes to the form hint names the same way.
func choose(downloads []download, runes []rune, hint Han, has func(string) bool) int {
	han := false
	for _, r := range runes {
		if unicode.Is(unicode.Han, r) {
			han = true
			break
		}
	}
	best := -1
	// better reports whether download i beats best: on the card when best
	// isn't, or else earlier in the order of forms.
	better := func(i int) bool {
		if best < 0 {
			return true
		}
		a, b := downloads[i], downloads[best]
		if ha, hb := has(a.Name), has(b.Name); ha != hb {
			return ha
		}
		return a.Han < b.Han
	}
	for i, d := range downloads {
		if d.Weight != Regular || !draws(d.cover, runes) {
			continue
		}
		if hint != NoHan && d.Han == hint || !han && hint == NoHan {
			return i
		}
		if better(i) {
			best = i
		}
	}
	return best
}

// draws reports whether c has a glyph for each of runes that needs one. A
// variation selector, as the one that makes ❤ an emoji, needs none.
func draws(c Coverage, runes []rune) bool {
	for _, r := range runes {
		if !c.Has(r) && !invisible(r) && !unicode.Is(unicode.Variation_Selector, r) {
			return false
		}
	}
	return true
}

// load is the downloaded face name, parsed the first time it is needed, or
// nil. One not on the card is asked for once; one that won't parse is
// reported and not parsed again until Refresh.
func (e *Engine) load(name string) *font.Face {
	if face, ok := e.loaded[name]; ok {
		return face
	}
	if e.broken[name] {
		return nil
	}
	if !e.source.Has(name) {
		if !e.asked[name] {
			e.asked[name] = true
			e.source.Want(name)
		}
		return nil
	}
	f, err := e.source.Open(name)
	var face *font.Face
	if err == nil {
		face, err = font.ParseTTF(f)
		f.Close()
	}
	if err != nil {
		e.broken[name] = true
		e.source.Bad(name)
		return nil
	}
	e.loaded[name] = face
	return face
}

// hanHint is the form Chinese characters take in runes: Japanese where there
// is kana.
func hanHint(runes []rune) Han {
	for _, r := range runes {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana) {
			return Japanese
		}
	}
	return NoHan
}

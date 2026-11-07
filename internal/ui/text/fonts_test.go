package text

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/go-text/typesetting/font"
)

// fixtureFonts are the fonts make fixtures cuts into testdata/fonts, as the
// catalog would list them.
var fixtureFonts = []struct {
	name, group string
	weight      Weight
	han         Han
}{
	{"sc.ttf", "sc", Regular, Simplified},
	{"jp.ttf", "jp", Regular, Japanese},
	{"tc.ttf", "tc", Regular, Traditional},
	{"kr.ttf", "kr", Regular, NoHan},
	{"arabic.ttf", "arabic", Regular, NoHan},
	{"arabic-semibold.ttf", "arabic", SemiBold, NoHan},
	{"devanagari.ttf", "devanagari", Regular, NoHan},
	{"emoji.ttf", "emoji", Regular, NoHan},
}

// fixtureCatalog is the catalog of the fixture fonts in dir. The test skips
// when they are missing.
func fixtureCatalog(t *testing.T, dir string) []Download {
	t.Helper()
	var list []Download
	for _, f := range fixtureFonts {
		data, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			t.Skipf("%s is missing; run make fixtures", f.name)
		}
		d, err := NewDownload(f.name, f.group, f.weight, f.han, data)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, d)
	}
	return list
}

// card is a Source of the fixture fonts that holds only those put on it, and
// remembers what it was asked for.
type card struct {
	Dir
	on         map[string]bool
	wants, bad []string
}

func (c *card) Has(name string) bool { return c.on[name] }
func (c *card) Want(name string)     { c.wants = append(c.wants, name) }
func (c *card) Bad(name string)      { c.bad = append(c.bad, name) }

// downloadsEngine is an engine with the fixture fonts to download, with
// those named on the card.
func downloadsEngine(t *testing.T, on ...string) (*Engine, *card) {
	t.Helper()
	dir := filepath.Join("testdata", "fonts")
	list := fixtureCatalog(t, dir)
	e := engine(t)
	c := &card{Dir: Dir(dir), on: map[string]bool{}}
	for _, n := range on {
		c.on[n] = true
	}
	e.UseDownloads(c, list)
	return e, c
}

func TestNewDownloadCoversItsFont(t *testing.T) {
	list := fixtureCatalog(t, filepath.Join("testdata", "fonts"))
	jp := decodeCoverage(list[1].Covers)
	if !jp.Has('東') || !jp.Has('の') || !jp.Has('A') || jp.Has('东') {
		t.Fatal("the Japanese font's coverage is wrong")
	}
	if list[1].Name != "jp.ttf" || list[1].Han != Japanese || list[1].Group != "jp" {
		t.Fatalf("download %+v", list[1])
	}
}

// faces lists the faces that draw l, left to right, each once where it draws
// runs side by side.
func faces(l Line) []*font.Face {
	var fs []*font.Face
	for _, run := range l.runs {
		if len(fs) == 0 || fs[len(fs)-1] != run.Face {
			fs = append(fs, run.Face)
		}
	}
	return fs
}

// A word no bundled font draws is drawn whole by the downloaded font that
// draws it, in a run for each script.
func TestDownloadedFontDrawsAWord(t *testing.T) {
	e, c := downloadsEngine(t, "jp.ttf")
	l := e.Shape("東京事変の新しいアルバム", Body)
	if fs := faces(l); !drawsAll(l) || !l.fallback || len(fs) != 1 || fs[0] != e.loaded["jp.ttf"] {
		t.Fatalf("every glyph drawn %v, %d faces", drawsAll(l), len(fs))
	}
	if len(c.wants) > 0 {
		t.Fatalf("asked for %v", c.wants)
	}
	// Latin text never loads one.
	e, _ = downloadsEngine(t, "jp.ttf")
	if e.Shape("Low Tide Radio", Body); len(e.loaded) > 0 {
		t.Fatal("a Latin line loaded a downloaded font")
	}
}

// A font that isn't on the card is asked for once, and the text draws as
// missing until it is there.
func TestMissingFontIsAskedForOnce(t *testing.T) {
	e, c := downloadsEngine(t)
	gen := e.Generation()
	for range 3 {
		if l := e.Shape("مرحبا", Body); drawsAll(l) {
			t.Fatal("Arabic drawn without its font")
		}
	}
	if !slices.Equal(c.wants, []string{"arabic.ttf"}) {
		t.Fatalf("asked for %v, want the Arabic font once", c.wants)
	}
	c.on["arabic.ttf"] = true
	e.Refresh()
	if e.Generation() == gen {
		t.Fatal("the generation didn't change")
	}
	if l := e.Shape("مرحبا", Body); !drawsAll(l) {
		t.Fatal("Arabic not drawn once its font arrived")
	}
}

// Chinese characters go to the form kana says, then to a font on the card,
// then to the first of Simplified, Traditional and Japanese that draws them.
func TestHanForms(t *testing.T) {
	for _, c := range []struct {
		s    string
		on   []string
		want string
	}{
		{"東京事変", nil, "jp.ttf"},                        // 変 is only Japanese
		{"东京", nil, "sc.ttf"},                          // 东 only Simplified
		{"音樂", nil, "tc.ttf"},                          // 樂 only Traditional
		{"大地", nil, "sc.ttf"},                          // all three: Simplified first
		{"東京", nil, "tc.ttf"},                          // Traditional and Japanese, no kana: Traditional first
		{"東京", []string{"jp.ttf"}, "jp.ttf"},           // unless Japanese is on the card
		{"大地", []string{"tc.ttf"}, "tc.ttf"},           // the one on the card
		{"大地の", []string{"sc.ttf"}, "jp.ttf"},          // kana means Japanese
		{"한국어", nil, "kr.ttf"},                         // Hangul
		{"大地", []string{"sc.ttf", "tc.ttf"}, "sc.ttf"}, // two on the card: Simplified first
	} {
		e, _ := downloadsEngine(t, c.on...)
		runes := []rune(c.s)
		if i := e.pick(runes, hanHint(runes)); i < 0 || e.downloads[i].Name != c.want {
			t.Errorf("%s with %v on the card: %d, want %s", c.s, c.on, i, c.want)
		}
	}
}

// The Simplified font has kana too, as GB 2312 does, unlike the fixture: kana
// in Japanese text still takes the Japanese font.
func TestKanaTakesTheJapaneseFont(t *testing.T) {
	kana, han := []rune("アルバムありがとうの、"), []rune("東京大地")
	cover := func(rs ...[]rune) Coverage { return decodeCoverage(EncodeCoverage(slices.Concat(rs...))) }
	list := []download{
		{Download{Name: "sc", Weight: Regular, Han: Simplified}, cover(kana, han)},
		{Download{Name: "jp", Weight: Regular, Han: Japanese}, cover(kana, han)},
		{Download{Name: "tc", Weight: Regular, Han: Traditional}, cover(han)},
	}
	none := func(string) bool { return false }
	for _, s := range []string{"アルバム", "ありがとう、", "大地のアルバム"} {
		r := []rune(s)
		if i := choose(list, r, hanHint(r), none); i != 1 {
			t.Errorf("%s: %d, want the Japanese font", s, i)
		}
	}
	// Kana in a Japanese title, in a word of its own.
	if i := choose(list, []rune("アルバム"), Japanese, none); i != 1 {
		t.Errorf("a kana word in Japanese text: %d, want the Japanese font", i)
	}
}

// An engine given no fonts to download draws what it lacks as missing.
func TestWithoutDownloadsTextDrawsAsMissing(t *testing.T) {
	if l := engine(t).Shape("東京 😀", Body); drawsAll(l) {
		t.Fatal("drawn whole with no fonts to download")
	}
}

// Headings take a script's semibold font where it has one, and the regular
// one until the semibold one arrives.
func TestSemiboldUsesTheScriptsSemibold(t *testing.T) {
	e, c := downloadsEngine(t, "arabic.ttf", "arabic-semibold.ttf")
	if l := e.Shape("مرحبا", Title); l.runs[0].Face != e.loaded["arabic-semibold.ttf"] {
		t.Fatal("a heading isn't in the semibold font")
	}
	if l := e.Shape("مرحبا", Body); l.runs[0].Face != e.loaded["arabic.ttf"] {
		t.Fatal("body text isn't in the regular font")
	}
	e, c = downloadsEngine(t, "arabic.ttf")
	if l := e.Shape("مرحبا", Title); !drawsAll(l) || l.runs[0].Face != e.loaded["arabic.ttf"] {
		t.Fatal("a heading isn't in the regular font while the semibold one is away")
	}
	if !slices.Equal(c.wants, []string{"arabic-semibold.ttf"}) {
		t.Fatalf("asked for %v", c.wants)
	}
}

// A font on the card that won't parse is reported once and not read again
// until fonts arrive.
func TestBadFontIsReportedOnce(t *testing.T) {
	dir := t.TempDir()
	list := fixtureCatalog(t, filepath.Join("testdata", "fonts"))
	if err := os.WriteFile(filepath.Join(dir, "arabic.ttf"), []byte("not a font"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := engine(t)
	c := &card{Dir: Dir(dir), on: map[string]bool{"arabic.ttf": true}}
	e.UseDownloads(c, list)
	for range 3 {
		if l := e.Shape("مرحبا", Body); drawsAll(l) {
			t.Fatal("drawn with a bad font")
		}
	}
	if !slices.Equal(c.bad, []string{"arabic.ttf"}) {
		t.Fatalf("reported %v", c.bad)
	}
	e.Refresh()
	e.Shape("مرحبا", Body)
	if len(c.bad) != 2 {
		t.Fatal("not read again after fonts arrived")
	}
}

// Kana in the Japanese font and Latin around it keep their own fonts.
func TestJapaneseWithEnglish(t *testing.T) {
	e, _ := downloadsEngine(t, "jp.ttf")
	l := e.Shape("Live 東京事変", Body)
	if !drawsAll(l) || !slices.Equal(faces(l), []*font.Face{e.faces[Figtree], e.loaded["jp.ttf"]}) {
		t.Fatalf("every glyph drawn %v, %d faces", drawsAll(l), len(faces(l)))
	}
}

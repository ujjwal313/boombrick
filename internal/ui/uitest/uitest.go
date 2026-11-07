// Package uitest draws UI into memory for tests and compares frames with
// approved images in testdata. Set BOOMBRICK_UPDATE_GOLDEN=1 to rewrite them;
// a missing image skips its test, since golden images are generated locally
// and kept out of git.
package uitest

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"

	"github.com/ujjwal313/boombrick/assets"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// Main runs a package's tests on the main OS thread, as SDL requires.
func Main(m *testing.M) {
	runtime.LockOSThread()
	os.Exit(m.Run())
}

// NewContext returns a context drawing into a w x h device pixel offscreen
// canvas cleared to the ground colour.
func NewContext(t *testing.T, w, h int) (*ui.Context, *gfx.Offscreen) {
	t.Helper()
	// The app scales textures with linear filtering; frames must match.
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "1")
	off, err := gfx.NewOffscreen(w, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	fonts, err := text.NewEngine(assets.Fonts())
	if err != nil {
		t.Fatal(err)
	}
	u := ui.NewContext(off.Canvas, fonts, theme.Scale)
	u.Now = time.Unix(0, 0)
	off.Clear(theme.Ground)
	return u, off
}

// fixtureFonts are the fonts make fixtures cuts into
// internal/ui/text/testdata/fonts, as the catalog would list them.
var fixtureFonts = []struct {
	name, group string
	weight      text.Weight
	han         text.Han
}{
	{"sc.ttf", "sc", text.Regular, text.Simplified},
	{"jp.ttf", "jp", text.Regular, text.Japanese},
	{"tc.ttf", "tc", text.Regular, text.Traditional},
	{"kr.ttf", "kr", text.Regular, text.NoHan},
	{"arabic.ttf", "arabic", text.Regular, text.NoHan},
	{"arabic-semibold.ttf", "arabic", text.SemiBold, text.NoHan},
	{"devanagari.ttf", "devanagari", text.Regular, text.NoHan},
	{"emoji.ttf", "emoji", text.Regular, text.NoHan},
}

// UseFixtureFonts gives u's engine the fixture fonts to draw other scripts
// and emoji with, all on the card. The test skips when they are missing.
func UseFixtureFonts(t *testing.T, u *ui.Context) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "text", "testdata", "fonts")
	var list []text.Download
	for _, f := range fixtureFonts {
		data, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			t.Skipf("%s is missing; run make fixtures", f.name)
		}
		d, err := text.NewDownload(f.name, f.group, f.weight, f.han, data)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, d)
	}
	u.Fonts.UseDownloads(text.Dir(dir), list)
}

// Golden compares got with testdata/<name>.png. If more than 0.5 % of pixels
// differ by more than 2 in any channel, the test fails and the frame is
// written to testdata/<name>.actual.png for inspection.
func Golden(t *testing.T, name string, got *image.NRGBA) {
	t.Helper()
	path := filepath.Join("testdata", name+".png")
	if os.Getenv("BOOMBRICK_UPDATE_GOLDEN") != "" {
		if err := writePNG(path, got); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := readPNG(path)
	if errors.Is(err, fs.ErrNotExist) {
		t.Skipf("%s is missing; run with BOOMBRICK_UPDATE_GOLDEN=1 to create it", path)
	}
	if err != nil {
		t.Fatal(err)
	}
	if want.Bounds() != got.Bounds() {
		t.Fatalf("%s: size %v, want %v", name, got.Bounds(), want.Bounds())
	}
	bad := 0
	for i := 0; i < len(got.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			d := int(got.Pix[i+c]) - int(want.Pix[i+c])
			if d > 2 || d < -2 {
				bad++
				break
			}
		}
	}
	if total := len(got.Pix) / 4; bad*1000 > total*5 {
		actual := filepath.Join("testdata", name+".actual.png")
		if err := writePNG(actual, got); err != nil {
			t.Error(err)
		}
		t.Fatalf("%s: %d of %d pixels differ; wrote %s", name, bad, total, actual)
	}
}

// HardEdge walks img along a row or a column, from one device pixel to
// another, looking for a step of more than 4 levels between neighbouring
// pixels, such as a glow cut off by a clip leaves. It reports the first.
func HardEdge(img *image.NRGBA, from, to image.Point) (image.Point, bool) {
	d := to.Sub(from)
	n := max(d.X, -d.X, d.Y, -d.Y)
	if n == 0 {
		return image.Point{}, false
	}
	step := d.Div(n)
	for i := range n {
		p := from.Add(step.Mul(i))
		a, b := img.NRGBAAt(p.X, p.Y), img.NRGBAAt(p.X+step.X, p.Y+step.Y)
		for _, c := range [3]int{int(a.R) - int(b.R), int(a.G) - int(b.G), int(a.B) - int(b.B)} {
			if c > 4 || c < -4 {
				return p, true
			}
		}
	}
	return image.Point{}, false
}

func writePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func readPNG(path string) (*image.NRGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := image.NewNRGBA(img.Bounds())
	draw.Draw(out, out.Bounds(), img, img.Bounds().Min, draw.Src)
	return out, nil
}

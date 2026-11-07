package ui

import (
	"bytes"
	"image"
	"image/color"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/assets"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

func TestMain(m *testing.M) {
	runtime.LockOSThread()
	os.Exit(m.Run())
}

func newContext(t *testing.T, scale float32) (*Context, *gfx.Offscreen) {
	t.Helper()
	off, err := gfx.NewOffscreen(200, 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(off.Close)
	fonts, err := text.NewEngine(assets.Fonts())
	if err != nil {
		t.Fatal(err)
	}
	return NewContext(off.Canvas, fonts, scale), off
}

func TestScaleMapsToDevicePixels(t *testing.T) {
	u := Context{Scale: 1.25}
	u.PushScale(0, 0, 2)
	if got, want := u.Apply(gfx.Rect{X: 4, Y: 8, W: 8, H: 4}), (gfx.Rect{X: 10, Y: 20, W: 20, H: 10}); got != want {
		t.Errorf("Apply = %v, want %v", got, want)
	}
}

func TestScaledTextIsMeasuredInDesignPixels(t *testing.T) {
	plain, _ := newContext(t, 1)
	scaled, _ := newContext(t, 1.25)
	w1 := plain.TextWidth("Continue listening", text.Heading)
	w2 := scaled.TextWidth("Continue listening", text.Heading)
	if d := w2 - w1; d < -2 || d > 2 {
		t.Fatalf("width at scale 1.25 = %v design px, at scale 1 = %v", w2, w1)
	}
	scaled.Text("Home", text.Body, 0, 0, theme.Paper)
	e, ok := scaled.texts.lru.Get(textKey{"Home", text.Body})
	if !ok {
		t.Fatal("text was not cached")
	}
	if least := plain.TextWidth("Home", text.Body) * 1.25; float32(e.tex.W) < least {
		t.Fatalf("texture is %d px wide, want at least %v: text must be rasterised at device size", e.tex.W, least)
	}
}

func TestIconsRasteriseAtDeviceSize(t *testing.T) {
	u, _ := newContext(t, 1.25)
	u.Icon("play", 16, 2, true, 0, 0, theme.Paper)
	if _, ok := u.icons[iconKey{"play", 20, 2, true}]; !ok {
		t.Fatal("a 16 px icon at scale 1.25 should be rasterised at 20 px")
	}
}

func TestTextDrawsAndMeasures(t *testing.T) {
	u, off := newContext(t, 1)
	off.Clear(theme.Ground)
	w := u.Text("Home", text.Body, 10, 10, theme.Paper)
	if w <= 0 || w != u.TextWidth("Home", text.Body) {
		t.Fatalf("width = %v, TextWidth = %v", w, u.TextWidth("Home", text.Body))
	}
	img := off.Image()
	lit := 0
	for y := 10; y < 10+int(u.LineHeight(text.Body)); y++ {
		for x := 10; x < 10+int(w); x++ {
			if img.NRGBAAt(x, y) != theme.Ground {
				lit++
			}
		}
	}
	if lit == 0 {
		t.Fatal("no text was drawn")
	}
}

func TestApplyNestsTransforms(t *testing.T) {
	u := Context{Scale: 1}
	r := gfx.Rect{X: 20, Y: 10, W: 10, H: 10}
	u.PushScale(100, 0, 2)
	u.PushScale(10, 10, 0.5)
	// Inner first: (15, 10, 5x5); then outer: (-70, 20, 10x10).
	if got, want := u.Apply(r), (gfx.Rect{X: -70, Y: 20, W: 10, H: 10}); got != want {
		t.Errorf("Apply = %v, want %v", got, want)
	}
	u.Pop()
	u.Pop()
	if got := u.Apply(r); got != r {
		t.Errorf("after Pop, Apply = %v, want identity", got)
	}
}

func TestFitIsCached(t *testing.T) {
	u, _ := newContext(t, 1)
	got := u.Fit("Paper Lanterns — The Hollow Pines", text.Body, 120)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("Fit = %q", got)
	}
	if len(u.fits) != 1 {
		t.Fatalf("fits cache has %d entries", len(u.fits))
	}
}

func TestTranslateNests(t *testing.T) {
	u := Context{Scale: 1}
	u.PushTranslate(10, 0)
	u.PushScale(0, 0, 2)
	if got, want := u.Apply(gfx.Rect{X: 1, Y: 1, W: 1, H: 1}), (gfx.Rect{X: 12, Y: 2, W: 2, H: 2}); got != want {
		t.Fatalf("Apply = %v, want %v", got, want)
	}
	u.Pop()
	u.Pop()
}

func TestAlphaFades(t *testing.T) {
	u, off := newContext(t, 1)
	off.Clear(color.NRGBA{0, 0, 0, 255})
	u.Alpha = 0.5
	u.Fill(gfx.Rect{X: 0, Y: 0, W: 10, H: 10}, color.NRGBA{255, 255, 255, 255})
	if r := off.Image().NRGBAAt(5, 5).R; r < 125 || r > 130 {
		t.Fatalf("half-faded white over black = %d, want about 127", r)
	}
}

func TestTextGlowSpreadsPastTheText(t *testing.T) {
	u, off := newContext(t, 1)
	off.Clear(color.NRGBA{0, 0, 0, 255})
	u.TextGlow("Hello", text.Body, 40, 30, 30, color.NRGBA{255, 255, 255, 255})
	img := off.Image()
	if c := img.NRGBAAt(60, 24); c.R == 0 {
		t.Fatal("no glow just above the line")
	}
	if c := img.NRGBAAt(199, 99); c.R != 0 {
		t.Fatalf("glow far from the text: %v", c)
	}
}

// Text drawn through a cache of its own stays out of the shared one, looks
// the same, and goes with the cache's Clear.
func TestTextInItsOwnCache(t *testing.T) {
	shared, sharedOff := newContext(t, 1.25)
	shared.Text("Low tide radio", text.Body, 10, 10, theme.Paper)
	u, off := newContext(t, 1.25)
	c := NewTextCache(1 << 20)
	if w := u.TextIn(c, "Low tide radio", text.Body, 10, 10, theme.Paper); w != u.TextWidth("Low tide radio", text.Body) {
		t.Fatalf("TextIn advanced %v", w)
	}
	if u.texts.Len() != 0 || c.Len() != 1 {
		t.Fatalf("shared cache holds %d, own %d", u.texts.Len(), c.Len())
	}
	if !bytes.Equal(off.Image().Pix, sharedOff.Image().Pix) {
		t.Fatal("text from its own cache looks different")
	}
	c.Clear()
	if c.Len() != 0 {
		t.Fatal("Clear left textures")
	}
}

// Text and glows made ahead are drawn without making them again.
func TestPreparedTextIsNotMadeAgain(t *testing.T) {
	u, _ := newContext(t, 1.25)
	c := NewTextCache(1 << 20)
	if !u.PrepareText(c, "Carry me home", text.Body) || !u.PrepareGlow("Carry me home", text.Body, 30) {
		t.Fatal("nothing was made")
	}
	if u.PrepareText(c, "Carry me home", text.Body) || u.PrepareGlow("Carry me home", text.Body, 30) {
		t.Fatal("made again")
	}
	made := u.Made()
	u.TextGlow("Carry me home", text.Body, 10, 10, 30, theme.Paper)
	u.TextIn(c, "Carry me home", text.Body, 10, 10, theme.Paper)
	if u.Made() != made {
		t.Fatalf("drawing what was prepared made %d textures", u.Made()-made)
	}
	u.Text("Carry me home", text.Body, 10, 10, theme.Paper) // the shared cache hasn't got it
	if u.Made() != made+1 {
		t.Fatalf("Made counts %d for a new text", u.Made()-made)
	}
}

// When fonts arrive, text, fits and glows made before are made again, in the
// shared cache and in a screen's own.
func TestTextMadeAgainWhenFontsArrive(t *testing.T) {
	u, _ := newContext(t, 1.25)
	c := NewTextCache(1 << 20)
	draw := func() {
		u.Text("Carry me home", text.Body, 10, 10, theme.Paper)
		u.TextIn(c, "Carry me home", text.Body, 10, 10, theme.Paper)
		u.TextGlow("Carry me home", text.Body, 10, 10, 30, theme.Paper)
		u.Fit("Paper Lanterns — The Hollow Pines", text.Body, 120)
	}
	draw()
	made := u.Made()
	if draw(); u.Made() != made {
		t.Fatal("made again before fonts arrived")
	}
	u.Fonts.Refresh()
	u.Fit("Low tide radio — Carry me home", text.Body, 120)
	if len(u.fits) != 1 {
		t.Fatalf("%d fits kept from before", len(u.fits)-1)
	}
	if draw(); u.Made() != made+3 {
		t.Fatalf("%d textures made again, want the text twice and its glow", u.Made()-made)
	}
}

func TestImageDrawsWhatWasPut(t *testing.T) {
	u, off := newContext(t, 1)
	img := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 200, 0, 200, 255
	}
	u.PutImage("made elsewhere", img)
	u.Image("made elsewhere", func() *image.NRGBA {
		t.Fatal("an image that was put was built again")
		return nil
	}, gfx.Rect{X: 0, Y: 0, W: 8, H: 8}, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
	if c := off.Image().NRGBAAt(4, 4); c != (color.NRGBA{R: 200, B: 200, A: 255}) {
		t.Fatalf("drew %v", c)
	}
}

// Backgrounds keep to a cache of their own, of the last few: one for album
// after album pushes no other image out.
func TestGroundsKeepToTheirOwnCache(t *testing.T) {
	u, _ := newContext(t, 1)
	u.PutImage("qr", image.NewNRGBA(image.Rect(0, 0, 8, 8)))
	ground := image.NewNRGBA(image.Rect(0, 0, theme.DeviceW, theme.DeviceH))
	n := imageBudget/len(ground.Pix) + 1 // more than the shared cache holds
	for i := range n {
		u.PutImage(GroundPrefix+strconv.Itoa(i), ground)
	}
	u.Image(GroundPrefix+"built", func() *image.NRGBA { return ground }, gfx.Rect{W: 8, H: 8}, theme.Paper)
	if u.grounds.Len() != maxGrounds || u.images.Len() != 1 {
		t.Fatalf("%d grounds kept and %d other images, want %d and 1", u.grounds.Len(), u.images.Len(), maxGrounds)
	}
	u.Image("qr", func() *image.NRGBA {
		t.Fatal("the grounds pushed an image out")
		return nil
	}, gfx.Rect{W: 8, H: 8}, theme.Paper)
}

// backdrops hands out a backdrop for any album, or says there is none.
type backdrops struct{ none bool }

func (b backdrops) Cover(media.Cover, int) (*image.NRGBA, bool) { return nil, true }
func (b backdrops) Backdrop(media.Cover) (*image.NRGBA, bool) {
	if b.none {
		return nil, true
	}
	return image.NewNRGBA(image.Rect(0, 0, 64, 64)), false
}

func TestBackdropFadesIn(t *testing.T) {
	u, _ := newContext(t, 1)
	u.Covers = backdrops{}
	c := media.Cover{Artist: "Odalys", Album: "Copper Season", Path: "/m/1.flac"}
	t0 := time.Unix(0, 0)
	u.BeginFrame(t0)
	if tex, a, none := u.Backdrop(c); tex == nil || a != 0 || none {
		t.Fatalf("first frame: %v, %v, %v", tex, a, none)
	}
	u.BeginFrame(t0.Add(anim.ArtFade))
	if tex, a, _ := u.Backdrop(c); tex == nil || a != 1 {
		t.Fatalf("after the fade: %v, %v", tex, a)
	}
	if n := testing.AllocsPerRun(20, func() { u.Backdrop(c) }); n != 0 {
		t.Fatalf("a cached backdrop allocates %v times", n)
	}
	u.Covers = backdrops{none: true}
	if _, _, none := u.Backdrop(media.Cover{Album: "Other"}); !none {
		t.Fatal("an album without art has a backdrop")
	}
}

// urlCovers hands out a picture for every cover and records what was asked.
type urlCovers struct{ asked []media.Cover }

func (c *urlCovers) Cover(cv media.Cover, px int) (*image.NRGBA, bool) {
	c.asked = append(c.asked, cv)
	return image.NewNRGBA(image.Rect(0, 0, px, px)), false
}

func (c *urlCovers) Backdrop(media.Cover) (*image.NRGBA, bool) { return nil, true }

func TestCoversNamedByURL(t *testing.T) {
	u, _ := newContext(t, 1.25)
	c := &urlCovers{}
	u.Covers = c
	u.BeginFrame(time.Unix(0, 0))
	a := media.Cover{URL: "https://example.com/a"}
	if tex, _ := u.Cover(a, 58, 8); tex == nil {
		t.Fatal("no texture for a cover named by URL")
	}
	// Another picture of the same album is another cover.
	b := media.Cover{Artist: "Radiohead", Album: "OK Computer", URL: "https://example.com/b"}
	u.Cover(b, 58, 8)
	u.Cover(a, 58, 8) // kept
	if len(c.asked) != 2 || c.asked[0].URL != a.URL || c.asked[1].URL != b.URL {
		t.Fatalf("asked %+v", c.asked)
	}
}

// A texture that couldn't be uploaded isn't made again every frame, only once
// failWait has passed.
func TestAFailedUploadWaits(t *testing.T) {
	u, _ := newContext(t, 1)
	builds := 0
	build := func() *image.NRGBA {
		builds++
		return image.NewNRGBA(image.Rect(0, 0, 0, 0)) // an empty image never uploads
	}
	t0 := time.Unix(0, 0)
	for i := range 3 {
		u.BeginFrame(t0.Add(time.Duration(i) * time.Second))
		u.Image("broken", build, gfx.Rect{W: 10, H: 10}, theme.Paper)
	}
	if builds != 1 {
		t.Fatalf("built %d times in 3 s, want once", builds)
	}
	u.BeginFrame(t0.Add(failWait))
	u.Image("broken", build, gfx.Rect{W: 10, H: 10}, theme.Paper)
	if builds != 2 {
		t.Fatalf("built %d times, want again after %v", builds, failWait)
	}
}

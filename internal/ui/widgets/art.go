package widgets

import (
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"math"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/icons"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// ArtSpec describes an item's art: the placeholder generated for it and,
// when it has one, the album whose cover replaces it.
type ArtSpec struct {
	Hue     float64
	Variant int
	Glyph   string
	Cover   media.Cover // zero: the placeholder only
}

// specs remembers SpecFor's answers: screens ask for every item they draw,
// every frame.
var specs struct {
	sync.Mutex
	m map[string]ArtSpec
}

// SpecFor derives stable placeholder art for an album or playlist name.
func SpecFor(name string) ArtSpec {
	specs.Lock()
	defer specs.Unlock()
	if s, ok := specs.m[name]; ok {
		return s
	}
	if len(specs.m) >= 4096 || specs.m == nil {
		specs.m = map[string]ArtSpec{}
	}
	h := fnv.New32a()
	h.Write([]byte(name))
	v := h.Sum32()
	r, _ := utf8.DecodeRuneInString(name)
	s := ArtSpec{Hue: float64(v % 360), Variant: int(v / 360 % 4), Glyph: strings.ToUpper(string(r))}
	specs.m[name] = s
	return s
}

// placeholderKey is what a placeholder's image depends on.
type placeholderKey struct {
	hue     float64
	variant int
	glyph   string
	px      int
	radius  float64
	fonts   uint64 // the font engine's generation, for a placeholder with a letter
}

// placeholderKeys remembers the image key of each placeholder drawn lately,
// so that a frame formats none. Only the UI thread draws.
var placeholderKeys = map[placeholderKey]string{}

// placeholderImageKey is the image key of spec's placeholder px pixels square.
// One that shows a letter names the fonts it was drawn in, so that a letter
// drawn as a box before its font arrived is drawn again with it.
func placeholderImageKey(u *ui.Context, spec ArtSpec, px int, radius float64) string {
	var fonts uint64
	if spec.Variant%4 == 2 && spec.Glyph != "" {
		fonts = u.Fonts.Generation()
	}
	pk := placeholderKey{spec.Hue, spec.Variant, spec.Glyph, px, radius, fonts}
	key, ok := placeholderKeys[pk]
	if !ok {
		if len(placeholderKeys) >= 4096 {
			clear(placeholderKeys)
		}
		key = fmt.Sprintf("art:%g:%d:%s:%d:%g:%d", spec.Hue, spec.Variant, spec.Glyph, px, radius, fonts)
		placeholderKeys[pk] = key
	}
	return key
}

// placeholder draws spec's generated art, size design pixels square, into box.
func placeholder(u *ui.Context, spec ArtSpec, size int, radius float64, box gfx.Rect) {
	px := u.Px(float32(size))
	// Draw at the rasterised size so the copy stays 1:1.
	k := float32(px) / (float32(size) * u.Scale)
	u.Image(placeholderImageKey(u, spec, px, radius), func() *image.NRGBA {
		var glyph *image.NRGBA
		gx, gy := 0, 0
		if spec.Variant%4 == 2 && spec.Glyph != "" {
			glyph, gx, gy = artGlyph(u, spec.Glyph, px)
		}
		return raster.Placeholder(px, radius*float64(u.Scale), spec.Hue, spec.Variant, glyph, gx, gy)
	}, gfx.Rect{X: box.X, Y: box.Y, W: box.W * k, H: box.H * k}, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
}

// Art draws spec's art, size design pixels square with corners of radius,
// into box: the album's cover once it has arrived, faded in over the
// placeholder, and the placeholder until then or when there is none.
func Art(u *ui.Context, spec ArtSpec, size int, radius float64, box gfx.Rect) {
	t, a := u.Cover(spec.Cover, float32(size), radius)
	if a < 1 {
		placeholder(u, spec, size, radius, box)
	}
	if t != nil {
		u.DrawCover(t, box, a)
	}
}

// artGlyph renders the letter for variant 2: Outfit at 90 % of the art size,
// 10 % from the left, in a line box that ends 6 % below the art.
// Liked Songs uses a heart icon instead of ♥, which the fonts lack. size is in
// device pixels: this calls the text engine directly, which works in whatever
// pixels the style gives it.
func artGlyph(u *ui.Context, glyph string, size int) (*image.NRGBA, int, int) {
	s := float64(size)
	if glyph == "♥" {
		hs := int(0.45 * s)
		img, err := icons.Rasterize("heart", hs, 0, true)
		if err != nil {
			return nil, 0, 0
		}
		return img, int(0.12 * s), size - hs - int(0.08*s)
	}
	fs := int(math.Round(0.9 * s))
	st := text.Style{Face: text.Outfit, Size: float32(fs)}
	line := u.Fonts.Shape(glyph, st)
	img, pad := u.Fonts.Rasterize(line, st)
	top := 1.06*s - float64(fs) + (float64(fs)-float64(line.Height()))/2
	return img, int(0.1*s) - pad.X, int(math.Round(top)) - pad.Y
}

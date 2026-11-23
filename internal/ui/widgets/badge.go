package widgets

import (
	"image/color"
	"math"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

var badges = map[media.Source]struct{ icon, label string }{
	media.Spotify:    {"stream", "Spotify"},
	media.YouTube:    {"circleplay", "YouTube Music"},
	media.Downloaded: {"circledown", "Downloaded"},
	media.Local:      {"folder", "Local"},
	media.Radio:      {"radio", "Radio"},
}

// BadgeWidth measures a source badge.
func BadgeWidth(u *ui.Context, src media.Source) float32 {
	return float32(math.Ceil(float64(10 + 18 + 7 + u.TextWidth(badges[src].label, text.Badge) + 13)))
}

// Badge draws a source badge with its top-left at (x, y) and returns its
// width. dim fades it, as the Spotify badge is while offline.
func Badge(u *ui.Context, src media.Source, x, y float32, dim bool) float32 {
	b := badges[src]
	w := BadgeWidth(u, src)
	a := float32(1)
	if dim {
		a = 0.45
	}
	box := gfx.Rect{X: x, Y: y, W: w, H: 32}
	shape := raster.Shape{W: int(w), H: 32, Radius: 16, Kind: raster.Fill}
	u.Shape(shape, box, fade(theme.BadgeBG, a))
	shape.Kind, shape.RingOffset, shape.RingWidth = raster.Ring, -1.5, 1.5
	u.Shape(shape, box, fade(theme.PaperA(0.30), a))
	ink := fade(theme.PaperA(0.86), a)
	u.Icon(b.icon, 18, 2, false, x+10, y+7, ink)
	u.Text(b.label, text.Badge, x+35, u.CenterY(text.Badge, y, 32), ink)
	return w
}

// fade multiplies c's opacity by a.
func fade(c color.NRGBA, a float32) color.NRGBA {
	c.A = uint8(float32(c.A) * min(max(a, 0), 1))
	return c
}

// Package gfx is the thin layer over SDL's renderer that the UI draws through.
package gfx

import (
	"errors"
	"image"
	"image/color"
	"math"
	"unsafe"

	"github.com/veandco/go-sdl2/sdl"
)

// Rect is a destination rectangle in device pixels.
type Rect struct{ X, Y, W, H float32 }

// Canvas draws textures and rectangles with an SDL renderer, from the UI
// thread only.
type Canvas struct {
	r *sdl.Renderer
	// What a draw hands SDL. A rectangle passed to C escapes to the heap, so
	// these are reused rather than made for every draw.
	dst  sdl.FRect
	src  sdl.Rect
	clip sdl.Rect
}

// New wraps r.
func New(r *sdl.Renderer) *Canvas { return &Canvas{r: r} }

// Texture is an uploaded image.
type Texture struct {
	t    *sdl.Texture
	W, H int
}

// Bytes is the texture's approximate memory use.
func (t *Texture) Bytes() int { return t.W * t.H * 4 }

// Destroy frees the texture.
func (t *Texture) Destroy() { t.t.Destroy() }

// Upload copies img into a new texture with alpha blending enabled.
func (c *Canvas) Upload(img *image.NRGBA) (*Texture, error) {
	b := img.Bounds()
	if b.Empty() {
		return nil, errors.New("gfx: empty image")
	}
	t, err := c.r.CreateTexture(uint32(sdl.PIXELFORMAT_ABGR8888), sdl.TEXTUREACCESS_STATIC, int32(b.Dx()), int32(b.Dy()))
	if err != nil {
		return nil, err
	}
	if err := t.Update(nil, unsafe.Pointer(&img.Pix[0]), img.Stride); err != nil {
		t.Destroy()
		return nil, err
	}
	if err := t.SetBlendMode(sdl.BLENDMODE_BLEND); err != nil {
		t.Destroy()
		return nil, err
	}
	return &Texture{t: t, W: b.Dx(), H: b.Dy()}, nil
}

// Draw copies the whole texture into dst, multiplied by col.
func (c *Canvas) Draw(t *Texture, dst Rect, col color.NRGBA) {
	// Linear filtering blurs a 1:1 copy that lands between pixels. Sizes come
	// from design pixels times the scale, so allow for float rounding.
	if nearInt(dst.W, t.W) && nearInt(dst.H, t.H) {
		dst.W, dst.H = float32(t.W), float32(t.H)
		dst.X = float32(math.Round(float64(dst.X)))
		dst.Y = float32(math.Round(float64(dst.Y)))
	}
	t.t.SetColorMod(col.R, col.G, col.B)
	t.t.SetAlphaMod(col.A)
	c.dst = sdl.FRect{X: dst.X, Y: dst.Y, W: dst.W, H: dst.H}
	c.r.CopyF(t.t, nil, &c.dst)
}

// DrawPart copies the part src of the texture, in its own pixels, into dst,
// multiplied by col.
func (c *Canvas) DrawPart(t *Texture, src image.Rectangle, dst Rect, col color.NRGBA) {
	t.t.SetColorMod(col.R, col.G, col.B)
	t.t.SetAlphaMod(col.A)
	c.src = sdl.Rect{X: int32(src.Min.X), Y: int32(src.Min.Y), W: int32(src.Dx()), H: int32(src.Dy())}
	c.dst = sdl.FRect{X: dst.X, Y: dst.Y, W: dst.W, H: dst.H}
	c.r.CopyF(t.t, &c.src, &c.dst)
}

func nearInt(v float32, n int) bool { return math.Abs(float64(v)-float64(n)) < 0.01 }

// Fill blends a solid rectangle.
func (c *Canvas) Fill(r Rect, col color.NRGBA) {
	c.r.SetDrawBlendMode(sdl.BLENDMODE_BLEND)
	c.r.SetDrawColor(col.R, col.G, col.B, col.A)
	x, y, w, h := snapEdges(r)
	c.dst = sdl.FRect{X: float32(x), Y: float32(y), W: float32(w), H: float32(h)}
	c.r.FillRectF(&c.dst)
}

// snapEdges rounds r's edges, not its size, to whole pixels. A texture and a
// clip or fill that meet at the same fractional edge then agree on the row.
func snapEdges(r Rect) (x, y, w, h int32) {
	x0, x1 := math.Round(float64(r.X)), math.Round(float64(r.X+r.W))
	y0, y1 := math.Round(float64(r.Y)), math.Round(float64(r.Y+r.H))
	return int32(x0), int32(y0), int32(x1 - x0), int32(y1 - y0)
}

// Clear fills the whole target with an opaque colour.
func (c *Canvas) Clear(col color.NRGBA) {
	c.r.SetDrawColor(col.R, col.G, col.B, 255)
	c.r.Clear()
}

// Present shows the frame.
func (c *Canvas) Present() { c.r.Present() }

// Clip limits drawing to r until Unclip.
func (c *Canvas) Clip(r Rect) {
	x, y, w, h := snapEdges(r)
	c.clip = sdl.Rect{X: x, Y: y, W: w, H: h}
	c.r.SetClipRect(&c.clip)
}

// Unclip removes the clip rectangle.
func (c *Canvas) Unclip() { c.r.SetClipRect(nil) }

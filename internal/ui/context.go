// Package ui is the drawing context shared by widgets and screens. It wraps
// the canvas with caches for text, icons, masks and images, applies the scale
// transforms used by focus animation, and carries per-frame state. Callers work
// in design pixels; the context draws at Scale device pixels per design pixel
// and rasterises at device size so nothing is stretched.
package ui

import (
	"image"
	"image/color"
	"math"
	"strings"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/icons"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// Status is what the status bar shows.
type Status struct {
	Time       string
	NowPlaying string
	Online     bool
	Bluetooth  string
	Battery    int
	Charging   bool // the battery is on the charger
}

// Covers supplies album art. Neither method blocks: each returns nil while
// the picture is being prepared, and none once it's known there is none. A
// picture is handed over once; the context keeps it as a texture.
type Covers interface {
	Cover(c media.Cover, px int) (img *image.NRGBA, none bool)
	Backdrop(c media.Cover) (img *image.NRGBA, none bool)
}

// Fade is a change of accent in progress: backgrounds drawn with From show
// under those drawn with To, which are at T, from 0 to 1. The zero Fade is
// none, and backgrounds use Accent.
type Fade struct {
	From, To theme.Accent
	T        float32
	On       bool
}

// Context is passed to every widget and screen while drawing. It is only used
// from the UI thread.
type Context struct {
	Canvas     *gfx.Canvas
	Fonts      *text.Engine
	Accent     theme.Accent
	Fade       Fade // a change of accent under way, for the backgrounds
	Status     Status
	Now        time.Time
	Scale      float32 // device pixels per design pixel
	Alpha      float32 // multiplies the opacity of everything drawn; screens fade with it
	Covers     Covers  // nil: placeholders only
	MaxUploads int     // covers turned into textures per frame; the rest wait for the next frames

	texts    *TextCache
	fits     map[fitKey]string
	masks    *LRU[raster.Shape, *gfx.Texture]
	images   *LRU[string, *gfx.Texture]
	grounds  *LRU[string, *gfx.Texture] // images under GroundPrefix, by count
	icons    map[iconKey]*gfx.Texture
	covers   *LRU[coverKey, *gfx.Texture]
	glows    *LRU[glowKey, glowEntry]
	fadeFrom map[coverKey]time.Time // when each cover arrived, while it fades in
	failed   map[any]time.Time      // uploads that failed lately, by cache key, and when
	uploads  int                    // covers uploaded this frame
	pending  bool                   // this frame left a cover fading in or waiting to be uploaded
	tf       []transform
	clips    []gfx.Rect
	made     int // text and glow textures made
}

// TextCache holds the textures of text that has been drawn, up to a budget
// of bytes, letting the least recently drawn go first. Text draws through the
// context's own; a screen whose text is large keeps one of its own for it, so
// as not to push everything else out of the shared one.
type TextCache struct {
	lru *LRU[textKey, textEntry]
}

// NewTextCache returns a cache for budget bytes of textures.
func NewTextCache(budget int) *TextCache {
	return &TextCache{NewLRU[textKey, textEntry](budget,
		func(e textEntry) int { return e.tex.Bytes() },
		func(e textEntry) { e.tex.Destroy() })}
}

// Len is the number of texts in the cache.
func (c *TextCache) Len() int { return c.lru.Len() }

// Clear frees the cache's textures.
func (c *TextCache) Clear() { c.lru.Clear() }

type textKey struct {
	s  string
	st text.Style
}

type textEntry struct {
	tex   *gfx.Texture
	width float32 // advance in design pixels
	top   float32 // how far the texture starts above the line box, in design pixels
}

type fitKey struct {
	s    string
	st   text.Style
	maxW int
}

type coverKey struct {
	artist string
	album  string
	url    string
	px     int
	radius float64 // device pixels
}

// glowDown is how much smaller than the text its glow is made; a blur this
// soft loses nothing when the renderer scales it back up.
const glowDown = 4

type glowKey struct {
	s  string
	st text.Style
	r  int // blur radius, device pixels
}

type glowEntry struct {
	tex       *gfx.Texture
	left, top int // texture pixels (1/glowDown of device) from where the text is drawn to the texture's corner
}

type iconKey struct {
	name   string
	size   int
	sw     float64
	filled bool
}

type transform struct{ ox, oy, s, dx, dy float32 }

// Cache budgets in bytes of texture memory, plus the number of remembered
// truncations.
const (
	textBudget  = 24 << 20
	maskBudget  = 16 << 20
	imageBudget = 48 << 20
	coverBudget = 24 << 20
	glowBudget  = 8 << 20
	maxFits     = 4096
)

// GroundPrefix starts the keys of screen backgrounds. Image and PutImage keep
// them apart from other images, the last maxGrounds of them: a background is
// rendered for every album's colours, and in the shared cache they would push
// out the images drawn over them.
const GroundPrefix = "bg:"

// maxGrounds is more than a frame draws: the tabs' and Now Playing's grounds
// in both colours of a change, as a screen slides in over a tab, and the
// state screens' ground.
const maxGrounds = 6

// failWait is how long a texture that couldn't be uploaded isn't made again:
// making it every frame would only fail again, at the frame's cost. At most
// maxFailed failures are remembered.
const (
	failWait  = 5 * time.Second
	maxFailed = 256
)

// NewContext creates a context that draws to c using fonts, with scale device
// pixels per design pixel.
func NewContext(c *gfx.Canvas, fonts *text.Engine, scale float32) *Context {
	texSize := func(t *gfx.Texture) int { return t.Bytes() }
	texFree := func(t *gfx.Texture) { t.Destroy() }
	return &Context{
		Canvas: c,
		Fonts:  fonts,
		Accent: theme.Fallback(),
		Scale:  scale,
		Alpha:  1,
		texts:  NewTextCache(textBudget),
		glows: NewLRU[glowKey, glowEntry](glowBudget,
			func(e glowEntry) int { return e.tex.Bytes() },
			func(e glowEntry) { e.tex.Destroy() }),
		fits:       map[fitKey]string{},
		masks:      NewLRU[raster.Shape, *gfx.Texture](maskBudget, texSize, texFree),
		images:     NewLRU[string, *gfx.Texture](imageBudget, texSize, texFree),
		grounds:    NewLRU[string, *gfx.Texture](maxGrounds, func(*gfx.Texture) int { return 1 }, texFree),
		icons:      map[iconKey]*gfx.Texture{},
		covers:     NewLRU[coverKey, *gfx.Texture](coverBudget, texSize, texFree),
		fadeFrom:   map[coverKey]time.Time{},
		MaxUploads: 3,
	}
}

// failedLately reports whether the texture under cache key k failed to upload
// within failWait.
func (u *Context) failedLately(k any) bool {
	at, ok := u.failed[k]
	if !ok {
		return false
	}
	if u.Now.Sub(at) < failWait {
		return true
	}
	delete(u.failed, k)
	return false
}

// upload uploads img, the texture for cache key k, remembering a failure.
func (u *Context) upload(k any, img *image.NRGBA) (*gfx.Texture, error) {
	t, err := u.Canvas.Upload(img)
	if err != nil {
		if u.failed == nil || len(u.failed) >= maxFailed {
			u.failed = map[any]time.Time{}
		}
		u.failed[k] = u.Now
	}
	return t, err
}

// PushScale scales everything drawn until the matching Pop by s around (ox, oy).
func (u *Context) PushScale(ox, oy, s float32) { u.tf = append(u.tf, transform{ox: ox, oy: oy, s: s}) }

// PushTranslate moves everything drawn until the matching Pop by (dx, dy).
func (u *Context) PushTranslate(dx, dy float32) {
	u.tf = append(u.tf, transform{s: 1, dx: dx, dy: dy})
}

// Pop undoes the most recent PushScale or PushTranslate.
func (u *Context) Pop() { u.tf = u.tf[:len(u.tf)-1] }

// Apply maps r through the active transforms, innermost first, and into
// device pixels.
func (u *Context) Apply(r gfx.Rect) gfx.Rect {
	for i := len(u.tf) - 1; i >= 0; i-- {
		t := u.tf[i]
		r = gfx.Rect{X: t.ox + (r.X-t.ox)*t.s + t.dx, Y: t.oy + (r.Y-t.oy)*t.s + t.dy, W: r.W * t.s, H: r.H * t.s}
	}
	s := u.Scale
	return gfx.Rect{X: r.X * s, Y: r.Y * s, W: r.W * s, H: r.H * s}
}

// Px is v design pixels as a whole number of device pixels: the size to
// rasterise at. Draw the result Px(v)/Scale design pixels wide for a 1:1 copy.
func (u *Context) Px(v float32) int { return int(math.Round(float64(v * u.Scale))) }

// device is st at its device pixel size.
func (u *Context) device(st text.Style) text.Style {
	st.Size *= u.Scale
	return st
}

// PushClip restricts drawing to r, intersected with any enclosing clip, until
// the matching PopClip.
func (u *Context) PushClip(r gfx.Rect) {
	r = u.Apply(r)
	if n := len(u.clips); n > 0 {
		r = intersect(u.clips[n-1], r)
	}
	u.clips = append(u.clips, r)
	u.Canvas.Clip(r)
}

// PopClip restores the previous clip.
func (u *Context) PopClip() {
	u.clips = u.clips[:len(u.clips)-1]
	if n := len(u.clips); n > 0 {
		u.Canvas.Clip(u.clips[n-1])
	} else {
		u.Canvas.Unclip()
	}
}

func intersect(a, b gfx.Rect) gfx.Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	return gfx.Rect{X: x0, Y: y0, W: max(0, x1-x0), H: max(0, y1-y0)}
}

// Fill draws a solid rectangle.
func (u *Context) Fill(r gfx.Rect, col color.NRGBA) { u.Canvas.Fill(u.Apply(r), u.tint(col)) }

// tint applies Alpha to col.
func (u *Context) tint(col color.NRGBA) color.NRGBA {
	if u.Alpha < 1 {
		col.A = uint8(float32(col.A)*max(u.Alpha, 0) + 0.5)
	}
	return col
}

func (u *Context) text(c *TextCache, s string, st text.Style) (textEntry, bool) {
	k := textKey{s, st}
	if e, ok := c.lru.Get(k); ok {
		return e, true
	}
	if u.failedLately(k) {
		return textEntry{}, false
	}
	ds := u.device(st)
	line := u.Fonts.Shape(s, ds)
	img, pad := u.Fonts.Rasterize(line, ds)
	u.made++
	tex, err := u.upload(k, img)
	if err != nil {
		return textEntry{}, false
	}
	e := textEntry{tex: tex, width: float32(line.Width) / u.Scale, top: float32(pad.Y) / u.Scale}
	c.lru.Put(k, e)
	return e, true
}

// Text draws s with the top of its line box at y and returns its advance.
func (u *Context) Text(s string, st text.Style, x, y float32, col color.NRGBA) float32 {
	return u.TextIn(u.texts, s, st, x, y, col)
}

// TextIn draws s as Text does, keeping its texture in c.
func (u *Context) TextIn(c *TextCache, s string, st text.Style, x, y float32, col color.NRGBA) float32 {
	if s == "" {
		return 0
	}
	e, ok := u.text(c, s, st)
	if !ok {
		return 0
	}
	pad := float32(text.Pad(u.device(st))) / u.Scale
	dst := gfx.Rect{X: x - pad, Y: y - e.top, W: float32(e.tex.W) / u.Scale, H: float32(e.tex.H) / u.Scale}
	u.Canvas.Draw(e.tex, u.Apply(dst), u.tint(col))
	return e.width
}

// PrepareText makes the texture TextIn would draw s with and keeps it in c,
// drawing nothing, so that a frame to come can draw it without making it. It
// reports whether it had to be made.
func (u *Context) PrepareText(c *TextCache, s string, st text.Style) bool {
	if s == "" {
		return false
	}
	if _, ok := c.lru.Get(textKey{s, st}); ok {
		return false
	}
	u.text(c, s, st)
	return true
}

// Made is how many text and glow textures the context has made: a frame that
// leaves it unchanged drew only what was ready.
func (u *Context) Made() int { return u.made }

// TextWidth measures s without drawing it.
func (u *Context) TextWidth(s string, st text.Style) float32 {
	if e, ok := u.texts.lru.Get(textKey{s, st}); ok {
		return e.width
	}
	return float32(u.Fonts.Shape(s, u.device(st)).Width) / u.Scale
}

// Fit returns s shortened with an ellipsis to fit maxW. Results are cached
// because shaping during every frame would be wasteful.
func (u *Context) Fit(s string, st text.Style, maxW float32) string {
	dw := int(maxW * u.Scale)
	k := fitKey{s, st, dw}
	if f, ok := u.fits[k]; ok {
		return f
	}
	if len(u.fits) >= maxFits {
		clear(u.fits)
	}
	f := u.Fonts.Fit(s, u.device(st), dw)
	u.fits[k] = f
	return f
}

// LineHeight is the height of st's line box.
func (u *Context) LineHeight(st text.Style) float32 {
	a, d := u.Fonts.Metrics(u.device(st))
	return float32(a+d) / u.Scale
}

// Ascent is the distance from the top of st's line box to its baseline.
func (u *Context) Ascent(st text.Style) float32 {
	a, _ := u.Fonts.Metrics(u.device(st))
	return float32(a) / u.Scale
}

// CenterY is the y at which st's line box sits centred in [top, top+h).
func (u *Context) CenterY(st text.Style, top, h float32) float32 {
	return top + (h-u.LineHeight(st))/2
}

// Baseline is the y at which to draw st so its baseline lands on baseline.
func (u *Context) Baseline(st text.Style, baseline float32) float32 {
	return baseline - u.Ascent(st)
}

// Icon draws an icon size design pixels square with its top-left at (x, y).
func (u *Context) Icon(name string, size int, strokeWidth float64, filled bool, x, y float32, col color.NRGBA) {
	px := u.Px(float32(size))
	k := iconKey{name, px, strokeWidth, filled}
	t, ok := u.icons[k]
	if !ok {
		if u.failedLately(k) {
			return
		}
		img, err := icons.Rasterize(name, px, strokeWidth, filled)
		if err != nil {
			return
		}
		if t, err = u.upload(k, img); err != nil {
			return
		}
		u.icons[k] = t
	}
	d := float32(px) / u.Scale
	u.Canvas.Draw(t, u.Apply(gfx.Rect{X: x, Y: y, W: d, H: d}), u.tint(col))
}

// Shape draws mask s for an element occupying box, stretching the mask if box
// differs from s's size. s is in design pixels; the mask is rendered at device
// size.
func (u *Context) Shape(s raster.Shape, box gfx.Rect, col color.NRGBA) {
	k := float64(u.Scale)
	ds := raster.Shape{
		W: u.Px(float32(s.W)), H: u.Px(float32(s.H)), Kind: s.Kind,
		Radius: s.Radius * k, RingOffset: s.RingOffset * k, RingWidth: s.RingWidth * k,
		GlowSpread: s.GlowSpread * k, GlowBlur: s.GlowBlur * k,
		Sweep: s.Sweep,
	}
	t, ok := u.masks.Get(ds)
	if !ok {
		if u.failedLately(ds) {
			return
		}
		var err error
		if t, err = u.upload(ds, ds.Mask()); err != nil {
			return
		}
		u.masks.Put(ds, t)
	}
	// Design pixels per mask pixel. Relative to s, not ds, so an unscaled box
	// draws the mask at its rasterised size.
	sx, sy := box.W/float32(s.W)/u.Scale, box.H/float32(s.H)/u.Scale
	o := float32(ds.Outset())
	u.Canvas.Draw(t, u.Apply(gfx.Rect{X: box.X - o*sx, Y: box.Y - o*sy, W: float32(t.W) * sx, H: float32(t.H) * sy}), u.tint(col))
}

// Image draws the cached image named key into dst, building it on first use.
// A nil result from build is retried on the next call.
func (u *Context) Image(key string, build func() *image.NRGBA, dst gfx.Rect, col color.NRGBA) {
	c := u.imagesFor(key)
	t, ok := c.Get(key)
	if !ok {
		if u.failedLately(key) {
			return
		}
		img := build()
		if img == nil {
			return
		}
		var err error
		if t, err = u.upload(key, img); err != nil {
			return
		}
		c.Put(key, t)
	}
	u.Canvas.Draw(t, u.Apply(dst), u.tint(col))
}

// PutImage uploads img and keeps it under key, as Image keeps what it builds,
// so an image too slow to build on the UI thread can be made on another
// goroutine and drawn by Image without building it.
func (u *Context) PutImage(key string, img *image.NRGBA) {
	t, err := u.Canvas.Upload(img)
	if err != nil {
		return // Image builds it when it is drawn
	}
	u.imagesFor(key).Put(key, t)
}

// imagesFor is the cache the image under key is kept in.
func (u *Context) imagesFor(key string) *LRU[string, *gfx.Texture] {
	if strings.HasPrefix(key, GroundPrefix) {
		return u.grounds
	}
	return u.images
}

// BeginFrame starts a frame drawn at now.
func (u *Context) BeginFrame(now time.Time) {
	u.Now = now
	u.uploads, u.pending = 0, false
}

// Animating reports whether the last frame left a cover fading in, or one
// waiting for its turn to be uploaded, so another frame is due.
func (u *Context) Animating() bool { return u.pending }

// Cover returns c's art for an element size design pixels square with
// corners of radius, and how opaque to draw it: each cover fades in over
// anim.ArtFade when it first shows. A nil texture means there is no art, or
// none yet; the caller draws the placeholder then, and under a cover that is
// still fading in.
func (u *Context) Cover(c media.Cover, size float32, radius float64) (*gfx.Texture, float32) {
	if c.Album == "" && c.URL == "" || u.Covers == nil {
		return nil, 0
	}
	k := coverKey{url: c.URL, px: u.Px(size), radius: radius * float64(u.Scale)}
	if c.URL == "" {
		k.artist, k.album = c.Artist, c.Album
	}
	t, ok := u.covers.Get(k)
	if !ok {
		if u.uploads >= u.MaxUploads {
			u.pending = true
			return nil, 0
		}
		img, _ := u.Covers.Cover(c, k.px)
		if img == nil {
			return nil, 0
		}
		// Art smaller than asked for is scaled up when drawn; round it in its own pixels.
		raster.RoundCorners(img, k.radius*float64(img.Rect.Dx())/float64(k.px))
		var err error
		if t, err = u.Canvas.Upload(img); err != nil {
			return nil, 0
		}
		u.uploads++
		u.covers.Put(k, t)
		u.fadeFrom[k] = u.Now
	}
	return t, u.fadeIn(k)
}

// fadeIn is how opaque the cover or backdrop k is at u.Now, fading in over
// anim.ArtFade from when it arrived. While it fades, more frames are due.
func (u *Context) fadeIn(k coverKey) float32 {
	from, fading := u.fadeFrom[k]
	if !fading {
		return 1
	}
	a := float32(u.Now.Sub(from)) / float32(anim.ArtFade)
	if a >= 1 {
		delete(u.fadeFrom, k)
		return 1
	}
	u.pending = true
	return max(a, 0)
}

// Backdrop returns c's Lyrics backdrop and how opaque to draw it, fading in
// as covers do. none is true when the album has no art; a nil texture
// without none means it isn't ready yet.
func (u *Context) Backdrop(c media.Cover) (t *gfx.Texture, alpha float32, none bool) {
	if c.Album == "" || u.Covers == nil {
		return nil, 0, true
	}
	k := coverKey{artist: c.Artist, album: c.Album} // px 0 marks the backdrop
	t, ok := u.covers.Get(k)
	if !ok {
		if u.uploads >= u.MaxUploads {
			u.pending = true
			return nil, 0, false
		}
		img, none := u.Covers.Backdrop(c)
		if img == nil {
			return nil, 0, none
		}
		var err error
		if t, err = u.Canvas.Upload(img); err != nil {
			return nil, 0, false
		}
		u.uploads++
		u.covers.Put(k, t)
		u.fadeFrom[k] = u.Now
	}
	return t, u.fadeIn(k), false
}

// TextGlow draws a glow behind s as Text draws it at (x, y): the glyphs
// blurred as CSS's text-shadow blurs them by blur design pixels, in col.
func (u *Context) TextGlow(s string, st text.Style, x, y, blur float32, col color.NRGBA) {
	if s == "" {
		return
	}
	e, ok := u.glow(s, st, blur)
	if !ok {
		return
	}
	dst := gfx.Rect{X: x - float32(e.left*glowDown)/u.Scale, Y: y - float32(e.top*glowDown)/u.Scale,
		W: float32(e.tex.W*glowDown) / u.Scale, H: float32(e.tex.H*glowDown) / u.Scale}
	u.Canvas.Draw(e.tex, u.Apply(dst), u.tint(col))
}

// PrepareGlow makes the glow TextGlow would draw behind s, drawing nothing, as
// PrepareText does for text, and reports whether it had to be made.
func (u *Context) PrepareGlow(s string, st text.Style, blur float32) bool {
	if s == "" {
		return false
	}
	if _, ok := u.glows.Get(glowKey{s, st, u.glowRadius(blur)}); ok {
		return false
	}
	u.glow(s, st, blur)
	return true
}

// glowRadius is the box blur radius in device pixels for a CSS blur of blur
// design pixels: a CSS blur is twice the Gaussian's deviation, and a box blur
// of radius r comes close to a deviation of r.
func (u *Context) glowRadius(blur float32) int { return max(int(blur*u.Scale/2+0.5), 1) }

// glow returns the glow of s blurred by blur design pixels, making it the first
// time.
func (u *Context) glow(s string, st text.Style, blur float32) (glowEntry, bool) {
	r := u.glowRadius(blur)
	k := glowKey{s, st, r}
	e, ok := u.glows.Get(k)
	if !ok {
		if u.failedLately(k) {
			return glowEntry{}, false
		}
		ds := u.device(st)
		ds.Size /= glowDown
		mask, pad := u.Fonts.Rasterize(u.Fonts.Shape(s, ds), ds)
		rq := max((r+glowDown/2)/glowDown, 1)
		m := 3 * rq // room for the glow to spread
		b := mask.Rect
		img := image.NewNRGBA(image.Rect(0, 0, b.Dx()+2*m, b.Dy()+2*m))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 255, 255, 255
		}
		for yy := range b.Dy() {
			for xx := range b.Dx() {
				img.Pix[img.PixOffset(xx+m, yy+m)+3] = mask.Pix[mask.PixOffset(xx+b.Min.X, yy+b.Min.Y)+3]
			}
		}
		raster.BlurAlpha(img, rq)
		u.made++
		tex, err := u.upload(k, img)
		if err != nil {
			return glowEntry{}, false
		}
		e = glowEntry{tex: tex, left: pad.X + m, top: pad.Y + m}
		u.glows.Put(k, e)
	}
	return e, true
}

// DrawCover draws a texture from Cover into dst at alpha.
func (u *Context) DrawCover(t *gfx.Texture, dst gfx.Rect, alpha float32) {
	u.Canvas.Draw(t, u.Apply(dst), u.tint(color.NRGBA{R: 255, G: 255, B: 255, A: uint8(255*alpha + 0.5)}))
}

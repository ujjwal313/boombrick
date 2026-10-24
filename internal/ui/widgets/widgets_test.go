package widgets

import (
	"bytes"
	"image"
	"image/color"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var designStatus = ui.Status{Time: "9:41", NowPlaying: "Low Tide Radio", Online: true, Bluetooth: "Kite Buds", Battery: 78}

// TestChromeSheet renders every shared component on one frame, laid out like
// the components sheet in the design system.
func TestChromeSheet(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(35, 0.12)
	u.Status = designStatus
	DrawBackground(u, TabGround)
	StatusBar(u)
	Tabs(u, 0)

	x := float32(32)
	for _, src := range []media.Source{media.Spotify, media.YouTube, media.Downloaded, media.Local} {
		x += Badge(u, src, x, 140, false) + 12
	}

	specs := []ArtSpec{{Hue: 35, Variant: 0, Glyph: "S"}, {Hue: 150, Variant: 1, Glyph: "N"}, {Hue: 60, Variant: 2, Glyph: "C"}, {Hue: 265, Variant: 3, Glyph: "A"}, {Hue: 330, Variant: 2, Glyph: "♥"}}
	for i, spec := range specs {
		Art(u, spec, 140, 12, gfx.Rect{X: 32 + float32(i)*156, Y: 190, W: 140, H: 140})
	}

	LogoMark(u, 40, 32, 380, theme.Gold)

	card := gfx.Rect{X: 600, Y: 360, W: 170, H: 170}
	u.PushScale(card.X+card.W/2, card.Y, 1.06)
	FocusGlow(u, card, 12, 1)
	FocusRing(u, card, 12, 1, theme.Ground)
	Art(u, ArtSpec{Hue: 35, Variant: 0, Glyph: "S"}, 170, 12, card)
	u.Pop()

	HintBar(u, []Hint{{Key: "A", Label: "Play"}, {Key: "B", Label: "Back"}, {Key: "X", Label: "Download"},
		{Key: "Y", Label: "Add to queue"}, {Key: "L1/R1", Label: "Tabs"}})

	uitest.Golden(t, "chrome", off.Image())
}

// A title too long for the space left of the right-hand items is cut short
// with an ellipsis instead of running under them.
func TestStatusBarLongTitle(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, int(StatusHeight*theme.Scale))
	u.Status = ui.Status{Time: "21:07", NowPlaying: "Paper Lanterns (Extended Session) — The Hollow Pines",
		Online: true, Bluetooth: "Kite Buds", Battery: 78}
	StatusBar(u)
	uitest.Golden(t, "status-long-title", off.Image())
}

func TestStatusBarOffline(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, int(StatusHeight*theme.Scale))
	u.Status = ui.Status{Time: "21:07", Battery: 23}
	StatusBar(u)
	uitest.Golden(t, "status-offline", off.Image())
}

// With no battery the percentage and glyph go, and "Offline" moves up to the
// right-hand padding.
func TestStatusBarWithoutBattery(t *testing.T) {
	bar := func(battery int) *image.NRGBA {
		u, off := uitest.NewContext(t, theme.DeviceW, int(StatusHeight*theme.Scale))
		u.Status = ui.Status{Time: "21:07", Battery: battery}
		StatusBar(u)
		return off.Image()
	}
	none, some := bar(-1), bar(23)
	// The right-most 40 px hold the battery glyph, or the end of "Offline".
	right := image.Rect(int((theme.ScreenW-28-40)*theme.Scale), 0, int((theme.ScreenW-28)*theme.Scale), none.Bounds().Dy())
	if brightest(some, right) < 200 {
		t.Fatal("the battery glyph should be drawn at the right")
	}
	if b := brightest(none, right); b < 120 || b >= brightest(some, right) {
		t.Fatalf("brightest at the right without a battery = %d: want the gold Offline label there", b)
	}
	if b := brightest(none, image.Rect(int((theme.ScreenW-28)*theme.Scale)+2, 0, theme.DeviceW, none.Bounds().Dy())); b > 40 {
		t.Fatalf("something is drawn in the right-hand padding: %d", b)
	}
	uitest.Golden(t, "status-no-battery", none)
}

// brightest is the highest blue value in r: 242 for paper, 141 for the gold
// Offline label and 20 for the ground.
func brightest(img *image.NRGBA, r image.Rectangle) uint8 {
	var b uint8
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			b = max(b, img.NRGBAAt(x, y).B)
		}
	}
	return b
}

func TestSpecForIsStable(t *testing.T) {
	a, b := SpecFor("Morning Ferry"), SpecFor("Morning Ferry")
	if a != b {
		t.Fatalf("SpecFor is not deterministic: %v vs %v", a, b)
	}
	if a.Glyph != "M" || a.Hue < 0 || a.Hue >= 360 || a.Variant < 0 || a.Variant > 3 {
		t.Fatalf("SpecFor = %+v", a)
	}
}

// TestListSheet renders list rows, pills, a toast and the empty state.
func TestListSheet(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(35, 0.12)
	DrawBackground(u, StateGround)
	art := SpecFor("Copper Season")
	rows := []Row{
		{Index: "1", Title: "Rust", Duration: "3:42"},
		{Index: "2", Title: "Ember", Duration: "4:05", Playing: true},
		{Art: &art, Title: "Rust", Subtitle: "Odalys · Copper Season", Duration: "3:42"},
		{Art: &art, Round: true, Title: "Odalys", Subtitle: "2 albums · 21 songs"},
		{Grip: true, Art: &art, Title: "Rust", Subtitle: "Odalys", Duration: "3:42"},
	}
	for i, r := range rows {
		k := float32(0)
		if i == 1 {
			k = 1
		}
		box := gfx.Rect{X: 32, Y: 20 + float32(i)*RowHeight, W: theme.ScreenW - 64, H: RowHeight}
		RowGlow(u, box, RowRadius, k)
		ListRow(u, r, box, k)
	}
	x := float32(32)
	PillGlow(u, "play", "Play", x, 440, 1)
	x += Pill(u, "play", "Play", x, 440, 1) + 12
	Pill(u, "shuffle", "", x, 440, 0)
	Toast(u, "Added to queue", 1)
	uitest.Golden(t, "list-sheet", off.Image())
}

func TestEmptyState(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	DrawBackground(u, StateGround)
	EmptyState(u, nil, "Your library is empty",
		"Copy music to the Music folder on the SD card, or find something on YouTube Music.", "/mnt/SDCARD/Music")
	uitest.Golden(t, "empty-state", off.Image())
}

// Given a cache, a state screen drawn again allocates nothing of its own: no
// more than drawing its mark and lines does on the test canvas, which
// allocates for each.
func TestEmptyStateWrapsOnce(t *testing.T) {
	if raceEnabled {
		t.Skip("allocations aren't counted under the race detector")
	}
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	const title, body, detail = "Your library is empty",
		"Copy music to the Music folder on the SD card, or find something on YouTube Music.", "/mnt/SDCARD/Music"
	var c Wrapped
	EmptyState(u, &c, title, body, detail)
	cached := testing.AllocsPerRun(20, func() { EmptyState(u, &c, title, body, detail) })
	lines := Wrap(u, body, stateBody, stateWidth)
	drawing := testing.AllocsPerRun(20, func() {
		LogoMark(u, 54, 0, 0, theme.Ink4)
		u.Text(title, stateTitle, 0, 0, theme.Paper)
		for _, l := range lines {
			u.Text(l, stateBody, 0, 0, theme.Ink2)
		}
		u.Text(detail, stateDetail, 0, 0, theme.Ink3)
	})
	if cached > drawing {
		t.Fatalf("a cached state allocates %v times a frame, drawing it %v", cached, drawing)
	}
	if every := testing.AllocsPerRun(20, func() { EmptyState(u, nil, title, body, detail) }); every <= drawing {
		t.Fatalf("without a cache, %v allocations: the description isn't wrapped each time", every)
	}
}

func TestWrap(t *testing.T) {
	u, _ := uitest.NewContext(t, 64, 64)
	lines := Wrap(u, "one two three four five six seven", text.Body, 120)
	if len(lines) < 2 || strings.Join(lines, " ") != "one two three four five six seven" {
		t.Fatalf("Wrap = %q", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, " ") && u.TextWidth(l, text.Body) > 120 {
			t.Fatalf("line %q is wider than 120", l)
		}
	}
}

func TestWrappedWrapsOnce(t *testing.T) {
	u, _ := uitest.NewContext(t, 64, 64)
	const s = "one two three four five six seven"
	var c Wrapped
	first := c.Lines(u, s, text.Body, 120)
	if !slices.Equal(first, Wrap(u, s, text.Body, 120)) {
		t.Fatalf("Lines = %q", first)
	}
	if again := c.Lines(u, s, text.Body, 120); &again[0] != &first[0] {
		t.Fatal("the same text was wrapped again")
	}
	if n := testing.AllocsPerRun(100, func() { c.Lines(u, s, text.Body, 120) }); n != 0 {
		t.Fatalf("Lines allocates %v times for the text it holds", n)
	}
	if got := c.Lines(u, "one two", text.Body, 120); !slices.Equal(got, []string{"one two"}) {
		t.Fatalf("new text: %q", got)
	}
	if got := c.Lines(u, s, text.Body, 1000); !slices.Equal(got, []string{s}) {
		t.Fatalf("new width: %q", got)
	}
	wide := c.Lines(u, s, text.Body, 1000)
	u.Scale = 2
	if got := c.Lines(u, s, text.Body, 1000); &got[0] == &wide[0] {
		t.Fatal("a new scale didn't wrap again")
	}
}

func TestSpecForRemembers(t *testing.T) {
	SpecFor("Copper Season")
	if n := testing.AllocsPerRun(100, func() { SpecFor("Copper Season") }); n != 0 {
		t.Fatalf("SpecFor allocates %v times per call for a name it has seen", n)
	}
}

// The status bar, a placeholder and the mark drawn again allocate nothing:
// their texts and image keys are made once, not every frame.
func TestChromeDrawnAgainAllocatesNothing(t *testing.T) {
	if raceEnabled {
		t.Skip("allocations aren't counted under the race detector")
	}
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Status = designStatus
	spec := ArtSpec{Hue: 60, Variant: 2, Glyph: "C"}
	draw := func() {
		StatusBar(u)
		Art(u, spec, 140, 12, gfx.Rect{W: 140, H: 140})
		LogoMark(u, 24, 0, 0, theme.Gold)
	}
	draw()
	if n := testing.AllocsPerRun(20, draw); n != 0 {
		t.Fatalf("drawn again, they allocate %v times", n)
	}
	u.Status.Battery = 5
	StatusBar(u)
	if batteryText.text != "5%" {
		t.Fatalf("at 5 %% the battery says %q", batteryText.text)
	}
}

// fakeCovers hands out a solid picture for any album once ready is set.
type fakeCovers struct {
	ready, none bool
	asked       int
	col         color.NRGBA
}

func (f *fakeCovers) Cover(c media.Cover, px int) (*image.NRGBA, bool) {
	f.asked++
	if f.none {
		return nil, true
	}
	if !f.ready {
		return nil, false
	}
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = f.col.R, f.col.G, f.col.B, 255
	}
	return img, false
}

func (f *fakeCovers) Backdrop(media.Cover) (*image.NRGBA, bool) { return nil, true }

func coverSpec(album string) ArtSpec {
	s := SpecFor(album)
	s.Cover = media.Cover{Artist: "Odalys", Album: album, Path: "/m/" + album + "/01.flac"}
	return s
}

func TestArtFadesInOverThePlaceholder(t *testing.T) {
	u, off := uitest.NewContext(t, 200, 200)
	covers := &fakeCovers{col: color.NRGBA{0, 200, 0, 255}}
	u.Covers = covers
	spec := coverSpec("Copper Season")
	box := gfx.Rect{X: 20, Y: 20, W: 100, H: 100}
	centre := func(at time.Time) color.NRGBA {
		u.BeginFrame(at)
		off.Clear(theme.Ground)
		Art(u, spec, 100, CardRadius, box)
		return off.Image().NRGBAAt(87, 87)
	}
	t0 := time.Unix(0, 0)
	placeholder := centre(t0)
	if u.Animating() {
		t.Fatal("art that isn't ready yet keeps frames coming")
	}
	covers.ready = true
	if got := centre(t0); got != placeholder || !u.Animating() {
		t.Fatalf("the frame the cover arrives shows %v (placeholder %v), animating %v", got, placeholder, u.Animating())
	}
	mid := centre(t0.Add(anim.ArtFade / 2))
	end := centre(t0.Add(anim.ArtFade))
	if end != (color.NRGBA{0, 200, 0, 255}) || u.Animating() {
		t.Fatalf("after the fade: %v, animating %v", end, u.Animating())
	}
	if lo, hi := min(placeholder.G, end.G), max(placeholder.G, end.G); mid.G <= lo || mid.G >= hi {
		t.Fatalf("halfway: %v, not between %v and %v", mid, placeholder, end)
	}
}

func TestArtWithoutACoverIsThePlaceholder(t *testing.T) {
	u, off := uitest.NewContext(t, 200, 200)
	box := gfx.Rect{X: 20, Y: 20, W: 100, H: 100}
	Art(u, SpecFor("Copper Season"), 100, CardRadius, box)
	want := off.Image()

	u2, off2 := uitest.NewContext(t, 200, 200)
	u2.Covers = &fakeCovers{none: true}
	u2.BeginFrame(time.Unix(0, 0))
	Art(u2, coverSpec("Copper Season"), 100, CardRadius, box)
	if !bytes.Equal(off2.Image().Pix, want.Pix) {
		t.Fatal("an album without art doesn't look like the placeholder")
	}
}

func TestCoverCornersAreRounded(t *testing.T) {
	u, off := uitest.NewContext(t, 200, 200)
	u.Covers = &fakeCovers{ready: true, col: color.NRGBA{0, 200, 0, 255}}
	box := gfx.Rect{X: 20, Y: 20, W: 100, H: 100}
	for _, at := range []time.Time{time.Unix(0, 0), time.Unix(1, 0)} {
		u.BeginFrame(at)
		off.Clear(theme.Ground)
		Art(u, coverSpec("Copper Season"), 100, CardRadius, box)
	}
	img := off.Image()
	if c := img.NRGBAAt(25, 25); c != theme.Ground {
		t.Fatalf("the corner shows %v, want the ground", c)
	}
	if c := img.NRGBAAt(87, 87); c != (color.NRGBA{0, 200, 0, 255}) {
		t.Fatalf("the centre shows %v", c)
	}
}

func TestCachedCoverDoesNotAllocate(t *testing.T) {
	u, _ := uitest.NewContext(t, 200, 200)
	u.Covers = &fakeCovers{ready: true, col: color.NRGBA{0, 200, 0, 255}}
	c := coverSpec("Copper Season").Cover
	t0 := time.Unix(0, 0)
	u.BeginFrame(t0)
	u.Cover(c, 100, 8)
	u.BeginFrame(t0.Add(2 * anim.ArtFade)) // the fade ends and its record goes
	u.Cover(c, 100, 8)
	if n := testing.AllocsPerRun(100, func() { u.Cover(c, 100, 8) }); n != 0 {
		t.Fatalf("Cover allocates %v times per call for a cover it holds", n)
	}
}

func TestUploadsWaitForTheNextFrame(t *testing.T) {
	u, _ := uitest.NewContext(t, 400, 200)
	covers := &fakeCovers{ready: true}
	u.Covers = covers
	u.MaxUploads = 2
	draw := func(at time.Time) {
		u.BeginFrame(at)
		for i, name := range []string{"A", "B", "C"} {
			Art(u, coverSpec(name), 60, 8, gfx.Rect{X: float32(10 + 70*i), Y: 10, W: 60, H: 60})
		}
	}
	draw(time.Unix(0, 0))
	if covers.asked != 2 || !u.Animating() {
		t.Fatalf("first frame asked for %d covers, animating %v; want 2 and another frame due", covers.asked, u.Animating())
	}
	draw(time.Unix(0, 0).Add(time.Millisecond))
	if covers.asked != 3 {
		t.Fatalf("second frame: %d asks in all, want 3", covers.asked)
	}
}

// A ground rendered on another goroutine and put in the context is drawn, not
// rendered again on the UI thread.
func TestBackgroundDrawsAGroundRenderedElsewhere(t *testing.T) {
	acc := theme.Derive(265, 0.12)
	for _, kind := range []Background{TabGround, NowPlayingGround} {
		u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
		u.Accent = acc
		made := GroundImage(kind, acc)
		for i := 0; i < len(made.Pix); i += 4 {
			made.Pix[i], made.Pix[i+1], made.Pix[i+2] = 200, 0, 200 // marked, to tell it from one rendered again
		}
		u.PutImage(GroundKey(kind, acc), made)
		DrawBackground(u, kind)
		if c := off.Image().NRGBAAt(10, 10); c != (color.NRGBA{R: 200, B: 200, A: 255}) {
			t.Errorf("ground %d: drew %v", kind, c)
		}
	}
}

// A ground drawn again costs no more allocations than drawing its texture:
// its key isn't formatted every frame.
func TestCachedGroundAllocatesOnlyToDraw(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	from, to := theme.Derive(35, 0.12), theme.Derive(265, 0.12)
	u.Accent = to
	u.PutImage("plain", image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	draw := testing.AllocsPerRun(20, func() {
		u.Image("plain", nil, gfx.Rect{W: theme.ScreenW, H: theme.ScreenH}, white)
	})
	DrawBackground(u, NowPlayingGround)
	if n := testing.AllocsPerRun(20, func() { DrawBackground(u, NowPlayingGround) }); n > draw {
		t.Fatalf("a cached ground allocates %v times; drawing a texture takes %v", n, draw)
	}
	u.Fade = ui.Fade{From: from, To: to, T: 0.5, On: true}
	DrawBackground(u, NowPlayingGround)
	if n := testing.AllocsPerRun(20, func() { DrawBackground(u, NowPlayingGround) }); n > 2*draw {
		t.Fatalf("a cross-fade of cached grounds allocates %v times; drawing a texture takes %v", n, draw)
	}
}

func TestBackgroundCrossFades(t *testing.T) {
	from, to := theme.Derive(35, 0.12), theme.Derive(265, 0.12)
	shot := func(f ui.Fade, acc theme.Accent) color.NRGBA {
		u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
		u.Accent, u.Fade = acc, f
		DrawBackground(u, TabGround)
		return off.Image().NRGBAAt(10, 10)
	}
	a, b := shot(ui.Fade{}, from), shot(ui.Fade{}, to)
	if a == b {
		t.Fatal("the two accents draw the same background")
	}
	if got := shot(ui.Fade{From: from, To: to, T: 0, On: true}, to); got != a {
		t.Fatalf("a fade's start shows %v, want the old background %v", got, a)
	}
	if got := shot(ui.Fade{From: from, To: to, T: 1, On: true}, to); got != b {
		t.Fatalf("a fade's end shows %v, want the new background %v", got, b)
	}
	mid := shot(ui.Fade{From: from, To: to, T: 0.5, On: true}, to)
	for _, c := range [][3]uint8{{a.R, b.R, mid.R}, {a.G, b.G, mid.G}, {a.B, b.B, mid.B}} {
		if lo, hi := min(c[0], c[1]), max(c[0], c[1]); c[2]+1 < lo || c[2] > hi+1 {
			t.Fatalf("halfway %v isn't between %v and %v", mid, a, b)
		}
	}
}

// TestDownloadStates draws each download state at the sizes the YouTube
// screens use: 42 in the typing screen's results, 44 elsewhere.
func TestDownloadStates(t *testing.T) {
	u, off := uitest.NewContext(t, 400, 130)
	u.Accent = theme.Derive(265, 0.12)
	states := []media.DownloadState{media.Available, media.Queued, media.Downloading, media.Saved, media.Failed}
	for i, st := range states {
		x := 10 + float32(i)*60
		DownloadCircle(u, st, 0.62, 42, x, 10)
		DownloadCircle(u, st, 0.25, 44, x, 60)
	}
	uitest.Golden(t, "download-states", off.Image())
}

// A download at 0 % draws no sector, not even a hairline to 12 o'clock, and
// one whose progress is NaN draws as 0 % does, making no mask each frame.
func TestDownloadCircleAtNothing(t *testing.T) {
	draw := func(p float32) *image.NRGBA {
		u, off := uitest.NewContext(t, 80, 80)
		u.Accent = theme.Derive(265, 0.12)
		DownloadCircle(u, media.Downloading, p, 44, 8, 8) // 55 device pixels from (10, 10)
		return off.Image()
	}
	zero := draw(0)
	// Straight up from the centre, between the ring and the icon.
	for y := 14; y < 23; y++ {
		for x := 36; x <= 38; x++ {
			if c := zero.NRGBAAt(x, y); c != theme.Ground {
				t.Fatalf("0 %% draws %v at (%d, %d)", c, x, y)
			}
		}
	}
	nan := float32(math.NaN())
	if !bytes.Equal(draw(nan).Pix, zero.Pix) {
		t.Fatal("NaN draws otherwise than 0 %")
	}

	u, _ := uitest.NewContext(t, 80, 80)
	DownloadCircle(u, media.Downloading, 0, 44, 8, 8)
	DownloadCircle(u, media.Downloading, nan, 44, 8, 8)
	base := testing.AllocsPerRun(20, func() { DownloadCircle(u, media.Downloading, 0, 44, 8, 8) })
	if n := testing.AllocsPerRun(20, func() { DownloadCircle(u, media.Downloading, nan, 44, 8, 8) }); n > base {
		t.Fatalf("NaN allocates %v times a frame, 0 %% %v: a mask each frame", n, base)
	}
}

func TestRowKeepsRoomForWhatTrails(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, 120)
	box := gfx.Rect{X: 32, Y: 8, W: 600, H: RowHeight}
	r := Row{Title: strings.Repeat("Night Drive Theory ", 6), Subtitle: strings.Repeat("Lumen Coast ", 8), Trail: 150}
	ListRow(u, r, box, 0)
	img := off.Image()
	ground := img.NRGBAAt(2, 2)
	tb := TrailBox(box, 150)
	for y := int(tb.Y * theme.Scale); y < int((tb.Y+tb.H)*theme.Scale); y++ {
		for x := int((tb.X-rowGap)*theme.Scale) + 1; x < int((tb.X+tb.W)*theme.Scale); x++ {
			if img.NRGBAAt(x, y) != ground {
				t.Fatalf("drawn at (%d, %d), in the room kept", x, y)
			}
		}
	}
}

func TestFiltersMeasureWhatTheyDraw(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, 100)
	names := []string{"All", "Songs", "Albums", "Artists"}
	for _, counts := range [][]string{nil, {"40", "20", "12", "3"}} {
		if w, drawn := FiltersWidth(u, names, counts), Filters(u, names, counts, 1, 32, 0); w != drawn {
			t.Errorf("counts %v: measured %v, drawn %v", counts, w, drawn)
		}
	}
}

// TestDialog draws the "Download failed" dialog over a tab.
func TestDialog(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(265, 0.12)
	DrawBackground(u, TabGround)
	StatusBar(u)
	Tabs(u, 4)
	art := SpecFor("Night Drive Theory")
	d := Dialog{Icon: "alert", IconColor: theme.GoldSoft, Title: "Download failed", Art: &art,
		ItemTitle: "Night Drive Theory", ItemDetail: "Lumen Coast · YouTube Music",
		Body:  "The downloader couldn’t read this track. It is usually fixed by updating the downloader.",
		Pills: []DialogPill{{Icon: "refresh", Label: "Update and retry"}, {Label: "Retry"}, {Label: "Remove"}}}
	DrawDialog(u, &d, 1, []float32{1, 0, 0})
	uitest.Golden(t, "dialog", off.Image())
	lines := d.body.lines
	DrawDialog(u, &d, 1, []float32{1, 0, 0})
	if len(lines) < 2 || &d.body.lines[0] != &lines[0] {
		t.Fatal("the body was wrapped again")
	}
}

// TestVolumePill draws the volume pill at 14 of 20 over a plain ground.
func TestVolumePill(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	DrawBackground(u, StateGround)
	VolumePill(u, 14, 1)
	uitest.Golden(t, "volume-pill", off.Image())
}

func TestTabsScrollOnlyWhenTheNamesDontFit(t *testing.T) {
	// 900 of names in 800 of room: the active one is brought towards the middle
	// without leaving a gap at either end.
	for _, c := range []struct {
		name                     string
		total, view, x, w, shift float32
	}{
		{"everything fits", 700, 800, 500, 100, 0},
		{"the first", 900, 800, 0, 100, 0},
		{"the middle", 900, 800, 400, 100, 50},
		{"the last", 900, 800, 800, 100, 100},
	} {
		if got := tabsScroll(c.total, c.view, c.x, c.w); got != c.shift {
			t.Errorf("%s: shifted %v, want %v", c.name, got, c.shift)
		}
	}
}

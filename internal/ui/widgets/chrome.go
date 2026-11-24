// Package widgets draws the components shared by screens.
package widgets

import (
	"image/color"
	"math"
	"strconv"

	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/focus"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// Heights of the fixed bars in design pixels.
const (
	StatusHeight = 44
	TabsHeight   = 72
	HintsHeight  = 56
)

// The top-level tabs' IDs, in the order the row shows them. Which are shown
// is u.Tabs.Shown.
const (
	TabHome = iota
	TabLibrary
	TabSpotify
	TabYouTube
	TabDownloads
	TabRadio
	TabCount
)

var tabNames = [TabCount]string{"Home", "Library", "Spotify", "YouTube", "Downloads", "Radio"}

// allTabs is every tab, shown when u.Tabs.Shown is nil.
var allTabs = []int{TabHome, TabLibrary, TabSpotify, TabYouTube, TabDownloads, TabRadio}

// TabName is the name tab id shows.
func TabName(id int) string { return tabNames[id] }

// The tab row's layout, in design pixels.
const (
	tabSide    = 32 // the row's padding
	tabPillGap = 26 // between a pill and the names
	tabGap     = 30 // between names
	tabFade    = 24 // the names fade out over this inside the window between the pills
	tabPeek    = 40 // of the names beside the active one kept in view
)

// maxDeviceName is the most room the Bluetooth device's name takes in the
// status bar.
const maxDeviceName = 220

// boltW is what the charging bolt takes before the battery's percentage: the
// icon and the gap after it.
const boltW = 18 + 6

// batteryText is the battery's level as the status bar last showed it, so
// that a frame formats it only when it changes. Only the UI thread draws.
var batteryText struct {
	level int
	text  string
}

// StatusBar draws u.Status across the top of the screen. A negative Battery
// means there is none to show.
func StatusBar(u *ui.Context) {
	s := u.Status
	const h = StatusHeight
	ty := u.CenterY(text.ChromeTnum, 0, h)

	// The right-hand items, 22 px apart, are measured first: they end 28 px
	// from the edge, and the track title gets whatever space is left of them.
	// The Wi-Fi icon is always there.
	wifiW := float32(24)
	if !s.Online {
		wifiW = 22
	}
	total := wifiW
	var name string
	var nameW float32
	if s.Bluetooth != "" {
		// A long device name is cut short, so the track title keeps its room.
		name = u.Fit(s.Bluetooth, text.Chrome, maxDeviceName)
		nameW = 28 + u.TextWidth(name, text.Chrome)
		total += 22 + nameW
	}
	var pct string
	var pw float32
	if s.Battery >= 0 {
		if batteryText.text == "" || batteryText.level != s.Battery {
			batteryText.level, batteryText.text = s.Battery, strconv.Itoa(s.Battery)+"%"
		}
		pct = batteryText.text
		pw = u.TextWidth(pct, text.ChromeTnum)
		total += 22 + pw + 8 + 40
		if s.Charging {
			total += boltW
		}
	}
	right := float32(theme.ScreenW) - 28 - total

	x := float32(28)
	x += u.Text(s.Time, text.ChromeTnum, x, ty, theme.Paper)
	if s.NowPlaying != "" {
		x += 14
		u.Icon("music", 18, 2, false, x, (h-18)/2, theme.Ink3)
		x += 26
		u.Text(u.Fit(s.NowPlaying, text.Chrome, right-22-x), text.Chrome, x, ty, theme.Ink3)
	}

	x = right
	if s.Online {
		u.Icon("wifi", 24, 2, false, x, (h-24)/2, theme.Paper)
	} else {
		// The icon alone: a Brick kept off Wi-Fi would show a word all the
		// time, and the screens that need the network say so themselves.
		u.Icon("wifioff", 22, 2, false, x, (h-22)/2, theme.GoldSoft)
	}
	x += wifiW + 22
	if s.Bluetooth != "" {
		u.Icon("headphones", 22, 2, false, x, (h-22)/2, theme.Paper)
		u.Text(name, text.Chrome, x+28, ty, theme.Paper)
		x += nameW + 22
	}
	if s.Battery >= 0 {
		if s.Charging { // a bolt before the percentage: the Brick is on the charger
			u.Icon("bolt", 18, 2, false, x, (h-18)/2, theme.Paper)
			x += boltW
		}
		u.Text(pct, text.ChromeTnum, x, ty, theme.Paper)
		Battery(u, x+pw+8, (h-18)/2, s.Battery)
	}
}

// Battery draws the 34 x 18 battery glyph and its terminal, 40 px wide in all.
func Battery(u *ui.Context, x, y float32, percent int) {
	box := gfx.Rect{X: x, Y: y, W: 34, H: 18}
	u.Shape(raster.Shape{W: 34, H: 18, Radius: 5, Kind: raster.Ring, RingOffset: -2, RingWidth: 2}, box, theme.Paper)
	if p := float32(min(max(percent, 0), 100)) / 100; p > 0 {
		u.Fill(gfx.Rect{X: x + 4, Y: y + 4, W: 26 * p, H: 10}, theme.Paper)
	}
	u.Fill(gfx.Rect{X: x + 37, Y: y + 6, W: 3, H: 6}, theme.Paper)
}

// Tabs draws the tab row under the status bar: the tabs shown, tab active
// underlined in the accent. The row spans the screen, the L1 and R1 pills at
// its paddings, and the names start after L1. When they don't fit between the
// pills they scroll there, fading out at the window's edges; the row slides,
// as little as it can, to keep the active name and some of those beside it in
// view.
func Tabs(u *ui.Context, active int) {
	const y = StatusHeight
	pillY := float32(y + (TabsHeight-TabPill.Height)/2)
	border := theme.PaperA(0.45)
	start := tabSide + KeyGlyph(u, "L1", tabSide, pillY, TabPill, border) + tabPillGap
	r1 := glyphWidth(u, "R1", TabPill)
	lh := u.LineHeight(text.Tab)
	ty := y + (TabsHeight-lh)/2 - 4
	shown := u.Tabs.Shown
	if shown == nil {
		shown = allTabs
	}
	// Each name's place along the names, unscrolled, and its width.
	var xs, ws [TabCount]float32
	var total float32
	at := -1
	for i, id := range shown {
		if i > 0 {
			total += tabGap
		}
		xs[i], ws[i] = total, u.TextWidth(tabNames[id], text.Tab)
		total += ws[i]
		if id == active {
			at = i
		}
	}
	right := float32(theme.ScreenW) - tabSide - r1 - tabPillGap // the window's right edge
	// How far the names are scrolled, and how much each edge fades: as much as
	// is hidden past it, up to tabFade, so an end of the row doesn't fade.
	// Names that fit neither scroll nor fade.
	var off, fadeL, fadeR float32
	if start+total > right {
		view := float64(right - start)
		to := u.Tabs.Scroll.Target()
		if at >= 0 {
			to = focus.Scroll(to, float64(xs[at]-tabPeek), float64(xs[at]+ws[at]+tabPeek), view)
		}
		to = min(max(to, 0), float64(total)-view)
		switch {
		case !u.Tabs.Placed:
			u.Tabs.Scroll.Set(to)
			u.Tabs.Placed = true
		case to != u.Tabs.Scroll.Target():
			u.Tabs.Scroll.To(to, u.Now, anim.Focus, anim.EaseOutCubic)
		}
		if !u.Tabs.Scroll.Done(u.Now) {
			u.KeepDrawing()
		}
		off = float32(u.Tabs.Scroll.At(u.Now))
		fadeL, fadeR = min(off, tabFade), min(total-float32(view)-off, tabFade)
	} else {
		u.Tabs.Scroll.Set(0)
		u.Tabs.Placed = true
	}
	for i, id := range shown {
		x := start + xs[i] - off
		// A name out of the window is drawn too, fully faded: that keeps its
		// texture, and so its width, in the cache. Measured without one, it
		// would be shaped again in every frame.
		if x+ws[i] <= start || x >= right {
			u.TextFade(tabNames[id], text.Tab, x, ty, theme.Ink3, start, right, fadeL, fadeR)
			continue
		}
		col := theme.Ink3
		if id == active {
			col = theme.Paper
		}
		u.TextFade(tabNames[id], text.Tab, x, ty, col, start, right, fadeL, fadeR)
		if id == active {
			l, r := max(x, start), min(x+ws[i], right)
			u.Fill(gfx.Rect{X: l, Y: ty + lh + 4, W: r - l, H: 3}, u.Accent.Color)
		}
	}
	KeyGlyph(u, "R1", right+tabPillGap, pillY, TabPill, border)
}

// GlyphStyle sizes a button glyph.
type GlyphStyle struct {
	Height float32
	Pad    float32 // horizontal padding inside pills
	Radius float64 // pill corner radius
	Text   text.Style
}

// Glyph styles.
var (
	HintRound = GlyphStyle{Height: 34, Text: text.Style{Face: text.FigtreeSemiBold, Size: 19}}
	HintPill  = GlyphStyle{Height: 34, Pad: 10, Radius: 9, Text: text.Style{Face: text.FigtreeSemiBold, Size: 17}}
	TabPill   = GlyphStyle{Height: 30, Pad: 9, Radius: 9, Text: text.Style{Face: text.FigtreeSemiBold, Size: 16}}
)

// arrowIcons draws arrow hints with icons, which the bundled fonts don't cover.
var arrowIcons = map[string][2]string{"◀ ▶": {"left", "right"}, "▲ ▼": {"up", "down"}}

func isFaceButton(key string) bool { return key == "A" || key == "B" || key == "X" || key == "Y" }

// glyphWidth is the width KeyGlyph draws key at.
func glyphWidth(u *ui.Context, key string, gs GlyphStyle) float32 {
	_, isArrow := arrowIcons[key]
	var w float32
	switch {
	case isFaceButton(key):
		w = gs.Height
	case isArrow:
		w = 2*gs.Pad + 36
	default:
		w = max(gs.Height, u.TextWidth(key, gs.Text)+2*gs.Pad)
	}
	return float32(math.Ceil(float64(w)))
}

// KeyGlyph draws a button label: a circle for A, B, X and Y and a pill for
// everything else, outlined 2 px in border. It returns the glyph's width.
func KeyGlyph(u *ui.Context, key string, x, y float32, gs GlyphStyle, border color.NRGBA) float32 {
	h := gs.Height
	w := glyphWidth(u, key, gs)
	radius := gs.Radius
	if isFaceButton(key) {
		radius = float64(h) / 2
	}
	u.Shape(raster.Shape{W: int(w), H: int(h), Radius: radius, Kind: raster.Ring, RingOffset: -2, RingWidth: 2},
		gfx.Rect{X: x, Y: y, W: w, H: h}, border)
	if arrows, ok := arrowIcons[key]; ok {
		iy := y + (h-16)/2
		u.Icon(arrows[0], 16, 3, false, x+gs.Pad, iy, theme.Paper)
		u.Icon(arrows[1], 16, 3, false, x+gs.Pad+20, iy, theme.Paper)
		return w
	}
	tw := u.TextWidth(key, gs.Text)
	u.Text(key, gs.Text, x+(w-tw)/2, u.CenterY(gs.Text, y, h), theme.Paper)
	return w
}

// Hint is one entry in the button hint bar.
type Hint struct {
	Key, Label string
}

// MaxHintsWidth is the room between the hint bar's padding. A screen's hints
// must fit in it.
const MaxHintsWidth = theme.ScreenW - 2*28

func hintStyle(key string) GlyphStyle {
	if isFaceButton(key) {
		return HintRound
	}
	return HintPill
}

// Spacing in the hint bar, in design pixels.
const (
	hintGap  = 10 // glyph to label
	hintStep = 30 // between hints
)

// HintsWidth measures hints as HintBar lays them out, without its padding.
func HintsWidth(u *ui.Context, hints []Hint) float32 {
	var w float32
	for i, h := range hints {
		if i > 0 {
			w += hintStep
		}
		w += glyphWidth(u, h.Key, hintStyle(h.Key)) + hintGap + u.TextWidth(h.Label, text.Chrome)
	}
	return w
}

// HintBar draws the button hint bar along the bottom of the screen.
func HintBar(u *ui.Context, hints []Hint) {
	const y = theme.ScreenH - HintsHeight
	u.Fill(gfx.Rect{X: 0, Y: y, W: theme.ScreenW, H: HintsHeight}, theme.HintBarBG)
	u.Fill(gfx.Rect{X: 0, Y: y, W: theme.ScreenW, H: 1}, theme.PaperA(0.14))
	ty := u.CenterY(text.Chrome, y, HintsHeight)
	x := float32(28)
	for _, h := range hints {
		gs := hintStyle(h.Key)
		border := theme.PaperA(0.55)
		if h.Key == "A" {
			border = theme.Gold
		}
		x += KeyGlyph(u, h.Key, x, y+(HintsHeight-gs.Height)/2, gs, border) + hintGap
		x += u.Text(h.Label, text.Chrome, x, ty, theme.Paper) + hintStep
	}
}

package widgets

import (
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

var (
	stateTitle  = text.Style{Face: text.OutfitSemiBold, Size: 48}
	stateBody   = text.Body
	stateDetail = text.Meta
)

// stateWidth is how wide a state's description runs before wrapping.
const stateWidth = 600

// Wrap breaks s into lines no wider than w, at spaces and between Chinese or
// Japanese characters; a single word wider than w gets a line of its own.
func Wrap(u *ui.Context, s string, st text.Style, w float32) []string { return u.Wrap(s, st, w) }

// Wrapped keeps the lines of the last text wrapped, so a screen that shows
// the same text frame after frame wraps it once.
type Wrapped struct {
	text  string
	style text.Style
	w     float32
	scale float32
	gen   uint64
	lines []string
}

// Lines returns s wrapped to w, wrapping again only when s, st, w, the
// context's scale or the fonts change.
func (c *Wrapped) Lines(u *ui.Context, s string, st text.Style, w float32) []string {
	// The zero Wrapped has no scale, so the first call always wraps.
	if s != c.text || st != c.style || w != c.w || u.Scale != c.scale || u.Fonts.Generation() != c.gen {
		c.text, c.style, c.w, c.scale, c.gen = s, st, w, u.Scale, u.Fonts.Generation()
		c.lines = Wrap(u, s, st, w)
	}
	return c.lines
}

// offlineWidth is how wide the offline state's line runs before wrapping.
const offlineWidth = 700

// OfflineState draws the offline state centred between the tabs and the hint
// bar: the Wi-Fi-off icon in a gold circle, "You’re offline", body wrapped in
// ink2, and, with pill, an Open Downloads pill, focused. As the state ground
// is, it is drawn in the fallback gold, whatever the album playing. The
// screen keeps body's lines in c.
func OfflineState(u *ui.Context, c *Wrapped, body string, pill bool) {
	OfflineStateBelow(u, c, body, pill, StatusHeight+TabsHeight)
}

// OfflineStateBelow draws the offline state as OfflineState does, centred
// between top and the hint bar, for a screen that keeps what is above top,
// as the Radio tab keeps its filter row.
func OfflineStateBelow(u *ui.Context, c *Wrapped, body string, pill bool, top float32) {
	offlineState(u, c, body, pill, gfx.Rect{X: 0, Y: top, W: theme.ScreenW, H: theme.ScreenH - HintsHeight - top})
}

// OfflineStateIn draws the offline state, without its pill, centred in box,
// for a screen that shows it beside something else: Radio search keeps its
// keyboard.
func OfflineStateIn(u *ui.Context, c *Wrapped, body string, box gfx.Rect) {
	offlineState(u, c, body, false, box)
}

// offlineState draws the offline state centred in box.
func offlineState(u *ui.Context, c *Wrapped, body string, pill bool, box gfx.Rect) {
	const circle, icon = 96, 46
	accent := u.Accent
	u.Accent = theme.Fallback()
	defer func() { u.Accent = accent }()
	lines := c.Lines(u, body, stateBody, min(offlineWidth, box.W))
	titleH, bodyH := u.LineHeight(stateTitle), u.LineHeight(stateBody)
	h := circle + 24 + titleH + 12 + float32(len(lines))*bodyH
	if pill {
		h += 28 + PillHeight
	}
	y := box.Y + (box.H-h)/2
	mid := box.X + box.W/2
	x := mid - circle/2
	u.Shape(raster.Shape{W: circle, H: circle, Radius: circle / 2, Kind: raster.Fill}, gfx.Rect{X: x, Y: y, W: circle, H: circle}, theme.Gold)
	u.Icon("wifioff", icon, 2.5, false, x+(circle-icon)/2, y+(circle-icon)/2, theme.Ground)
	y += circle + 24
	title := "You’re offline"
	u.Text(title, stateTitle, mid-u.TextWidth(title, stateTitle)/2, y, theme.Paper)
	y += titleH + 12
	for _, l := range lines {
		u.Text(l, stateBody, mid-u.TextWidth(l, stateBody)/2, y, theme.Ink2)
		y += bodyH
	}
	if !pill {
		return
	}
	y += 28
	px := (theme.ScreenW - PillWidth(u, "download", openDownloads)) / 2
	PillGlow(u, "download", openDownloads, px, y, 1)
	Pill(u, "download", openDownloads, px, y, 1)
}

// openDownloads is the offline state's one action.
const openDownloads = "Open Downloads"

// EmptyState draws a state screen's message centred between the tabs and the
// hint bar: the logo mark in ink4, a title, a wrapped
// description in ink2 and a detail line in ink3. The screen keeps the
// description's lines in c, so that it isn't wrapped every frame; with c nil
// it is.
func EmptyState(u *ui.Context, c *Wrapped, title, body, detail string) {
	const mark = 54 // em: the mark is 81 x 69
	var lines []string
	if c != nil {
		lines = c.Lines(u, body, stateBody, stateWidth)
	} else {
		lines = Wrap(u, body, stateBody, stateWidth)
	}
	titleH, bodyH, detailH := u.LineHeight(stateTitle), u.LineHeight(stateBody), u.LineHeight(stateDetail)
	h := 1.27*mark + 24 + titleH + 12 + float32(len(lines))*bodyH + 16 + detailH
	top := float32(StatusHeight + TabsHeight)
	y := top + (theme.ScreenH-HintsHeight-top-h)/2
	LogoMark(u, mark, (theme.ScreenW-1.5*mark)/2, y, theme.Ink4)
	y += 1.27*mark + 24
	u.Text(title, stateTitle, (theme.ScreenW-u.TextWidth(title, stateTitle))/2, y, theme.Paper)
	y += titleH + 12
	for _, l := range lines {
		u.Text(l, stateBody, (theme.ScreenW-u.TextWidth(l, stateBody))/2, y, theme.Ink2)
		y += bodyH
	}
	y += 16
	u.Text(detail, stateDetail, (theme.ScreenW-u.TextWidth(detail, stateDetail))/2, y, theme.Ink3)
}

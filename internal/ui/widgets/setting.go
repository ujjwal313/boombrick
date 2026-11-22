package widgets

import (
	"image/color"
	"math"

	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// Panel is a dialog's panel as BeginPanel drew it.
type Panel struct {
	Box   gfx.Rect // in design pixels, before the panel's grow
	alpha float32  // the context's Alpha before, for End
}

// BeginPanel draws the scrim and an empty dialog panel h design pixels tall,
// centred: 660 wide, radius 20, raised, with a
// hairline. shown, from 0 to 1, fades it in and grows it from 96 %. Draw what
// the panel holds inside Box, then call End.
func BeginPanel(u *ui.Context, h, shown float32) Panel {
	p := Panel{alpha: u.Alpha}
	u.Alpha = p.alpha * shown
	u.Fill(gfx.Rect{W: theme.ScreenW, H: theme.ScreenH}, theme.Scrim)
	h = float32(math.Ceil(float64(h))) // the shape is whole pixels; keep the box the same size
	p.Box = gfx.Rect{X: (theme.ScreenW - dialogW) / 2, Y: (theme.ScreenH - h) / 2, W: dialogW, H: h}
	u.PushScale(p.Box.X+p.Box.W/2, p.Box.Y+p.Box.H/2, 0.96+0.04*shown)
	panel := raster.Shape{W: dialogW, H: int(h), Radius: 20, Kind: raster.Fill}
	u.Shape(panel, p.Box, theme.Raised)
	panel.Kind, panel.RingOffset, panel.RingWidth = raster.Ring, -1, 1
	u.Shape(panel, p.Box, theme.PaperA(0.14))
	return p
}

// End finishes the panel: the grow and the fade stop applying.
func (p Panel) End(u *ui.Context) {
	u.Pop()
	u.Alpha = p.alpha
}

// SettingHeight is the height of a row of Settings or the quick menu, and
// SettingDetailHeight of one with a line about it under its label.
const (
	SettingHeight       = 74
	SettingDetailHeight = 92
)

var (
	settingLabel  = text.Style{Face: text.Figtree, Size: 28}
	settingValue  = text.Style{Face: text.Figtree, Size: 22, Tabular: true}
	settingDetail = text.Style{Face: text.Figtree, Size: 22}
)

// Setting is a row of Settings or the quick menu: an icon, a label, its value
// on the right, and a chevron when the row opens a page. A row with a Detail,
// a line about it under its label, is SettingDetailHeight tall.
type Setting struct {
	Icon, Label, Value string
	Detail             string
	Active             bool // the value in gold, as a running sleep timer's
	Chevron            bool
}

// SettingFocus draws the gold focus of Settings and the quick menu in box: a
// gold tint at 8 % and a 1.5 px gold border at 45 %, radius 14, at strength k.
func SettingFocus(u *ui.Context, box gfx.Rect, k float32) {
	if k <= 0 {
		return
	}
	w, h := int(box.W), int(box.H)
	u.Shape(raster.Shape{W: w, H: h, Radius: RowRadius, Kind: raster.Fill}, box, fade(theme.Gold, 0.08*k))
	u.Shape(raster.Shape{W: w, H: h, Radius: RowRadius, Kind: raster.Ring, RingOffset: -1.5, RingWidth: 1.5}, box,
		fade(theme.Gold, 0.45*k))
}

// Radio draws a radio button size design pixels across at (x, y): a 2 px ring
// in col and, when on, a dot half its size inside it.
func Radio(u *ui.Context, x, y, size float32, col color.NRGBA, on bool) {
	n := int(size)
	u.Shape(raster.Shape{W: n, H: n, Radius: float64(size) / 2, Kind: raster.Ring, RingOffset: -2, RingWidth: 2},
		gfx.Rect{X: x, Y: y, W: size, H: size}, col)
	if on {
		d := size / 2
		u.Shape(raster.Shape{W: int(d), H: int(d), Radius: float64(d) / 2, Kind: raster.Fill},
			gfx.Rect{X: x + (size-d)/2, Y: y + (size-d)/2, W: d, H: d}, col)
	}
}

// DrawSetting draws s in box, SettingHeight tall, focused at strength k, with
// a hairline under it unless it is the last row.
func DrawSetting(u *ui.Context, s Setting, box gfx.Rect, k float32, last bool) {
	SettingFocus(u, box, k)
	if !last {
		u.Fill(gfx.Rect{X: box.X + 16, Y: box.Y + box.H - 1, W: box.W - 32, H: 1}, theme.PaperA(0.14))
	}
	x := box.X + 20
	u.Icon(s.Icon, 26, 2, false, x, box.Y+(box.H-26)/2, theme.Ink2)
	x += 26 + 18
	right := box.X + box.W - 20
	if s.Chevron {
		u.Icon("right", 22, 2, false, right-22, box.Y+(box.H-22)/2, theme.Ink3)
		right -= 22 + 12
	}
	col := theme.Ink2
	if s.Active {
		col = theme.Gold
	}
	vw := u.TextWidth(s.Value, settingValue)
	u.Text(s.Value, settingValue, right-vw, u.CenterY(settingValue, box.Y, box.H), col)
	room := right - vw - 24 - x
	if s.Detail == "" {
		u.Text(u.Fit(s.Label, settingLabel, room), settingLabel, x, u.CenterY(settingLabel, box.Y, box.H), theme.Paper)
		return
	}
	// The label and the line about it, centred together.
	lh, dh := u.LineHeight(settingLabel), u.LineHeight(settingDetail)
	y := box.Y + (box.H-lh-dh)/2
	u.Text(u.Fit(s.Label, settingLabel, room), settingLabel, x, y, theme.Paper)
	u.Text(u.Fit(s.Detail, settingDetail, room), settingDetail, x, y+lh, theme.Ink3)
}

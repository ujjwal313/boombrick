package screens

import (
	"math"

	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// QueryH is the height of a search results page's query pill.
const QueryH = 52

var queryText = text.Style{Face: text.Figtree, Size: 26}

// pillInner is what a query pill takes besides its text: padding, the icon,
// its gap and the padding after the query.
const pillInner = 16 + 22 + 10 + 20

// FitQuery cuts query to fit a pill no wider than maxW, and says how wide
// that pill is, in whole pixels.
func FitQuery(u *ui.Context, query string, maxW float32) (shown string, w float32) {
	shown = u.Fit(query, queryText, maxW-pillInner)
	return shown, float32(math.Ceil(float64(min(pillInner+u.TextWidth(shown, queryText), maxW))))
}

// QueryPill draws shown, the query already cut to fit by FitQuery, in a pill
// w wide with its top-left at (x, y): a search icon, then the text.
func QueryPill(u *ui.Context, shown string, w, x, y float32) {
	box := gfx.Rect{X: x, Y: y, W: w, H: QueryH}
	u.Shape(raster.Shape{W: int(w), H: QueryH, Radius: QueryH / 2, Kind: raster.Ring, RingOffset: -1.5, RingWidth: 1.5},
		box, theme.PaperA(0.30))
	u.Icon("search", 22, 2, false, x+16, y+(QueryH-22)/2, theme.Ink2)
	u.Text(shown, queryText, x+16+22+10, u.CenterY(queryText, y, QueryH), theme.Paper)
}

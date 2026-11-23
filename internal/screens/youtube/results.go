package youtube

import (
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// The results screen: the query pill and the filters in a
// row across the top, 84 px rows under them. A song's row ends with its
// status, right-aligned in 96, and its download circle.
const (
	resultH     = 84
	statusW     = 96
	resultTrail = statusW + 12 + circle
	resultsTop  = screens.ContentTop + widgets.FiltersHeight
	filterCount = len(ytmusic.FilterNames)
)

var statusText = text.Style{Face: text.Figtree, Size: 20}

// Results shows what a search found, under each filter in turn.
type Results struct {
	*shared
	query  string
	filter ytmusic.Filter
	views  [filterCount]views.Grid
	items  [filterCount][]ytmusic.Item
	rows   [filterCount][]widgets.Row
	have   [filterCount]bool

	none   string  // "No results for …", made when an answer comes with nothing
	placed bool    // whether the top row and the hints have been measured
	fx     float32 // where the filters start
	shown  string  // the query as the pill shows it
	pillW  float32
	msg    widgets.Wrapped
	kinds  kindHints // the hints by the focused result's kind
}

// NewResults shows query's results, under All first.
func NewResults(nav screens.Nav, src Source, dl Downloads, query string) *Results {
	return newResults(&shared{nav: nav, src: src, dl: dl}, query)
}

// newResults shows query's results, sharing sh with the screen that opened
// them.
func newResults(sh *shared, query string) *Results {
	r := &Results{shared: sh, query: query}
	for f := range r.views {
		r.views[f] = views.Grid{Cols: 1, CellW: theme.ScreenW - 2*screens.Side, CellH: resultH, Pad: screens.Pad,
			ViewH: screens.ContentBot - resultsTop}
	}
	return r
}

// load takes the filter's results once they have come, and says how far
// they have got.
func (r *Results) load() ytmusic.State {
	f := r.filter
	if r.have[f] {
		return ytmusic.Done
	}
	items, st := r.src.Search(r.query, f)
	if st == ytmusic.Done {
		var pill float32 // room for the "Download album" pill, when albums can be downloaded
		if r.dl != nil {
			pill = albumPill
		}
		r.items[f], r.rows[f], r.have[f] = items, resultRows(items, true, resultTrail, pill), true
		r.views[f].SetCount(len(items))
		if len(items) == 0 && r.none == "" {
			r.none = "No results for “" + r.query + "”"
		}
	}
	return st
}

// emptyText says why the screen has no rows, given how far the answer got.
func (r *Results) emptyText(st ytmusic.State) string {
	switch st {
	case ytmusic.Waiting:
		return "Searching…"
	case ytmusic.Done:
		return r.none
	}
	return stateText(st)
}

// place measures the top row, which only the query changes, and makes the
// hints.
func (r *Results) place(u *ui.Context) {
	names := ytmusic.FilterNames[:]
	r.fx = theme.ScreenW - screens.Side - widgets.FiltersWidth(u, names, nil)
	maxW := r.fx - 16 - screens.Side
	r.shown, r.pillW = screens.FitQuery(u, r.query, maxW)
	back := widgets.Hint{Key: "B", Label: "Edit search"}
	for k := range r.kinds {
		kind := ytmusic.Kind(k)
		if kind == ytmusic.SongItem && r.dl != nil {
			r.kinds[k] = fitHints(u, resultHints(kind, true, filterHint, tabsHint)) // no room for B: the filters take its place
			continue
		}
		r.kinds[k] = fitHints(u, resultHints(kind, r.dl != nil, back, filterHint, tabsHint))
	}
	r.placed = true
}

// Handle switches filters with L2 and R2 and moves with the D-pad. A plays a
// song or opens an album or artist; X downloads a song or an album, and Y
// queues a song.
func (r *Results) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	r.load()
	g := &r.views[r.filter]
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.L2, input.R2:
			f := int(r.filter) - 1
			if ev.Button == input.R2 {
				f = int(r.filter) + 1
			}
			if f < 0 || f >= filterCount {
				return false
			}
			r.filter = ytmusic.Filter(f)
			return true
		case input.A:
			if g.Count() == 0 {
				return false
			}
			it := r.items[r.filter][g.Focus()]
			if it.Kind == ytmusic.SongItem {
				return play(r.dl, []ytmusic.Song{it.Song()}, 0)
			}
			return openItem(r.shared, it)
		case input.X, input.Y:
			if g.Count() == 0 {
				return false
			}
			it := r.items[r.filter][g.Focus()]
			switch {
			case it.Kind == ytmusic.SongItem && ev.Button == input.X:
				return get(r.nav, r.dl, []ytmusic.Song{it.Song()})
			case it.Kind == ytmusic.SongItem:
				return enqueue(r.nav, r.dl, []ytmusic.Song{it.Song()})
			case it.Kind == ytmusic.AlbumItem && ev.Button == input.X:
				return r.getAlbum(it.ID)
			}
			return false
		}
	}
	return g.Move(ev.Button, now)
}

// Animating reports whether focus or the list is moving.
func (r *Results) Animating(now time.Time) bool {
	return r.views[r.filter].Animating(now)
}

// ShowsDownloads reports whether the songs' download circles show.
func (r *Results) ShowsDownloads() bool { return r.dl != nil }

// Draw renders the frame.
func (r *Results) Draw(u *ui.Context) {
	r.pollAlbums()
	st := r.load()
	widgets.DrawBackground(u, widgets.TabGround)
	if !r.placed {
		r.place(u)
	}
	widgets.Filters(u, ytmusic.FilterNames[:], nil, int(r.filter), r.fx, screens.ContentTop)
	screens.QueryPill(u, r.shown, r.pillW, screens.Side, screens.ContentTop+(widgets.FiltersHeight-screens.QueryH)/2)
	if len(r.items[r.filter]) > 0 {
		r.drawList(u)
	} else {
		message(u, &r.msg, r.emptyText(st), gfx.Rect{X: screens.Side, Y: resultsTop, W: theme.ScreenW - 2*screens.Side,
			H: screens.ContentBot - resultsTop})
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, tab)
	widgets.HintBar(u, r.hints())
}

func (r *Results) drawList(u *ui.Context) {
	g := &r.views[r.filter]
	items, rows := r.items[r.filter], r.rows[r.filter]
	u.PushClip(screens.GlowArea)
	g.Draw(u.Now, screens.Side, resultsTop, func(i int, x, y, k float32) {
		box := gfx.Rect{X: x, Y: y, W: g.CellW, H: resultH}
		widgets.RowGlow(u, box, widgets.RowRadius, k)
		u.PushClip(screens.Below(resultsTop))
		widgets.ListRow(u, rows[i], box, k)
		if items[i].Kind == ytmusic.SongItem {
			st, p := songState(r.dl, items[i].ID)
			if s := statusLabel(st, p); s != "" {
				tb := widgets.TrailBox(box, resultTrail)
				u.Text(s, statusText, tb.X+statusW-u.TextWidth(s, statusText), u.CenterY(statusText, box.Y, box.H), theme.Ink2)
			}
			drawCircle(u, r.dl, items[i].ID, box, circle)
		} else if items[i].Kind == ytmusic.AlbumItem && r.dl != nil {
			drawAlbumPill(u, box, k)
		}
		u.PopClip()
	})
	u.PopClip()
}

// albumPill is the room an album row keeps for the "X Download album" pill
// shown while it is focused: the X glyph 10 in, then the
// words, and 18 after them.
const (
	albumPill  = 230
	albumPillH = 44
)

// drawAlbumPill draws the pill in the room an album row kept, faded in with
// focus k.
func drawAlbumPill(u *ui.Context, box gfx.Rect, k float32) {
	if k <= 0 {
		return
	}
	alpha := u.Alpha
	u.Alpha = alpha * k
	tb := widgets.TrailBox(box, albumPill)
	y := box.Y + (box.H-albumPillH)/2
	u.Shape(raster.Shape{W: albumPill, H: albumPillH, Radius: albumPillH / 2, Kind: raster.Ring, RingOffset: -1.5, RingWidth: 1.5},
		gfx.Rect{X: tb.X, Y: y, W: albumPill, H: albumPillH}, theme.PaperA(0.30))
	x := tb.X + 10
	x += widgets.KeyGlyph(u, "X", x, y+(albumPillH-widgets.HintRound.Height)/2, widgets.HintRound, theme.PaperA(0.55)) + 10
	u.Text("Download album", statusText, x, u.CenterY(statusText, y, albumPillH), theme.Paper)
	u.Alpha = alpha
}

// percents are "0 %" to "100 %", made on first use so that drawing a
// download's progress doesn't build a string each frame.
var percents [101]string

func percent(p float32) string {
	if percents[0] == "" {
		for i := range percents {
			percents[i] = strconv.Itoa(i) + " %"
		}
	}
	return percents[min(max(int(p*100+0.5), 0), 100)]
}

// statusLabel is the text beside a song's circle: "Saved", "62 %",
// "Queued", "Failed", or none.
func statusLabel(st media.DownloadState, p float32) string {
	switch st {
	case media.Saved:
		return "Saved"
	case media.Downloading:
		return percent(p)
	case media.Queued:
		return "Queued"
	case media.Failed:
		return "Failed"
	}
	return ""
}

// noResultHints are the hints while there are no results, kept so that no
// frame builds them.
var noResultHints = []widgets.Hint{{Key: "B", Label: "Edit search"}, filterHint, tabsHint}

func (r *Results) hints() []widgets.Hint {
	g := &r.views[r.filter]
	if g.Count() == 0 {
		return noResultHints
	}
	return r.kinds[r.items[r.filter][g.Focus()].Kind]
}

package radio

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// What the lists say in place of their rows.
const (
	loadingText     = "Loading stations…"
	unreachableText = "Couldn’t reach Radio Browser"
	noStationsText  = "No stations here"
	noSavedText     = "Press Y on a station to save it"
	offlineText     = "Radio needs Wi-Fi or a hotspot — your downloads still play."
)

// heart is the size of the heart a saved station's row shows at its right.
const heart = 26

// line is a station row's second line, "India · MP3 128 kbps · news,
// hindi": its country, its codec and bitrate as listed, and its first two
// tags, each left out when it is empty.
func line(s radio.Station) string {
	format := s.Codec
	if s.Bitrate > 0 {
		format = strings.TrimSpace(format + " " + strconv.Itoa(s.Bitrate) + " kbps")
	}
	parts := make([]string, 0, 3)
	for _, p := range []string{s.Country, format, strings.Join(s.Tags[:min(len(s.Tags), 2)], ", ")} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " · ")
}

// stationCount counts n stations: "1 station", "1,204 stations".
func stationCount(n int) string {
	if n == 1 {
		return "1 station"
	}
	return screens.Count(n) + " stations"
}

// list is a list of stations, or of countries or genres, with the rows that
// show it, made when the list comes rather than every frame.
type list struct {
	grid     views.Grid
	left     float32 // where the rows start across
	stations []radio.Station
	counts   []radio.Count
	arts     []widgets.ArtSpec
	rows     []widgets.Row
	have     bool  // an answer has come
	err      error // the last asking's failure
}

// newList makes a list of full-width rows for the content from top down.
func newList(top float32) list {
	return list{left: screens.Side, grid: views.Grid{Cols: 1, CellW: theme.ScreenW - 2*screens.Side, CellH: widgets.RowHeight,
		Pad: screens.Pad, ViewH: screens.ContentBot - top}}
}

// same reports whether a and b are the one list. The Directory hands back
// the list it keeps, so a list that hasn't changed comes back as the same
// slice.
func same[T any](a, b []T) bool { return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0]) }

// setStations shows sts, as asked with have and err. A list that has changed
// gets its rows made again, with focus kept on the station focused while it
// is still listed, or else on the row where it was, and the saved stations'
// hearts.
func (l *list) setStations(sts []radio.Station, have bool, err error, saved Saved) {
	l.have, l.err = have, err
	if same(sts, l.stations) {
		return
	}
	f := l.grid.Focus()
	if f < len(l.stations) {
		id := l.stations[f].ID
		if i := slices.IndexFunc(sts, func(s radio.Station) bool { return s.ID == id }); i >= 0 {
			f = i
		}
	}
	l.stations = sts
	l.arts = make([]widgets.ArtSpec, len(sts))
	l.rows = make([]widgets.Row, len(sts))
	for i, s := range sts {
		l.arts[i] = screens.StationArt(s.Name, s.Logo)
		l.rows[i] = widgets.Row{Art: &l.arts[i], Title: s.Name, Subtitle: line(s)}
	}
	l.mark(saved)
	l.grid.SetCountFocus(len(sts), f)
}

// setCounts shows the countries or genres cs, as asked with have and err,
// making their rows again when they have changed.
func (l *list) setCounts(cs []radio.Count, have bool, err error) {
	l.have, l.err = have, err
	if same(cs, l.counts) {
		return
	}
	l.counts = cs
	l.arts = make([]widgets.ArtSpec, len(cs))
	l.rows = make([]widgets.Row, len(cs))
	for i, c := range cs {
		l.arts[i] = widgets.SpecFor(c.Name)
		l.rows[i] = widgets.Row{Art: &l.arts[i], Title: c.Name, Subtitle: stationCount(c.Stations)}
	}
	l.grid.SetCount(len(cs))
}

// mark keeps room at the right of each saved station's row for its heart.
func (l *list) mark(saved Saved) {
	for i, s := range l.stations {
		l.rows[i].Trail = 0
		if saved.Has(s.ID) {
			l.rows[i].Trail = heart
		}
	}
}

// failed reports whether the asking failed with nothing had.
func (l *list) failed() bool { return !l.have && l.err != nil }

// note says why there are no rows: they are still to come, Radio Browser
// couldn't be reached, or the list is empty.
func (l *list) note() string {
	switch {
	case l.failed():
		return unreachableText
	case !l.have:
		return loadingText
	}
	return noStationsText
}

// focusSaved reports whether the station focused is saved.
func (l *list) focusSaved() bool {
	f := l.grid.Focus()
	return f < len(l.stations) && l.rows[f].Trail > 0
}

// play plays the stations from the one focused, the list as the queue.
func (l *list) play(p Player) {
	ts := make([]player.Track, len(l.stations))
	for i, s := range l.stations {
		ts[i] = s.Track()
	}
	// Next and Prev step through the list, not to a random station.
	p.SetShuffle(false)
	p.Play(ts, l.grid.Focus())
}

// toggle saves the station focused, or removes it when it is saved, and
// says which.
func (l *list) toggle(e *Env) {
	s := l.stations[l.grid.Focus()]
	if e.Saved.Toggle(s) {
		e.Nav.Toast("Saved " + s.Name)
	} else {
		e.Nav.Toast("Removed " + s.Name)
	}
}

// draw draws the rows from top down to the hint bar: a saved station's with
// its heart, and the playing one's in the accent with a note.
func (l *list) draw(u *ui.Context, top float32, playing string) {
	u.PushClip(screens.GlowArea)
	l.grid.Draw(u.Now, l.left, top, func(i int, x, y, k float32) {
		r := l.rows[i]
		r.Playing = playing != "" && i < len(l.stations) && l.stations[i].ID == playing
		box := gfx.Rect{X: x, Y: y, W: l.grid.CellW, H: l.grid.CellH}
		widgets.RowGlow(u, box, widgets.RowRadius, k)
		u.PushClip(screens.Below(top))
		widgets.ListRow(u, r, box, k)
		if r.Trail > 0 {
			tb := widgets.TrailBox(box, heart)
			u.Icon("heart", heart, 2, true, tb.X, box.Y+(box.H-heart)/2, u.Accent.Color)
		}
		u.PopClip()
	})
	u.PopClip()
}

// hintBars are a screen's hint bars, made once so that no frame builds them:
// on a station saved or not, on a country or genre, with no rows, with Radio
// Browser out of reach, and offline with Downloads to open or without. The
// tab's carry L2/R2 Filter before L1/R1 Tabs, a page's B Back.
type hintBars struct {
	save, remove, open, search, retry, offline, offlineAlone []widgets.Hint
}

var tabsHint = widgets.Hint{Key: "L1/R1", Label: "Tabs"}

func makeHints(nav widgets.Hint) hintBars {
	bar := func(h ...widgets.Hint) []widgets.Hint { return append(h, nav, tabsHint) }
	play, search := widgets.Hint{Key: "A", Label: "Play"}, widgets.Hint{Key: "X", Label: "Search"}
	return hintBars{
		save:         bar(play, widgets.Hint{Key: "Y", Label: "Save"}, search),
		remove:       bar(play, widgets.Hint{Key: "Y", Label: "Remove"}, search),
		open:         bar(widgets.Hint{Key: "A", Label: "Open"}, search),
		search:       bar(search),
		retry:        bar(widgets.Hint{Key: "X", Label: "Retry"}),
		offline:      bar(widgets.Hint{Key: "A", Label: "Open Downloads"}),
		offlineAlone: bar(),
	}
}

var (
	tabHints  = makeHints(widgets.Hint{Key: "L2/R2", Label: "Filter"})
	pageHints = makeHints(widgets.Hint{Key: "B", Label: "Back"})
)

// pick is the bar for l as shown: offline, without rows, or on the row
// focused.
func (b *hintBars) pick(l *list, offline bool) []widgets.Hint {
	switch {
	case offline && screens.DownloadsOn():
		return b.offline
	case offline:
		return b.offlineAlone
	case len(l.rows) == 0 && l.failed():
		return b.retry
	case len(l.rows) == 0:
		return b.search
	case len(l.counts) > 0:
		return b.open
	case l.focusSaved():
		return b.remove
	}
	return b.save
}

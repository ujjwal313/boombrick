package radio

import (
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// resultsTop is where the stations start, under the query pill's row.
const resultsTop = screens.ContentTop + widgets.FiltersHeight

// Results is Radio search's results (13d): the query pill and the stations
// found, as the tab's rows. B goes back to the typing screen, through the
// shell.
type Results struct {
	e      *Env
	query  string
	l      list
	shown  string // the query as the pill shows it
	pillW  float32
	placed bool // whether the pill has been measured
	online bool // as last drawn
	msg    widgets.Wrapped
	offMsg widgets.Wrapped

	// The list as last asked, as the tab's.
	savedVersion, dirVersion uint64
	asked                    time.Time
}

// newResults shows the stations found for query.
func newResults(e *Env, query string) *Results {
	return &Results{e: e, query: query, l: newList(resultsTop), online: true, savedVersion: e.Saved.Version()}
}

// refresh takes the stations as they now stand, asking again as the tab does.
// Offline, Radio Browser isn't asked.
func (r *Results) refresh(now time.Time) {
	if v := r.e.Saved.Version(); v != r.savedVersion {
		r.savedVersion = v
		r.l.mark(r.e.Saved)
	}
	if !r.online {
		return
	}
	if v := r.e.Dir.Version(); v != r.dirVersion || now.Sub(r.asked) >= time.Second {
		r.dirVersion, r.asked = v, now
		sts, have, err := r.e.Dir.Search(r.query)
		r.l.setStations(sts, have, err, r.e.Saved)
	}
}

// Handle moves through the stations. A plays them from the one focused, and
// Y saves or removes it.
func (r *Results) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	if !r.online {
		return ev.Kind == input.Press && ev.Button == input.A && screens.OpenDownloads(r.e.Nav)
	}
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.A:
			if len(r.l.stations) == 0 {
				return false
			}
			r.l.play(r.e.Player)
			return true
		case input.Y:
			if len(r.l.stations) == 0 {
				return false
			}
			r.l.toggle(r.e)
			r.savedVersion = r.e.Saved.Version()
			r.l.mark(r.e.Saved)
			return true
		case input.X:
			if len(r.l.rows) == 0 && r.l.failed() {
				r.e.Dir.Retry()
				r.asked = time.Time{}
				return true
			}
		}
	}
	return r.l.grid.Move(ev.Button, now)
}

// Animating reports whether the list is moving.
func (r *Results) Animating(now time.Time) bool { return r.l.grid.Animating(now) }

// note says why there are no stations.
func (r *Results) note() string {
	if r.l.have && len(r.l.stations) == 0 {
		return noneText
	}
	return r.l.note()
}

// Draw renders the frame.
func (r *Results) Draw(u *ui.Context) {
	if !r.placed {
		r.shown, r.pillW = screens.FitQuery(u, r.query, theme.ScreenW-2*screens.Side)
		r.placed = true
	}
	r.online = u.Status.Online
	r.refresh(u.Now)
	if r.online {
		widgets.DrawBackground(u, widgets.TabGround)
	} else {
		widgets.DrawBackground(u, widgets.StateGround)
	}
	screens.QueryPill(u, r.shown, r.pillW, screens.Side, screens.ContentTop+(widgets.FiltersHeight-screens.QueryH)/2)
	switch {
	case !r.online:
		widgets.OfflineStateBelow(u, &r.offMsg, offlineText, screens.DownloadsOn(), resultsTop)
	case len(r.l.rows) > 0:
		r.l.draw(u, resultsTop, r.e.Player.State().Track.StationID)
	default:
		screens.Message(u, &r.msg, r.note(), gfx.Rect{X: screens.Side, Y: resultsTop, W: theme.ScreenW - 2*screens.Side,
			H: screens.ContentBot - resultsTop})
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, tab)
	widgets.HintBar(u, r.hints())
}

// stationBars are the hints of a list of stations that a search page keeps
// beside its own back hint: on a station saved or not, and with no stations.
type stationBars struct {
	save, remove, none []widgets.Hint
}

// stationHints makes the bars with back, B's hint, before the tabs'.
func stationHints(back widgets.Hint) stationBars {
	play := widgets.Hint{Key: "A", Label: "Play"}
	return stationBars{
		save:   []widgets.Hint{play, {Key: "Y", Label: "Save"}, back, tabsHint},
		remove: []widgets.Hint{play, {Key: "Y", Label: "Remove"}, back, tabsHint},
		none:   []widgets.Hint{back, tabsHint},
	}
}

// pick is the bar for the list as shown.
func (b *stationBars) pick(l *list) []widgets.Hint {
	switch {
	case len(l.rows) == 0:
		return b.none
	case l.focusSaved():
		return b.remove
	}
	return b.save
}

var (
	editSearch   = widgets.Hint{Key: "B", Label: "Edit search"}
	resultsHints = stationHints(editSearch)
	// resultsRetry is the bar when the search failed: B still goes back to
	// the keyboard, as it does on the results.
	resultsRetry = []widgets.Hint{{Key: "X", Label: "Retry"}, editSearch, tabsHint}
)

func (r *Results) hints() []widgets.Hint {
	switch {
	case !r.online && screens.DownloadsOn():
		return pageHints.offline
	case !r.online:
		return pageHints.offlineAlone
	}
	if len(r.l.rows) == 0 && r.l.failed() {
		return resultsRetry
	}
	return resultsHints.pick(&r.l)
}

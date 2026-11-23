package radio

import (
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

const (
	savedFilter = iota
	topFilter
	countriesFilter
	genresFilter
	filterCount
)

var filterNames = [filterCount]string{"Saved", "Top", "Countries", "Genres"}

// listTop is where the lists start, under the filter row.
const listTop = screens.ContentTop + widgets.FiltersHeight

// Tab is the Radio tab: the stations saved, Radio Browser's most listened,
// and its countries and genres, chosen with L2 and R2. Saved works offline;
// the others need Radio Browser.
type Tab struct {
	e      *Env
	filter int
	lists  [filterCount]list
	counts [filterCount]string // the filters' counts: Saved's alone
	online bool                // as last drawn
	msg    widgets.Wrapped
	offMsg widgets.Wrapped // the offline state's line

	// The lists as last asked: Saved's once it has changed, and Radio
	// Browser's once its lists have, or a second on, as asking is what has
	// the Directory ask again for lists that are old or failed.
	savedVersion, dirVersion uint64
	asked                    time.Time
}

// NewTab makes the tab, on Saved, or on Top while nothing is saved.
func NewTab(e *Env) *Tab {
	t := &Tab{e: e, online: true}
	for f := range t.lists {
		t.lists[f] = newList(listTop)
	}
	t.readSaved()
	if len(t.lists[savedFilter].stations) == 0 {
		t.filter = topFilter
	}
	return t
}

// readSaved takes the saved stations as they now stand: Saved's rows and
// count, and the hearts on Top's rows.
func (t *Tab) readSaved() {
	t.savedVersion = t.e.Saved.Version()
	saved := t.e.Saved.List()
	t.lists[savedFilter].setStations(saved, true, nil, t.e.Saved)
	t.counts[savedFilter] = screens.Count(len(saved))
	t.lists[topFilter].mark(t.e.Saved)
}

// refresh takes the lists as they now stand. Offline, or on Saved, Radio
// Browser isn't asked.
func (t *Tab) refresh(now time.Time) {
	if t.e.Saved.Version() != t.savedVersion {
		t.readSaved()
	}
	if t.filter == savedFilter || !t.online {
		return
	}
	if v := t.e.Dir.Version(); v != t.dirVersion || now.Sub(t.asked) >= time.Second {
		t.dirVersion = v
		t.ask(now)
	}
}

// ask asks the Directory for the filter's list, making its rows again when
// it has changed.
func (t *Tab) ask(now time.Time) {
	t.asked = now
	l := &t.lists[t.filter]
	switch t.filter {
	case topFilter:
		sts, have, err := t.e.Dir.Top()
		l.setStations(sts, have, err, t.e.Saved)
	case countriesFilter:
		l.setCounts(t.e.Dir.Countries())
	case genresFilter:
		l.setCounts(t.e.Dir.Genres())
	}
}

// offline reports whether the tab shows the offline state: on any filter but
// Saved, as last drawn.
func (t *Tab) offline() bool { return !t.online && t.filter != savedFilter }

// Handle switches filters with L2/R2 and moves with the D-pad. A plays the
// list from the station focused, or opens a country or a genre; Y saves or
// removes a station; X opens search, or asks again when Radio Browser
// couldn't be reached.
func (t *Tab) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	if ev.Kind == input.Press && (ev.Button == input.L2 || ev.Button == input.R2) {
		f := t.filter - 1
		if ev.Button == input.R2 {
			f = t.filter + 1
		}
		if f < 0 || f >= filterCount {
			return false
		}
		t.filter = f
		if f != savedFilter && t.online {
			t.ask(now)
		}
		return true
	}
	if t.offline() {
		return ev.Kind == input.Press && ev.Button == input.A && screens.OpenDownloads(t.e.Nav)
	}
	l := &t.lists[t.filter]
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.A:
			switch {
			case len(l.rows) == 0:
				return false
			case len(l.counts) > 0:
				t.open(l.counts[l.grid.Focus()])
			default:
				l.play(t.e.Player)
			}
			return true
		case input.Y:
			if len(l.stations) == 0 {
				return false
			}
			l.toggle(t.e)
			t.readSaved()
			return true
		case input.X:
			if len(l.rows) == 0 && l.failed() {
				t.e.Dir.Retry()
				t.asked = time.Time{}
				return true
			}
			search(t.e)
			return true
		}
	}
	return l.grid.Move(ev.Button, now)
}

// open opens a country's or a genre's page.
func (t *Tab) open(c radio.Count) {
	e := t.e
	if t.filter == countriesFilter {
		e.Nav.Push(newPage(e, c.Name, func() ([]radio.Station, bool, error) { return e.Dir.ByCountry(c.Code) }))
		return
	}
	e.Nav.Push(newPage(e, c.Name, func() ([]radio.Station, bool, error) { return e.Dir.ByTag(c.Name) }))
}

// search opens Radio search (13c).
func search(e *Env) { e.Nav.Push(NewSearch(e)) }

// Animating reports whether the list shown is moving.
func (t *Tab) Animating(now time.Time) bool { return t.lists[t.filter].grid.Animating(now) }

// Draw renders the frame.
func (t *Tab) Draw(u *ui.Context) {
	t.online = u.Status.Online
	t.refresh(u.Now)
	l := &t.lists[t.filter]
	if t.offline() {
		widgets.DrawBackground(u, widgets.StateGround)
	} else {
		widgets.DrawBackground(u, widgets.TabGround)
	}
	widgets.Filters(u, filterNames[:], t.counts[:], t.filter, screens.Side, screens.ContentTop)
	switch {
	case t.offline():
		widgets.OfflineStateBelow(u, &t.offMsg, offlineText, screens.DownloadsOn(), listTop)
	case len(l.rows) > 0:
		l.draw(u, listTop, t.e.Player.State().Track.StationID)
	default:
		s := l.note()
		if t.filter == savedFilter {
			s = noSavedText
		}
		screens.Message(u, &t.msg, s, gfx.Rect{X: screens.Side, Y: listTop, W: theme.ScreenW - 2*screens.Side,
			H: screens.ContentBot - listTop})
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, tab)
	widgets.HintBar(u, t.hints())
}

func (t *Tab) hints() []widgets.Hint { return tabHints.pick(&t.lists[t.filter], t.offline()) }

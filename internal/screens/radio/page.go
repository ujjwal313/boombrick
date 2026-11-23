package radio

import (
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Page is a country's or a genre's stations, most listened first, under a
// heading with its name and how many there are. B goes back, through the
// shell.
type Page struct {
	e      *Env
	title  string
	ask    func() ([]radio.Station, bool, error)
	meta   string // "N stations", once they have come
	l      list
	top    float32 // the list's top, once the heading is measured
	online bool    // as last drawn
	msg    widgets.Wrapped
	offMsg widgets.Wrapped

	// The list as last asked, as the tab's.
	savedVersion, dirVersion uint64
	asked                    time.Time
}

// newPage opens the stations list returns, under the heading title.
func newPage(e *Env, title string, list func() ([]radio.Station, bool, error)) *Page {
	return &Page{e: e, title: title, ask: list, l: newList(screens.PageTop), online: true,
		savedVersion: e.Saved.Version()}
}

// refresh takes the list as it now stands, asking again as the tab does, and
// counts it for the heading when it has come or changed. Offline, Radio
// Browser isn't asked.
func (pg *Page) refresh(now time.Time) {
	if v := pg.e.Saved.Version(); v != pg.savedVersion {
		pg.savedVersion = v
		pg.l.mark(pg.e.Saved)
	}
	if !pg.online {
		return
	}
	if v := pg.e.Dir.Version(); v != pg.dirVersion || now.Sub(pg.asked) >= time.Second {
		pg.dirVersion, pg.asked = v, now
		sts, have, err := pg.ask()
		changed := have != pg.l.have || !same(sts, pg.l.stations)
		pg.l.setStations(sts, have, err, pg.e.Saved)
		if changed && have {
			pg.meta = stationCount(len(sts))
		}
	}
}

// Handle moves through the stations. A plays the page's list from the one
// focused, Y saves or removes it, and X opens search, or asks again when
// Radio Browser couldn't be reached.
func (pg *Page) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	if !pg.online {
		return ev.Kind == input.Press && ev.Button == input.A && screens.OpenDownloads(pg.e.Nav)
	}
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.A:
			if len(pg.l.stations) == 0 {
				return false
			}
			pg.l.play(pg.e.Player)
			return true
		case input.Y:
			if len(pg.l.stations) == 0 {
				return false
			}
			pg.l.toggle(pg.e)
			pg.savedVersion = pg.e.Saved.Version()
			pg.l.mark(pg.e.Saved)
			return true
		case input.X:
			if len(pg.l.rows) == 0 && pg.l.failed() {
				pg.e.Dir.Retry()
				pg.asked = time.Time{}
				return true
			}
			search(pg.e)
			return true
		}
	}
	return pg.l.grid.Move(ev.Button, now)
}

// Animating reports whether the list is moving.
func (pg *Page) Animating(now time.Time) bool { return pg.l.grid.Animating(now) }

// Draw renders the frame.
func (pg *Page) Draw(u *ui.Context) {
	if pg.top == 0 {
		pg.top = screens.PageTop + u.LineHeight(text.Heading)
		pg.l.grid.ViewH = screens.ContentBot - pg.top
	}
	pg.online = u.Status.Online
	pg.refresh(u.Now)
	if pg.online {
		widgets.DrawBackground(u, widgets.TabGround)
	} else {
		widgets.DrawBackground(u, widgets.StateGround)
	}
	screens.Heading(u, pg.title, pg.meta, screens.PageTop)
	switch {
	case !pg.online:
		widgets.OfflineStateBelow(u, &pg.offMsg, offlineText, screens.DownloadsOn(), pg.top)
	case len(pg.l.rows) > 0:
		pg.l.draw(u, pg.top, pg.e.Player.State().Track.StationID)
	default:
		screens.Message(u, &pg.msg, pg.l.note(), gfx.Rect{X: screens.Side, Y: pg.top, W: theme.ScreenW - 2*screens.Side,
			H: screens.ContentBot - pg.top})
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, tab)
	widgets.HintBar(u, pg.hints())
}

func (pg *Page) hints() []widgets.Hint { return pageHints.pick(&pg.l, !pg.online) }

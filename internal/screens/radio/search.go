package radio

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// The typing screen, laid out as YouTube's: the keyboard on the left, the top
// stations on the right under their label. Five rows of 76 px, the shortest a
// list row may be, are what fit under the label.
const (
	kbX        = screens.Side
	kbY        = screens.ContentTop + screens.Pad
	resultsX   = kbX + widgets.KeyboardW + 28
	resultsW   = theme.ScreenW - screens.Side - resultsX
	topList    = kbY + 30 // under the label
	topRowH    = 76
	topResults = 5
	maxQuery   = 80 // runes
)

var labelText = text.Style{Face: text.Figtree, Size: 20}

const (
	typeText     = "Type to search stations by name."
	searchingMsg = "Searching…"
	noneText     = "No stations found"
)

// Search is Radio search (13c), the page X opens: typing a query on the
// keyboard while the top stations come in beside it.
type Search struct {
	e         *Env
	kb        widgets.Keyboard
	query     string
	onResults bool // focus is on the stations rather than the keyboard
	offline   bool // as last drawn
	l         list
	shownFor  string // the query the stations listed answer
	done      bool   // the answer for the query asked has come
	err       error  // the last asking's failure
	msg       widgets.Wrapped
	offMsg    widgets.Wrapped // the offline state's description, wrapped

	// The search as last asked: after the query changes, the Directory
	// changes, or a second on, as asking is what has the Directory send a
	// search that has stood for its pause.
	askedFor     string
	savedVersion uint64
	dirVersion   uint64
	asked        time.Time
}

// NewSearch returns the typing screen, with focus on the keyboard's "a".
func NewSearch(e *Env) *Search {
	l := newList(topList)
	l.left = resultsX
	l.grid.CellW, l.grid.CellH, l.grid.ViewH = resultsW, topRowH, screens.ContentBot-topList
	return &Search{e: e, kb: widgets.NewKeyboard(), l: l, savedVersion: e.Saved.Version()}
}

// Handle types, deletes and searches on the keyboard, and plays, saves and
// removes the top stations.
func (s *Search) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	if s.onResults {
		return s.handleResults(ev, now)
	}
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.A:
			switch act, r := s.kb.Press(); act {
			case widgets.TypeKey:
				s.typeRune(r)
			case widgets.SpaceKey:
				s.space()
			case widgets.BackspaceKey:
				s.backspace()
			case widgets.SearchKey:
				s.submit()
			}
			return true
		case input.B:
			if s.query == "" {
				s.e.Nav.Pop()
				return true
			}
			return s.backspace()
		case input.X:
			return s.space()
		case input.Y:
			return s.toResults(now)
		case input.Start:
			return s.submit()
		}
	}
	if s.kb.Move(ev.Button, now) {
		return true
	}
	return ev.Button == input.Right && s.toResults(now)
}

// handleResults plays the top stations, and saves and removes them.
func (s *Search) handleResults(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.A:
			s.l.play(s.e.Player)
			return true
		case input.Y:
			s.l.toggle(s.e)
			s.savedVersion = s.e.Saved.Version()
			s.l.mark(s.e.Saved)
			return true
		case input.B:
			s.toKeyboard(now)
			return true
		}
	}
	if s.l.grid.Move(ev.Button, now) {
		return true
	}
	if ev.Button == input.Left {
		s.toKeyboard(now)
		return true
	}
	return false
}

func (s *Search) typeRune(r rune) {
	if utf8.RuneCountInString(s.query) < maxQuery {
		s.query += string(r)
	}
}

// space adds a space between words: never a leading one or two together.
func (s *Search) space() bool {
	if s.query == "" || strings.HasSuffix(s.query, " ") {
		return false
	}
	s.typeRune(' ')
	return true
}

func (s *Search) backspace() bool {
	if s.query == "" {
		return false
	}
	_, n := utf8.DecodeLastRuneInString(s.query)
	s.query = s.query[:len(s.query)-n]
	return true
}

// submit opens the query's results, when there is a query and Radio Browser
// can be reached.
func (s *Search) submit() bool {
	q := strings.TrimSpace(s.query)
	if q == "" || s.offline {
		return false
	}
	s.e.Nav.Push(newResults(s.e, q))
	return true
}

// toResults moves focus to the top stations, when there are some.
func (s *Search) toResults(now time.Time) bool {
	if len(s.l.stations) == 0 || s.offline {
		return false
	}
	s.onResults = true
	s.kb.SetActive(false, now)
	s.l.grid.SetActive(true, now)
	return true
}

func (s *Search) toKeyboard(now time.Time) {
	s.onResults = false
	s.l.grid.SetActive(false, now)
	s.kb.SetActive(true, now)
}

// Claims takes B, which deletes, goes back to the keyboard or, with nothing
// to delete, goes back to the tab itself, and Start while there is a query
// and the keyboard has focus: here it searches rather than opening Now
// Playing.
func (s *Search) Claims(b input.Button) bool {
	switch b {
	case input.B:
		return true
	case input.Start:
		return !s.offline && !s.onResults && strings.TrimSpace(s.query) != ""
	}
	return false
}

// Animating reports whether focus is moving.
func (s *Search) Animating(now time.Time) bool {
	return s.kb.Animating(now) || s.l.grid.Animating(now)
}

// refresh takes the query's stations as they now stand, keeping the last
// ones until the answer for the query comes. Offline, Radio Browser isn't
// asked.
func (s *Search) refresh(now time.Time) {
	if v := s.e.Saved.Version(); v != s.savedVersion {
		s.savedVersion = v
		s.l.mark(s.e.Saved)
	}
	q := strings.TrimSpace(s.query)
	if q == "" {
		s.askedFor, s.done, s.err = "", false, nil
		s.show(nil, "")
		return
	}
	if s.offline {
		if s.onResults {
			s.toKeyboard(now)
		}
		return
	}
	if v := s.e.Dir.Version(); q != s.askedFor || v != s.dirVersion || now.Sub(s.asked) >= time.Second {
		s.askedFor, s.dirVersion, s.asked = q, v, now
		sts, have, err := s.e.Dir.Search(q)
		s.done, s.err = have, err
		switch {
		case have:
			s.show(sts[:min(len(sts), topResults)], q)
		case err != nil:
			// The query failed: the last query's stations aren't its.
			s.show(nil, q)
		}
	}
}

// show lists sts, the answer for q, unless they are already listed.
func (s *Search) show(sts []radio.Station, q string) {
	if q == s.shownFor && same(sts, s.l.stations) {
		return
	}
	s.shownFor = q
	s.l.setStations(sts, true, nil, s.e.Saved)
	switch {
	case len(sts) == 0 && s.onResults:
		s.onResults = false
		s.kb.SetActive(true, time.Time{})
	case !s.onResults:
		s.l.grid.SetActive(false, time.Time{})
	}
}

// emptyText says why there are no stations.
func (s *Search) emptyText() string {
	switch {
	case strings.TrimSpace(s.query) == "":
		return typeText
	case s.err != nil && !s.done:
		return unreachableText
	case !s.done:
		return searchingMsg
	}
	return noneText
}

// Draw renders the frame.
func (s *Search) Draw(u *ui.Context) {
	s.offline = !u.Status.Online
	s.refresh(u.Now)
	right := gfx.Rect{X: resultsX, Y: topList, W: resultsW, H: screens.ContentBot - topList}
	if s.offline {
		widgets.DrawBackground(u, widgets.StateGround)
	} else {
		widgets.DrawBackground(u, widgets.TabGround)
	}
	s.kb.Draw(u, kbX, kbY, s.query)
	switch {
	case s.offline:
		widgets.OfflineStateIn(u, &s.offMsg, offlineText, gfx.Rect{X: resultsX, Y: kbY, W: resultsW,
			H: screens.ContentBot - kbY})
	case len(s.l.rows) > 0:
		u.Text("TOP RESULTS", labelText, resultsX, kbY, theme.Ink3)
		s.l.draw(u, topList, s.e.Player.State().Track.StationID)
	default:
		u.Text("TOP RESULTS", labelText, resultsX, kbY, theme.Ink3)
		screens.Message(u, &s.msg, s.emptyText(), right)
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, tab)
	widgets.HintBar(u, s.hints())
}

// The keyboard's hints, kept so that no frame builds them: with stations to
// go to, or without. The top stations' are the page's, with B going back to
// the keyboard.
var (
	typingHints = []widgets.Hint{{Key: "A", Label: "Type"}, {Key: "B", Label: "Delete"}, {Key: "X", Label: "Space"},
		{Key: "Y", Label: "To results"}, {Key: "Start", Label: "Search"}}
	typingAloneHints = []widgets.Hint{{Key: "A", Label: "Type"}, {Key: "B", Label: "Delete"}, {Key: "X", Label: "Space"},
		{Key: "Start", Label: "Search"}}
	offlineHints = []widgets.Hint{{Key: "A", Label: "Type"}, {Key: "B", Label: "Delete"},
		{Key: "X", Label: "Space"}} // nothing to search
	topHints = stationHints(widgets.Hint{Key: "B", Label: "Keyboard"})
)

func (s *Search) hints() []widgets.Hint {
	switch {
	case s.offline:
		return offlineHints
	case s.onResults:
		return topHints.pick(&s.l)
	case len(s.l.stations) > 0:
		return typingHints
	}
	return typingAloneHints
}

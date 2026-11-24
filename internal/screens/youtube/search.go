package youtube

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// The typing screen: the keyboard on the left, the top
// results on the right under their label. Five 76 px rows, the shortest a
// list row may be, fit under it.
const (
	kbX         = screens.Side
	kbY         = screens.ContentTop + screens.Pad
	resultsX    = kbX + widgets.KeyboardW + 28
	resultsW    = theme.ScreenW - screens.Side - resultsX
	topList     = kbY + 30 // under the label
	topRowH     = 76
	topResults  = 5
	smallCircle = 42
	maxQuery    = 80 // runes
)

var labelText = text.Style{Face: text.Figtree, Size: 20}

// Search is the YouTube tab's first screen: typing a query on the keyboard
// while the top results come in beside it.
type Search struct {
	*shared
	kb        widgets.Keyboard
	query     string
	onResults bool // focus is on the results rather than the keyboard
	offline   bool // as last drawn
	shown     []ytmusic.Item
	shownFor  string // the query shown's results are for
	none      string // what to say when shownFor has no results
	rows      []widgets.Row
	list      views.Grid
	msg       widgets.Wrapped
	offMsg    widgets.Wrapped // the offline state's description, wrapped
	top       kindHints       // on the top results, made on the first frame
}

// NewSearch returns the typing screen, with focus on the keyboard's "a". The
// screens it opens share its downloads of albums.
func NewSearch(nav screens.Nav, src Source, dl Downloads) *Search {
	return &Search{shared: &shared{nav: nav, src: src, dl: dl}, kb: widgets.NewKeyboard()}
}

// Handle types, deletes and searches on the keyboard, and opens results.
func (s *Search) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	if s.offline {
		return ev.Kind == input.Press && ev.Button == input.A && screens.OpenDownloads(s.nav)
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

// handleResults plays, downloads and queues songs, opens albums and
// artists, and downloads albums.
func (s *Search) handleResults(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Press {
		it := s.shown[s.list.Focus()]
		switch ev.Button {
		case input.A:
			if it.Kind == ytmusic.SongItem {
				return play(s.dl, []ytmusic.Song{it.Song()}, 0)
			}
			return openItem(s.shared, it)
		case input.X:
			switch it.Kind {
			case ytmusic.SongItem:
				return get(s.nav, s.dl, []ytmusic.Song{it.Song()})
			case ytmusic.AlbumItem:
				return s.getAlbum(it.ID)
			}
			return false
		case input.Y:
			return it.Kind == ytmusic.SongItem && enqueue(s.nav, s.dl, []ytmusic.Song{it.Song()})
		case input.B:
			s.toKeyboard(now)
			return true
		}
	}
	if s.list.Move(ev.Button, now) {
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

// submit opens the query's results, when there is a query.
func (s *Search) submit() bool {
	q := strings.TrimSpace(s.query)
	if q == "" {
		return false
	}
	s.nav.Push(newResults(s.shared, q))
	return true
}

// toResults moves focus to the top results, when there are some.
func (s *Search) toResults(now time.Time) bool {
	if len(s.shown) == 0 {
		return false
	}
	s.onResults = true
	s.kb.SetActive(false, now)
	s.list.SetActive(true, now)
	return true
}

func (s *Search) toKeyboard(now time.Time) {
	s.onResults = false
	s.list.SetActive(false, now)
	s.kb.SetActive(true, now)
}

// Claims takes Start while there is a query and the keyboard has focus: here
// it searches rather than opening Now Playing. On the
// results it goes to the shell.
func (s *Search) Claims(b input.Button) bool {
	return b == input.Start && !s.offline && !s.onResults && strings.TrimSpace(s.query) != ""
}

// Animating reports whether focus is moving.
func (s *Search) Animating(now time.Time) bool {
	return s.kb.Animating(now) || s.list.Animating(now)
}

// ShowsDownloads reports whether the results' download circles show.
func (s *Search) ShowsDownloads() bool { return s.dl != nil && !s.offline }

// Draw renders the frame.
func (s *Search) Draw(u *ui.Context) {
	s.offline = !u.Status.Online
	if s.offline {
		widgets.DrawBackground(u, widgets.StateGround)
		widgets.OfflineState(u, &s.offMsg, "YouTube Music needs Wi-Fi or a hotspot — your downloads still play.", true)
		widgets.StatusBar(u)
		widgets.Tabs(u, tab)
		widgets.HintBar(u, offlineHints)
		return
	}
	s.pollAlbums()
	if s.top[0] == nil {
		back := widgets.Hint{Key: "B", Label: "Keyboard"}
		for k := range s.top {
			s.top[k] = fitHints(u, resultHints(ytmusic.Kind(k), s.dl != nil, back, tabsHint))
		}
	}
	st := s.refresh()
	widgets.DrawBackground(u, widgets.TabGround)
	s.kb.Draw(u, kbX, kbY, s.query)
	u.Text("TOP RESULTS", labelText, resultsX, kbY, theme.Ink3)
	if len(s.shown) > 0 {
		s.drawResults(u)
	} else {
		message(u, &s.msg, s.emptyText(st), gfx.Rect{X: resultsX, Y: topList, W: resultsW, H: screens.ContentBot - topList})
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, tab)
	widgets.HintBar(u, s.hints())
}

// refresh asks for the query's results and shows them once they come,
// keeping the last ones until then. It says how far they have got.
func (s *Search) refresh() ytmusic.State {
	q := strings.TrimSpace(s.query)
	if q == "" {
		s.show(nil, "")
		return ytmusic.Done
	}
	items, st := s.src.Search(q, ytmusic.All)
	if st == ytmusic.Done {
		s.show(items, q)
	}
	return st
}

// show lists the top results of items, the answer for q, unless they are
// already listed.
func (s *Search) show(items []ytmusic.Item, q string) {
	if q == s.shownFor {
		return
	}
	s.shownFor = q
	s.shown = items[:min(len(items), topResults)]
	s.rows = resultRows(s.shown, false, smallCircle, 0)
	s.none = "No results for “" + q + "”"
	if s.onResults && len(s.shown) > 0 {
		// A late answer: the place under the thumb stays, within the new rows.
		s.list.SetCount(len(s.shown))
		return
	}
	s.list = views.Grid{Cols: 1, CellW: resultsW, CellH: topRowH, Pad: screens.Pad, ViewH: screens.ContentBot - topList}
	s.list.SetCount(len(s.shown))
	switch {
	case len(s.shown) == 0 && s.onResults:
		s.onResults = false
		s.kb.SetActive(true, time.Time{})
	case !s.onResults:
		s.list.SetActive(false, time.Time{})
	}
}

func (s *Search) emptyText(st ytmusic.State) string {
	q := strings.TrimSpace(s.query)
	switch {
	case q == "":
		return "Type to search songs, albums and artists."
	case st == ytmusic.Waiting:
		return "Searching…"
	case st == ytmusic.Done:
		return s.none
	}
	return stateText(st)
}

func (s *Search) drawResults(u *ui.Context) {
	u.PushClip(screens.GlowArea)
	s.list.Draw(u.Now, resultsX, topList, func(i int, x, y, k float32) {
		box := gfx.Rect{X: x, Y: y, W: resultsW, H: topRowH}
		widgets.RowGlow(u, box, widgets.RowRadius, k)
		u.PushClip(screens.Below(topList))
		widgets.ListRow(u, s.rows[i], box, k)
		if s.shown[i].Kind == ytmusic.SongItem {
			drawCircle(u, s.dl, s.shown[i].ID, box, smallCircle)
		}
		u.PopClip()
	})
	u.PopClip()
}

// The keyboard's hints, kept so that no frame builds them: with results to
// go to, or without. The top results' are made on the first frame.
var (
	typingHints = []widgets.Hint{{Key: "A", Label: "Type"}, {Key: "B", Label: "Delete"}, {Key: "X", Label: "Space"},
		{Key: "Y", Label: "To results"}, {Key: "Start", Label: "Search"}}
	typingAloneHints = []widgets.Hint{{Key: "A", Label: "Type"}, {Key: "B", Label: "Delete"}, {Key: "X", Label: "Space"},
		{Key: "Start", Label: "Search"}}
	offlineHints = []widgets.Hint{{Key: "A", Label: "Open Downloads"}, tabsHint}
)

func (s *Search) hints() []widgets.Hint {
	switch {
	case s.onResults:
		return s.top[s.shown[s.list.Focus()].Kind]
	case len(s.shown) > 0:
		return typingHints
	}
	return typingAloneHints
}

package radio

import (
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// jazzFM is a sixth station for "jazz", which the typing screen leaves out.
var jazzFM = radio.Station{ID: "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d", Name: "Smooth Jazz FM",
	URL: "http://stream.radio.test/smooth", Country: "Germany", CountryCode: "DE", Codec: "MP3", Bitrate: 192,
	Tags: []string{"jazz", "smooth jazz"}}

// jazzHits are what a search for "jazz" finds.
var jazzHits = []radio.Station{jazzLounge, jazzFM, deepSpace, worldNews, cityHindi, kantipur}

func searchDir() *fakeDir {
	d := directory()
	d.lists["search:jazz"] = jazzHits
	d.lists["search:ni"] = jazzHits[:2]
	return d
}

// newSearch opens the typing screen on the Radio tab's page stack, as X
// does, with jazz typed.
func newSearch(t *testing.T, dir *fakeDir, saved *fakeSaved) (*Search, *fakeNav, *fakePlayer) {
	t.Helper()
	e, nav, p := newEnv(dir, saved)
	s := NewSearch(e)
	nav.Push(s)
	s.query = "jazz"
	return s, nav, p
}

func TestTyping(t *testing.T) {
	s, nav, _ := newSearch(t, searchDir(), &fakeSaved{})
	s.query = ""
	render(t, s)
	// Down to "h", along to "n", type it; back to "i", type it.
	for _, b := range []input.Button{input.Down, input.Right, input.Right, input.Right, input.Right, input.Right,
		input.Right, input.A, input.Left, input.Left, input.Left, input.Left, input.Left, input.A} {
		s.Handle(press(b), t0)
	}
	if s.query != "ni" {
		t.Fatalf("typed %q", s.query)
	}
	s.Handle(press(input.X), t0)
	s.Handle(press(input.X), t0)
	if s.query != "ni " {
		t.Fatalf("spaces: %q", s.query)
	}
	s.Handle(press(input.B), t0)
	if s.query != "ni" || len(nav.pushed) != 1 {
		t.Fatalf("delete: %q, %d pages", s.query, len(nav.pushed))
	}
	if !s.Claims(input.Start) || s.Claims(input.Select) || !s.Claims(input.B) {
		t.Fatal("B and Start are claimed with a query, nothing else")
	}
	s.Handle(press(input.Start), t0)
	if r, ok := nav.pushed[1].(*Results); !ok || r.query != "ni" {
		t.Fatalf("Start opened %#v", nav.pushed[1])
	}
}

// With nothing typed, B goes back to the tab and nothing else does anything.
func TestAnEmptyQuery(t *testing.T) {
	s, nav, _ := newSearch(t, searchDir(), &fakeSaved{})
	s.query = ""
	render(t, s)
	for _, b := range []input.Button{input.X, input.Y, input.Start} {
		if s.Handle(press(b), t0) {
			t.Errorf("%v did something with no query", b)
		}
	}
	if s.Claims(input.Start) {
		t.Fatal("Start claimed with nothing to search")
	}
	if !s.Claims(input.B) || !s.Handle(press(input.B), t0) || len(nav.pushed) != 0 {
		t.Fatalf("B with the field empty left %d pages", len(nav.pushed))
	}
}

func TestTypingScreen(t *testing.T) {
	s, _, _ := newSearch(t, searchDir(), &fakeSaved{list: []radio.Station{jazzLounge}})
	uitest.Golden(t, "radio-search", render(t, s))
	if len(s.l.stations) != topResults {
		t.Fatalf("%d stations shown, want %d", len(s.l.stations), topResults)
	}
}

func TestTypingScreenStates(t *testing.T) {
	dir := searchDir()
	s, _, _ := newSearch(t, dir, &fakeSaved{})
	s.query = ""
	render(t, s)
	if got := s.emptyText(); got != typeText {
		t.Errorf("empty: %q", got)
	}
	s.query = "zzz"
	dir.have = false
	render(t, s)
	if got := s.emptyText(); got != searchingMsg {
		t.Errorf("waiting: %q", got)
	}
	dir.err = radio.ErrUnreachable
	s.asked = time.Time{}
	render(t, s)
	if got := s.emptyText(); got != unreachableText {
		t.Errorf("unreachable: %q", got)
	}
	dir.have, dir.err = true, nil
	s.asked = time.Time{}
	render(t, s)
	if got := s.emptyText(); got != noneText {
		t.Errorf("none: %q", got)
	}
}

// Y goes to the stations, A plays the five from the focused one, Y saves and
// removes it, and B goes back to the keyboard.
func TestTopStations(t *testing.T) {
	s, nav, p := newSearch(t, searchDir(), &fakeSaved{})
	render(t, s)
	if !s.Handle(press(input.Y), t0) || !s.onResults {
		t.Fatal("Y didn't go to the stations")
	}
	s.Handle(press(input.Down), t0)
	s.Handle(press(input.A), t0)
	if len(p.played) != 1 || !slices.Equal(p.played[0], tracks(jazzHits[:topResults])) || p.starts[0] != 1 {
		t.Fatalf("played %d lists, starts %v", len(p.played), p.starts)
	}
	s.Handle(press(input.Y), t0)
	s.Handle(press(input.Y), t0)
	if want := []string{"Saved " + jazzFM.Name, "Removed " + jazzFM.Name}; !slices.Equal(nav.toasts, want) {
		t.Fatalf("toasts %q, want %q", nav.toasts, want)
	}
	if s.Claims(input.Start) {
		t.Fatal("Start claimed on the stations, where it does nothing")
	}
	s.Handle(press(input.B), t0)
	if s.onResults || !s.Claims(input.Start) || len(nav.pushed) != 1 {
		t.Fatalf("B: on the stations %v, %d pages", s.onResults, len(nav.pushed))
	}
	s.Handle(press(input.Y), t0)
	for range 4 {
		s.Handle(press(input.Right), t0)
	}
	if !s.onResults {
		t.Fatal("focus left the stations")
	}
	s.Handle(press(input.Left), t0)
	if s.onResults {
		t.Fatal("Left didn't go back to the keyboard")
	}
}

// The keyboard's right edge leads to the stations, when there are some.
func TestRightAtTheEdge(t *testing.T) {
	s, _, _ := newSearch(t, searchDir(), &fakeSaved{})
	render(t, s)
	for range 7 {
		s.Handle(press(input.Right), t0)
	}
	if !s.onResults {
		t.Fatal("Right at the keyboard's edge stayed on the keyboard")
	}
	s.query = "none"
	s.onResults = false
	s.l.setStations(nil, true, nil, s.e.Saved)
	if s.toResults(t0) {
		t.Fatal("went to no stations")
	}
}

// The search asks the Directory when the query or the Directory changes, or
// a second on, and keeps the last stations until the new query's come.
func TestSearchAsks(t *testing.T) {
	dir := searchDir()
	s, _, _ := newSearch(t, dir, &fakeSaved{})
	render(t, s)
	n := len(dir.asked)
	if n == 0 || dir.asked[0] != "search:jazz" {
		t.Fatalf("asked %v", dir.asked)
	}
	if render(t, s); len(dir.asked) != n {
		t.Fatalf("asked %d times more with nothing changed", len(dir.asked)-n)
	}
	dir.have = false
	s.query = "jazzy"
	render(t, s)
	if len(dir.asked) != n+1 || len(s.l.stations) != topResults || s.done {
		t.Fatalf("asked %d times more, %d stations kept, done %v", len(dir.asked)-n, len(s.l.stations), s.done)
	}
	dir.have = true
	dir.version++
	dir.lists["search:jazzy"] = nil
	render(t, s)
	if len(dir.asked) != n+2 || len(s.l.stations) != 0 {
		t.Fatalf("asked %d times more, %d stations", len(dir.asked)-n, len(s.l.stations))
	}
	s.refresh(t0.Add(time.Second))
	if len(dir.asked) != n+3 {
		t.Fatal("not asked again a second on")
	}
}

// A late answer with no stations takes focus back to the keyboard.
func TestLateAnswerLeavesNoFocusOnNothing(t *testing.T) {
	dir := searchDir()
	s, _, _ := newSearch(t, dir, &fakeSaved{})
	render(t, s)
	s.Handle(press(input.Y), t0)
	s.query = "none"
	dir.version++
	render(t, s)
	if s.onResults || len(s.l.stations) != 0 {
		t.Fatal("focus stayed on empty stations")
	}
}

// Offline, the right column says so, without the pill, and the keyboard is
// there for typing; nothing is asked or opened.
func TestSearchOffline(t *testing.T) {
	dir := searchDir()
	s, nav, _ := newSearch(t, dir, &fakeSaved{})
	uitest.Golden(t, "radio-search-offline", render(t, s, offline))
	if len(dir.asked) != 0 {
		t.Fatalf("asked %v offline", dir.asked)
	}
	if s.Claims(input.Start) || s.Handle(press(input.Start), t0) || s.Handle(press(input.Y), t0) || len(nav.pushed) != 1 {
		t.Fatal("offline, Start or Y did something")
	}
	if !s.Handle(press(input.A), t0) || s.query != "jazza" || len(nav.tabs) != 0 {
		t.Fatalf("A offline: query %q, tabs %v", s.query, nav.tabs)
	}
}

func TestResults(t *testing.T) {
	dir := searchDir()
	e, _, _ := newEnv(dir, &fakeSaved{list: []radio.Station{jazzFM}})
	uitest.Golden(t, "radio-results", render(t, newResults(e, "jazz")))
	dir.lists["search:zzz"] = nil
	uitest.Golden(t, "radio-no-results", render(t, newResults(e, "zzz")))
}

func TestResultsStates(t *testing.T) {
	dir := searchDir()
	e, _, _ := newEnv(dir, &fakeSaved{})
	r := newResults(e, "zzz")
	dir.have = false
	render(t, r)
	if got := r.note(); got != loadingText {
		t.Errorf("waiting: %q", got)
	}
	dir.err = radio.ErrUnreachable
	r.asked = time.Time{}
	render(t, r)
	if got := r.note(); got != unreachableText {
		t.Errorf("unreachable: %q", got)
	}
}

// A plays the stations from the focused one, Y saves and removes it, and B
// is left to the shell, which goes back to the typing screen.
func TestResultsKeys(t *testing.T) {
	dir := searchDir()
	e, nav, p := newEnv(dir, &fakeSaved{})
	r := newResults(e, "jazz")
	render(t, r)
	r.Handle(press(input.Down), t0)
	r.Handle(press(input.Down), t0)
	r.Handle(press(input.A), t0)
	if len(p.played) != 1 || !slices.Equal(p.played[0], tracks(jazzHits)) || p.starts[0] != 2 {
		t.Fatalf("played %d lists, starts %v", len(p.played), p.starts)
	}
	r.Handle(press(input.Y), t0)
	if hints := r.hints(); hints[1].Label != "Remove" {
		t.Fatalf("hints after saving: %v", hints)
	}
	r.Handle(press(input.Y), t0)
	if want := []string{"Saved " + deepSpace.Name, "Removed " + deepSpace.Name}; !slices.Equal(nav.toasts, want) {
		t.Fatalf("toasts %q, want %q", nav.toasts, want)
	}
	if r.Handle(press(input.B), t0) || len(nav.pushed) != 0 {
		t.Fatal("B was taken, which the shell does")
	}
}

func TestResultsOffline(t *testing.T) {
	dir := searchDir()
	e, nav, p := newEnv(dir, &fakeSaved{})
	r := newResults(e, "jazz")
	render(t, r, offline)
	if len(dir.asked) != 0 {
		t.Fatalf("asked %v offline", dir.asked)
	}
	if r.Handle(press(input.Y), t0) || len(p.played) != 0 || !r.Handle(press(input.A), t0) || len(nav.tabs) != 1 {
		t.Fatalf("offline: tabs %v", nav.tabs)
	}
}

func TestSearchHintsFit(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	for _, h := range [][]widgets.Hint{typingHints, typingAloneHints, offlineHints,
		topHints.save, topHints.remove, topHints.none, resultsHints.save, resultsHints.remove, resultsHints.none} {
		if w := widgets.HintsWidth(u, h); w > widgets.MaxHintsWidth {
			t.Errorf("hints %v are %.1f wide, more than %.1f", h, w, widgets.MaxHintsWidth)
		}
	}
	if got := resultsHints.save[2]; got.Key != "B" || got.Label != "Edit search" {
		t.Fatalf("results' B hint is %v", got)
	}
}

// Frames drawn again allocate nothing on either search screen.
func TestSearchFramesAllocateNothing(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Status = fixture.Status()
	u.Now = t0
	draw := func(name string, s screens.Screen) {
		t.Helper()
		s.Draw(u)
		if n := testing.AllocsPerRun(10, func() { s.Draw(u) }); n != 0 {
			t.Errorf("%s: a frame allocates %v times", name, n)
		}
	}
	s, _, p := newSearch(t, searchDir(), &fakeSaved{list: []radio.Station{jazzLounge}})
	p.state.Track = jazzFM.Track()
	draw("typing", s)
	s.Handle(press(input.Y), t0)
	draw("on the stations", s)
	s.query = ""
	draw("empty", s)
	e, _, _ := newEnv(searchDir(), &fakeSaved{})
	draw("results", newResults(e, "jazz"))
	draw("no results", newResults(e, "zzz"))
	u.Status.Online = false
	draw("typing offline", s)
	draw("results offline", newResults(e, "jazz"))
}

// A query that fails leaves none of the last query's stations to play or
// save, and says why; one still being asked keeps them.
func TestFailedSearchDropsTheLastStations(t *testing.T) {
	dir := searchDir()
	s, _, _ := newSearch(t, dir, &fakeSaved{})
	render(t, s)
	if len(s.l.stations) != topResults {
		t.Fatalf("%d stations for jazz", len(s.l.stations))
	}
	dir.have, dir.err = false, nil
	s.query = "jazzy"
	render(t, s)
	if len(s.l.stations) != topResults {
		t.Fatal("stations not kept while the new query is asked")
	}
	dir.err = radio.ErrUnreachable
	dir.version++
	render(t, s)
	if len(s.l.stations) != 0 || len(s.l.rows) != 0 {
		t.Fatalf("%d stations kept after the query failed", len(s.l.stations))
	}
	if got := s.emptyText(); got != unreachableText {
		t.Fatalf("the screen says %q", got)
	}
}

// A search that failed with nothing to show offers X Retry, which asks again.
func TestResultsRetry(t *testing.T) {
	dir := searchDir()
	e, _, _ := newEnv(dir, &fakeSaved{})
	r := newResults(e, "zzz")
	dir.have, dir.err = false, radio.ErrUnreachable
	render(t, r)
	if r.note() != unreachableText {
		t.Fatalf("the page says %q", r.note())
	}
	if h := r.hints(); h[0].Key != "X" || h[0].Label != "Retry" || h[1].Label != "Edit search" {
		t.Fatalf("hints %v", h)
	}
	n := len(dir.asked)
	if !r.Handle(press(input.X), t0) || dir.retries != 1 {
		t.Fatalf("X retried %d times", dir.retries)
	}
	render(t, r)
	if len(dir.asked) != n+1 {
		t.Fatal("the page didn't ask again")
	}
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	if w := widgets.HintsWidth(u, r.hints()); w > widgets.MaxHintsWidth {
		t.Fatalf("hints are %.1f wide", w)
	}
}

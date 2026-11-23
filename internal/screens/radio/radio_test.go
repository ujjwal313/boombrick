package radio

import (
	"fmt"
	"image"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(1000, 0)

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

// fakeDir answers from its fields, every list alike; a test changing them
// after the first frame bumps version. It records the lists asked for, each
// asking allocating as the Directory's do, so that a frame that asks shows in
// TestFramesDrawnAgainAllocateNothing. Retry clears err, as the Directory's
// does while it asks again.
type fakeDir struct {
	top               []radio.Station
	countries, genres []radio.Count
	lists             map[string][]radio.Station // a country's under "country:" and its code, a genre's under "tag:"
	have              bool
	err               error
	asked             []string
	retries           int
	version           uint64
}

func (d *fakeDir) ask(key string) { d.asked = append(slices.Clip(d.asked), key) }

func (d *fakeDir) stations(key string, s []radio.Station) ([]radio.Station, bool, error) {
	d.ask(key)
	return s, d.have, d.err
}

func (d *fakeDir) Top() ([]radio.Station, bool, error) { return d.stations("top", d.top) }
func (d *fakeDir) Countries() ([]radio.Count, bool, error) {
	d.ask("countries")
	return d.countries, d.have, d.err
}
func (d *fakeDir) Genres() ([]radio.Count, bool, error) {
	d.ask("genres")
	return d.genres, d.have, d.err
}
func (d *fakeDir) ByCountry(code string) ([]radio.Station, bool, error) {
	return d.stations("country:"+code, d.lists["country:"+code])
}
func (d *fakeDir) ByTag(tag string) ([]radio.Station, bool, error) {
	return d.stations("tag:"+tag, d.lists["tag:"+tag])
}
func (d *fakeDir) Search(q string) ([]radio.Station, bool, error) {
	return d.stations("search:"+q, d.lists["search:"+q])
}
func (d *fakeDir) Retry()          { d.retries, d.err = d.retries+1, nil }
func (d *fakeDir) Version() uint64 { return d.version }

// fakeSaved keeps the saved stations newest first, as radio.Stations does.
type fakeSaved struct {
	list    []radio.Station
	version uint64
}

func (s *fakeSaved) index(id string) int {
	return slices.IndexFunc(s.list, func(st radio.Station) bool { return st.ID == id })
}

func (s *fakeSaved) List() []radio.Station { return slices.Clone(s.list) }
func (s *fakeSaved) Has(id string) bool    { return s.index(id) >= 0 }
func (s *fakeSaved) Version() uint64       { return s.version }
func (s *fakeSaved) Toggle(st radio.Station) bool {
	s.version++
	if i := s.index(st.ID); i >= 0 {
		s.list = slices.Delete(s.list, i, i+1)
		return false
	}
	s.list = slices.Insert(s.list, 0, st)
	return true
}

// fakePlayer records what the screens play, and whether shuffle was on as
// each list started.
type fakePlayer struct {
	played        [][]player.Track
	starts        []int
	shuffleAtPlay []bool
	shuffle       bool
	state         player.State
}

func (p *fakePlayer) Play(ts []player.Track, start int) {
	p.played = append(p.played, ts)
	p.starts = append(p.starts, start)
	p.shuffleAtPlay = append(p.shuffleAtPlay, p.shuffle)
}
func (p *fakePlayer) SetShuffle(on bool)  { p.shuffle = on }
func (p *fakePlayer) State() player.State { return p.state }

// fakeNav records pushes, toasts and the tabs shown.
type fakeNav struct {
	pushed []screens.Screen
	toasts []string
	tabs   []int
}

func (n *fakeNav) Push(s screens.Screen) { n.pushed = append(n.pushed, s) }
func (n *fakeNav) Pop()                  { n.pushed = n.pushed[:len(n.pushed)-1] }
func (n *fakeNav) Toast(msg string)      { n.toasts = append(n.toasts, msg) }
func (n *fakeNav) ShowTab(t int)         { n.tabs = append(n.tabs, t) }

var (
	cityHindi = radio.Station{ID: "96062a7b-0601-11e8-ae97-52543be04c81", Name: "Radio City Hindi",
		URL: "http://stream.radio.test/city", Logo: "https://logos.radio.test/city.png", Country: "India", CountryCode: "IN",
		Codec: "MP3", Bitrate: 128, Tags: []string{"bollywood", "hindi", "music"}}
	worldNews = radio.Station{ID: "98adecf7-2683-4408-9be7-02d3f9098eb8", Name: "World News Live",
		URL: "http://stream.radio.test/news", Country: "United Kingdom", CountryCode: "GB", Codec: "MP3", Bitrate: 56,
		Tags: []string{"news", "talk"}}
	jazzLounge = radio.Station{ID: "4f8e3d2a-1c7b-4a5e-9f60-8b2d7c1e3a49", Name: "Jazz Lounge",
		URL: "http://stream.radio.test/jazz", Logo: "https://logos.radio.test/jazz.png", Country: "France", CountryCode: "FR",
		Codec: "AAC+", Bitrate: 64, Tags: []string{"jazz", "lounge", "smooth jazz"}}
	deepSpace = radio.Station{ID: "c1d2e3f4-5a6b-4c7d-8e9f-0a1b2c3d4e5f", Name: "Deep Space One",
		URL: "http://stream.radio.test/space", Logo: "https://logos.radio.test/space.png",
		Country: "The United States Of America", CountryCode: "US", Codec: "MP3", Bitrate: 128,
		Tags: []string{"ambient", "space", "chillout"}}
	kantipur = radio.Station{ID: "e5f6a7b8-9c0d-4e1f-a2b3-c4d5e6f7a8b9", Name: "Kantipur FM",
		URL: "http://stream.radio.test/kantipur", Country: "Nepal", CountryCode: "NP", Codec: "AAC"}

	top       = []radio.Station{cityHindi, deepSpace, worldNews, jazzLounge, kantipur}
	countries = []radio.Count{{Name: "The United States Of America", Code: "US", Stations: 6210},
		{Name: "Germany", Code: "DE", Stations: 3480}, {Name: "India", Code: "IN", Stations: 1204},
		{Name: "United Kingdom", Code: "GB", Stations: 1532}, {Name: "France", Code: "FR", Stations: 1109}}
	genres = []radio.Count{{Name: "pop", Stations: 3051}, {Name: "news", Stations: 2846}, {Name: "jazz", Stations: 1201},
		{Name: "rock", Stations: 2612}, {Name: "classical", Stations: 1}}
)

// directory answers every list, with jazz's and India's stations.
func directory() *fakeDir {
	return &fakeDir{top: top, countries: countries, genres: genres, have: true, lists: map[string][]radio.Station{
		"tag:jazz":   {jazzLounge, deepSpace, kantipur},
		"country:IN": {cityHindi},
	}}
}

func newEnv(dir *fakeDir, saved *fakeSaved) (*Env, *fakeNav, *fakePlayer) {
	nav, p := &fakeNav{}, &fakePlayer{}
	return &Env{Nav: nav, Dir: dir, Saved: saved, Player: p}, nav, p
}

func tracks(sts []radio.Station) []player.Track {
	var ts []player.Track
	for _, s := range sts {
		ts = append(ts, s.Track())
	}
	return ts
}

func offline(u *ui.Context) { u.Status.Online = false }

// render draws s twice, as the app would: the first frame asks for its
// pictures and the second shows them, faded in. A station's accent is gold.
func render(t *testing.T, s screens.Screen, set ...func(*ui.Context)) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Accent = theme.Fallback()
	u.Status = fixture.Status()
	for _, f := range set {
		f(u)
	}
	u.BeginFrame(t0)
	s.Draw(u)
	u.BeginFrame(t0.Add(anim.ArtFade))
	s.Draw(u)
	return off.Image()
}

func TestRowLine(t *testing.T) {
	for _, c := range []struct {
		s    radio.Station
		want string
	}{
		{radio.Station{Country: "India", Codec: "MP3", Bitrate: 128, Tags: []string{"news", "hindi", "talk"}},
			"India · MP3 128 kbps · news, hindi"},
		{radio.Station{Country: "Nepal", Codec: "AAC"}, "Nepal · AAC"},
		{radio.Station{Codec: "AAC+", Bitrate: 64, Tags: []string{"jazz"}}, "AAC+ 64 kbps · jazz"},
		{radio.Station{Bitrate: 128}, "128 kbps"},
		{radio.Station{Country: "France", Tags: []string{"lounge", "jazz"}}, "France · lounge, jazz"},
		{radio.Station{}, ""},
	} {
		if got := line(c.s); got != c.want {
			t.Errorf("line(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
	if stationCount(1) != "1 station" || stationCount(1204) != "1,204 stations" {
		t.Fatalf("counts %q, %q", stationCount(1), stationCount(1204))
	}
}

// The tab opens on Saved, or on Top while nothing is saved.
func TestTabOpensOnSavedOrTop(t *testing.T) {
	e, _, _ := newEnv(directory(), &fakeSaved{list: []radio.Station{jazzLounge}})
	if tab := NewTab(e); tab.filter != savedFilter {
		t.Fatalf("with a station saved the tab opens on %s", filterNames[tab.filter])
	}
	e, _, _ = newEnv(directory(), &fakeSaved{})
	if tab := NewTab(e); tab.filter != topFilter {
		t.Fatalf("with none saved the tab opens on %s", filterNames[tab.filter])
	}
}

// Saved lists the stations saved, each with its heart, the one playing in
// the accent, and asks Radio Browser nothing.
func TestSavedStations(t *testing.T) {
	dir := directory()
	e, _, p := newEnv(dir, &fakeSaved{list: []radio.Station{jazzLounge, cityHindi, worldNews}})
	p.state.Track = cityHindi.Track()
	tab := NewTab(e)
	uitest.Golden(t, "radio-saved", render(t, tab))
	if len(dir.asked) != 0 {
		t.Fatalf("Saved asked for %v", dir.asked)
	}
	if !slices.Equal(tab.counts[:], []string{"3", "", "", ""}) {
		t.Fatalf("the filters' counts %q", tab.counts)
	}
	if h := tab.hints(); !reflect.DeepEqual(h, tabHints.remove) {
		t.Fatalf("hints %v", h)
	}
}

func TestSavedEmpty(t *testing.T) {
	e, _, _ := newEnv(directory(), &fakeSaved{})
	tab := NewTab(e)
	tab.Handle(press(input.L2), t0)
	uitest.Golden(t, "radio-saved-empty", render(t, tab))
	if h := tab.hints(); !reflect.DeepEqual(h, tabHints.search) {
		t.Fatalf("hints %v", h)
	}
}

// L2 and R2 move through the filters and stop at the ends, as the Library's
// do; each draws its list.
func TestFilters(t *testing.T) {
	dir := directory()
	e, _, _ := newEnv(dir, &fakeSaved{list: []radio.Station{jazzLounge, cityHindi}})
	tab := NewTab(e)
	if tab.Handle(press(input.L2), t0) {
		t.Fatal("L2 on the first filter does something")
	}
	tab.Handle(press(input.R2), t0)
	uitest.Golden(t, "radio-top", render(t, tab))
	if h := tab.hints(); !reflect.DeepEqual(h, tabHints.remove) {
		t.Fatalf("on a saved station the hints are %v", h)
	}
	tab.Handle(press(input.Down), t0)
	if h := tab.hints(); !reflect.DeepEqual(h, tabHints.save) {
		t.Fatalf("on a station not saved the hints are %v", h)
	}
	tab.Handle(press(input.R2), t0)
	uitest.Golden(t, "radio-countries", render(t, tab))
	tab.Handle(press(input.R2), t0)
	uitest.Golden(t, "radio-genres", render(t, tab))
	if h := tab.hints(); !reflect.DeepEqual(h, tabHints.open) {
		t.Fatalf("on Genres the hints are %v", h)
	}
	if !slices.Equal(dir.asked, []string{"top", "countries", "genres"}) {
		t.Fatalf("a filter chosen asks for its list at once, and once; asked for %v", dir.asked)
	}
	if tab.Handle(press(input.R2), t0) {
		t.Fatal("R2 on the last filter does something")
	}
	for range 3 {
		tab.Handle(press(input.L2), t0)
	}
	if tab.filter != savedFilter {
		t.Fatalf("L2 goes back through the filters to %s", filterNames[tab.filter])
	}
}

func TestTabStates(t *testing.T) {
	dir := &fakeDir{}
	e, nav, _ := newEnv(dir, &fakeSaved{})
	tab := NewTab(e)
	uitest.Golden(t, "radio-loading", render(t, tab))
	if h := tab.hints(); !reflect.DeepEqual(h, tabHints.search) {
		t.Fatalf("loading, the hints are %v", h)
	}
	if tab.Handle(press(input.A), t0) {
		t.Fatal("A with nothing listed did something")
	}
	if len(nav.pushed) > 0 {
		t.Fatal("A with nothing listed opened something")
	}

	unreachable := fmt.Errorf("%w: dial tcp: no route to host", radio.ErrUnreachable)
	dir.err = unreachable
	dir.version++
	uitest.Golden(t, "radio-unreachable", render(t, tab))
	if h := tab.hints(); !reflect.DeepEqual(h, tabHints.retry) {
		t.Fatalf("unreachable, the hints are %v", h)
	}
	if !tab.Handle(press(input.X), t0) || dir.retries != 1 || len(nav.pushed) != 0 {
		t.Fatalf("X retried %d times and pushed %d pages", dir.retries, len(nav.pushed))
	}

	dir.err = unreachable // asked again, and failed again
	dir.version++
	pg := newPage(e, "jazz", func() ([]radio.Station, bool, error) { return dir.ByTag("jazz") })
	render(t, pg)
	if pg.l.note() != "Couldn’t reach Radio Browser" || !reflect.DeepEqual(pg.hints(), pageHints.retry) {
		t.Fatalf("a page unreachable says %q, with hints %v", pg.l.note(), pg.hints())
	}
	if !pg.Handle(press(input.X), t0) || dir.retries != 2 || len(nav.pushed) != 0 {
		t.Fatalf("X on the page retried %d times and pushed %d pages", dir.retries, len(nav.pushed))
	}

	dir.have, dir.err = true, nil
	dir.version++
	render(t, tab)
	if got := tab.lists[topFilter].note(); got != "No stations here" {
		t.Fatalf("an empty answer says %q", got)
	}
}

// After X Retry, the tab and a page say "Loading stations…" while Radio
// Browser is asked again, and that it couldn't be reached if it fails again.
func TestRetrySaysLoading(t *testing.T) {
	unreachable := fmt.Errorf("%w: dial tcp: no route to host", radio.ErrUnreachable)
	dir := &fakeDir{}
	e, _, _ := newEnv(dir, &fakeSaved{})
	tab := NewTab(e)
	pg := newPage(e, "jazz", func() ([]radio.Station, bool, error) { return dir.ByTag("jazz") })
	type hinted interface {
		screens.Screen
		hints() []widgets.Hint
	}
	for _, c := range []struct {
		name  string
		s     hinted
		l     *list
		hints *hintBars
	}{
		{"the tab", tab, &tab.lists[topFilter], &tabHints},
		{"a page", pg, &pg.l, &pageHints},
	} {
		dir.err = unreachable
		dir.version++
		render(t, c.s)
		if got := c.l.note(); got != "Couldn’t reach Radio Browser" {
			t.Fatalf("%s unreachable says %q", c.name, got)
		}
		c.s.Handle(press(input.X), t0)
		render(t, c.s)
		if got, h := c.l.note(), c.s.hints(); got != "Loading stations…" || !reflect.DeepEqual(h, c.hints.search) {
			t.Fatalf("%s asking again says %q, with hints %v", c.name, got, h)
		}
		dir.err = unreachable
		dir.version++
		render(t, c.s)
		if got := c.l.note(); got != "Couldn’t reach Radio Browser" {
			t.Fatalf("%s failing again says %q", c.name, got)
		}
	}
}

// Offline, Top shows the offline state under the filter row and A opens
// Downloads; Saved still lists, and plays.
func TestOffline(t *testing.T) {
	e, nav, p := newEnv(directory(), &fakeSaved{})
	tab := NewTab(e)
	uitest.Golden(t, "radio-offline", render(t, tab, offline))
	if h := tab.hints(); !reflect.DeepEqual(h, tabHints.offline) {
		t.Fatalf("offline, the hints are %v", h)
	}
	if !tab.Handle(press(input.A), t0) || !slices.Equal(nav.tabs, []int{screens.DownloadsTab}) {
		t.Fatalf("A offline showed tabs %v", nav.tabs)
	}
	if tab.Handle(press(input.Y), t0) || tab.Handle(press(input.X), t0) {
		t.Fatal("Y or X did something offline")
	}

	saved := &fakeSaved{list: []radio.Station{jazzLounge, cityHindi}}
	e, _, p = newEnv(directory(), saved)
	tab = NewTab(e)
	render(t, tab, offline)
	if tab.offline() {
		t.Fatal("offline, Saved doesn't list")
	}
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.A), t0)
	if len(p.played) != 1 || p.starts[0] != 1 || !reflect.DeepEqual(p.played[0], tracks(saved.list)) {
		t.Fatalf("offline, A on Saved played %v from %v", p.played, p.starts)
	}

	dir := directory()
	e, nav, p = newEnv(dir, saved)
	pg := newPage(e, "The United States Of America", func() ([]radio.Station, bool, error) { return dir.ByCountry("US") })
	uitest.Golden(t, "radio-page-offline", render(t, pg, offline))
	if len(dir.asked) != 0 || !reflect.DeepEqual(pg.hints(), pageHints.offline) {
		t.Fatalf("a page offline asked for %v, with hints %v", dir.asked, pg.hints())
	}
	if !pg.Handle(press(input.A), t0) || !slices.Equal(nav.tabs, []int{screens.DownloadsTab}) || len(p.played) != 0 {
		t.Fatalf("A on a page offline showed tabs %v and played %v", nav.tabs, p.played)
	}
}

// A plays the list from the station focused, as stations.
func TestAPlaysTheListFromTheStation(t *testing.T) {
	e, _, p := newEnv(directory(), &fakeSaved{})
	tab := NewTab(e)
	render(t, tab)
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.A), t0)
	if len(p.played) != 1 || p.starts[0] != 2 || !reflect.DeepEqual(p.played[0], tracks(top)) {
		t.Fatalf("played %v from %v", p.played, p.starts)
	}
	if !p.played[0][2].Live() || p.played[0][2].StationID != worldNews.ID {
		t.Fatalf("the station started %+v", p.played[0][2])
	}
}

// A on a country or a genre opens its page, which lists its stations under
// the heading and plays them.
func TestCountryAndGenrePages(t *testing.T) {
	dir := directory()
	e, nav, p := newEnv(dir, &fakeSaved{list: []radio.Station{jazzLounge}})
	tab := NewTab(e)
	tab.Handle(press(input.R2), t0)
	tab.Handle(press(input.R2), t0) // Countries
	render(t, tab)
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.Down), t0) // India
	tab.Handle(press(input.A), t0)
	if len(nav.pushed) != 1 {
		t.Fatalf("A on a country pushed %d pages", len(nav.pushed))
	}
	pg, ok := nav.pushed[0].(*Page)
	if !ok || pg.title != "India" {
		t.Fatalf("A on India pushed %#v", nav.pushed[0])
	}
	render(t, pg)
	if !slices.Contains(dir.asked, "country:IN") || pg.meta != "1 station" {
		t.Fatalf("India's page asked for %v and says %q", dir.asked, pg.meta)
	}

	tab.Handle(press(input.R2), t0) // Genres
	render(t, tab)
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.Down), t0) // jazz
	tab.Handle(press(input.A), t0)
	pg = nav.pushed[1].(*Page)
	p.state.Track = deepSpace.Track()
	uitest.Golden(t, "radio-page", render(t, pg))
	if pg.title != "jazz" || pg.meta != "3 stations" || !slices.Contains(dir.asked, "tag:jazz") {
		t.Fatalf("jazz's page: %q, %q, asked for %v", pg.title, pg.meta, dir.asked)
	}
	if h := pg.hints(); !reflect.DeepEqual(h, pageHints.remove) {
		t.Fatalf("the page's hints are %v", h)
	}
	pg.Handle(press(input.Down), t0)
	pg.Handle(press(input.A), t0)
	if len(p.played) != 1 || p.starts[0] != 1 || !reflect.DeepEqual(p.played[0], tracks(dir.lists["tag:jazz"])) {
		t.Fatalf("the page played %v from %v", p.played, p.starts)
	}
	pg.Handle(press(input.Y), t0)
	if !slices.Equal(nav.toasts, []string{"Saved Deep Space One"}) || !reflect.DeepEqual(pg.hints(), pageHints.remove) {
		t.Fatalf("Y on the page: toasts %v, hints %v", nav.toasts, pg.hints())
	}
}

// Y saves a station or removes it, and says so. On Saved the row removed
// goes, and focus moves to the next.
func TestYSavesAndRemoves(t *testing.T) {
	saved := &fakeSaved{}
	e, nav, _ := newEnv(directory(), saved)
	tab := NewTab(e)
	render(t, tab)
	tab.Handle(press(input.Y), t0)
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.Y), t0)
	if !slices.Equal(nav.toasts, []string{"Saved Radio City Hindi", "Saved Deep Space One"}) {
		t.Fatalf("toasts %v", nav.toasts)
	}
	if tab.counts[savedFilter] != "2" || tab.lists[topFilter].rows[0].Trail == 0 || tab.lists[topFilter].rows[2].Trail != 0 {
		t.Fatalf("Saved's count %q; Top's hearts %v", tab.counts[savedFilter], tab.lists[topFilter].rows)
	}
	tab.Handle(press(input.Y), t0)
	if nav.toasts[2] != "Removed Deep Space One" || tab.lists[topFilter].rows[1].Trail != 0 {
		t.Fatalf("toasts %v", nav.toasts)
	}

	saved.list = []radio.Station{jazzLounge, cityHindi, worldNews, kantipur}
	e, nav, _ = newEnv(directory(), saved)
	tab = NewTab(e)
	render(t, tab)
	focused := func() string {
		l := &tab.lists[savedFilter]
		return l.stations[l.grid.Focus()].Name
	}
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.Y), t0) // Radio City Hindi
	if len(tab.lists[savedFilter].rows) != 3 || focused() != "World News Live" {
		t.Fatalf("%d rows left, focus on %s", len(tab.lists[savedFilter].rows), focused())
	}
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.Y), t0) // the last, Kantipur FM
	if focused() != "World News Live" || !slices.Equal(nav.toasts, []string{"Removed Radio City Hindi", "Removed Kantipur FM"}) {
		t.Fatalf("focus on %s, toasts %v", focused(), nav.toasts)
	}
}

// X opens search, or asks again when Radio Browser couldn't be reached.
func TestXSearches(t *testing.T) {
	dir := directory()
	e, nav, _ := newEnv(dir, &fakeSaved{list: []radio.Station{jazzLounge}})
	tab := NewTab(e)
	render(t, tab)
	if !tab.Handle(press(input.X), t0) || len(nav.pushed) != 1 || dir.retries != 0 {
		t.Fatalf("X pushed %d pages and retried %d times", len(nav.pushed), dir.retries)
	}
}

// The tab asks Radio Browser again only once the lists have changed, or a
// second on.
func TestTabAsksWhenTheListsChange(t *testing.T) {
	dir := directory()
	e, _, _ := newEnv(dir, &fakeSaved{})
	tab := NewTab(e)
	render(t, tab)
	n := len(dir.asked)
	if render(t, tab); len(dir.asked) != n {
		t.Fatalf("asked %d times more with nothing changed", len(dir.asked)-n)
	}
	dir.top = top[:2]
	dir.version++
	if render(t, tab); len(dir.asked) != n+1 || len(tab.lists[topFilter].rows) != 2 {
		t.Fatalf("changed, asked %d times more, %d rows", len(dir.asked)-n, len(tab.lists[topFilter].rows))
	}
	tab.refresh(t0.Add(time.Second))
	if len(dir.asked) != n+2 {
		t.Fatal("not asked again a second on")
	}
}

// A page counts its stations for its heading when its list comes or changes,
// not each second it asks again.
func TestPageCountsWhenTheListChanges(t *testing.T) {
	e, _, _ := newEnv(directory(), &fakeSaved{})
	var sts []radio.Station
	have := false
	pg := newPage(e, "jazz", func() ([]radio.Station, bool, error) { return sts, have, nil })
	now := t0
	pg.refresh(now)
	if pg.meta != "" {
		t.Fatalf("before an answer the heading says %q", pg.meta)
	}
	have = true
	pg.refresh(now.Add(time.Second))
	if pg.meta != "0 stations" {
		t.Fatalf("with an empty answer the heading says %q", pg.meta)
	}
	sts = []radio.Station{jazzLounge, deepSpace, kantipur}
	pg.refresh(now.Add(2 * time.Second))
	if pg.meta != "3 stations" {
		t.Fatalf("with three stations the heading says %q", pg.meta)
	}
	now = now.Add(2 * time.Second)
	if n := testing.AllocsPerRun(10, func() { now = now.Add(time.Second); pg.refresh(now) }); n != 0 {
		t.Fatalf("asking again a second on allocates %v times", n)
	}
}

// Every hint bar fits, with L2/R2 Filter kept.
func TestHintsFit(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	want := []widgets.Hint{{Key: "A", Label: "Play"}, {Key: "Y", Label: "Save"}, {Key: "X", Label: "Search"},
		{Key: "L2/R2", Label: "Filter"}, {Key: "L1/R1", Label: "Tabs"}}
	if !reflect.DeepEqual(tabHints.save, want) {
		t.Fatalf("the tab's hints on a station %v", tabHints.save)
	}
	for _, b := range []hintBars{tabHints, pageHints} {
		for _, h := range [][]widgets.Hint{b.save, b.remove, b.open, b.search, b.retry, b.offline, b.offlineAlone} {
			if w := widgets.HintsWidth(u, h); w > widgets.MaxHintsWidth {
				t.Errorf("hints %v are %.1f wide, more than %.1f", h, w, widgets.MaxHintsWidth)
			}
		}
	}
}

// Frames drawn again allocate nothing: rows, counts and hint bars are made
// once, and Radio Browser is asked only when its lists change.
func TestFramesDrawnAgainAllocateNothing(t *testing.T) {
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
	e, nav, p := newEnv(directory(), &fakeSaved{list: []radio.Station{jazzLounge, cityHindi}})
	p.state.Track = cityHindi.Track()
	tab := NewTab(e)
	for f := range filterCount {
		if f > 0 {
			tab.Handle(press(input.R2), t0)
		}
		draw(filterNames[f], tab)
	}
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.Down), t0) // jazz
	tab.Handle(press(input.A), t0)
	draw("a genre's page", nav.pushed[0])

	u.Status.Online = false
	draw("offline", tab)
	draw("a page offline", nav.pushed[0])
	u.Status.Online = true

	e, _, _ = newEnv(&fakeDir{}, &fakeSaved{})
	draw("loading", NewTab(e))
	e, _, _ = newEnv(&fakeDir{err: radio.ErrUnreachable}, &fakeSaved{})
	draw("unreachable", NewTab(e))
	tab = NewTab(e)
	tab.Handle(press(input.L2), t0)
	draw("Saved empty", tab)
}

// Station lists play in order, whatever shuffle was left on elsewhere.
func TestStationListsPlayInOrder(t *testing.T) {
	dir := searchDir()
	e, _, p := newEnv(dir, &fakeSaved{})
	p.shuffle = true
	r := newResults(e, "jazz")
	render(t, r)
	if !r.Handle(press(input.A), t0) || len(p.played) != 1 {
		t.Fatalf("A played %d lists", len(p.played))
	}
	if p.shuffleAtPlay[0] {
		t.Fatal("the list was played shuffled")
	}
}

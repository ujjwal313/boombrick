package app

import (
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/screens/home"
	radioscreen "github.com/ujjwal313/boombrick/internal/screens/radio"
	settingsscreen "github.com/ujjwal313/boombrick/internal/screens/settings"
	spotifyscreen "github.com/ujjwal313/boombrick/internal/screens/spotify"
	keys "github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/store"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// switchPrefs are settings in a map, the switches unset reading on.
type switchPrefs map[string]string

func (p switchPrefs) String(k string) string {
	if v, ok := p[k]; ok {
		return v
	}
	return keys.Defaults[k]
}
func (p switchPrefs) Bool(k string) bool { return p.String(k) == "true" }
func (p switchPrefs) Set(k, v string)    { p[k] = v }
func (p switchPrefs) SetBool(k string, v bool) {
	p[k] = map[bool]string{true: "true", false: "false"}[v]
}

func TestShownTabs(t *testing.T) {
	home, lib := widgets.TabHome, widgets.TabLibrary
	sp, yt, dl, rd := widgets.TabSpotify, widgets.TabYouTube, widgets.TabDownloads, widgets.TabRadio
	for _, c := range []struct {
		spotify, youtube, radio bool
		want                    []int
	}{
		{true, true, true, []int{home, lib, sp, yt, dl, rd}},
		{true, true, false, []int{home, lib, sp, yt, dl}},
		{false, true, true, []int{home, lib, yt, dl, rd}},
		{true, false, true, []int{home, lib, sp, rd}},
		{false, false, true, []int{home, lib, rd}},
		{true, false, false, []int{home, lib, sp}},
		{false, false, false, []int{home, lib}},
	} {
		if got := shownTabs(c.spotify, c.youtube, c.radio); !slices.Equal(got, c.want) {
			t.Errorf("Spotify %v, YouTube %v, Radio %v: tabs %v, want %v", c.spotify, c.youtube, c.radio, got, c.want)
		}
	}
}

// fakeSpotify is a Spotify that isn't nil, for the switches that ask only
// whether there is one.
type fakeSpotify struct{ Spotify }

// Radio is the third switch on the Integrations page, there only with the
// station directory.
func TestIntegrationsListRadioThird(t *testing.T) {
	a := &App{opts: Options{Spotify: fakeSpotify{}, Downloads: newLiveQueue(), Radio: &radio.Directory{}}}
	ins := a.integrations()
	want := settingsscreen.Integration{Key: keys.RadioOn, Name: "Radio", Icon: "radio", About: "Stations from Radio Browser"}
	if len(ins) != 3 || ins[2] != want {
		t.Fatalf("integrations %+v", ins)
	}
	a.opts.Radio = nil
	if got := a.integrations(); len(got) != 2 {
		t.Fatalf("without the directory there are %d switches, want 2", len(got))
	}
}

// Switching Radio off while a station plays says it plays on; at any other
// time it says nothing.
func TestSwitchingRadioOffWhileAStationPlays(t *testing.T) {
	p := switchPrefs{}
	a, _, _, _ := switchApp(t, p)
	a.last = player.State{Queued: true, Playing: true, Track: player.Track{StationID: "s1", Stream: "http://x/live"}}
	p.SetBool(keys.RadioOn, false)
	a.switched(keys.RadioOn, false)
	if a.shell.toast != "This station plays until you stop it" {
		t.Fatalf("toast %q", a.shell.toast)
	}
	if slices.Contains(a.ui.Tabs.Shown, widgets.TabRadio) {
		t.Fatal("the Radio tab is still shown")
	}

	a.shell.toast = ""
	a.last = player.State{Queued: true, Playing: true, Track: player.Track{Path: "/music/a.mp3"}}
	a.switched(keys.RadioOn, false)
	if a.shell.toast != "" {
		t.Fatalf("toast %q with no station playing", a.shell.toast)
	}
}

func TestSwitchedOffToast(t *testing.T) {
	if got := noticeText(player.Notice{Kind: player.SwitchedOff, Off: "Spotify"}); got != "Spotify is off" {
		t.Errorf("the toast says %q", got)
	}
}

// With Spotify switched off, an expired session is a toast: the dialog's
// Sign in would have no tab to open on.
func TestSignedOutWithSpotifyOffIsAToast(t *testing.T) {
	s, _, _ := testShell()
	a := &App{shell: s, opts: Options{Prefs: switchPrefs{keys.SpotifyOn: "false"}}}
	a.expired = spotifyscreen.NewExpired(func() {})
	s.alert = a.expired
	a.notice(player.Notice{Kind: player.SignedOut}, t0)
	if a.expired.IsOpen() || s.toast != "Spotify session expired" {
		t.Fatalf("dialog open %v, toast %q", a.expired.IsOpen(), s.toast)
	}
}

// switchApp is an app with the six tabs, recording the queue's hold and the
// importer's switch.
func switchApp(t *testing.T, p switchPrefs) (*App, *[]bool, *[]bool, *liveQueue) {
	t.Helper()
	t.Cleanup(func() { screens.SetDownloads(true) })
	s, _ := sixTabs()
	var holds, imports []bool
	q := newLiveQueue()
	a := &App{shell: s, ui: &ui.Context{}, opts: Options{Prefs: p, Downloads: q, Radio: &radio.Directory{},
		Hold:     func(on bool) { holds = append(holds, on) },
		ImportOn: func(on bool) { imports = append(imports, on) }}}
	a.applySwitches()
	return a, &holds, &imports, q
}

func TestSwitchesAtStart(t *testing.T) {
	a, holds, imports, _ := switchApp(t, switchPrefs{keys.YouTubeOn: "false"})
	if !slices.Equal(a.ui.Tabs.Shown, []int{widgets.TabHome, widgets.TabLibrary, widgets.TabSpotify, widgets.TabRadio}) {
		t.Fatalf("tabs %v", a.ui.Tabs.Shown)
	}
	if !slices.Equal(*holds, []bool{true}) || !slices.Equal(*imports, []bool{false}) {
		t.Fatalf("hold %v, importer %v", *holds, *imports)
	}
	if screens.DownloadsOn() {
		t.Fatal("the screens should know Downloads is gone")
	}
}

func TestSwitchingOffWhileBusySaysWhatFinishes(t *testing.T) {
	p := switchPrefs{}
	a, holds, imports, q := switchApp(t, p)

	a.last = player.State{Queued: true, Playing: true, Track: player.Track{SpotifyID: "s1"}}
	p.SetBool(keys.SpotifyOn, false)
	a.switched(keys.SpotifyOn, false)
	if a.shell.toast != "This song finishes, then Spotify songs are skipped" {
		t.Fatalf("toast %q", a.shell.toast)
	}
	if slices.Contains(a.ui.Tabs.Shown, widgets.TabSpotify) {
		t.Fatal("the Spotify tab is still shown")
	}
	if got := (*imports)[len(*imports)-1]; got {
		t.Fatal("the importer is on with Spotify off")
	}

	q.setBusy(true)
	p.SetBool(keys.YouTubeOn, false)
	a.switched(keys.YouTubeOn, false)
	if a.shell.toast != "Downloads under way will finish" {
		t.Fatalf("toast %q", a.shell.toast)
	}
	if got := (*holds)[len(*holds)-1]; !got {
		t.Fatal("the queue isn't held with YouTube off")
	}

	// Nothing busy, nothing to say.
	a.shell.toast = ""
	p.SetBool(keys.YouTubeOn, true)
	a.switched(keys.YouTubeOn, true)
	q.setBusy(false)
	p.SetBool(keys.YouTubeOn, false)
	a.switched(keys.YouTubeOn, false)
	if a.shell.toast != "" {
		t.Fatalf("toast %q with nothing under way", a.shell.toast)
	}
}

// Settings open over a tab that is switched off: closing it shows Home.
func TestSwitchingOffTheTabUnderSettings(t *testing.T) {
	p := switchPrefs{}
	a, _, _, _ := switchApp(t, p)
	a.shell.showTab(widgets.TabDownloads, t0)
	a.shell.overlays = []overlay{{screen: &page{name: "settings"}}}
	a.shell.openOverlay(0, t0)
	p.SetBool(keys.YouTubeOn, false)
	a.switched(keys.YouTubeOn, false)
	if a.shell.tab != widgets.TabHome || a.shell.open != 0 {
		t.Fatalf("tab %d, overlay %d: want Home under Settings", a.shell.tab, a.shell.open)
	}
}

// Without the station directory there is no Radio tab, whatever its switch
// says.
func TestNoRadioTabWithoutTheDirectory(t *testing.T) {
	a := &App{opts: Options{Prefs: switchPrefs{}}}
	if a.radioOn() {
		t.Fatal("Radio is on without its directory")
	}
	if slices.Contains(shownTabs(a.spotifyOn(), a.youtubeOn(), a.radioOn()), widgets.TabRadio) {
		t.Fatal("the Radio tab is shown without its directory")
	}
	a.opts.Radio = &radio.Directory{}
	if !a.radioOn() {
		t.Fatal("Radio is off with its directory and its switch on")
	}
}

// With the station directory and the stations saved, the Radio tab's root is
// the Radio tab; without the stations, a stand-in.
func TestRadioRootIsTheRadioTab(t *testing.T) {
	a, _ := homeApp(t, switchPrefs{})
	root := a.radioTab()
	if _, ok := root.(*radioscreen.Tab); !ok {
		t.Fatalf("the Radio root is %T", root)
	}
	a.opts.Stations = nil
	root = a.radioTab()
	if _, ok := root.(*screens.Placeholder); !ok {
		t.Fatalf("without the stations, the Radio root is %T", root)
	}
	a, _ = homeApp(t, switchPrefs{})
	a.opts.Player = nil
	root = a.radioTab()
	if _, ok := root.(*screens.Placeholder); !ok {
		t.Fatalf("without a player, the Radio root is %T", root)
	}
}

// quietOut is an audio output that plays nothing.
type quietOut struct{}

func (quietOut) Open(int) error      { return nil }
func (quietOut) Write([]int16) error { return nil }
func (quietOut) Drop() (int, error)  { return 0, nil }
func (quietOut) Close() error        { return nil }

// homeApp is switchApp with a library, a Home and one saved station, as
// showHome needs them.
func homeApp(t *testing.T, p switchPrefs) (*App, *[]string) {
	t.Helper()
	a, _, _, _ := switchApp(t, p)
	st, err := store.Open(filepath.Join(t.TempDir(), "boombrick.db"))
	if err != nil {
		t.Fatal(err)
	}
	stations, err := radio.NewStations(st.DB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stations.Close(); st.Close() })
	a.opts.Stations = stations
	pl := player.New(quietOut{}, func(string) (audio.Source, error) { return nil, io.EOF },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	a.opts.Player = pl
	t.Cleanup(func() { pl.Close() })
	a.ix = &library.Index{Recent: []*library.Album{{Title: "Album", Artist: "Artist"}}}
	a.home = home.New(nil)
	var played []string
	a.play = func(tracks []player.Track, start int) { played = append(played, tracks[start].Title) }
	return a, &played
}

func TestHomeRadioRow(t *testing.T) {
	station := radio.Station{ID: "u1", Name: "Radio X", URL: "http://s/x", Country: "India"}
	a, played := homeApp(t, switchPrefs{})
	a.opts.Stations.Toggle(station)
	a.opts.Stations.Toggle(radio.Station{ID: "u2", Name: "Radio Y", URL: "http://s/y"})
	a.opts.Spotify = fakeSpotify{}
	a.env = &spotifyscreen.Env{}
	a.playlists = []spotify.Playlist{{URI: "spotify:playlist:p1", Name: "Road trip", Count: 3}}
	a.showHome(nil)
	if got := a.home.RowTitles(); !slices.Equal(got, []string{"Spotify playlists", "Radio", "Recently added"}) {
		t.Fatalf("rows %v", got)
	}
	a.opts.Player.SetShuffle(true) // left on from elsewhere
	a.home.Handle(input.Event{Button: input.Down, Kind: input.Press}, time.Time{})
	a.home.Handle(input.Event{Button: input.Right, Kind: input.Press}, time.Time{})
	a.home.Handle(input.Event{Button: input.A, Kind: input.Press}, time.Time{})
	if !slices.Equal(*played, []string{"Radio X"}) {
		t.Fatalf("A played %v, want the first saved station (the latest saved is first)", *played)
	}
	if a.opts.Player.State().Shuffle {
		t.Fatal("the stations were played with shuffle on")
	}

	a.opts.Prefs.SetBool(keys.RadioOn, false)
	a.showHome(nil)
	if got := a.home.RowTitles(); slices.Contains(got, "Radio") {
		t.Fatalf("rows %v with Radio switched off", got)
	}
}

func TestHomeRadioRowNeedsAPlayer(t *testing.T) {
	a, _ := homeApp(t, switchPrefs{})
	a.opts.Stations.Toggle(radio.Station{ID: "u1", Name: "Radio X", URL: "http://s/x"})
	a.opts.Player = nil
	a.showHome(nil)
	if got := a.home.RowTitles(); slices.Contains(got, "Radio") {
		t.Fatalf("rows %v without a player", got)
	}
}

func TestHomeRadioRowNeedsASavedStation(t *testing.T) {
	a, _ := homeApp(t, switchPrefs{})
	a.showHome(nil)
	if got := a.home.RowTitles(); !slices.Equal(got, []string{"Recently added"}) {
		t.Fatalf("rows %v with nothing saved", got)
	}
}

func TestHomeFollowsSavedStations(t *testing.T) {
	a, _ := homeApp(t, switchPrefs{})
	a.showHome(nil)
	a.followStations()
	if got := a.home.RowTitles(); slices.Contains(got, "Radio") {
		t.Fatalf("rows %v before a station is saved", got)
	}
	a.opts.Stations.Toggle(radio.Station{ID: "u1", Name: "Radio X", URL: "http://s/x"})
	a.followStations()
	if got := a.home.RowTitles(); !slices.Equal(got, []string{"Radio", "Recently added"}) {
		t.Fatalf("rows %v after saving a station", got)
	}
	a.opts.Stations.Toggle(radio.Station{ID: "u1", Name: "Radio X", URL: "http://s/x"})
	a.followStations()
	if got := a.home.RowTitles(); slices.Contains(got, "Radio") {
		t.Fatalf("rows %v after removing it", got)
	}
}

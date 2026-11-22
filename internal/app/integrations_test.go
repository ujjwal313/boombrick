package app

import (
	"slices"
	"testing"

	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	spotifyscreen "github.com/ujjwal313/boombrick/internal/screens/spotify"
	keys "github.com/ujjwal313/boombrick/internal/settings"
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
	for _, c := range []struct {
		spotify, youtube bool
		want             []int
	}{
		{true, true, []int{home, lib, widgets.TabSpotify, widgets.TabYouTube, widgets.TabDownloads}},
		{false, true, []int{home, lib, widgets.TabYouTube, widgets.TabDownloads}},
		{true, false, []int{home, lib, widgets.TabSpotify}},
		{false, false, []int{home, lib}},
	} {
		if got := shownTabs(c.spotify, c.youtube); !slices.Equal(got, c.want) {
			t.Errorf("Spotify %v, YouTube %v: tabs %v, want %v", c.spotify, c.youtube, got, c.want)
		}
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

// switchApp is an app with the five tabs, recording the queue's hold and the
// importer's switch.
func switchApp(t *testing.T, p switchPrefs) (*App, *[]bool, *[]bool, *liveQueue) {
	t.Helper()
	t.Cleanup(func() { screens.SetDownloads(true) })
	s, _ := fiveTabs()
	var holds, imports []bool
	q := newLiveQueue()
	a := &App{shell: s, ui: &ui.Context{}, opts: Options{Prefs: p, Downloads: q,
		Hold:     func(on bool) { holds = append(holds, on) },
		ImportOn: func(on bool) { imports = append(imports, on) }}}
	a.applySwitches()
	return a, &holds, &imports, q
}

func TestSwitchesAtStart(t *testing.T) {
	a, holds, imports, _ := switchApp(t, switchPrefs{keys.YouTubeOn: "false"})
	if !slices.Equal(a.ui.Tabs.Shown, []int{widgets.TabHome, widgets.TabLibrary, widgets.TabSpotify}) {
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

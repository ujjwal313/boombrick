package settings

import (
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/download"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/release"
	keys "github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
)

var integrations = []Integration{
	{Key: keys.SpotifyOn, Name: "Spotify", Icon: "stream", About: "Stream your playlists and liked songs · needs Premium"},
	{Key: keys.YouTubeOn, Name: "YouTube", Icon: "circleplay", About: "Search YouTube Music and download songs"},
}

// switchEnv is Settings with both integrations, the Spotify account and the
// Downloader card, recording the switches.
type switchEnv struct {
	*env
	acct     *fakeAccount
	dl       *fakeDownloader
	tool     *fakeReleases
	switched []string
}

func newSwitchEnv(p prefs) *switchEnv {
	e := &switchEnv{env: &env{prefs: p, p: &fakePlayer{}, space: &fakeSpace{}},
		acct: &fakeAccount{st: spotify.Status{State: spotify.Ready, Name: "Ujjwal", Premium: true}},
		dl:   &fakeDownloader{d: download.Downloader{Version: "2026.09.27", Installed: t0.Add(-18 * 24 * time.Hour)}},
		tool: &fakeReleases{r: release.Release{Tag: "2026.10.11"}}}
	e.s = New(Options{Prefs: e.prefs, Player: e.p, Space: e.space, Version: "0.4.0", Device: "TrimUI Brick",
		Card:  func() (int64, int64, int64) { return 595 * gib / 10, 386 * gib / 10, 142 * gib / 10 },
		Toast: func(m string) { e.toasts = append(e.toasts, m) }, Spotify: e.acct, Copies: true,
		Downloader: e.dl, ToolReleases: e.tool, Integrations: integrations,
		Switched: func(key string, on bool) {
			e.switched = append(e.switched, key+"="+onOff(on))
		}})
	return e
}

func TestTheIntegrationsRowComesFirst(t *testing.T) {
	e := newSwitchEnv(prefs{})
	if e.s.focus != rowIntegrations {
		t.Fatalf("focus starts on %d, want Integrations", e.s.focus)
	}
	if got := e.s.rows[rowIntegrations].Value; got != "2 of 2 on" {
		t.Fatalf("the row says %q", got)
	}
	uitest.Golden(t, "settings-integrations-row", render(t, e.env, t0))
}

func TestTheIntegrationsPageSwitches(t *testing.T) {
	e := newSwitchEnv(prefs{})
	e.s.Handle(press(input.A), t0)
	if e.s.integrations == nil {
		t.Fatal("A on the row should open the page")
	}
	uitest.Golden(t, "integrations-page", render(t, e.env, t0))
	e.s.Handle(press(input.Down), t0)
	e.s.Handle(press(input.A), t0)
	if e.prefs.Bool(keys.YouTubeOn) {
		t.Fatal("A on YouTube should switch it off")
	}
	if !slices.Equal(e.switched, []string{keys.YouTubeOn + "=Off"}) {
		t.Fatalf("switched %v", e.switched)
	}
	if got := e.s.integrations.rows[1].Value; got != "Off" {
		t.Fatalf("the YouTube row says %q", got)
	}
	uitest.Golden(t, "integrations-page-off", render(t, e.env, t0.Add(anim.Focus)))
	if e.s.Back(); e.s.integrations != nil {
		t.Fatal("B should leave the page")
	}
	if got := e.s.rows[rowIntegrations].Value; got != "1 of 2 on" {
		t.Fatalf("back on Settings the row says %q", got)
	}
	e.s.Handle(press(input.A), t0)
	e.s.Handle(press(input.Down), t0)
	e.s.Handle(press(input.A), t0)
	if !e.prefs.Bool(keys.YouTubeOn) || len(e.switched) != 2 {
		t.Fatalf("switched back on: %v", e.switched)
	}
}

// Focus stops at the page's ends.
func TestTheIntegrationsPageFocus(t *testing.T) {
	e := newSwitchEnv(prefs{})
	e.s.Handle(press(input.A), t0)
	if e.s.Handle(press(input.Up), t0) {
		t.Fatal("Up from the first row should do nothing")
	}
	e.s.Handle(press(input.Down), t0)
	if e.s.Handle(press(input.Down), t0) {
		t.Fatal("Down from the last row should do nothing")
	}
	if e.s.integrations.focus != 1 {
		t.Fatalf("focus on %d", e.s.integrations.focus)
	}
}

// What belongs to an integration switched off is hidden: Spotify's
// Streaming quality, Prefer downloaded copies and account; YouTube's
// Download quality and Downloader card, and its release check.
func TestRowsOfAnIntegrationSwitchedOffAreHidden(t *testing.T) {
	both := newSwitchEnv(prefs{})
	render(t, both.env, t0)
	for _, item := range []int{rowStream, rowPrefer, rowQuality, pillTool, pillAccount} {
		if !both.s.shown(item) {
			t.Fatalf("item %d hidden with both on", item)
		}
	}

	sp := newSwitchEnv(prefs{keys.SpotifyOn: "false"})
	render(t, sp.env, t0)
	for _, item := range []int{rowStream, rowPrefer, pillAccount} {
		if sp.s.shown(item) {
			t.Errorf("item %d shown with Spotify off", item)
		}
	}
	if !sp.s.shown(rowQuality) || !sp.s.shown(pillTool) {
		t.Error("YouTube's rows went with Spotify")
	}
	if sp.s.rows[rowIntegrations].Value != "1 of 2 on" {
		t.Errorf("the row says %q", sp.s.rows[rowIntegrations].Value)
	}

	yt := newSwitchEnv(prefs{keys.YouTubeOn: "false"})
	yt.s.Opened()
	render(t, yt.env, t0)
	for _, item := range []int{rowQuality, pillTool} {
		if yt.s.shown(item) {
			t.Errorf("item %d shown with YouTube off", item)
		}
	}
	if yt.tool.checks != 0 {
		t.Error("yt-dlp's releases were checked with YouTube off")
	}
	uitest.Golden(t, "settings-youtube-off", render(t, yt.env, t0))
}

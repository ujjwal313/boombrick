package settings

import (
	"image"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/download"
	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/release"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/selfupdate"
	keys "github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/space"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(0, 0)

const gib = 1 << 30

type prefs map[string]string

func (p prefs) String(k string) string {
	if v, ok := p[k]; ok {
		return v
	}
	return keys.Defaults[k]
}
func (p prefs) Bool(k string) bool       { return p.String(k) == "true" }
func (p prefs) Set(k, v string)          { p[k] = v }
func (p prefs) SetBool(k string, v bool) { p[k] = map[bool]string{true: "true", false: "false"}[v] }

type fakePlayer struct {
	sleep     player.Sleep
	levelling []bool
}

func (f *fakePlayer) Sleep() player.Sleep { return f.sleep }
func (f *fakePlayer) SetSleep(m player.SleepMode) {
	f.sleep = player.Sleep{Mode: m}
	if m.Timed() {
		f.sleep.At = t0.Add(m.Duration())
	}
}
func (f *fakePlayer) SetLevelling(on bool) { f.levelling = append(f.levelling, on) }

type fakeSpace struct {
	sizes    space.Sizes
	measured bool
	measures int
	clears   int
	freed    int64
	cleared  bool
}

func (f *fakeSpace) Measure()                   { f.measures++ }
func (f *fakeSpace) Sizes() (space.Sizes, bool) { return f.sizes, f.measured }
func (f *fakeSpace) Clear()                     { f.clears++ }
func (f *fakeSpace) Cleared() (int64, bool) {
	ok := f.cleared
	f.cleared = false
	return f.freed, ok
}

type env struct {
	s      *Screen
	prefs  prefs
	p      *fakePlayer
	space  *fakeSpace
	toasts []string
}

func newEnv(withSpace bool) *env {
	e := &env{prefs: prefs{}, p: &fakePlayer{}, space: &fakeSpace{}}
	opts := Options{Prefs: e.prefs, Player: e.p, Version: "0.4.0", Device: "TrimUI Brick",
		Card:  func() (int64, int64, int64) { return 595 * gib / 10, 386 * gib / 10, 142 * gib / 10 },
		Toast: func(m string) { e.toasts = append(e.toasts, m) }}
	if withSpace {
		opts.Space = e.space
	}
	e.s = New(opts)
	return e
}

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

func render(t *testing.T, e *env, now time.Time) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Status = fixture.Status()
	u.BeginFrame(now)
	e.s.Draw(u)
	return off.Image()
}

func TestTheScreen(t *testing.T) {
	e := newEnv(true)
	e.p.sleep = player.Sleep{Mode: player.Sleep30, At: t0.Add(18*time.Minute + 22*time.Second)}
	uitest.Golden(t, "settings", render(t, e, t0))
	want := map[int]string{rowQuality: "Best", rowStorage: "38.6 GB free", rowSleep: "30 min · 18:22 left", rowLevelling: "On"}
	for row, v := range want {
		if got := e.s.rows[row].Value; got != v {
			t.Errorf("row %d says %q, want %q", row, got, v)
		}
	}
	if e.s.footer != "Boombrick 0.4.0 · TrimUI Brick · SD card 59.5 GB" {
		t.Errorf("footer %q", e.s.footer)
	}
}

func TestDownloadQuality(t *testing.T) {
	e := newEnv(true)
	e.s.Handle(press(input.A), t0)
	if e.s.open != rowQuality {
		t.Fatal("A didn't expand Download quality")
	}
	uitest.Golden(t, "settings-quality", render(t, e, t0.Add(anim.Focus)))
	e.s.Handle(press(input.Right), t0)
	if got := e.prefs.String(keys.DownloadQuality); got != "smaller" {
		t.Fatalf("quality %q after Right, want smaller", got)
	}
	if e.s.Handle(press(input.Right), t0) {
		t.Fatal("Right past the last option did something")
	}
	e.s.Handle(press(input.A), t0)
	if e.s.open >= 0 {
		t.Fatal("A didn't fold the options")
	}
}

func TestSleepTimer(t *testing.T) {
	e := newEnv(true)
	e.s.Handle(press(input.Down), t0)
	e.s.Handle(press(input.Down), t0)
	e.s.Handle(press(input.A), t0)
	if e.s.open != rowSleep {
		t.Fatalf("A on row %d expanded %d", e.s.focus, e.s.open)
	}
	e.s.Handle(press(input.Right), t0)
	e.s.Handle(press(input.Right), t0)
	if e.p.sleep.Mode != player.Sleep30 {
		t.Fatalf("two Rights set the timer to %v, want 30 min", e.p.sleep.Mode)
	}
	uitest.Golden(t, "settings-sleep", render(t, e, t0.Add(anim.Focus)))
	if due, ok := e.s.Due(t0); !ok || due != t0.Add(time.Second) {
		t.Fatalf("Due = %v, %v while a timer runs", due.Sub(t0), ok)
	}
	if !e.s.Back() || e.s.open >= 0 {
		t.Fatal("Back didn't fold the options first")
	}
	if e.s.Back() {
		t.Fatal("Back on the main page didn't leave it to close Settings")
	}
}

func TestVolumeLevelling(t *testing.T) {
	e := newEnv(true)
	for range 3 {
		e.s.Handle(press(input.Down), t0)
	}
	e.s.Handle(press(input.A), t0)
	if e.prefs.Bool(keys.VolumeLevelling) || len(e.p.levelling) != 1 || e.p.levelling[0] {
		t.Fatalf("A on Volume levelling: setting %v, player told %v", e.prefs.Bool(keys.VolumeLevelling), e.p.levelling)
	}
	if e.s.Handle(press(input.Down), t0) {
		t.Fatal("Down past the last row moved")
	}
}

// readCounter counts reads of the settings.
type readCounter struct {
	prefs
	reads int
}

func (r *readCounter) String(k string) string { r.reads++; return r.prefs.String(k) }
func (r *readCounter) Bool(k string) bool     { r.reads++; return r.prefs.Bool(k) }

// The settings are read when the screen opens and when it changes one, not
// every frame.
func TestSettingsAreReadOnChange(t *testing.T) {
	r := &readCounter{prefs: prefs{}}
	s := New(Options{Prefs: r, Player: &fakePlayer{}, Version: "0.4.0", Device: "TrimUI Brick", Toast: func(string) {}})
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.BeginFrame(t0)
	s.Draw(u)
	reads := r.reads
	s.Draw(u)
	s.Draw(u)
	if r.reads != reads {
		t.Fatalf("two frames read the settings %d times", r.reads-reads)
	}
	s.Handle(press(input.Down), t0) // the sleep timer
	s.Handle(press(input.Down), t0) // volume levelling
	s.Handle(press(input.A), t0)
	s.Draw(u)
	if v := s.rows[rowLevelling].Value; v != "Off" {
		t.Fatalf("switched off, volume levelling says %q", v)
	}
}

func TestNoStorageRowWithoutSpace(t *testing.T) {
	e := newEnv(false)
	e.s.Handle(press(input.Down), t0)
	if e.s.focus != rowSleep {
		t.Fatalf("Down from Download quality went to row %d, want the sleep timer", e.s.focus)
	}
}

func TestStoragePage(t *testing.T) {
	e := newEnv(true)
	e.s.Handle(press(input.Down), t0)
	e.s.Handle(press(input.A), t0)
	if e.s.storage == nil || e.space.measures != 1 {
		t.Fatalf("A on Storage: page %v, %d measures", e.s.storage != nil, e.space.measures)
	}
	render(t, e, t0)
	if e.s.storage.shown[0] != "Measuring…" {
		t.Fatalf("sizes before they are measured: %q", e.s.storage.shown)
	}
	e.space.sizes, e.space.measured = space.Sizes{Art: 42 << 20, Lyrics: 1200 << 10, Parts: 18 << 20, Downloader: 92 << 20}, true
	uitest.Golden(t, "settings-storage", render(t, e, t0))

	e.s.Handle(press(input.A), t0)
	if e.s.storage.dialog == nil || !e.s.Claims(input.Menu) {
		t.Fatal("A didn't open the dialog")
	}
	uitest.Golden(t, "settings-clear", render(t, e, t0.Add(anim.Dialog)))
	e.s.Handle(press(input.A), t0.Add(anim.Dialog))
	if e.space.clears != 1 {
		t.Fatal("Clear didn't clear the caches")
	}
	e.space.freed, e.space.cleared = 48<<20, true
	render(t, e, t0.Add(3*anim.Dialog))
	if len(e.toasts) != 1 || e.toasts[0] != "Freed 48.0 MB" {
		t.Fatalf("toasts %q", e.toasts)
	}
	if !e.s.Back() || e.s.storage != nil {
		t.Fatal("Back didn't leave the Storage page")
	}
}

// A clear that ended after the page was left isn't announced when it opens
// again.
func TestAnOldClearIsNotAnnounced(t *testing.T) {
	e := newEnv(true)
	e.space.freed, e.space.cleared = 48<<20, true
	e.s.Handle(press(input.Down), t0)
	e.s.Handle(press(input.A), t0)
	render(t, e, t0)
	if len(e.toasts) != 0 {
		t.Fatalf("toasts %q on opening the page", e.toasts)
	}
}

func TestCancellingTheClear(t *testing.T) {
	e := newEnv(true)
	e.s.Handle(press(input.Down), t0)
	e.s.Handle(press(input.A), t0)
	e.s.Handle(press(input.A), t0)
	e.s.Handle(press(input.Right), t0)
	e.s.Handle(press(input.A), t0)
	if e.space.clears != 0 {
		t.Fatal("Cancel cleared the caches")
	}
	e.s.Handle(press(input.A), t0)
	if !e.s.Back() || e.s.storage == nil {
		t.Fatal("Back with the dialog open left the page")
	}
}

func TestOpenedStartsAfresh(t *testing.T) {
	e := newEnv(true)
	e.s.Handle(press(input.Down), t0)
	e.s.Handle(press(input.A), t0)
	e.s.Opened()
	if e.s.storage != nil || e.s.focus != rowQuality || e.s.open >= 0 {
		t.Fatal("Opened kept the last visit's state")
	}
}

type fakeDownloader struct {
	d       download.Downloader
	updates int
}

func (f *fakeDownloader) Downloader() download.Downloader { return f.d }
func (f *fakeDownloader) UpdateDownloader()               { f.updates++ }

type fakeReleases struct {
	r      release.Release
	checks int
}

func (f *fakeReleases) Check() { f.checks++ }
func (f *fakeReleases) Latest() (release.Release, bool) {
	return f.r, f.r.Tag != ""
}

type fakeUpdater struct {
	st     selfupdate.Status
	staged []string
}

func (f *fakeUpdater) Stage(r release.Release)   { f.staged = append(f.staged, r.Version()) }
func (f *fakeUpdater) Status() selfupdate.Status { return f.st }

// cardsEnv is Settings on the Brick with both cards: yt-dlp installed 18 days
// ago with a newer release out, and Boombrick 0.5.0 out.
type cardsEnv struct {
	*env
	dl        *fakeDownloader
	tool, app *fakeReleases
	up        *fakeUpdater
	quits     int
}

func newCardsEnv() *cardsEnv {
	e := &cardsEnv{env: &env{prefs: prefs{}, p: &fakePlayer{}, space: &fakeSpace{}},
		dl:   &fakeDownloader{d: download.Downloader{Version: "2026.09.27", Installed: t0.Add(-18 * 24 * time.Hour)}},
		tool: &fakeReleases{r: release.Release{Tag: "2026.10.11"}},
		app:  &fakeReleases{r: release.Release{Tag: "v0.5.0"}},
		up:   &fakeUpdater{}}
	e.s = New(Options{Prefs: e.prefs, Player: e.p, Space: e.space, Version: "0.4.0", Device: "TrimUI Brick",
		Card:       func() (int64, int64, int64) { return 595 * gib / 10, 386 * gib / 10, 142 * gib / 10 },
		Downloader: e.dl, ToolReleases: e.tool, Releases: e.app, Updater: e.up, Quit: func() { e.quits++ }})
	return e
}

func TestCards(t *testing.T) {
	e := newCardsEnv()
	if e.tool.checks != 0 || e.app.checks != 0 {
		t.Fatal("making the screen asked for releases")
	}
	e.s.Opened()
	if e.tool.checks != 1 || e.app.checks != 1 {
		t.Fatalf("opening asked %d and %d times, want once each", e.tool.checks, e.app.checks)
	}
	render(t, e.env, t0)
	want := []struct{ got, want string }{
		{e.s.tool.line, "Version 2026.09.27 · updated 18 days ago"},
		{e.s.tool.note, "2026.10.11 is available"},
		{e.s.tool.pill, "Update downloader"},
		{e.s.app.line, "Version 0.4.0"},
		{e.s.app.note, "0.5.0 is available"},
		{e.s.app.pill, "Update Boombrick"},
	}
	for _, w := range want {
		if w.got != w.want {
			t.Errorf("%q, want %q", w.got, w.want)
		}
	}
	for range 4 {
		e.s.Handle(press(input.Down), t0)
	}
	if e.s.focus != pillTool {
		t.Fatalf("four Downs went to %d, want the Downloader card's pill", e.s.focus)
	}
	e.s.Handle(press(input.A), t0)
	if e.dl.updates != 1 {
		t.Fatal("A on Update downloader didn't ask")
	}
	e.dl.d.Updating, e.dl.d.Progress = true, 0.42
	e.s.Handle(press(input.Down), t0)
	// The first frame starts the scroll that brings the focused card in.
	render(t, e.env, t0)
	uitest.Golden(t, "settings-cards", render(t, e.env, t0.Add(2*anim.Focus)))
	if e.s.tool.note != "Updating… 42 %" {
		t.Errorf("while it updates: %q", e.s.tool.note)
	}
	e.s.Handle(press(input.A), t0)
	if len(e.up.staged) != 1 || e.up.staged[0] != "0.5.0" {
		t.Fatalf("A on Update Boombrick staged %q", e.up.staged)
	}
	if e.s.Handle(press(input.Down), t0) {
		t.Fatal("Down past the last pill moved")
	}
}

func TestUpdateReady(t *testing.T) {
	e := newCardsEnv()
	for range 5 {
		e.s.Handle(press(input.Down), t0)
	}
	e.up.st = selfupdate.Status{Staging: true, Progress: 0.42}
	render(t, e.env, t0)
	if e.s.app.note != "Downloading… 42 %" || e.s.dialog != nil {
		t.Fatalf("while it downloads: %q, dialog %v", e.s.app.note, e.s.dialog != nil)
	}
	e.up.st = selfupdate.Status{Staged: "0.5.0"}
	render(t, e.env, t0)
	if e.s.dialog == nil || !e.s.Claims(input.Menu) {
		t.Fatal("no dialog when the update was staged")
	}
	if e.s.app.pill != "" || e.s.focus == pillApp {
		t.Fatalf("the pill stayed: %q, focus %d", e.s.app.pill, e.s.focus)
	}
	uitest.Golden(t, "settings-update-ready", render(t, e.env, t0.Add(anim.Dialog)))
	e.s.Handle(press(input.A), t0.Add(anim.Dialog))
	if e.quits != 1 {
		t.Fatal("Quit now didn't quit")
	}
}

func TestUpdateReadyLater(t *testing.T) {
	e := newCardsEnv()
	e.up.st = selfupdate.Status{Staging: true}
	render(t, e.env, t0)
	e.up.st = selfupdate.Status{Staged: "0.5.0"}
	render(t, e.env, t0)
	if !e.s.Back() || e.quits != 0 {
		t.Fatal("Back didn't close the dialog as Later")
	}
	render(t, e.env, time.Now().Add(time.Second)) // Back closes it on the clock
	if e.s.dialog != nil {
		t.Fatal("the dialog stayed")
	}
	if e.s.app.note != "0.5.0 installs the next time you open Boombrick" {
		t.Fatalf("staged: %q", e.s.app.note)
	}
}

func TestCardLines(t *testing.T) {
	now := t0.Add(400 * 24 * time.Hour)
	tools := []struct {
		d                download.Downloader
		latest           string
		line, note, pill string
	}{
		{download.Downloader{}, "2026.10.11", "Not installed · installs with the first download", "", "Install downloader"},
		{download.Downloader{Version: "2026.10.11", Installed: now}, "2026.10.11", "Version 2026.10.11 · updated today", "", "Update downloader"},
		{download.Downloader{Installed: now.Add(-30 * time.Hour)}, "", "Installed · updated yesterday", "", "Update downloader"},
		{download.Downloader{Version: "2026.09.27", Installed: now, Failed: true}, "2026.10.11", "Version 2026.09.27 · updated today", "Couldn’t update the downloader", "Update downloader"},
	}
	for _, c := range tools {
		var k card
		k.setTool(c.d, c.latest, now)
		if k.line != c.line || k.note != c.note || k.pill != c.pill {
			t.Errorf("%+v: %q, %q, %q", c.d, k.line, k.note, k.pill)
		}
	}
	apps := []struct {
		version, latest  string
		st               selfupdate.Status
		canUpdate        bool
		line, note, pill string
	}{
		{"dev", "", selfupdate.Status{}, false, "Development build", "", ""},
		{"0.5.0", "0.5.0", selfupdate.Status{}, true, "Version 0.5.0 · up to date", "", ""},
		{"0.4.0", "0.5.0", selfupdate.Status{}, false, "Version 0.4.0", "0.5.0 is available", ""},
		{"0.4.0", "0.5.0", selfupdate.Status{Failed: true}, true, "Version 0.4.0", "Couldn’t download the update", "Update Boombrick"},
		{"0.4.0", "0.5.0", selfupdate.Status{Staged: "0.5.0"}, true, "Version 0.4.0", "0.5.0 installs the next time you open Boombrick", ""},
		// A beta isn't offered other betas, so it doesn't claim to be up to
		// date; the next full release is offered as to any version.
		{"0.2.0-beta.1", "0.1.1", selfupdate.Status{}, true, "Version 0.2.0-beta.1 · beta", "", ""},
		{"0.2.0-beta.1", "0.2.0", selfupdate.Status{}, true, "Version 0.2.0-beta.1 · beta", "0.2.0 is available", "Update Boombrick"},
	}
	for _, c := range apps {
		var k card
		k.setApp(c.version, c.latest, c.st, c.canUpdate)
		if k.line != c.line || k.note != c.note || k.pill != c.pill {
			t.Errorf("%s against %s, %+v: %q, %q, %q", c.version, c.latest, c.st, k.line, k.note, k.pill)
		}
	}
}

type fakeAccount struct {
	st      spotify.Status
	logouts int
}

func (a *fakeAccount) Status() (spotify.Status, error) { return a.st, nil }
func (a *fakeAccount) Logout()                         { a.logouts++ }

// With importing, Prefer downloaded copies follows Download quality, and A
// switches it.
func TestPreferDownloadedCopies(t *testing.T) {
	e := &env{prefs: prefs{}, p: &fakePlayer{}, space: &fakeSpace{}}
	e.s = New(Options{Prefs: e.prefs, Player: e.p, Space: e.space, Version: "0.4.0", Device: "TrimUI Brick",
		Toast: func(string) {}, Copies: true})
	e.s.Handle(press(input.Down), t0)
	if e.s.focus != rowPrefer {
		t.Fatalf("Down from Download quality went to %d", e.s.focus)
	}
	render(t, e, t0)
	if e.s.rows[rowPrefer].Value != "On" {
		t.Fatalf("the row says %q", e.s.rows[rowPrefer].Value)
	}
	e.s.Handle(press(input.A), t0)
	if e.prefs.Bool(keys.PreferDownloaded) {
		t.Fatal("A didn't switch it off")
	}
	uitest.Golden(t, "settings-prefer", render(t, e, t0.Add(anim.Focus)))
}

// With Spotify, Streaming quality comes first and the account has a card of
// its own, with Sign out.
func TestSpotifySettings(t *testing.T) {
	acct := &fakeAccount{st: spotify.Status{State: spotify.Ready, User: "bmfjclzv7pstc3zzlq0nteyni", Name: "Ujjwal", Premium: true}}
	e := &env{prefs: prefs{}, p: &fakePlayer{}, space: &fakeSpace{}}
	e.s = New(Options{Prefs: e.prefs, Player: e.p, Space: e.space, Version: "0.4.0", Device: "TrimUI Brick",
		Card:    func() (int64, int64, int64) { return 595 * gib / 10, 386 * gib / 10, 142 * gib / 10 },
		Toast:   func(m string) { e.toasts = append(e.toasts, m) },
		Spotify: acct})
	if e.s.focus != rowStream {
		t.Fatalf("focus starts on %d", e.s.focus)
	}
	e.s.Handle(press(input.A), t0)
	uitest.Golden(t, "settings-streaming", render(t, e, t0.Add(anim.Focus)))
	e.s.Handle(press(input.Right), t0)
	if got := e.prefs.String(keys.StreamingKbps); got != "320" {
		t.Fatalf("Right chose %q", got)
	}
	e.s.Handle(press(input.A), t0)
	render(t, e, t0)
	if e.s.rows[rowStream].Value != "320 kbps" || e.s.account.line != "Ujjwal · Premium · signed in" {
		t.Fatalf("row %q, card %q", e.s.rows[rowStream].Value, e.s.account.line)
	}
	for e.s.focus != pillAccount {
		if !e.s.Handle(press(input.Down), t0) {
			t.Fatalf("stopped on %d before Sign out", e.s.focus)
		}
	}
	render(t, e, t0)
	uitest.Golden(t, "settings-account", render(t, e, t0.Add(2*anim.Focus)))
	e.s.Handle(press(input.A), t0)
	if acct.logouts != 1 || len(e.toasts) != 1 || e.toasts[0] != "Signed out of Spotify" {
		t.Fatalf("%d sign-outs, toasts %v", acct.logouts, e.toasts)
	}
	acct.st = spotify.Status{State: spotify.SignedOut}
	render(t, e, t0)
	if e.s.account.pill != "" || e.s.focus == pillAccount {
		t.Fatalf("signed out: pill %q, focus %d", e.s.account.pill, e.s.focus)
	}
}

// The scroll comes to rest where focus needs it, drawn frame by frame as on
// the Brick. Here the Downloader card's pill is the last item, and it and the
// footer under it are taller than the list: its top stays in view, and the
// scroll doesn't swing between the two ends.
func TestScrollComesToRest(t *testing.T) {
	e := &env{prefs: prefs{}, p: &fakePlayer{}, space: &fakeSpace{}}
	e.s = New(Options{Prefs: e.prefs, Player: e.p, Space: e.space, Version: "0.4.0", Device: "TrimUI Brick",
		Card:       func() (int64, int64, int64) { return 1165 * gib / 10, 629 * gib / 10, 10 * gib },
		Toast:      func(string) {},
		Downloader: &fakeDownloader{d: download.Downloader{Updating: true, Progress: 0.22}},
		Releases:   &fakeReleases{r: release.Release{Tag: "v0.4.0"}},
		Updater:    &fakeUpdater{},
		Spotify:    &fakeAccount{st: spotify.Status{State: spotify.SignedOut}},
		Copies:     true})
	e.s.Opened()
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Status = fixture.Status()
	now := t0
	draw := func() {
		u.BeginFrame(now)
		e.s.Draw(u)
	}
	// down draws a second of frames after Down, and reports where the scroll
	// came to rest.
	down := func() float64 {
		t.Helper()
		if !e.s.Handle(press(input.Down), now) {
			t.Fatalf("Down from %d didn't move", e.s.focus)
		}
		for range 60 {
			draw()
			now = now.Add(time.Second / 60)
		}
		if e.s.Animating(now) {
			t.Fatalf("on %d the scroll is still moving, at %.1f for %.1f", e.s.focus, e.s.scroll.At(now), e.s.scroll.Target())
		}
		return e.s.scroll.At(now)
	}
	draw()
	for e.s.focus != rowLevelling {
		down()
	}
	view := float64(screens.ContentBot - listTop(u))
	if got, want := e.s.scroll.At(now), 6*widgets.SettingHeight-view; got != want {
		t.Errorf("Volume levelling scrolled to %.1f, want %.1f to bring its bottom in", got, want)
	}
	if got, want := down(), float64(6*widgets.SettingHeight+16); e.s.focus != pillTool || got != want {
		t.Errorf("on %d the scroll rests at %.1f, want %.1f for the Downloader card's top", e.s.focus, got, want)
	}
}

package app

import (
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/veandco/go-sdl2/sdl"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
)

// fakeMenu records what reaches it while open.
type fakeMenu struct {
	page
	open bool
	due  time.Time // when its sleep timer next changes; zero for none
}

func (m *fakeMenu) Open(time.Time)                  { m.open = true }
func (m *fakeMenu) IsOpen() bool                    { return m.open }
func (m *fakeMenu) Visible(time.Time) bool          { return m.open }
func (m *fakeMenu) Due(time.Time) (time.Time, bool) { return m.due, !m.due.IsZero() }

func (m *fakeMenu) Handle(e input.Event, now time.Time) bool {
	m.got = append(m.got, e.Button)
	if e.Button == input.B {
		m.open = false
	}
	return true
}

// menuClaimer keeps Menu for itself, as a screen with a dialog open does.
type menuClaimer struct{ page }

func (c *menuClaimer) Claims(b input.Button) bool { return b == input.Menu }

func TestMenuOpensTheQuickMenu(t *testing.T) {
	s, tabs, np := testShell()
	m := &fakeMenu{}
	s.menu = m
	if s.handle(press(input.Menu), t0) || m.open {
		t.Fatal("the quick menu opened before Menu was let go")
	}
	if !s.handle(release(input.Menu), t0) || !m.open {
		t.Fatal("letting Menu go didn't open the quick menu")
	}
	for _, b := range []input.Button{input.L1, input.Start, input.A, input.B} {
		s.handle(press(b), t0)
	}
	if !slices.Equal(m.got, []input.Button{input.L1, input.Start, input.A, input.B}) || len(tabs[0].got) > 0 || s.open >= 0 || s.tab != 0 {
		t.Fatalf("with the menu open: menu got %v, tab %v, overlay %d, tab %d", m.got, tabs[0].got, s.open, s.tab)
	}
	// Over an overlay too.
	s.handle(press(input.Start), t0)
	s.handle(press(input.Menu), t0)
	s.handle(release(input.Menu), t0)
	if !m.open || s.overlay() != np {
		t.Fatal("Menu over Now Playing didn't open the menu over it")
	}
}

// NextUI sets the brightness with Menu held and the volume buttons: that
// chord doesn't open the quick menu.
func TestAMenuChordDoesNotOpenTheMenu(t *testing.T) {
	s, _, _ := testShell()
	m := &fakeMenu{}
	s.menu = m
	for _, e := range []input.Event{press(input.Menu), press(input.VolUp), release(input.VolUp), release(input.Menu)} {
		s.handle(e, t0)
	}
	if m.open {
		t.Fatal("Menu with volume up opened the quick menu")
	}
	s.handle(press(input.Menu), t0)
	s.handle(release(input.Menu), t0)
	if !m.open {
		t.Fatal("Menu alone, after a chord, didn't open it")
	}
}

func TestAScreenCanKeepMenu(t *testing.T) {
	tab := &menuClaimer{page{name: "downloads"}}
	s := newShell(tab)
	m := &fakeMenu{}
	s.menu = m
	s.handle(press(input.Menu), t0)
	if m.open || !slices.Equal(tab.got, []input.Button{input.Menu}) {
		t.Fatal("Menu went past the screen keeping it")
	}
	s.handle(release(input.Menu), t0)
	if m.open {
		t.Fatal("letting go of Menu kept by the screen opened the quick menu")
	}
}

// An overlay may keep a button, as Settings keeps Start with its dialog open.
type startKeeper struct{ page }

func (k *startKeeper) Claims(b input.Button) bool { return b == input.Start }

func TestAnOverlayCanKeepAButton(t *testing.T) {
	s := newShell(&page{name: "home"})
	keeper := &startKeeper{page{name: "settings"}}
	s.overlays = []overlay{{button: input.Select, screen: keeper}, {button: input.Start, screen: &page{name: "now playing"}}}
	s.openOverlay(0, t0)
	s.handle(press(input.Start), t0)
	if s.open != 0 || !slices.Equal(keeper.got, []input.Button{input.Start}) {
		t.Fatal("Start left an overlay that keeps it")
	}
}

// tickingPage shows a time that moves on by itself.
type tickingPage struct {
	page
	due time.Time
}

func (p *tickingPage) Due(time.Time) (time.Time, bool) { return p.due, true }

func TestDueIsTheSoonestChange(t *testing.T) {
	tab := &tickingPage{page: page{name: "settings"}, due: t0.Add(800 * time.Millisecond)}
	s := newShell(tab)
	m := &fakeMenu{due: t0.Add(300 * time.Millisecond)}
	s.menu = m
	if d, ok := s.due(t0); !ok || d != tab.due {
		t.Fatalf("due %v, %v with the menu closed; want the screen's", d.Sub(t0), ok)
	}
	m.open = true
	if d, ok := s.due(t0); !ok || d != m.due {
		t.Fatalf("due %v, %v with the menu open; want the menu's", d.Sub(t0), ok)
	}
	a := &App{shell: s, repeat: input.NewRepeater(), fade: newAccentFade(theme.Fallback()), ui: &ui.Context{}}
	if got := a.timeout(t0, t0.Add(100*time.Millisecond)); got != 200*time.Millisecond {
		t.Fatalf("sleep %v, want until the menu's timer changes", got)
	}
}

// lamp is a backlight.
type lamp struct {
	off       bool
	fail      bool
	offs, ons int
}

func (l *lamp) Off() error {
	if l.fail {
		return errors.New("no display driver")
	}
	l.off, l.offs = true, l.offs+1
	return nil
}

func (l *lamp) On() error { l.off, l.ons = false, l.ons+1; return nil }

func darkApp(light Backlight) (*App, *page) {
	tab := &page{name: "home"}
	a := &App{shell: newShell(tab), repeat: input.NewRepeater(),
		fade: newAccentFade(theme.Fallback()), ui: &ui.Context{},
		opts: Options{Backlight: light, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	return a, tab
}

func release(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Release} }

func TestPowerTurnsTheScreenOffAndOn(t *testing.T) {
	l := &lamp{}
	a, tab := darkApp(l)
	a.dispatch(press(input.Power))
	a.dispatch(release(input.Power))
	if !a.dark || !l.off {
		t.Fatalf("a short press of Power: dark %v, light off %v", a.dark, l.off)
	}
	a.dispatch(press(input.A))
	a.dispatch(press(input.Menu))
	if len(tab.got) > 0 {
		t.Fatalf("buttons reached the screen while it was off: %v", tab.got)
	}
	a.dispatch(press(input.Power))
	a.dispatch(release(input.Power))
	if a.dark || l.off || l.ons != 1 {
		t.Fatalf("a second press: dark %v, light off %v, %d turned on", a.dark, l.off, l.ons)
	}
	a.dispatch(press(input.A))
	if !slices.Equal(tab.got, []input.Button{input.A}) {
		t.Fatal("buttons don't reach the screen once it is on again")
	}
}

// remote records what the buttons heard with the screen off ask of the music.
type remote struct{ got []string }

func (r *remote) Prev()                  { r.got = append(r.got, "prev") }
func (r *remote) SeekBy(d time.Duration) { r.got = append(r.got, "seek "+d.String()) }
func (r *remote) Next()                  { r.got = append(r.got, "next") }
func (r *remote) Toggle()                { r.got = append(r.got, "toggle") }

// With the screen off, L1 and R1 skip back and on and A plays or pauses; held,
// they don't repeat, and the rest are ignored.
func TestTheMusicsButtonsWithTheScreenOff(t *testing.T) {
	a, tab := darkApp(&lamp{})
	r := &remote{}
	a.music = r
	a.setDark(true)
	for _, e := range []input.Event{press(input.R1), release(input.R1), press(input.L1), press(input.A),
		{Button: input.R1, Kind: input.Repeat}, press(input.B), press(input.Start), press(input.Down)} {
		a.dispatch(e)
	}
	if !slices.Equal(r.got, []string{"next", "prev", "toggle"}) {
		t.Fatalf("the music was asked for %v", r.got)
	}
	if len(tab.got) > 0 || !a.dark {
		t.Fatalf("with the screen off the screen heard %v; dark %v", tab.got, a.dark)
	}
}

// With the backlight off nothing is drawn, however often something changes;
// the test's context has no canvas, so a frame would panic.
func TestNothingIsDrawnWithTheLightOff(t *testing.T) {
	a, _ := darkApp(&lamp{})
	a.setDark(true)
	for range 3 {
		a.dirty = true
		a.darkFrame()
	}
	if a.dirty {
		t.Fatal("the change is still waiting to be drawn")
	}
}

// A key Boombrick has no use for, as the Brick's volume keys may be, still
// makes a chord of Menu, so NextUI's brightness doesn't open the quick menu.
func TestAnUnusedKeyMakesAChordOfMenu(t *testing.T) {
	a, _ := darkApp(nil)
	m := &fakeMenu{}
	a.shell.menu = m
	a.dispatch(press(input.Menu))
	a.event(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Scancode: 128}})
	a.dispatch(release(input.Menu))
	if m.open {
		t.Fatal("Menu with a key Boombrick doesn't use opened the quick menu")
	}
}

// lateClaimer keeps Menu once its dialog opens.
type lateClaimer struct {
	page
	dialog bool
}

func (c *lateClaimer) Claims(b input.Button) bool { return c.dialog && b == input.Menu }

// A dialog that opens while Menu is down keeps the quick menu from opening
// over it.
func TestADialogOpenedWhileMenuIsDownKeepsIt(t *testing.T) {
	tab := &lateClaimer{page: page{name: "settings"}}
	s := newShell(tab)
	m := &fakeMenu{}
	s.menu = m
	s.handle(press(input.Menu), t0)
	tab.dialog = true
	s.handle(release(input.Menu), t0)
	if m.open {
		t.Fatal("the quick menu opened over the dialog")
	}
}

func TestScreenOffWithoutABacklight(t *testing.T) {
	a, _ := darkApp(&lamp{fail: true})
	a.setDark(true)
	if !a.dark || a.lightOff || !a.dirty {
		t.Fatalf("dark %v, light off %v, dirty %v; want dark, a black frame to draw", a.dark, a.lightOff, a.dirty)
	}
	a.setDark(false) // nothing to turn on
}

func TestQuittingTurnsTheLightBackOn(t *testing.T) {
	l := &lamp{}
	a, _ := darkApp(l)
	a.setDark(true)
	a.lightUp()
	if l.off || l.ons != 1 {
		t.Fatal("the light stays off after quitting with the screen off")
	}
	a.lightUp()
	if l.ons != 1 {
		t.Fatal("the light was turned on twice")
	}
}

func TestHoldingPowerPowersOff(t *testing.T) {
	a, _ := darkApp(nil)
	asked := 0
	a.opts.PowerOff = func() error { asked++; return nil }
	a.dispatch(press(input.Power))
	// Pressed at t0, well before a minute ends: in a minute's last second the
	// clock's tick comes before the hold's end, and the sleep is shorter.
	a.powerAt = t0
	if a.powerHeld(a.powerAt.Add(powerHold / 2)) {
		t.Fatal("powered off half a hold in")
	}
	if got := a.timeout(a.powerAt, a.powerAt); got != powerHold {
		t.Fatalf("sleep %v while Power is held, want %v", got, powerHold)
	}
	if !a.powerHeld(a.powerAt.Add(powerHold)) || !a.quit || asked != 1 {
		t.Fatalf("held for %v: quit %v, NextUI asked %d times", powerHold, a.quit, asked)
	}
	a.dispatch(release(input.Power))
	if a.dark {
		t.Fatal("letting go after the hold turned the screen off")
	}
}

// Where the firmware keeps Power, as Knulli suspends with it and shuts down
// when it is held, Boombrick neither turns the screen off with it nor
// quits when it is held.
func TestFirmwarePowerIsLeftAlone(t *testing.T) {
	l := &lamp{}
	a, tab := darkApp(l)
	a.opts.FirmwarePower = true
	asked := 0
	a.opts.PowerOff = func() error { asked++; return nil }
	a.dispatch(press(input.Power))
	if a.powerHeld(time.Now().Add(powerHold)) || a.quit || asked > 0 {
		t.Fatal("holding Power quit")
	}
	a.dispatch(release(input.Power))
	if a.dark || l.off || len(tab.got) > 0 {
		t.Fatalf("Power: dark %v, light off %v, the screen heard %v", a.dark, l.off, tab.got)
	}
}

// With Power the firmware's, a screen turned off from the quick menu comes
// back on with any button the music doesn't use, which goes no further.
func TestAnyButtonTurnsTheScreenOnWhenTheFirmwareHasPower(t *testing.T) {
	l := &lamp{}
	a, tab := darkApp(l)
	a.opts.FirmwarePower = true
	r := &remote{}
	a.music = r
	a.setDark(true)
	a.dispatch(press(input.A))
	a.dispatch(release(input.A))
	if !a.dark || !slices.Equal(r.got, []string{"toggle"}) {
		t.Fatalf("A: dark %v, the music was asked for %v", a.dark, r.got)
	}
	a.dispatch(press(input.B))
	if a.dark || l.off || len(tab.got) > 0 {
		t.Fatalf("B: dark %v, light off %v, the screen heard %v", a.dark, l.off, tab.got)
	}
}

func TestNothingTicksWhileDark(t *testing.T) {
	a, _ := darkApp(&lamp{})
	a.last.Playing = true
	a.setDark(true)
	if got := a.timeout(t0, t0); got < 10*time.Second {
		t.Fatalf("sleep %v with the screen off and music playing, want until the clock's next minute", got)
	}
}

// When the background player can't take the music, the app says so and
// carries on.
func TestPlayInBackgroundFails(t *testing.T) {
	s, _, _ := testShell()
	a := &App{shell: s, closed: true, opts: Options{Log: slog.New(slog.DiscardHandler),
		Background: func() error { return errors.New("no answer") }}}
	a.playInBackground()
	for end := time.Now().Add(2 * time.Second); !a.handFailed.Load(); time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the failure never came")
		}
	}
	a.backgroundNews()
	if s.toast != "Couldn’t play in the background" {
		t.Fatalf("toast %q", s.toast)
	}
}

// While the music is handed over, a second pick does nothing and the buttons
// reach no screen, though their press and release are still counted; when it
// fails, input comes back.
func TestInputWaitsWhileHandingOver(t *testing.T) {
	a, tab := darkApp(nil)
	a.closed = true
	unblock := make(chan struct{})
	var entered atomic.Int32
	a.opts.Background = func() error {
		entered.Add(1)
		<-unblock
		return errors.New("no answer")
	}
	a.playInBackground()
	a.playInBackground()
	for end := time.Now().Add(2 * time.Second); entered.Load() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the hand-over never began")
		}
	}
	time.Sleep(50 * time.Millisecond) // time for a second hand-over to begin, were it let
	if n := entered.Load(); n != 1 {
		t.Fatalf("Background entered %d times while handing over, want 1", n)
	}
	a.dispatch(press(input.Down))
	if _, held := a.repeat.Next(); !held || len(tab.got) > 0 {
		t.Fatalf("during the hand-over: Down held %v, the screen heard %v", held, tab.got)
	}
	a.dispatch(release(input.Down))
	if _, held := a.repeat.Next(); held {
		t.Fatal("a button let go during the hand-over is stuck")
	}
	close(unblock)
	for end := time.Now().Add(2 * time.Second); !a.handFailed.Load(); time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the failure never came")
		}
	}
	time.Sleep(50 * time.Millisecond)
	if n := entered.Load(); n != 1 {
		t.Fatalf("Background entered %d times in all, want 1", n)
	}
	if a.handing.Load() {
		t.Fatal("still handing over after the failure")
	}
	a.backgroundNews()
	if a.shell.toast != "Couldn’t play in the background" {
		t.Fatalf("toast %q", a.shell.toast)
	}
	a.dispatch(press(input.A))
	if !slices.Equal(tab.got, []input.Button{input.A}) {
		t.Fatalf("input didn't come back: %v", tab.got)
	}
}

func TestVolumeChangedShowsThePill(t *testing.T) {
	a, _ := darkApp(&lamp{})
	a.closed = true // no SDL here to wake
	a.VolumeChanged(0)
	if a.shell.volumeOn {
		t.Fatal("the pill showed before the loop looked")
	}
	a.volumeNews(t0)
	if !a.shell.volumeOn || a.shell.volume != 0 || !a.dirty || a.volume.Load() != 0 {
		t.Fatalf("level 0: on %v, level %d, dirty %v, flag %d", a.shell.volumeOn, a.shell.volume, a.dirty, a.volume.Load())
	}
	a.dirty = false
	a.volumeNews(t0)
	if a.dirty {
		t.Fatal("a volume change was shown twice")
	}
	a.VolumeChanged(17)
	a.volumeNews(t0.Add(time.Second))
	if a.shell.volume != 17 {
		t.Fatalf("level %d, want 17", a.shell.volume)
	}
}

// volumeWatcher records what the app tells it.
type volumeWatcher struct {
	presses int
	paused  []bool
}

func (v *volumeWatcher) Pressed()          { v.presses++ }
func (v *volumeWatcher) Pause(paused bool) { v.paused = append(v.paused, paused) }

// Every button pressed, even one Boombrick doesn't use, has the volume read.
// With the screen off the volume watcher and the status reader wait, and they
// carry on as it comes back on.
func TestTheReadersFollowTheScreen(t *testing.T) {
	a, _ := darkApp(&lamp{})
	v := &volumeWatcher{}
	a.opts.Volume = v
	a.lit = make(chan bool, 1)
	a.event(&sdl.KeyboardEvent{Type: sdl.KEYDOWN, Keysym: sdl.Keysym{Scancode: 128}})
	a.event(&sdl.JoyButtonEvent{Type: sdl.JOYBUTTONDOWN, Button: 14, State: sdl.PRESSED})
	a.event(&sdl.JoyButtonEvent{Type: sdl.JOYBUTTONUP, Button: 14, State: sdl.RELEASED})
	if v.presses != 2 {
		t.Fatalf("the volume watcher heard %d presses, want 2", v.presses)
	}
	a.setDark(true)
	if on := <-a.lit; on || !slices.Equal(v.paused, []bool{true}) {
		t.Fatalf("screen off: lit %v, volume paused %v", on, v.paused)
	}
	a.setDark(false)
	if on := <-a.lit; !on || !slices.Equal(v.paused, []bool{true, false}) {
		t.Fatalf("screen on: lit %v, volume paused %v", on, v.paused)
	}
}

func TestVolumeChangedWithTheScreenOffShowsNothing(t *testing.T) {
	a, _ := darkApp(&lamp{})
	a.closed = true
	a.dark = true
	a.VolumeChanged(9)
	a.volumeNews(t0)
	if a.shell.volumeOn || a.volume.Load() != 0 {
		t.Fatal("the pill showed with the screen off, or the news was kept")
	}
}

func (r *remote) BookSpeed() float64   { return 1 }
func (r *remote) SetBookSpeed(float64) {}

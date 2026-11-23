package quickmenu

import (
	"image"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(0, 0)

// acts records what the menu asked for.
type acts struct {
	sleep                  player.Sleep
	screenOff, settings, q int
	playing                bool
	background             int
	live                   bool
}

func (a *acts) actions() Actions {
	return Actions{
		Sleep:            func() player.Sleep { return a.sleep },
		SetSleep:         func(m player.SleepMode) { a.sleep = player.Sleep{Mode: m} },
		ScreenOff:        func() { a.screenOff++ },
		OpenSettings:     func() { a.settings++ },
		Quit:             func() { a.q++ },
		Playing:          func() bool { return a.playing },
		PlayInBackground: func() { a.background++ },
		Live:             func() bool { return a.live },
	}
}

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

// draw renders the menu open over a tab, as the app shows it.
func draw(t *testing.T, m *Menu) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Status = fixture.Status()
	u.BeginFrame(t0.Add(anim.Dialog))
	widgets.DrawBackground(u, widgets.TabGround)
	widgets.StatusBar(u)
	widgets.Tabs(u, widgets.TabHome)
	widgets.HintBar(u, []widgets.Hint{{Key: "L1/R1", Label: "Tabs"}})
	m.Draw(u)
	return off.Image()
}

func TestTheMenu(t *testing.T) {
	a := &acts{sleep: player.Sleep{Mode: player.Sleep30, At: t0.Add(18*time.Minute + 22*time.Second)}}
	m := New(a.actions())
	m.Open(t0)
	uitest.Golden(t, "quick-menu", draw(t, m))
	if got := m.rows[rowSleep].Value; got != "30 min · 18:22 left" {
		t.Fatalf("sleep row says %q", got)
	}
}

// Play in background shows only while music plays, between Screen off and
// Settings, and hands the music over, closing the menu.
func TestPlayInBackground(t *testing.T) {
	a := &acts{}
	m := New(a.actions())
	m.Open(t0)
	for range 4 {
		m.Handle(press(input.Down), t0)
	}
	if m.focus != rowQuit {
		t.Fatalf("with no music playing, the last row is %d", m.focus)
	}
	a.playing = true
	hand := a.actions()
	hand.PlayInBackground = nil // Options.Background unset: the row stays hidden
	nb := New(hand)
	nb.Open(t0)
	for range 4 {
		nb.Handle(press(input.Down), t0)
	}
	if nb.focus != rowQuit {
		t.Fatalf("with no hand-over to call, the last row is %d", nb.focus)
	}
	m.Open(t0)
	m.Handle(press(input.Down), t0)
	m.Handle(press(input.Down), t0)
	if m.focus != rowBackground {
		t.Fatalf("two Downs reached row %d, want Play in background", m.focus)
	}
	img := draw(t, m)
	m.Handle(press(input.A), t0)
	if a.background != 1 || m.IsOpen() {
		t.Fatalf("A: %d hand-overs, open %v", a.background, m.IsOpen())
	}
	uitest.Golden(t, "quick-menu-background", img)
}

func TestMovingAndActing(t *testing.T) {
	a := &acts{}
	m := New(a.actions())
	m.Open(t0)
	if m.Handle(press(input.Up), t0) {
		t.Fatal("Up from the first row moved")
	}
	m.Handle(press(input.Right), t0)
	m.Handle(press(input.Right), t0)
	if a.sleep.Mode != player.Sleep30 {
		t.Fatalf("two Rights set the timer to %v, want 30 min", a.sleep.Mode)
	}
	m.Handle(press(input.Left), t0)
	m.Handle(press(input.A), t0)
	if a.sleep.Mode != player.Sleep30 {
		t.Fatalf("Left then A set the timer to %v, want 30 min", a.sleep.Mode)
	}

	m.Handle(press(input.Down), t0)
	if m.Handle(press(input.Right), t0) {
		t.Fatal("Right on Screen off did something")
	}
	m.Handle(press(input.A), t0)
	if a.screenOff != 1 || m.IsOpen() {
		t.Fatalf("A on Screen off: %d calls, open %v", a.screenOff, m.IsOpen())
	}

	m.Open(t0)
	m.Handle(press(input.Down), t0)
	m.Handle(press(input.Down), t0)
	m.Handle(press(input.A), t0)
	if a.settings != 1 || m.IsOpen() {
		t.Fatalf("A on Settings: %d calls, open %v", a.settings, m.IsOpen())
	}

	m.Open(t0)
	for range 3 {
		m.Handle(press(input.Down), t0)
	}
	m.Handle(press(input.A), t0)
	if a.q != 1 {
		t.Fatal("A on Quit didn't quit")
	}
}

func TestClosing(t *testing.T) {
	m := New((&acts{}).actions())
	shown := t0.Add(anim.Dialog)
	for _, b := range []input.Button{input.B, input.Menu} {
		m.Open(t0)
		if !m.Handle(press(b), shown) || m.IsOpen() {
			t.Fatalf("%v didn't close the menu", b)
		}
		if !m.Visible(shown.Add(anim.Dialog/2)) || m.Visible(shown.Add(anim.Dialog)) {
			t.Fatalf("after %v the menu doesn't fade out", b)
		}
	}
	if m.Handle(press(input.A), t0) {
		t.Fatal("a closed menu took input")
	}
}

func TestDueWhileATimerRuns(t *testing.T) {
	a := &acts{}
	m := New(a.actions())
	m.Open(t0)
	if _, ok := m.Due(t0); ok {
		t.Fatal("due with no timer")
	}
	a.sleep = player.Sleep{Mode: player.Sleep15, At: t0.Add(10*time.Second + 250*time.Millisecond)}
	if due, ok := m.Due(t0); !ok || due != t0.Add(250*time.Millisecond) {
		t.Fatalf("Due = %v, %v", due.Sub(t0), ok)
	}
}

func TestSleepTimerPassesOverTheEndsWhileAStationPlays(t *testing.T) {
	a := &acts{live: true, sleep: player.Sleep{Mode: player.Sleep60}}
	m := New(a.actions())
	m.Open(t0)
	m.Handle(press(input.Right), t0)
	if a.sleep.Mode != player.SleepOff {
		t.Fatalf("Right after 60 min set %v, want Off", a.sleep.Mode)
	}
	m.Handle(press(input.Left), t0)
	if a.sleep.Mode != player.Sleep60 {
		t.Fatalf("Left from Off set %v, want 60 min", a.sleep.Mode)
	}
	a.live = false
	m.Handle(press(input.Right), t0)
	if a.sleep.Mode != player.SleepTrack {
		t.Fatalf("Right after 60 min set %v, want End of track", a.sleep.Mode)
	}
}

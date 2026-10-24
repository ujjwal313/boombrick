package audible

import (
	"errors"
	"image"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audible"
	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type fakeNav struct {
	pops   int
	toasts []string
}

func (*fakeNav) Push(screens.Screen) {}
func (n *fakeNav) Pop()              { n.pops++ }
func (n *fakeNav) Toast(msg string)  { n.toasts = append(n.toasts, msg) }

type fakeSource struct {
	url           string
	err           error
	state         audible.SignInState
	starts, stops int
}

func (f *fakeSource) Start() (string, error)              { f.starts++; return f.url, f.err }
func (f *fakeSource) Stop()                               { f.stops++ }
func (f *fakeSource) State() (audible.SignInState, error) { return f.state, nil }

func draw(t *testing.T, s screens.Screen) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	u.Now = t0
	s.Draw(u)
	return off.Image()
}

func TestTheSignInShowsItsAddressAndClosesWhenDone(t *testing.T) {
	nav, src := &fakeNav{}, &fakeSource{url: "http://192.168.1.20:41234/"}
	s := NewSignIn(nav, src)
	if src.starts != 1 {
		t.Fatalf("started %d times", src.starts)
	}
	draw(t, s)
	if nav.pops != 0 {
		t.Fatal("closed before the phone signed in")
	}
	src.state = audible.SignedIn
	draw(t, s)
	draw(t, s)
	if nav.pops != 1 || len(nav.toasts) != 1 || nav.toasts[0] != "Signed in to Audible" {
		t.Fatalf("pops %d, toasts %v", nav.pops, nav.toasts)
	}
	s.Close()
	if src.stops != 1 {
		t.Fatalf("stopped %d times", src.stops)
	}
}

func TestWithoutANetworkItSaysSoAndTriesAgain(t *testing.T) {
	src := &fakeSource{err: errors.New("no address")}
	s := NewSignIn(&fakeNav{}, src)
	draw(t, s)
	if !s.Handle(input.Event{Button: input.X, Kind: input.Press}, t0) || src.starts != 2 {
		t.Fatalf("X didn't try again (%d starts)", src.starts)
	}
	src.err, src.url = nil, "http://10.0.0.5:1/"
	s.Handle(input.Event{Button: input.X, Kind: input.Press}, t0)
	draw(t, s)
	if s.err != nil || s.url == "" {
		t.Fatal("the page didn't start once there was a network")
	}
}

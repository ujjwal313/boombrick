package screens

import (
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
)

type seeker struct {
	st    player.State
	seeks []time.Duration
}

func (s *seeker) State() player.State { return s.st }

// Seek moves elapsed as the player does, clamped to the song's length.
func (s *seeker) Seek(d time.Duration) {
	s.seeks = append(s.seeks, d)
	s.st.Elapsed = min(d, s.st.Duration)
}

func playingFor(length time.Duration) *seeker {
	return &seeker{st: player.State{Queued: true, Playing: true, Track: player.Track{Path: "/m/a.flac", Duration: length},
		Elapsed: 93 * time.Second, Duration: length}}
}

var t0 = time.Unix(1000, 0)

func trigger(b input.Button, k input.Kind) input.Event { return input.Event{Button: b, Kind: k} }

func secs(n ...int) []time.Duration {
	d := make([]time.Duration, len(n))
	for i := range n {
		d[i] = time.Duration(n[i]) * time.Second
	}
	return d
}

func TestTapsJumpTenSeconds(t *testing.T) {
	p := playingFor(222 * time.Second)
	s := NewScrub(p)
	if !s.Handle(trigger(input.R2, input.Press), t0) {
		t.Fatal("R2 did nothing")
	}
	s.Handle(trigger(input.R2, input.Release), t0.Add(100*time.Millisecond))
	s.Handle(trigger(input.L2, input.Press), t0.Add(time.Second))
	s.Handle(trigger(input.L2, input.Release), t0.Add(1100*time.Millisecond))
	if want := secs(103, 93); !slices.Equal(p.seeks, want) {
		t.Fatalf("seeks %v, want %v: a tap seeks when pressed and nothing more when let go", p.seeks, want)
	}
	p.st.Elapsed = 4 * time.Second
	s.Handle(trigger(input.L2, input.Press), t0.Add(2*time.Second))
	if got := p.seeks[len(p.seeks)-1]; got != 0 {
		t.Fatalf("L2 4 s in seeked to %v, want the start", got)
	}
}

func TestHoldingScrubsThenSeeksOnce(t *testing.T) {
	p := playingFor(time.Hour)
	s := NewScrub(p)
	s.Handle(trigger(input.R2, input.Press), t0) // to 1:43
	if _, ok := s.position(p.st, t0.Add(300*time.Millisecond)); ok {
		t.Fatal("scrubbing before the trigger was held long enough")
	}
	if !s.Active() {
		t.Fatal("no frames asked for while the trigger is down")
	}
	// 20 times as fast for the first 2 s, then 60 times.
	for _, c := range []struct {
		held, want time.Duration
	}{
		{scrubAfter + time.Second, 123 * time.Second},
		{scrubAfter + 2*time.Second, 143 * time.Second},
		{scrubAfter + 3*time.Second, 203 * time.Second},
	} {
		if got, ok := s.position(p.st, t0.Add(c.held)); !ok || got != c.want {
			t.Fatalf("held %v: shows %v (%v), want %v", c.held, got, ok, c.want)
		}
	}
	if want := secs(103); !slices.Equal(p.seeks, want) {
		t.Fatalf("seeks %v while scrubbing, want only the press's %v", p.seeks, want)
	}
	s.Handle(trigger(input.R2, input.Release), t0.Add(scrubAfter+3*time.Second))
	if want := secs(103, 203); !slices.Equal(p.seeks, want) {
		t.Fatalf("seeks %v, want %v", p.seeks, want)
	}
	if _, ok := s.position(p.st, t0.Add(4*time.Second)); ok || s.Active() {
		t.Fatal("still scrubbing after the trigger was let go")
	}
}

func TestScrubStopsAtEitherEnd(t *testing.T) {
	p := playingFor(222 * time.Second)
	s := NewScrub(p)
	s.Handle(trigger(input.R2, input.Press), t0)
	if got, _ := s.position(p.st, t0.Add(time.Minute)); got != 222*time.Second {
		t.Fatalf("fast-forwarding a minute shows %v, want the end", got)
	}
	s.Handle(trigger(input.R2, input.Release), t0.Add(time.Minute))
	s.Handle(trigger(input.L2, input.Press), t0.Add(2*time.Minute))
	if got, _ := s.position(p.st, t0.Add(3*time.Minute)); got != 0 {
		t.Fatalf("rewinding a minute shows %v, want the start", got)
	}
}

// A stream still opening has no length to scrub through: taps still seek.
func TestNoScrubWithoutALength(t *testing.T) {
	p := playingFor(0)
	p.st.Loading = true
	s := NewScrub(p)
	s.Handle(trigger(input.R2, input.Press), t0)
	if _, ok := s.position(p.st, t0.Add(5*time.Second)); ok {
		t.Fatal("scrubbing a song of unknown length")
	}
	if s.Active() {
		t.Fatal("frames asked for while holding a trigger that can't scrub")
	}
	s.Handle(trigger(input.R2, input.Release), t0.Add(5*time.Second))
	if len(p.seeks) != 1 {
		t.Fatalf("seeks %v, want the press's alone", p.seeks)
	}
}

func TestNothingQueuedNothingSeeks(t *testing.T) {
	p := &seeker{}
	s := NewScrub(p)
	if s.Handle(trigger(input.R2, input.Press), t0) || s.Active() || len(p.seeks) != 0 {
		t.Fatal("R2 seeked with nothing queued")
	}
	if s.Handle(trigger(input.L1, input.Press), t0) {
		t.Fatal("L1 taken")
	}
}

// The quick menu, another screen or the screen going off can take the
// trigger's release: the scrub is dropped, and asks for no more frames.
func TestScrubDroppedWhenTheReleaseGoesElsewhere(t *testing.T) {
	p := playingFor(time.Hour)
	down := true
	s := NewScrub(p)
	s.SetHeld(func(b input.Button) bool { return b == input.R2 && down })
	s.Handle(trigger(input.R2, input.Press), t0)
	if _, ok := s.position(p.st, t0.Add(2*time.Second)); !ok {
		t.Fatal("not scrubbing while held")
	}
	down = false
	if _, ok := s.position(p.st, t0.Add(3*time.Second)); ok || s.Active() {
		t.Fatal("still scrubbing once the trigger is up")
	}
	if len(p.seeks) != 1 {
		t.Fatalf("seeks %v, want the press's alone", p.seeks)
	}
}

func TestScrubDroppedWhenTheSongChanges(t *testing.T) {
	p := playingFor(time.Hour)
	s := NewScrub(p)
	s.Handle(trigger(input.R2, input.Press), t0)
	p.st.Track = player.Track{Path: "/m/b.flac", Duration: time.Hour}
	if _, ok := s.position(p.st, t0.Add(2*time.Second)); ok {
		t.Fatal("scrubbing the next song")
	}
	s.Handle(trigger(input.R2, input.Release), t0.Add(2*time.Second))
	if len(p.seeks) != 1 {
		t.Fatalf("seeks %v: the next song was seeked", p.seeks)
	}
}

// Shown is the state a screen draws: the scrub's position in place of the
// elapsed time while one runs, the state as it is otherwise.
func TestShownPutsTheScrubInPlaceOfTheElapsedTime(t *testing.T) {
	p := playingFor(time.Hour)
	s := NewScrub(p)
	if got := s.Shown(p.st, t0).Elapsed; got != 93*time.Second {
		t.Fatalf("no scrub: shows %v, want the elapsed 1:33", got)
	}
	s.Handle(trigger(input.R2, input.Press), t0)
	if got := s.Shown(p.st, t0.Add(scrubAfter+time.Second)).Elapsed; got != 123*time.Second {
		t.Fatalf("scrubbing: shows %v, want 2:03", got)
	}
	if a := testing.AllocsPerRun(20, func() { s.Shown(p.st, t0.Add(2*time.Second)) }); a != 0 {
		t.Fatalf("Shown allocates %v times", a)
	}
}

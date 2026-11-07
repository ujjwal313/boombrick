package screens

import (
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
)

// SeekStep is how far a tap of L2 or R2 seeks, and Lyrics' ◀ ▶.
const SeekStep = 10 * time.Second

// A trigger held scrubAfter starts a scrub, which runs slowRate times as fast
// as playing for slowFor, then fastRate times: a 4-minute song in about 4 s,
// an hour-long mix in about a minute.
const (
	scrubAfter = 400 * time.Millisecond
	slowFor    = 2 * time.Second
	slowRate   = 20
	fastRate   = 60
)

// Seeker is what a Scrub needs from the player.
type Seeker interface {
	State() player.State
	Seek(d time.Duration)
}

// Scrub rewinds with L2 and fast-forwards with R2, on Now Playing and Lyrics.
// A press seeks SeekStep at once. Held, the position shown runs on while the
// music plays where it was, and letting go seeks there: one seek, so a stream
// isn't opened again and again. A scrub whose song changes under it, or whose
// release went elsewhere, as to the quick menu, is dropped.
type Scrub struct {
	p    Seeker
	held func(input.Button) bool // whether a button is down; nil: as its events say

	down   input.Button  // the trigger held, or None
	at     time.Time     // when it went down
	from   time.Duration // where its press seeked to
	length time.Duration // the song's length then; 0: unknown, no scrub
	song   song          // the song it seeks in
}

// song tells one song from the next.
type song struct{ path, video, spotify string }

func songOf(t player.Track) song { return song{t.Path, t.VideoID, t.SpotifyID} }

// NewScrub returns a Scrub seeking in p.
func NewScrub(p Seeker) *Scrub { return &Scrub{p: p} }

// SetHeld tells the scrub whether a button is down, which it asks while a
// trigger is held in case its release goes to another screen or the menu.
func (s *Scrub) SetHeld(held func(input.Button) bool) { s.held = held }

// Handle takes L2 and R2, and reports whether ev was one of them and did
// something.
func (s *Scrub) Handle(ev input.Event, now time.Time) bool {
	if ev.Button != input.L2 && ev.Button != input.R2 {
		return false
	}
	switch ev.Kind {
	case input.Press:
		s.down = input.None
		st := s.p.State()
		if !st.Queued {
			return false
		}
		to := max(st.Elapsed-SeekStep, 0)
		if ev.Button == input.R2 {
			to = st.Elapsed + SeekStep
			if st.Duration > 0 {
				to = min(to, st.Duration)
			}
		}
		s.p.Seek(to)
		s.down, s.at, s.from, s.song, s.length = ev.Button, now, to, songOf(st.Track), 0
		if !st.Loading && !st.Waiting {
			s.length = st.Duration
		}
		return true
	case input.Release:
		if ev.Button != s.down {
			return false
		}
		to, ok := s.target(now)
		s.down = input.None
		if ok && songOf(s.p.State().Track) == s.song {
			s.p.Seek(to)
		}
		return true
	}
	return false
}

// Shown is st as a screen shows it: with a scrub's position in place of the
// elapsed time while one runs.
func (s *Scrub) Shown(st player.State, now time.Time) player.State {
	if at, ok := s.position(st, now); ok {
		st.Elapsed = at
	}
	return st
}

// position is where a scrub has got to by now in st's song, and whether one
// runs.
func (s *Scrub) position(st player.State, now time.Time) (time.Duration, bool) {
	if !s.Active() {
		return 0, false
	}
	if songOf(st.Track) != s.song {
		s.down = input.None
		return 0, false
	}
	return s.target(now)
}

// Active reports whether a trigger is down on a song it can scrub, and frames
// are wanted for the scrub.
func (s *Scrub) Active() bool {
	if s.down != input.None && s.held != nil && !s.held(s.down) {
		s.down = input.None
	}
	return s.down != input.None && s.length > 0
}

// target is where the scrub has got to by now, once the trigger has been
// held long enough on a song of known length.
func (s *Scrub) target(now time.Time) (time.Duration, bool) {
	h := now.Sub(s.at) - scrubAfter
	if s.down == input.None || s.length <= 0 || h <= 0 {
		return 0, false
	}
	slow := min(h, slowFor)
	d := slow*slowRate + (h-slow)*fastRate
	if s.down == input.L2 {
		return max(s.from-d, 0), true
	}
	return min(s.from+d, s.length), true
}

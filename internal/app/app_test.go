package app

import (
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
	lrc "github.com/ujjwal313/boombrick/internal/lyrics"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	lyricsscreen "github.com/ujjwal313/boombrick/internal/screens/lyrics"
	"github.com/ujjwal313/boombrick/internal/screens/nowplaying"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
)

// stillPlayer is enough of a player for Now Playing and Lyrics.
type stillPlayer struct{ st player.State }

func (p *stillPlayer) State() player.State  { return p.st }
func (p *stillPlayer) Toggle()              {}
func (p *stillPlayer) Next()                {}
func (p *stillPlayer) Prev()                {}
func (p *stillPlayer) SeekBy(time.Duration) {}
func (p *stillPlayer) SetShuffle(bool)      {}
func (p *stillPlayer) CycleRepeat()         {}
func (p *stillPlayer) Seek(time.Duration)   {}

type noLyrics struct{}

func (noLyrics) Lyrics(lrc.Track) (lrc.Lyrics, lrc.State) { return lrc.Lyrics{}, lrc.None }

// Now Playing's Y opens Lyrics, whose B and Y go back to Now Playing.
func TestLyricsOpensFromNowPlayingAndGoesBack(t *testing.T) {
	s := newShell(&page{name: "home"})
	p := &stillPlayer{}
	np := nowplaying.New(p)
	addOverlays(s, np, &page{name: "queue"}, nil, p, noLyrics{})
	s.handle(press(input.Start), t0)
	for _, back := range []input.Button{input.B, input.Y} {
		if s.overlay() != np {
			t.Fatalf("%T is open, want Now Playing", s.overlay())
		}
		s.handle(press(input.Y), t0)
		if _, ok := s.overlay().(*lyricsscreen.Screen); !ok {
			t.Fatalf("Y on Now Playing opened %T", s.overlay())
		}
		s.handle(press(back), t0)
		if s.overlay() != np {
			t.Fatalf("%v on Lyrics went to %T, want Now Playing", back, s.overlay())
		}
	}
	s2 := newShell(&page{name: "home"})
	np2 := nowplaying.New(p)
	addOverlays(s2, np2, &page{name: "queue"}, nil, p, nil)
	s2.handle(press(input.Start), t0)
	if s2.handle(press(input.Y), t0) || len(s2.overlays) != 2 {
		t.Fatal("without lyrics there is no Lyrics screen")
	}
}

// While music plays the loop sleeps until the open screen's next tick counted
// from the frame's start: drawing the frame doesn't stretch the tick.
func TestTicksCountFromTheFrameStart(t *testing.T) {
	s := newShell(&page{name: "home"})
	s.overlays = []overlay{{button: input.Select, screen: &ticking{}}} // every 250 ms
	s.openOverlay(0, t0)
	a := &App{shell: s, repeat: input.NewRepeater(), fade: newAccentFade(theme.Fallback()), ui: &ui.Context{}}
	a.last.Playing = true
	start := t0.Add(anim.Push) // the overlay is in place
	if got := a.timeout(start, start.Add(40*time.Millisecond)); got != 210*time.Millisecond {
		t.Fatalf("40 ms into the frame: sleep %v, want 210ms", got)
	}
	if got := a.timeout(start, start.Add(300*time.Millisecond)); got != 0 {
		t.Fatalf("a frame longer than the tick: sleep %v", got)
	}
	a.last.Playing = false
	if got := a.timeout(start, start.Add(40*time.Millisecond)); got < time.Second {
		t.Fatalf("paused: sleep %v, want until the clock's next minute", got)
	}
}

// A toast raised after the frame began, while it was polled or drawn, hasn't
// shown yet: the loop draws again at once rather than sleeping until the
// toast fades out.
func TestAToastRaisedDuringAFrameDrawsNext(t *testing.T) {
	s := newShell(&page{name: "home"})
	a := &App{shell: s, repeat: input.NewRepeater(), fade: newAccentFade(theme.Fallback()), ui: &ui.Context{}}
	start := t0
	if got := a.timeout(start, start.Add(5*time.Millisecond)); got == 0 {
		t.Fatal("nothing to draw, yet no sleep")
	}
	s.showToast("Skipped a song that couldn’t download", start.Add(2*time.Millisecond))
	if got := a.timeout(start, start.Add(5*time.Millisecond)); got != 0 {
		t.Fatalf("sleep %v before the toast shows", got)
	}
	next := start.Add(16 * time.Millisecond)
	if got := a.timeout(next, next.Add(4*time.Millisecond)); got != 12*time.Millisecond {
		t.Fatalf("sleep %v while the toast fades in, want the rest of the frame", got)
	}
}

// While something moves, frames come every 16 ms from each frame's start,
// though presenting doesn't wait for the display; a frame that took longer
// is followed at once.
func TestAnimationFramesArePaced(t *testing.T) {
	s := newShell(&page{name: "home"})
	a := &App{shell: s, repeat: input.NewRepeater(), fade: newAccentFade(theme.Fallback()), ui: &ui.Context{}}
	s.showToast("Saved", t0) // fades in
	start := t0.Add(time.Millisecond)
	if got := a.timeout(start, start.Add(4*time.Millisecond)); got != 12*time.Millisecond {
		t.Fatalf("after a 4 ms frame: sleep %v, want 12ms", got)
	}
	if got := a.timeout(start, start.Add(20*time.Millisecond)); got != 0 {
		t.Fatalf("after a 20 ms frame: sleep %v", got)
	}
}

// albumCalls records what starting an album asks of the player.
type albumCalls struct {
	shuffle      []bool
	tracks       int
	start        int
	frame        int64
	shuffledPlay bool // PlayFrom came while shuffle was on
}

func (c *albumCalls) SetShuffle(on bool) { c.shuffle = append(c.shuffle, on) }
func (c *albumCalls) PlayFrom(ts []player.Track, start int, frame int64) {
	c.tracks, c.start, c.frame = len(ts), start, frame
	c.shuffledPlay = len(c.shuffle) == 0 || c.shuffle[len(c.shuffle)-1]
}

// Home's Play and Continue listening start albums in order, whatever shuffle
// an album page left on.
func TestAlbumsFromHomePlayInOrder(t *testing.T) {
	al := &library.Album{Artist: "Odalys", Title: "Copper Season", Tracks: []library.Track{{Path: "/a/1"}, {Path: "/a/2"}}}
	for _, c := range []struct {
		track int
		frame int64
	}{{0, 0}, {1, 4800}} {
		p := &albumCalls{shuffle: []bool{true}}
		playAlbum(p, al, c.track, c.frame)
		if !slices.Equal(p.shuffle, []bool{true, false}) || p.shuffledPlay {
			t.Fatalf("shuffle set %v before playing; want it off first", p.shuffle)
		}
		if p.tracks != 2 || p.start != c.track || p.frame != c.frame {
			t.Fatalf("played %d tracks from %d at %d, want 2 from %d at %d", p.tracks, p.start, p.frame, c.track, c.frame)
		}
	}
}

// Two Spotify songs both have no path; moving from one to the next must
// still prefetch the lyrics of the new one, and a pending song that lands
// (its key changes from none to its file) must too.
func TestPrefetchesWhenTheStreamedSongChanges(t *testing.T) {
	a := player.State{Queued: true, Track: player.Track{SpotifyID: "a"}}
	b := player.State{Queued: true, Track: player.Track{SpotifyID: "b"}}
	if !wantsPrefetch(b, a) {
		t.Fatal("Spotify song b after a was not prefetched")
	}
	if wantsPrefetch(a, a) {
		t.Fatal("the same song was prefetched again")
	}
	if !wantsPrefetch(a, player.State{}) {
		t.Fatal("a song queued from nothing was not prefetched")
	}
	pending := player.State{Queued: true, Track: player.Track{VideoID: "v"}}
	landed := player.State{Queued: true, Track: player.Track{VideoID: "v", Path: "/m/v.opus"}}
	if !wantsPrefetch(landed, pending) {
		t.Fatal("a landed download was not prefetched")
	}
	if wantsPrefetch(player.State{Track: b.Track}, a) {
		t.Fatal("prefetched with nothing queued")
	}
}

func (p *stillPlayer) BookSpeed() float64   { return 1 }
func (p *stillPlayer) SetBookSpeed(float64) {}

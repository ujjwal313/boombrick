package player

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
)

// endless is a station's stream: silence at 48 kHz for ever, with a title and
// a format. An error sent on fail ends it with that error; while stall is set
// it gives no audio, as a stream that dropped.
type endless struct {
	title  string
	fail   chan error
	stall  atomic.Bool
	seeks  atomic.Int32
	closed atomic.Bool
}

func (s *endless) SampleRate() int { return 48000 }
func (s *endless) Frames() int64   { return -1 }
func (s *endless) Read(buf []float32) (int, error) {
	select {
	case err := <-s.fail:
		return 0, err
	default:
	}
	if s.stall.Load() {
		time.Sleep(time.Millisecond)
		return 0, nil
	}
	clear(buf)
	return len(buf) / 2, nil
}
func (s *endless) SeekFrame(int64) error { s.seeks.Add(1); return nil }
func (s *endless) Close() error          { s.closed.Store(true); return nil }
func (s *endless) Title() string         { return s.title }
func (s *endless) Format() string        { return "MP3" }

// radios opens stations as endless streams and records each open by the
// station's name, with the context it was given and the stream it handed out.
// A station in refuse is refused with its error, and while hold is set opens
// wait for it to close.
type radios struct {
	mu      sync.Mutex
	refuse  map[string]error
	hold    chan struct{}
	opened  []string
	ctxs    []context.Context
	sources []*endless
}

func (r *radios) open(ctx context.Context, t Track) (audio.Source, error) {
	r.mu.Lock()
	r.opened = append(r.opened, t.Title)
	r.ctxs = append(r.ctxs, ctx)
	hold, err := r.hold, r.refuse[t.Title]
	r.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	s := &endless{title: t.Title + " song", fail: make(chan error, 1)}
	r.mu.Lock()
	r.sources = append(r.sources, s)
	r.mu.Unlock()
	return s, nil
}

func (r *radios) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.opened)
}

// source is the ith stream handed out.
func (r *radios) source(t *testing.T, i int) *endless {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if i >= len(r.sources) {
		t.Fatalf("%d streams handed out, want stream %d", len(r.sources), i)
	}
	return r.sources[i]
}

// ctx is the context the ith open was given.
func (r *radios) ctx(i int) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctxs[i]
}

func (n *notices) all() []Notice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.list)
}

func radioPlayer(t *testing.T, lengths map[string]int64) (*Player, *radios, *files, *alarms, *notices) {
	t.Helper()
	p, f, a := sleepyPlayer(t, lengths)
	r := &radios{refuse: map[string]error{}}
	n := &notices{}
	p.SetStreaming(nil, n.add)
	p.SetLive(r.open)
	return p, r, f, a, n
}

// stations are radio stations with the given names.
func stations(names ...string) []Track {
	var ts []Track
	for _, name := range names {
		ts = append(ts, Track{Title: name, StationID: "id-" + name, Stream: "http://radio.test/" + name})
	}
	return ts
}

// onAir reports whether the station at index i plays, connected.
func onAir(p *Player, i int) bool {
	st := p.State()
	return st.Index == i && st.Playing && !st.Loading && st.Elapsed > 0
}

func TestStationsConnectOffTheCallersGoroutine(t *testing.T) {
	p, r, _, _, _ := radioPlayer(t, nil)
	r.hold = make(chan struct{})
	start := time.Now()
	p.Play(stations("a"), 0)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Play waited %v for the station", d)
	}
	if st := p.State(); !st.Loading || !st.Playing {
		t.Fatalf("while it connects: loading %v, playing %v", st.Loading, st.Playing)
	}
	close(r.hold)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	ctx := r.ctx(0)
	eventually(t, "the open's context to end", func() bool { return ctx.Err() != nil })
	at := p.State().Elapsed
	eventually(t, "a to play on", func() bool { return p.State().Elapsed > at })
	if r.source(t, 0).closed.Load() {
		t.Fatal("the stream closed with the open's context")
	}
}

// Nothing is primed to follow a station, nor is a station primed to follow a
// file: each opens when it is reached.
func TestNothingIsPrimedAroundStations(t *testing.T) {
	p, r, f, _, _ := radioPlayer(t, map[string]int64{"c": long, "d": long})
	p.Play(append(append([]Track{{Path: "c"}}, stations("a", "b")...), Track{Path: "d"}), 0)
	settled(t, p)
	time.Sleep(50 * time.Millisecond)
	if got := r.log(); len(got) != 0 {
		t.Fatalf("opened %v while c played", got)
	}
	p.Next()
	eventually(t, "a to play", func() bool { return onAir(p, 1) })
	p.Next()
	eventually(t, "b to play", func() bool { return onAir(p, 2) })
	time.Sleep(50 * time.Millisecond)
	if got := r.log(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("stations opened %v, want a, then b", got)
	}
	if got := f.log(); !slices.Equal(got, []string{"c"}) {
		t.Fatalf("files opened %v, want only c", got)
	}
	p.mu.Lock()
	next := p.next
	p.mu.Unlock()
	if next != nil {
		t.Fatal("something is primed to follow b")
	}
}

// A Spotify song after a station isn't opened while the station plays, as a
// song after a song would be: the station never ends, and Spotify would give
// a key for nothing. Adding to the queue, repeat and shuffle, which prime
// what follows again, open nothing either.
func TestNoSongIsPrimedToFollowAStation(t *testing.T) {
	p, _, _, _, n := radioPlayer(t, nil)
	s := &streams{length: long, refuse: map[string]error{}}
	p.SetStreaming(s.open, n.add)
	p.Play(append(stations("a"), spotifyTracks("b")...), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	p.Enqueue(spotifyTracks("c"))
	p.CycleRepeat()
	p.SetShuffle(true)
	time.Sleep(50 * time.Millisecond)
	if got := s.log(); len(got) != 0 {
		t.Fatalf("opened %v while a played", got)
	}
	p.mu.Lock()
	next := p.next
	p.mu.Unlock()
	if next != nil {
		t.Fatal("something is primed to follow a")
	}
}

func TestSeekingAStationDoesNothing(t *testing.T) {
	p, r, _, _, _ := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	p.Seek(30 * time.Second)
	eventually(t, "a to play a while", func() bool { return p.State().Elapsed > 100*time.Millisecond })
	if n := r.source(t, 0).seeks.Load(); n != 0 {
		t.Fatalf("the stream was sought %d times", n)
	}
	if s, _ := p.Snapshot(); s.Frame != 0 {
		t.Fatalf("saved at frame %d, want 0", s.Frame)
	}
}

// Pause closes the stream; play connects again, to what's live now.
func TestPausingAStationClosesIt(t *testing.T) {
	p, r, _, _, _ := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	p.Toggle()
	src := r.source(t, 0)
	eventually(t, "a's stream to close", src.closed.Load)
	if st := p.State(); st.Playing || st.Loading || !st.Cued || st.Index != 0 {
		t.Fatalf("paused: %+v, want a cued", st)
	}
	p.Toggle()
	eventually(t, "a to play again", func() bool { return onAir(p, 0) })
	if got := r.log(); !slices.Equal(got, []string{"a", "a"}) {
		t.Fatalf("opened %v, want a twice", got)
	}
}

// Paused while it connects, the stream it gets is closed at once, and play
// connects again.
func TestPausedWhileAStationConnects(t *testing.T) {
	p, r, _, _, _ := radioPlayer(t, nil)
	r.hold = make(chan struct{})
	p.Play(stations("a"), 0)
	p.Toggle()
	if st := p.State(); st.Playing || !st.Loading || !st.Cued {
		t.Fatalf("paused while it connects: %+v", st)
	}
	close(r.hold)
	eventually(t, "a to connect", func() bool { return !p.State().Loading })
	if !r.source(t, 0).closed.Load() {
		t.Fatal("the stream is kept open while paused")
	}
	if st := p.State(); st.Playing || !st.Cued {
		t.Fatalf("after it connected: %+v, want it cued", st)
	}
	p.Toggle()
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	if got := r.log(); !slices.Equal(got, []string{"a", "a"}) {
		t.Fatalf("opened %v, want a twice", got)
	}
}

// A station the engine paused by itself, as Release does, connects again on
// play rather than playing on behind what's live.
func TestPlayAfterTheEnginePausedAStationConnectsAgain(t *testing.T) {
	p, r, _, _, _ := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	if err := p.Release(); err != nil {
		t.Fatal(err)
	}
	if st := p.State(); st.Playing {
		t.Fatalf("after Release: %+v, want a paused", st)
	}
	p.Toggle()
	eventually(t, "a to play again", func() bool { return onAir(p, 0) && len(r.log()) == 2 })
	if !r.source(t, 0).closed.Load() {
		t.Fatal("the old stream is still open")
	}
	if got := r.log(); !slices.Equal(got, []string{"a", "a"}) {
		t.Fatalf("opened %v, want a twice", got)
	}
}

// A player given no way to open stations stops on one, saying it couldn't
// play it.
func TestAStationWithNoOpenerStaysCued(t *testing.T) {
	p, _, _ := sleepyPlayer(t, nil)
	n := &notices{}
	p.SetStreaming(nil, n.add)
	p.Play(stations("a", "b"), 0)
	eventually(t, "the notice", func() bool { return len(n.all()) > 0 })
	time.Sleep(50 * time.Millisecond)
	if st := p.State(); st.Index != 0 || st.Playing || st.Loading || !st.Cued {
		t.Fatalf("after the failure: %+v, want a cued", st)
	}
	if got := n.all(); len(got) != 1 || got[0].Kind != CouldNotPlay || got[0].Name != "a" {
		t.Fatalf("notices %+v, want one CouldNotPlay for a", got)
	}
}

// A station that won't connect isn't skipped: playback stays on it, saying
// so, and Next is a press away.
func TestAStationThatWontConnectStaysCued(t *testing.T) {
	p, r, _, _, n := radioPlayer(t, nil)
	r.refuse["a"] = errors.New("connection refused")
	p.Play(stations("a", "b"), 0)
	eventually(t, "the notice", func() bool { return len(n.all()) > 0 })
	time.Sleep(50 * time.Millisecond)
	if st := p.State(); st.Index != 0 || st.Playing || st.Loading || !st.Cued {
		t.Fatalf("after the failure: %+v, want a cued", st)
	}
	if got := n.all(); len(got) != 1 || got[0].Kind != CouldNotPlay || got[0].Name != "a" {
		t.Fatalf("notices %+v, want one CouldNotPlay for a", got)
	}
	p.Next()
	eventually(t, "b to play", func() bool { return onAir(p, 1) })
}

// A stream that fails for good stops playback on its station, saying so.
func TestALostStationStopsOnIt(t *testing.T) {
	p, r, _, _, n := radioPlayer(t, nil)
	p.Play(stations("a", "b"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	r.source(t, 0).fail <- errors.New("radio: the station is gone")
	eventually(t, "the notice", func() bool { return len(n.all()) > 0 })
	time.Sleep(50 * time.Millisecond)
	if st := p.State(); st.Index != 0 || st.Playing || st.Loading || !st.Cued {
		t.Fatalf("after the loss: %+v, want a cued", st)
	}
	if got := n.all(); len(got) != 1 || got[0].Kind != Lost || got[0].Name != "a" {
		t.Fatalf("notices %+v, want one Lost for a", got)
	}
	if got := r.log(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("opened %v, want a once", got)
	}
}

// A stream back at another rate is opened again, without a word.
func TestAStationAtAnotherRateIsOpenedAgain(t *testing.T) {
	p, r, _, _, n := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	r.source(t, 0).fail <- audio.ErrRateChanged
	eventually(t, "a to be opened again", func() bool { return len(r.log()) == 2 })
	eventually(t, "a to play again", func() bool { return onAir(p, 0) && !r.source(t, 1).closed.Load() })
	if got := n.all(); len(got) != 0 {
		t.Fatalf("notices %+v, want none", got)
	}
}

// With Radio off, stations are skipped as other switched-off tracks are.
func TestTheGateRefusesStations(t *testing.T) {
	p, r, _, _, n := radioPlayer(t, map[string]int64{"c": long})
	on, off := func() bool { return true }, func() bool { return false }
	p.SetGate(Switches(on, on, off, off))
	p.Play(append(stations("a"), Track{Path: "c"}), 0)
	eventually(t, "c to play", func() bool { st := p.State(); return st.Index == 1 && st.Playing })
	if got := n.offs(); !slices.Equal(got, []string{"Radio"}) {
		t.Fatalf("switched-off notices %v, want one for Radio", got)
	}
	if got := r.log(); len(got) != 0 {
		t.Fatalf("opened %v", got)
	}
}

// A station has no end, so a timer set to stop at the end of a track or an
// album would never stop: it is turned off, saying so.
func TestAStationTurnsTheEndOfTrackTimerOff(t *testing.T) {
	for _, m := range []SleepMode{SleepTrack, SleepAlbum} {
		p, _, _, _, n := radioPlayer(t, nil)
		p.SetSleep(m)
		p.Play(stations("a"), 0)
		if s := p.Sleep(); s.Mode != SleepOff {
			t.Fatalf("sleep %v: the timer is %v after a station started, want off", m, s.Mode)
		}
		if got := n.kinds(); !slices.Equal(got, []NoticeKind{SleepTimerOff}) {
			t.Fatalf("sleep %v: notices %v, want SleepTimerOff", m, got)
		}
		eventually(t, "a to play", func() bool { return onAir(p, 0) })
	}
}

// A timed sleep fades a station out, pauses it, and then closes its stream.
// Play then connects again.
func TestATimedSleepClosesTheStation(t *testing.T) {
	p, r, _, a, _ := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	p.SetSleep(Sleep15)
	a.last(t).f() // the fade begins
	eventually(t, "the fade to pause playback", func() bool { return !p.State().Playing })
	src := r.source(t, 0)
	al := a.last(t)
	if al.d != sleepFade {
		t.Fatalf("the stream is closed after %v, want the fade's %v", al.d, sleepFade)
	}
	al.f()
	eventually(t, "a's stream to close", src.closed.Load)
	if st := p.State(); st.Playing || !st.Cued || st.Index != 0 {
		t.Fatalf("after the fade: %+v, want a cued", st)
	}
	p.Toggle()
	eventually(t, "a to play again", func() bool { return onAir(p, 0) })
	if got := r.log(); !slices.Equal(got, []string{"a", "a"}) {
		t.Fatalf("opened %v, want a twice", got)
	}
}

// A stream that stalls during the fade holds the fade back, as it gives no
// audio: it is closed when the fade's time is up all the same.
func TestATimedSleepClosesAStalledStation(t *testing.T) {
	p, r, _, a, _ := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	src := r.source(t, 0)
	src.stall.Store(true)
	p.SetSleep(Sleep15)
	a.last(t).f() // the fade begins
	a.last(t).f() // and its time is up
	eventually(t, "a's stream to close", src.closed.Load)
	if st := p.State(); st.Playing || !st.Cued {
		t.Fatalf("after the fade: %+v, want a cued", st)
	}
}

// Play during the fade keeps the station playing, its stream open.
func TestPlayDuringTheFadeKeepsTheStation(t *testing.T) {
	p, r, _, a, _ := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	p.SetSleep(Sleep15)
	a.last(t).f() // the fade begins
	p.Toggle()
	a.last(t).f() // the fade's time is up
	time.Sleep(50 * time.Millisecond)
	if st := p.State(); !st.Playing || r.source(t, 0).closed.Load() {
		t.Fatalf("after play during the fade: %+v, want a playing on", st)
	}
}

// A stream back at another rate during the fade isn't opened again: the
// fade stops there, as at a change of track.
func TestAStationAtAnotherRateDuringTheFadeStops(t *testing.T) {
	p, r, _, a, _ := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	p.SetSleep(Sleep15)
	a.last(t).f() // the fade begins
	r.source(t, 0).fail <- audio.ErrRateChanged
	eventually(t, "a to stop", func() bool { st := p.State(); return !st.Playing && st.Cued })
	time.Sleep(50 * time.Millisecond)
	if got := r.log(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("opened %v during the fade, want a once", got)
	}
}

// A restored station connects only when played, and is saved at frame 0.
func TestARestoredStationOpensWhenPlayed(t *testing.T) {
	p, r, _, _, _ := radioPlayer(t, nil)
	p.Restore(Snapshot{Queue: stations("a", "b"), Order: []int{0, 1}, Pos: 1, Frame: 4410})
	time.Sleep(50 * time.Millisecond)
	if got := r.log(); len(got) != 0 {
		t.Fatalf("opened %v on restoring", got)
	}
	if st := p.State(); st.Index != 1 || st.Playing || st.Loading || st.Elapsed != 0 {
		t.Fatalf("restored: %+v", st)
	}
	if s, _ := p.Snapshot(); s.Frame != 0 {
		t.Fatalf("saved at frame %d, want 0", s.Frame)
	}
	p.Toggle()
	eventually(t, "b to play", func() bool { return onAir(p, 1) })
	if got := r.log(); !slices.Equal(got, []string{"b"}) {
		t.Fatalf("opened %v", got)
	}
}

// Prev on a station goes back to the station before however long it has
// played: a station has no start to go back to.
func TestPrevFromAStationGoesBack(t *testing.T) {
	p, r, _, _, _ := radioPlayer(t, nil)
	p.Play(stations("a", "b"), 1)
	eventually(t, "b to play", func() bool { return onAir(p, 1) })
	p.mu.Lock()
	p.engine.SeekFrame(11 * 48000) // 11 s in, behind the player's back
	p.mu.Unlock()
	eventually(t, "b to have played 10 s", func() bool { return p.State().Elapsed >= 10*time.Second })
	p.Prev()
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	if got := r.log(); !slices.Equal(got, []string{"b", "a"}) {
		t.Fatalf("opened %v, want b, then a", got)
	}
}

func TestStateShowsTheStationsTitleAndFormat(t *testing.T) {
	p, _, _, _, _ := radioPlayer(t, nil)
	p.Play(stations("a"), 0)
	eventually(t, "a to play", func() bool { return onAir(p, 0) })
	if st := p.State(); st.LiveTitle != "a song" || st.Format != "MP3" {
		t.Fatalf("title %q, format %q; want the stream's", st.LiveTitle, st.Format)
	}
}

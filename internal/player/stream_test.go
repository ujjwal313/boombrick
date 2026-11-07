package player

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
	"github.com/ujjwal313/boombrick/internal/spotify"
)

// streams opens streamed tracks as tones of length frames and records each
// open as "id@frame". An ID in refuse is refused with its error, and while
// hold is set opens wait for it to close.
type streams struct {
	mu     sync.Mutex
	length int64
	refuse map[string]error
	hold   chan struct{}
	opened []string
}

func (s *streams) open(ctx context.Context, t Track, frame int64, level bool) (audio.Source, error) {
	s.mu.Lock()
	s.opened = append(s.opened, fmt.Sprintf("%s@%d", t.SpotifyID, frame))
	hold, err := s.hold, s.refuse[t.SpotifyID]
	s.mu.Unlock()
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
	return &tone{n: s.length, pos: frame}, nil
}

func (s *streams) log() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.opened)
}

func (s *streams) set(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		delete(s.refuse, id)
		return
	}
	s.refuse[id] = err
}

// notices records what the player tells the listener.
type notices struct {
	mu   sync.Mutex
	list []Notice
}

func (n *notices) add(x Notice) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.list = append(n.list, x)
}

func (n *notices) kinds() []NoticeKind {
	n.mu.Lock()
	defer n.mu.Unlock()
	var ks []NoticeKind
	for _, x := range n.list {
		ks = append(ks, x.Kind)
	}
	return ks
}

func streamPlayer(t *testing.T, files map[string]int64, length int64) (*Player, *streams, *alarms, *notices) {
	t.Helper()
	p, _, a := sleepyPlayer(t, files)
	s := &streams{length: length, refuse: map[string]error{}}
	n := &notices{}
	p.SetStreaming(s.open, n.add)
	return p, s, a, n
}

// spotifyTracks are streamed tracks with the given IDs.
func spotifyTracks(ids ...string) []Track {
	var ts []Track
	for _, id := range ids {
		ts = append(ts, Track{SpotifyID: id, Title: id})
	}
	return ts
}

func TestStreamsOpenOffTheCallersGoroutine(t *testing.T) {
	p, s, _, _ := streamPlayer(t, nil, long)
	s.hold = make(chan struct{})
	start := time.Now()
	p.Play(spotifyTracks("a", "b"), 0)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("Play waited %v for the stream", d)
	}
	if st := p.State(); !st.Loading || !st.Playing {
		t.Fatalf("while it opens: loading %v, playing %v", st.Loading, st.Playing)
	}
	close(s.hold)
	eventually(t, "the stream to play", func() bool {
		st := p.State()
		return !st.Loading && st.Playing && st.Elapsed > 0
	})
}

// Skipping through songs opens only the one it stops on.
func TestSkippingOpensOnlyWhereItStops(t *testing.T) {
	p, s, _, _ := streamPlayer(t, nil, long)
	p.Play(spotifyTracks("a", "b", "c", "d"), 0)
	eventually(t, "a to open", func() bool { return len(s.log()) == 1 })
	p.Next()
	p.Next()
	p.Next()
	time.Sleep(settle + 300*time.Millisecond)
	if got := s.log(); !slices.Equal(got, []string{"a@0", "d@0"}) {
		t.Fatalf("opened %v", got)
	}
}

// A stream to follow is opened when the one playing has streamLead left,
// and then follows it without a gap.
func TestStreamToFollowOpensNearTheEnd(t *testing.T) {
	const length = 25 * 48000 // 25 s
	p, s, a, _ := streamPlayer(t, nil, length)
	p.Play(spotifyTracks("a", "b"), 0)
	eventually(t, "a to play", func() bool { return p.State().Elapsed > 0 })
	al := a.last(t)
	if al.d > 5*time.Second || al.d < 4*time.Second {
		t.Fatalf("b opens in %v, want about 5 s", al.d)
	}
	if got := s.log(); !slices.Equal(got, []string{"a@0"}) {
		t.Fatalf("opened %v before the time", got)
	}
	eventually(t, "a to play past 6 s", func() bool { return p.State().Elapsed > 6*time.Second })
	al.f()
	eventually(t, "b to open", func() bool { return len(s.log()) == 2 })
	if st := p.State(); st.Index != 0 {
		t.Fatalf("b opened at index %d, not while a played", st.Index)
	}
	eventually(t, "b to follow", func() bool { return p.State().Index == 1 })
	if got := s.log(); !slices.Equal(got, []string{"a@0", "b@0"}) {
		t.Fatalf("opened %v", got)
	}
}

// The wait to open the stream to follow is stopped when another replaces it,
// as on each seek, and when the player closes.
func TestWaitsForTheStreamToFollowAreStopped(t *testing.T) {
	p, _, a, _ := streamPlayer(t, nil, long)
	p.Play(spotifyTracks("a", "b"), 0)
	eventually(t, "a to play", func() bool { return p.State().Elapsed > 0 })
	first := a.last(t)
	p.Seek(time.Second)
	second := a.last(t)
	if second == first || !first.stopped {
		t.Fatalf("after a seek: waiting again %v, the first wait stopped %v", second != first, first.stopped)
	}
	p.Close()
	if !second.stopped {
		t.Fatal("the wait outlived the player")
	}
}

// A refused key is waited out once; refused again, playback stops on the
// track, and play tries again. It never skips on to the next track.
func TestThrottledStreamWaitsThenTriesOnce(t *testing.T) {
	p, s, a, n := streamPlayer(t, map[string]int64{"c": long}, long)
	s.set("b", &spotify.Throttled{Wait: 40 * time.Second})
	p.Play(append(spotifyTracks("b"), Track{Path: "c"}), 0)
	eventually(t, "the throttle notice", func() bool { return slices.Equal(n.kinds(), []NoticeKind{Throttled}) })
	if al := a.last(t); al.d != 40*time.Second {
		t.Fatalf("tried again after %v", al.d)
	}
	if st := p.State(); st.Index != 0 || !st.Loading {
		t.Fatalf("while waiting: index %d, loading %v", st.Index, st.Loading)
	}
	a.last(t).f()
	eventually(t, "the second refusal", func() bool {
		return slices.Equal(n.kinds(), []NoticeKind{Throttled, StillThrottled})
	})
	if st := p.State(); st.Index != 0 || st.Playing || st.Loading {
		t.Fatalf("after: index %d, playing %v, loading %v", st.Index, st.Playing, st.Loading)
	}
	s.set("b", nil)
	p.Toggle()
	eventually(t, "b to play", func() bool { st := p.State(); return st.Playing && !st.Loading && st.Index == 0 })
	if got := s.log(); !slices.Equal(got, []string{"b@0", "b@0", "b@0"}) {
		t.Fatalf("opened %v", got)
	}
}

func TestUnavailableStreamIsSkipped(t *testing.T) {
	p, s, _, n := streamPlayer(t, map[string]int64{"c": long}, long)
	s.set("b", fmt.Errorf("restricted: %w", spotify.ErrUnavailable))
	p.Play(append(spotifyTracks("b"), Track{Path: "c"}), 0)
	eventually(t, "c to play", func() bool { st := p.State(); return st.Index == 1 && st.Playing })
	if got := n.kinds(); !slices.Equal(got, []NoticeKind{Unavailable}) {
		t.Fatalf("notices %v", got)
	}
}

// Spotify refuses a free account every stream: they are skipped, saying so,
// without waiting to try again, and the files still play.
func TestStreamsNeedingPremiumAreSkipped(t *testing.T) {
	p, s, a, n := streamPlayer(t, map[string]int64{"d": long}, long)
	s.set("b", fmt.Errorf("audio key: %w", spotify.ErrPremium))
	s.set("c", fmt.Errorf("audio key: %w", spotify.ErrPremium))
	p.Play(append(spotifyTracks("b", "c"), Track{Path: "d"}), 0)
	eventually(t, "d to play", func() bool { st := p.State(); return st.Index == 2 && st.Playing })
	if got := n.kinds(); !slices.Equal(got, []NoticeKind{NeedsPremium, NeedsPremium}) {
		t.Fatalf("notices %v", got)
	}
	a.mu.Lock()
	waits := len(a.set)
	a.mu.Unlock()
	if got := s.log(); !slices.Equal(got, []string{"b@0", "c@0"}) || waits != 0 {
		t.Fatalf("opened %v, %d waits set", got, waits)
	}
}

// Signed out, streams are skipped as when offline: the files still play.
func TestSignedOutSkipsStreams(t *testing.T) {
	p, s, _, n := streamPlayer(t, map[string]int64{"c": long}, long)
	s.set("b", spotify.ErrSignedOut)
	p.Play(append(spotifyTracks("b"), Track{Path: "c"}), 0)
	eventually(t, "c to play", func() bool { st := p.State(); return st.Index == 1 && st.Playing })
	if got := n.kinds(); !slices.Equal(got, []NoticeKind{SignedOut}) {
		t.Fatalf("notices %v", got)
	}
}

// A restored stream asks for nothing until it is played, then opens where
// it was left.
func TestRestoredStreamOpensWhenPlayed(t *testing.T) {
	p, s, _, _ := streamPlayer(t, nil, long)
	p.Restore(Snapshot{Queue: spotifyTracks("a", "b"), Order: []int{0, 1}, Pos: 1, Frame: 4410})
	time.Sleep(50 * time.Millisecond)
	if got := s.log(); len(got) != 0 {
		t.Fatalf("opened %v on restoring", got)
	}
	if st := p.State(); st.Index != 1 || st.Playing || st.Elapsed != 0 {
		t.Fatalf("restored: %+v", st)
	}
	p.Toggle()
	eventually(t, "b to play", func() bool { return p.State().Playing && !p.State().Loading })
	if got := s.log(); !slices.Equal(got, []string{"b@4410"}) {
		t.Fatalf("opened %v", got)
	}
}

// Paused while it opens, the stream is cued rather than played.
func TestPausedWhileOpening(t *testing.T) {
	p, s, _, _ := streamPlayer(t, nil, long)
	s.hold = make(chan struct{})
	p.Play(spotifyTracks("a"), 0)
	p.Toggle()
	if st := p.State(); st.Playing || !st.Loading {
		t.Fatalf("paused while opening: playing %v, loading %v", st.Playing, st.Loading)
	}
	close(s.hold)
	eventually(t, "a to open", func() bool { return !p.State().Loading })
	time.Sleep(50 * time.Millisecond)
	if st := p.State(); st.Playing || st.Elapsed != 0 {
		t.Fatalf("after opening: playing %v at %v", st.Playing, st.Elapsed)
	}
	p.Toggle()
	eventually(t, "a to play", func() bool { return p.State().Elapsed > 0 })
}

// A stream cued while it loads that then fails to open moves on to the next
// track without playing it: the pause stays, and play starts that track.
func TestCuedStreamThatFailsStaysCuedOnTheNext(t *testing.T) {
	p, s, _, _ := streamPlayer(t, map[string]int64{"c": long}, long)
	s.set("b", fmt.Errorf("restricted: %w", spotify.ErrUnavailable))
	s.hold = make(chan struct{})
	p.Play(append(spotifyTracks("b"), Track{Path: "c"}), 0)
	p.Toggle()
	if st := p.State(); st.Playing || !st.Loading || !st.Cued {
		t.Fatalf("paused while opening: %+v", st)
	}
	close(s.hold)
	eventually(t, "b to fail", func() bool { return p.State().Index == 1 })
	time.Sleep(50 * time.Millisecond)
	if st := p.State(); st.Playing || st.Loading || !st.Cued || st.Index != 1 {
		t.Fatalf("after the failure: %+v, want it cued on the next track", st)
	}
	p.Toggle()
	eventually(t, "c to play", func() bool { st := p.State(); return st.Playing && st.Index == 1 })
}

// A stream being opened from a restored place is still saved at that place.
func TestSnapshotWhileAStreamLoadsKeepsItsFrame(t *testing.T) {
	p, s, _, _ := streamPlayer(t, nil, long)
	s.hold = make(chan struct{})
	defer close(s.hold)
	p.Restore(Snapshot{Queue: spotifyTracks("a"), Order: []int{0}, Frame: 4410})
	p.Toggle()
	if st := p.State(); !st.Loading {
		t.Fatalf("not loading: %+v", st)
	}
	if got, _ := p.Snapshot(); got.Frame != 4410 {
		t.Fatalf("frame %d while it loads, want 4410", got.Frame)
	}
}

// The sleep timer's time comes while a stream opens: it lands paused rather
// than playing on with the timer spent.
func TestSleepWhileAStreamLoads(t *testing.T) {
	p, s, a, _ := streamPlayer(t, nil, long)
	s.hold = make(chan struct{})
	p.Play(spotifyTracks("a"), 0)
	p.SetSleep(Sleep15)
	a.last(t).f()
	if st := p.State(); st.Playing || !st.Loading || !st.Cued {
		t.Fatalf("after the timer's time: %+v, want it loading and cued", st)
	}
	close(s.hold)
	eventually(t, "a to open", func() bool { return !p.State().Loading })
	time.Sleep(50 * time.Millisecond)
	if st := p.State(); st.Playing || st.Elapsed != 0 {
		t.Fatalf("after opening: playing %v at %v", st.Playing, st.Elapsed)
	}
}

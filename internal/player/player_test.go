package player

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
)

// tone is a silent Source of n frames at 48 kHz.
type tone struct{ n, pos int64 }

func (s *tone) SampleRate() int { return 48000 }
func (s *tone) Frames() int64   { return s.n }
func (s *tone) Read(buf []float32) (int, error) {
	k := min(int64(len(buf)/2), s.n-s.pos)
	clear(buf[:2*k])
	s.pos += k
	if s.pos >= s.n {
		return int(k), io.EOF
	}
	return int(k), nil
}
func (s *tone) SeekFrame(f int64) error { s.pos = f; return nil }
func (s *tone) Close() error            { return nil }

type nullOut struct{}

func (nullOut) Open(int) error { return nil }
func (nullOut) Write([]int16) error {
	time.Sleep(time.Millisecond)
	return nil
}
func (nullOut) Drop() (int, error) { return 0, nil }
func (nullOut) Close() error       { return nil }

// files opens tracks as tones of the given lengths and records every open.
type files struct {
	mu     sync.Mutex
	length map[string]int64 // frames; missing paths fail to open
	opened []string
}

func (f *files) open(path string) (audio.Source, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.length[path]
	if !ok {
		return nil, errors.New("unreadable")
	}
	f.opened = append(f.opened, path)
	return &tone{n: n}, nil
}

func (f *files) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.opened)
}

const (
	short = 20000   // plays in about 20 ms
	long  = 1 << 40 // never ends during a test
)

func newPlayer(t *testing.T, lengths map[string]int64) (*Player, *files) {
	t.Helper()
	f := &files{length: lengths}
	p := New(nullOut{}, f.open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { p.Close() })
	return p, f
}

func queue(paths ...string) []Track {
	var ts []Track
	for _, p := range paths {
		ts = append(ts, Track{Path: p, Title: p})
	}
	return ts
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPlaysTheQueueInOrderThenStops(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a.flac": short, "b.flac": short, "c.flac": short})
	p.Play(queue("a.flac", "b.flac", "c.flac"), 0)
	eventually(t, "the queue to play out", func() bool { return len(f.log()) == 3 && !p.State().Playing })
	if got := f.log(); !slices.Equal(got, []string{"a.flac", "b.flac", "c.flac"}) {
		t.Fatalf("played %v", got)
	}
	if s := p.State(); !s.Queued || s.Track.Path != "c.flac" || s.Format != "FLAC" {
		t.Fatalf("state after the end = %+v", s)
	}
}

func TestRepeatAllWrapsAndRepeatOneRepeats(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a.mp3": short, "b.mp3": short})
	p.CycleRepeat() // all
	p.Play(queue("a.mp3", "b.mp3"), 0)
	eventually(t, "a wrap", func() bool { return len(f.log()) >= 3 })
	if got := f.log()[:3]; !slices.Equal(got, []string{"a.mp3", "b.mp3", "a.mp3"}) {
		t.Fatalf("repeat all played %v", got)
	}

	p2, f2 := newPlayer(t, map[string]int64{"a.mp3": short, "b.mp3": short})
	p2.CycleRepeat()
	p2.CycleRepeat() // one
	if p2.State().Repeat != RepeatOne {
		t.Fatalf("repeat = %v, want RepeatOne", p2.State().Repeat)
	}
	p2.Play(queue("a.mp3", "b.mp3"), 0)
	eventually(t, "a repeat", func() bool { return len(f2.log()) >= 3 })
	if got := f2.log()[:3]; !slices.Equal(got, []string{"a.mp3", "a.mp3", "a.mp3"}) {
		t.Fatalf("repeat one played %v", got)
	}
}

func TestNextAndPrev(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	p.Play(queue("a", "b", "c"), 0)
	p.Next()
	if s := p.State(); s.Track.Path != "b" || s.Index != 1 {
		t.Fatalf("after Next = %+v", s)
	}
	p.Prev() // only just started: back a track
	if s := p.State(); s.Track.Path != "a" {
		t.Fatalf("after Prev = %+v", s)
	}
	p.Prev() // first track: restart it
	p.Next()
	p.Next()
	p.Next() // last track, repeat off: stays
	if s := p.State(); s.Track.Path != "c" {
		t.Fatalf("Next past the end = %+v", s)
	}
}

func TestShuffleStartsWithTheChosenTrack(t *testing.T) {
	lengths := map[string]int64{}
	var paths []string
	for _, p := range []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"} {
		lengths[p] = long
		paths = append(paths, p)
	}
	p, f := newPlayer(t, lengths)
	p.SetShuffle(true)
	p.Play(queue(paths...), 3)
	settled(t, p)
	if got := f.log(); len(got) == 0 || got[0] != "3" {
		t.Fatalf("shuffle opened %v first, want 3", got)
	}
	seen := map[string]bool{"3": true}
	for i := 0; i < 9; i++ {
		p.Next()
		seen[p.State().Track.Path] = true
	}
	if len(seen) != 10 {
		t.Fatalf("shuffled order visited %d tracks, want all 10", len(seen))
	}
	p.SetShuffle(false)
	if s := p.State(); s.Shuffle || s.Index != slices.Index(paths, s.Track.Path) {
		t.Fatalf("unshuffling should keep the track and restore queue order: %+v", s)
	}
}

func TestSkipsUnplayableTracks(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": short, "c": short})
	p.Play(queue("a", "broken", "c"), 0)
	eventually(t, "the queue to play out", func() bool { return len(f.log()) == 2 && !p.State().Playing })
	if got := f.log(); !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("played %v", got)
	}
}

func TestToggle(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long})
	p.Toggle() // nothing queued: no-op
	p.Play(queue("a"), 0)
	eventually(t, "playback", func() bool { return p.State().Playing })
	p.Toggle()
	eventually(t, "pause", func() bool { return !p.State().Playing })
	p.Toggle()
	eventually(t, "resume", func() bool { return p.State().Playing })
}

func TestSkippingStopsAtTheEndUnlessRepeatingAll(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": short})
	p.Play(queue("a", "broken"), 0)
	eventually(t, "the queue to stop", func() bool { return len(f.log()) == 1 && !p.State().Playing })
	time.Sleep(20 * time.Millisecond)
	if got := f.log(); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("repeat off played %v", got)
	}

	p2, f2 := newPlayer(t, map[string]int64{"a": short})
	p2.CycleRepeat() // all
	p2.Play(queue("a", "broken"), 0)
	eventually(t, "a wrap", func() bool { return len(f2.log()) >= 3 })
	if got := f2.log()[:3]; !slices.Equal(got, []string{"a", "a", "a"}) {
		t.Fatalf("repeat all played %v", got)
	}
}

// gated blocks every write until the test releases it.
type gated struct {
	nullOut
	entered chan struct{}
	gate    chan struct{}
}

func (g gated) Write([]int16) error {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.gate
	return nil
}

// failing is an Output whose writes fail.
type failing struct{ nullOut }

func (failing) Write([]int16) error { return errors.New("device gone") }

func TestStateReportsAnOutputError(t *testing.T) {
	f := &files{length: map[string]int64{"a": long}}
	p := New(failing{}, f.open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { p.Close() })
	p.Play(queue("a"), 0)
	eventually(t, "the error", func() bool { return p.State().Err != nil })
	if s := p.State(); s.Playing {
		t.Fatalf("state after the error = %+v", s)
	}
}

func TestStateDescribesTheNewTrackBeforeTheEngineSwitches(t *testing.T) {
	f := &files{length: map[string]int64{"a": long, "b": 96000}}
	out := gated{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	p := New(out, f.open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { p.Close() })
	t.Cleanup(func() { close(out.gate) }) // runs first, so Close isn't stuck in a write

	p.Play(queue("a", "b"), 0)
	<-out.entered // the engine is stuck mid-write on a
	p.Next()
	settled(t, p)
	s := p.State()
	if s.Track.Path != "b" || !s.Playing || s.Elapsed != 0 || s.Duration != 2*time.Second || s.Rate != 48000 {
		t.Fatalf("state once b opened = %+v", s)
	}
}

// countOut counts drops, which happen when audio is cut off.
type countOut struct {
	nullOut
	mu    sync.Mutex
	drops int
}

func (c *countOut) Drop() (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drops++
	return 0, nil
}

func TestTracksFollowWithoutAGap(t *testing.T) {
	f := &files{length: map[string]int64{"a": short, "b": short, "c": short}}
	out := &countOut{}
	p := New(out, f.open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer p.Close()
	p.Play(queue("a", "b", "c"), 0)
	eventually(t, "the queue to play out", func() bool { return len(f.log()) >= 3 && !p.State().Playing })
	time.Sleep(20 * time.Millisecond)
	if got := f.log(); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("opened %v; each track should be opened once, primed ahead of time", got)
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	if out.drops != 0 {
		t.Fatalf("%d drops between tracks; gapless playback shouldn't cut anything off", out.drops)
	}
	if s := p.State(); s.Track.Path != "c" {
		t.Fatalf("ended on %q, want c", s.Track.Path)
	}
}

func TestSilentTracksDontLoop(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"empty": 0})
	p.CycleRepeat()
	p.CycleRepeat() // one
	p.Play(queue("empty"), 0)
	time.Sleep(100 * time.Millisecond)
	if n := len(f.log()); n > 3 {
		t.Fatalf("a silent track under repeat one was opened %d times", n)
	}
	if p.State().Playing {
		t.Fatal("playback should stop on a silent track under repeat one")
	}
}

func TestSnapshotRestore(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	p.SetShuffle(true)
	p.Play(queue("a", "b", "c"), 1)
	p.Next()
	eventually(t, "playback", func() bool { return p.State().Elapsed > 0 })
	snap, ok := p.Snapshot()
	if !ok || snap.Frame <= 0 || !snap.Shuffle {
		t.Fatalf("Snapshot = %+v, %v", snap, ok)
	}
	want := p.State().Track

	q, f := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	q.Restore(snap)
	eventually(t, "the cue", func() bool { return q.State().Elapsed > 0 })
	s := q.State()
	if s.Track != want || s.Playing || !s.Shuffle {
		t.Fatalf("restored %+v, want %v paused with shuffle", s, want)
	}
	if got := f.log(); len(got) == 0 || got[0] != want.Path {
		t.Fatalf("restore opened %v first", got)
	}
	q.Toggle()
	eventually(t, "resume", func() bool { return q.State().Playing })
}

func TestSnapshotFractionCountsTheAlbum(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"1": long, "2": long, "3": long, "4": long})
	tracks := queue("1", "2", "3", "4")
	for i := range tracks {
		tracks[i].AlbumArtist, tracks[i].Album = "A", "X"
	}
	p.Play(tracks, 2)
	snap, _ := p.Snapshot()
	if snap.Fraction < 0.5 || snap.Fraction >= 0.75 || !snap.AlbumRun {
		t.Fatalf("third of four tracks just started: fraction %v, run %v; want [0.5, 0.75) of an album run", snap.Fraction, snap.AlbumRun)
	}
}

// album is n tracks of one album, named prefix1, prefix2 and so on.
func album(albumArtist, title, prefix string, n int) []Track {
	var ts []Track
	for i := range n {
		path := fmt.Sprintf("%s%d", prefix, i+1)
		ts = append(ts, Track{Path: path, Title: path, Artist: albumArtist, AlbumArtist: albumArtist, Album: title})
	}
	return ts
}

func playable(ts ...[]Track) map[string]int64 {
	lengths := map[string]int64{}
	for _, list := range ts {
		for _, t := range list {
			lengths[t.Path] = long
		}
	}
	return lengths
}

// Shuffled, an album's fraction counts the tracks played before the current
// one, not those before it in the album.
func TestSnapshotFractionFollowsShuffledPlay(t *testing.T) {
	x := album("A", "X", "x", 4)
	p, _ := newPlayer(t, playable(x))
	p.SetShuffle(true)
	p.Play(x, 2) // the third track plays first
	snap, _ := p.Snapshot()
	if snap.Fraction >= 0.25 || !snap.AlbumRun {
		t.Fatalf("first track played: fraction %v, run %v; want [0, 0.25) of an album run", snap.Fraction, snap.AlbumRun)
	}
	p.Next()
	if snap, _ = p.Snapshot(); snap.Fraction < 0.25 || snap.Fraction >= 0.5 {
		t.Fatalf("second track played: fraction %v, want [0.25, 0.5)", snap.Fraction)
	}
}

// Only an album queued as one, its tracks together, is a run whose progress
// is kept; songs played from a list of songs or added one at a time aren't.
func TestSnapshotAlbumRun(t *testing.T) {
	x, y := album("A", "X", "x", 3), album("B", "Y", "y", 2)
	songs := []Track{x[0], y[0], x[1], y[1], x[2]} // sorted by title, the albums interleave
	p, _ := newPlayer(t, playable(x, y))
	run := func(what string, want bool) {
		t.Helper()
		if snap, ok := p.Snapshot(); !ok || snap.AlbumRun != want {
			t.Fatalf("%s: album run %v, want %v", what, snap.AlbumRun, want)
		}
	}

	p.Play(songs, 1)
	run("a song from the list of songs", false)
	p.Play(songs[:1], 0)
	run("a single song", false)

	p.Play(x, 0)
	p.Enqueue(y)
	run("an album", true)
	p.PlayIndex(3)
	run("an album added after another", true)
	if snap, _ := p.Snapshot(); snap.Fraction >= 0.5 {
		t.Fatalf("the first of two tracks: fraction %v", snap.Fraction)
	}

	p.Play(x, 0)
	p.Enqueue(y[:1])
	p.PlayIndex(3)
	run("one song added after an album", false)
}

func TestNextTrackIsOpenedWhileTheCurrentOnePlays(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long})
	p.Play(queue("a", "b"), 0)
	eventually(t, "b to be primed", func() bool { return len(f.log()) == 2 })
	if s := p.State(); s.Track.Path != "a" || !s.Playing {
		t.Fatalf("state while b is primed = %+v, want a playing", s)
	}
}

func TestSilentTracksDontCarryOverToANewQueue(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"x": short, "silent": 0, "silent2": 0, "z": long})
	p.Play(queue("x", "silent"), 0)
	eventually(t, "the first queue to stop", func() bool { return len(f.log()) == 2 && !p.State().Playing })
	p.Play(queue("silent2", "z"), 0)
	eventually(t, "z to play", func() bool { return p.State().Track.Path == "z" && p.State().Playing })
}

func TestSnapshotRightAfterRestoreKeepsThePosition(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long, "b": long})
	p.Restore(Snapshot{Queue: queue("a", "b"), Order: []int{0, 1}, Pos: 1, Frame: 5000})
	snap, ok := p.Snapshot()
	if !ok || snap.Frame != 5000 || snap.Pos != 1 {
		t.Fatalf("Snapshot = %+v, %v; want frame 5000 at pos 1", snap, ok)
	}
}

func TestRestoreOfAnUnreadableTrackKeepsThePosition(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"b": long})
	p.Restore(Snapshot{Queue: queue("a", "b"), Order: []int{0, 1}, Pos: 0, Frame: 7000})
	if s := p.State(); !s.Queued || s.Playing || s.Track.Path != "a" {
		t.Fatalf("state = %+v, want a queued and stopped", s)
	}
	snap, ok := p.Snapshot()
	if !ok || snap.Frame != 7000 || snap.Pos != 0 {
		t.Fatalf("Snapshot = %+v, %v; want frame 7000 at pos 0", snap, ok)
	}
	p.Next()
	eventually(t, "b to play", func() bool { return p.State().Track.Path == "b" && p.State().Playing })
	if snap, _ := p.Snapshot(); snap.Frame >= 7000 {
		t.Fatalf("frame %d carried over to another track", snap.Frame)
	}
}

func TestRestoreAndCloseTogether(t *testing.T) {
	f := &files{length: map[string]int64{"a": long, "b": long}}
	p := New(nullOut{}, f.open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	snap := Snapshot{Queue: queue("a", "b"), Order: []int{0, 1}, Frame: 100}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.Restore(snap)
	}()
	p.Close()
	wg.Wait()
}

func TestUntaggedTracksAreAlbumsOfTheirOwn(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"1": long, "2": long, "3": long})
	p.Play(queue("1", "2", "3"), 2)
	snap, _ := p.Snapshot()
	if snap.Fraction < 0 || snap.Fraction >= 0.1 {
		t.Fatalf("fraction %v for an untagged track just started, want about 0", snap.Fraction)
	}
}

// lowRate is a source at a rate the engine can't play.
type lowRate struct{ tone }

func (*lowRate) SampleRate() int { return 4000 }

func TestUnsupportedRatesAreSkipped(t *testing.T) {
	var mu sync.Mutex
	var opened []string
	open := func(path string) (audio.Source, error) {
		mu.Lock()
		defer mu.Unlock()
		opened = append(opened, path)
		if path == "low" {
			return &lowRate{tone{n: long}}, nil
		}
		return &tone{n: long}, nil
	}
	p := New(nullOut{}, open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { p.Close() })
	p.Play(queue("low", "ok"), 0)
	eventually(t, "the skip", func() bool { return p.State().Track.Path == "ok" && p.State().Playing })
}

func TestLevelledTracksPlaySeekAndEnd(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": short, "b": short, "c": short})
	p.SetLevelling(true)
	tracks := queue("a", "b", "c")
	for i := range tracks {
		tracks[i].Gain, tracks[i].Peak, tracks[i].HasGain = -3, 0.8, true
	}
	p.Play(tracks, 0)
	eventually(t, "the queue to play out", func() bool { return len(f.log()) >= 3 && !p.State().Playing })
	p.Prev()
	eventually(t, "b to play again", func() bool { return p.State().Track.Path == "b" })
	snap, _ := p.Snapshot()
	q, _ := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	q.SetLevelling(true)
	snap.Frame = 1000
	q.Restore(snap)
	eventually(t, "the cue", func() bool { return q.State().Elapsed > 0 })
}

func TestSettingLevellingRePrimesTheNextTrack(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long})
	p.Play(queue("a", "b"), 0)
	eventually(t, "b to be primed", func() bool { return len(f.log()) == 2 })
	p.SetLevelling(true)
	eventually(t, "b to be opened again", func() bool { return len(f.log()) == 3 })
}

func TestStateAfterRestoreShowsTheRestoredPosition(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long})
	p.Restore(Snapshot{Queue: queue("a"), Order: []int{0}, Frame: 5 * 48000})
	settled(t, p)
	if s := p.State(); s.Elapsed != 5*time.Second || s.Playing {
		t.Fatalf("state = %+v, want paused at 5 s", s)
	}
}

func paths(ts []Track) (s []string) {
	for _, t := range ts {
		s = append(s, t.Path)
	}
	return s
}

func TestEnqueueAppendsWithoutPlaying(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	p.Enqueue(queue("a", "b"))
	if s := p.State(); !s.Queued || s.Playing || len(f.log()) != 0 {
		t.Fatalf("enqueueing into an empty queue: %+v, opened %v", s, f.log())
	}
	p.Toggle()
	eventually(t, "playback", func() bool { return p.State().Playing })
	v := p.State().Version
	p.Enqueue(queue("c"))
	got, pos := p.Queue(nil)
	if !slices.Equal(paths(got), []string{"a", "b", "c"}) || pos != 0 || p.State().Version == v {
		t.Fatalf("queue %v at %d, version %d (was %d)", paths(got), pos, p.State().Version, v)
	}
}

func TestRemoveAndMove(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long, "d": long})
	p.Play(queue("a", "b", "c", "d"), 1) // playing b
	p.Remove(1)                          // the current track stays
	p.Remove(0)                          // a, before it
	got, pos := p.Queue(nil)
	if !slices.Equal(paths(got), []string{"b", "c", "d"}) || pos != 0 {
		t.Fatalf("after removing a: %v at %d", paths(got), pos)
	}
	p.Move(2, 1) // d before c
	got, _ = p.Queue(nil)
	if !slices.Equal(paths(got), []string{"b", "d", "c"}) {
		t.Fatalf("after moving d up: %v", paths(got))
	}
	p.Move(1, 0) // nothing moves onto the current track's place
	if got, _ = p.Queue(nil); !slices.Equal(paths(got), []string{"b", "d", "c"}) {
		t.Fatalf("a move onto the current track changed the queue: %v", paths(got))
	}
	p.PlayIndex(2)
	if s := p.State(); s.Track.Path != "c" || s.Index != 2 {
		t.Fatalf("PlayIndex(2) = %+v", s)
	}
}

func TestPlayFromResumesPartWay(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long, "b": long})
	p.PlayFrom(queue("a", "b"), 1, 48000*30)
	eventually(t, "playback", func() bool { return p.State().Elapsed >= 30*time.Second })
	if s := p.State(); s.Track.Path != "b" {
		t.Fatalf("PlayFrom played %q", s.Track.Path)
	}
}

func TestEditsRePrimeTheNextTrack(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": short, "b": short})
	p.Play(queue("a"), 0)
	p.Enqueue(queue("b")) // b should follow a without a gap
	eventually(t, "both to play", func() bool { return len(f.log()) >= 2 && !p.State().Playing })
	time.Sleep(20 * time.Millisecond)
	if got := f.log(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("opened %v; b should be primed once and play", got)
	}
}

func TestVersionFollowsTheQueue(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	v := p.State().Version
	changed := func(what string, want bool) {
		t.Helper()
		s := p.State()
		if snap, ok := p.Snapshot(); ok && snap.Version != s.Version {
			t.Fatalf("%s: snapshot version %d, state %d", what, snap.Version, s.Version)
		}
		if (s.Version != v) != want {
			t.Fatalf("%s: version %d, was %d; changed should be %v", what, s.Version, v, want)
		}
		v = s.Version
	}
	p.Play(queue("a", "b", "c"), 0)
	changed("Play", true)
	p.PlayIndex(1)
	p.Next()
	p.CycleRepeat()
	p.Move(0, 2) // the current track and those before it stay put
	p.Remove(2)
	changed("playing, repeating and refused edits", false)
	p.SetShuffle(true)
	changed("SetShuffle", true)
	p.SetShuffle(true)
	changed("SetShuffle to what it was", false)
	p.Remove(1) // shuffled, the current track is first
	changed("Remove", true)

	q, _ := newPlayer(t, map[string]int64{"a": long})
	v = q.State().Version
	q.Restore(Snapshot{Queue: queue("a"), Order: []int{0}})
	if q.State().Version == v {
		t.Fatal("Restore kept the version")
	}
}

func TestPlayedOutAlbumIsFinished(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"1": short, "2": short, "3": short})
	tracks := queue("1", "2", "3")
	for i := range tracks {
		tracks[i].AlbumArtist, tracks[i].Album = "A", "X"
	}
	p.Play(tracks, 0)
	eventually(t, "the queue to play out", func() bool { return len(f.log()) == 3 && !p.State().Playing })
	snap, ok := p.Snapshot()
	if !ok || snap.Fraction != 1 || snap.Frame != short {
		t.Fatalf("Snapshot = %+v, %v; want fraction 1 at frame %d", snap, ok, short)
	}
}

func TestLevelGain(t *testing.T) {
	tr := Track{Gain: -6, Peak: 0.8, HasGain: true, AlbumGain: -8, AlbumPeak: 0.9, HasAlbumGain: true}
	if db, peak, ok := levelGain(tr, true); !ok || db != -8+levellingOffset || peak != 0.9 {
		t.Fatalf("album mode = %v, %v, %v; want the album gain", db, peak, ok)
	}
	if db, _, _ := levelGain(tr, false); db != -6+levellingOffset {
		t.Fatalf("track mode = %v; want the track gain", db)
	}
	tr.HasAlbumGain = false
	if db, _, _ := levelGain(tr, true); db != -6+levellingOffset {
		t.Fatalf("album mode without an album gain = %v; want the track gain", db)
	}
	if _, _, ok := levelGain(Track{}, true); ok {
		t.Fatal("an untagged track has no gain")
	}
}

func (p *Player) albumMode() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.albumModeLocked()
}

func TestAlbumModeIsOneAlbumUnshuffled(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"1": long, "2": long, "x": long})
	album := queue("1", "2")
	for i := range album {
		album[i].AlbumArtist, album[i].Album = "A", "X"
	}
	p.Play(album, 0)
	if !p.albumMode() {
		t.Fatal("one album in order should level by album")
	}
	p.SetShuffle(true)
	if p.albumMode() {
		t.Fatal("shuffled, tracks level by track")
	}
	p.SetShuffle(false)
	p.Enqueue(queue("x"))
	if p.albumMode() {
		t.Fatal("a queue mixing albums levels by track")
	}
}

func TestEnqueueAfterPlayOutStartsTheNewTracks(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": short, "b": long})
	p.Play(queue("a"), 0)
	eventually(t, "the queue to play out", func() bool { return len(f.log()) == 1 && !p.State().Playing })
	p.Enqueue(queue("b"))
	if s := p.State(); s.Track.Path != "b" || s.Index != 1 {
		t.Fatalf("after Enqueue = %+v; want b, the first track added", s)
	}
	p.Toggle()
	eventually(t, "b to play", func() bool { return p.State().Playing })
	settled(t, p)
	if got := f.log(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("opened %v; Toggle should start b, not replay a", got)
	}
}

// primed waits for the engine to hold a track primed after cur.
func primed(t *testing.T, p *Player) {
	t.Helper()
	eventually(t, "the next track to be primed", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.next != nil
	})
}

// settled waits for the current track to have opened, or failed to.
func settled(t *testing.T, p *Player) {
	t.Helper()
	eventually(t, "the track to open", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return !p.loading
	})
}

func opens(f *files, path string) int {
	n := 0
	for _, o := range f.log() {
		if o == path {
			n++
		}
	}
	return n
}

func TestUnrelatedEditsKeepThePrimedTrack(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long, "d": long})
	p.Play(queue("a", "b", "c"), 0)
	primed(t, p)
	v := p.State().Version
	p.Enqueue(queue("d"))
	p.Remove(2) // c, after the primed b
	time.Sleep(20 * time.Millisecond)
	if n := opens(f, "b"); n != 1 {
		t.Fatalf("b opened %d times; edits after it should keep it primed", n)
	}
	if p.State().Version != v+2 {
		t.Fatalf("version = %d, want %d", p.State().Version, v+2)
	}
	p.mu.Lock()
	next, nextPos := p.next, p.nextPos
	p.mu.Unlock()
	if next == nil || nextPos != 1 {
		t.Fatalf("primed %v at %d; want b still primed at 1", next, nextPos)
	}
}

func TestEditsToTheNextTrackReprime(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	p.Play(queue("a", "b", "c"), 0)
	primed(t, p)
	p.Remove(1)
	eventually(t, "c to be primed", func() bool { return opens(f, "c") == 1 })
	if opens(f, "b") != 1 {
		t.Fatalf("opened %v", f.log())
	}
}

func TestPrimedTrackIsKeptByEntryNotPath(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long})
	p.Play(queue("a", "b", "b"), 0)
	primed(t, p)
	p.Remove(2) // the second b; the primed one is the first
	p.Enqueue(queue("a"))
	time.Sleep(20 * time.Millisecond)
	if n := opens(f, "b"); n != 1 {
		t.Fatalf("b opened %d times", n)
	}
}

func TestSamePathAfterTheRemovedPrimedEntryIsReprimed(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long})
	p.Play(queue("a", "b", "b"), 0)
	primed(t, p)
	p.Remove(1) // the primed entry; the other b is a different entry with the same path
	eventually(t, "the second b to be primed", func() bool { return opens(f, "b") == 2 })
}

// A kept source takes over at its new position in the order.
func TestKeptPrimedTrackTakesOverGaplessly(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"x": long, "a": 150000, "b": long})
	p.Play(queue("x", "a", "b"), 1)
	primed(t, p)
	p.Remove(0) // b's position in the order goes from 2 to 1
	eventually(t, "b to take over", func() bool { return p.State().Track.Path == "b" })
	if s := p.State(); s.Index != 1 {
		t.Fatalf("after the hand-over = %+v; want b at index 1", s)
	}
	if n := opens(f, "b"); n != 1 {
		t.Fatalf("b opened %d times; the kept source should have played", n)
	}
}

func TestShuffleToggledWhilePrimedStillHandsOver(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"x": long, "a": 150000, "b": long})
	p.Play(queue("x", "a", "b"), 1)
	primed(t, p)
	p.SetShuffle(true)
	p.SetShuffle(false)
	eventually(t, "b to take over", func() bool { return p.State().Track.Path == "b" })
	if s := p.State(); s.Index != 2 {
		t.Fatalf("after the hand-over = %+v; want b at index 2", s)
	}
}

func TestSwitchingLevellingModeReprimes(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"1": long, "2": long, "3": long, "x": long})
	album := queue("1", "2", "3")
	for i := range album {
		album[i].AlbumArtist, album[i].Album = "A", "X"
	}
	p.Play(album, 0)
	primed(t, p)
	p.Enqueue(queue("x")) // the queue is no longer one album
	eventually(t, "2 to be opened again", func() bool { return opens(f, "2") == 2 })
}

func TestPlayFromShowsItsStartBeforeTheEngine(t *testing.T) {
	f := &files{length: map[string]int64{"a": long, "b": long}}
	out := gated{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	p := New(out, f.open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { p.Close() })
	t.Cleanup(func() { close(out.gate) })

	p.Play(queue("a"), 0)
	<-out.entered // the engine is stuck mid-write on a
	p.PlayFrom(queue("b"), 0, 96000)
	if snap, _ := p.Snapshot(); snap.Frame != 96000 {
		t.Fatalf("snapshot frame %d right after PlayFrom, want 96000", snap.Frame)
	}
	settled(t, p)
	if s := p.State(); s.Track.Path != "b" || s.Elapsed != 2*time.Second {
		t.Fatalf("state once b opened = %+v, want b at 2s", s)
	}
	if snap, _ := p.Snapshot(); snap.Frame != 96000 {
		t.Fatalf("snapshot frame %d once b opened, want 96000", snap.Frame)
	}
}

func TestSeek(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": 48000 * 60})
	p.Seek(time.Second) // nothing queued: nothing happens
	p.Play(queue("a"), 0)
	settled(t, p)
	p.Seek(30 * time.Second)
	if s := p.State(); s.Elapsed < 30*time.Second {
		t.Fatalf("elapsed %v right after seeking to 30s", s.Elapsed)
	}
	eventually(t, "the seek", func() bool {
		e := p.State().Elapsed
		return e >= 30*time.Second && e < 40*time.Second
	})
	p.Seek(2 * time.Second)
	eventually(t, "the seek back", func() bool { return p.State().Elapsed < 10*time.Second })
	p.Seek(time.Hour)
	if s := p.State(); s.Elapsed > time.Minute {
		t.Fatalf("seeking past the end shows %v of a 1 min track", s.Elapsed)
	}
}

func TestRemovingTheNextTrackPrimesTheOneAfter(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	p.Play(queue("a", "b", "c"), 0)
	eventually(t, "b to be primed", func() bool { return slices.Contains(f.log(), "b") })
	p.Remove(1)
	eventually(t, "c to be primed", func() bool { return slices.Contains(f.log(), "c") })
	p.Next()
	if s := p.State(); s.Track.Path != "c" || s.Count != 2 {
		t.Fatalf("after removing b and skipping: %+v", s)
	}
}

func TestMovingATrackUpToNextPrimesIt(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long, "d": long})
	p.Play(queue("a", "b", "c", "d"), 0)
	eventually(t, "b to be primed", func() bool { return slices.Contains(f.log(), "b") })
	p.Move(3, 1)
	eventually(t, "d to be primed", func() bool { return slices.Contains(f.log(), "d") })
	p.Next()
	if s := p.State(); s.Track.Path != "d" {
		t.Fatalf("after moving d up and skipping, playing %q", s.Track.Path)
	}
}

func TestRemoveWithShuffle(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long, "d": long, "e": long})
	p.SetShuffle(true)
	p.Play(queue("a", "b", "c", "d", "e"), 2)
	before, pos := p.Queue(nil)
	gone := before[pos+2].Path
	p.Remove(pos + 2)
	after, pos2 := p.Queue(nil)
	if len(after) != 4 || pos2 != pos || after[pos2].Path != before[pos].Path {
		t.Fatalf("after removing: %d tracks, current %d (%q); before: current %d (%q)",
			len(after), pos2, after[pos2].Path, pos, before[pos].Path)
	}
	seen := map[string]bool{}
	for _, tr := range after {
		if tr.Path == gone || seen[tr.Path] {
			t.Fatalf("queue after removing %q: %v", gone, after)
		}
		seen[tr.Path] = true
	}
}

func TestAPendingTrackWaitsForItsDownload(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"/m/v1.opus": long, "/m/b.flac": long})
	p.Play([]Track{{VideoID: "v1", Title: "Night Drive Theory"}, {Path: "/m/b.flac", Title: "b"}}, 0)
	st := p.State()
	if !st.Waiting || st.Playing || !st.Queued || st.Track.VideoID != "v1" || !st.Track.Pending() {
		t.Fatalf("state %+v", st)
	}
	time.Sleep(20 * time.Millisecond)
	if len(f.log()) != 0 {
		t.Fatalf("opened %q while waiting", f.log())
	}
	p.Resolve("v1", "/m/v1.opus")
	eventually(t, "it plays", func() bool {
		s := p.State()
		return s.Playing && !s.Waiting && s.Track.Path == "/m/v1.opus"
	})
	eventually(t, "the next primed", func() bool { return slices.Contains(f.log(), "/m/b.flac") })
}

func TestAPendingNextIsPrimedWhenItLands(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"/m/a.flac": long, "/m/v2.opus": long})
	p.Play([]Track{{Path: "/m/a.flac"}, {VideoID: "v2"}}, 0)
	time.Sleep(20 * time.Millisecond)
	if got := f.log(); !slices.Equal(got, []string{"/m/a.flac"}) {
		t.Fatalf("opened %q", got)
	}
	p.Resolve("v2", "/m/v2.opus")
	eventually(t, "v2 primed", func() bool { return slices.Contains(f.log(), "/m/v2.opus") })
}

func TestPlaybackReachesAPendingTrack(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": short, "/m/v2.opus": long})
	p.Play([]Track{{Path: "/m/a.flac"}, {VideoID: "v2"}}, 0)
	eventually(t, "waiting on v2", func() bool { s := p.State(); return s.Waiting && s.Index == 1 })
	p.Resolve("v2", "/m/v2.opus")
	eventually(t, "v2 plays", func() bool { s := p.State(); return s.Playing && s.Track.Path == "/m/v2.opus" })
}

func TestDroppingAFailedDownload(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/b.flac": long})
	p.Play([]Track{{VideoID: "v1"}, {Path: "/m/b.flac"}, {VideoID: "v1"}}, 0)
	if n := p.Drop("v1"); n != 2 {
		t.Fatalf("dropped %d", n)
	}
	eventually(t, "b plays", func() bool {
		s := p.State()
		return s.Playing && s.Track.Path == "/m/b.flac" && s.Count == 1
	})
	if n := p.Drop("v1"); n != 0 {
		t.Fatalf("dropped %d more", n)
	}
	p.Play([]Track{{VideoID: "v9"}}, 0)
	p.Drop("v9")
	if s := p.State(); s.Queued || s.Waiting {
		t.Fatalf("dropping the only track: %+v", s)
	}
}

func TestARestoredPendingTrackIsCuedWhenItLands(t *testing.T) {
	snap := Snapshot{Queue: []Track{{VideoID: "v1", Title: "Night Drive Theory"}}, Order: []int{0}, Frame: 4800}
	p, _ := newPlayer(t, map[string]int64{"/m/v1.opus": long})
	p.Restore(snap)
	if s := p.State(); !s.Waiting || s.Playing {
		t.Fatalf("restored %+v", s)
	}
	p.Resolve("v1", "/m/v1.opus")
	settled(t, p)
	if s := p.State(); s.Waiting || s.Playing || s.Track.Path != "/m/v1.opus" || s.Elapsed != 100*time.Millisecond {
		t.Fatalf("landed %+v", s)
	}
	// Play while it waits: it plays once it lands.
	q, _ := newPlayer(t, map[string]int64{"/m/v1.opus": long})
	q.Restore(snap)
	q.Toggle()
	q.Resolve("v1", "/m/v1.opus")
	eventually(t, "it plays", func() bool { return q.State().Playing })
}

func TestDroppingTheCurrentPendingTrackAfterThePlayerStoppedOnIt(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": short, "/m/n.flac": long})
	p.Play([]Track{{Path: "/m/a.flac"}}, 0)
	eventually(t, "played out", func() bool { return !p.State().Playing })
	p.Enqueue([]Track{{VideoID: "v1"}})
	if n := p.Drop("v1"); n != 1 {
		t.Fatalf("dropped %d", n)
	}
	if s := p.State(); !s.Queued || s.Count != 1 || s.Waiting || s.Playing {
		t.Fatalf("state %+v", s)
	}
	p.Enqueue([]Track{{Path: "/m/n.flac"}})
	p.Toggle()
	eventually(t, "n plays", func() bool { s := p.State(); return s.Playing && s.Track.Path == "/m/n.flac" })
}

func TestDroppingPendingTracksOneAfterAnother(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": long})
	p.Play([]Track{{Path: "/m/a.flac"}, {VideoID: "v1"}, {VideoID: "v2"}}, 0)
	p.Next()
	if s := p.State(); !s.Waiting || s.Track.VideoID != "v1" {
		t.Fatalf("state %+v", s)
	}
	p.Drop("v1")
	if s := p.State(); !s.Waiting || s.Track.VideoID != "v2" {
		t.Fatalf("after v1: %+v", s)
	}
	p.Drop("v2")
	if s := p.State(); !s.Queued || s.Count != 1 || s.Waiting || s.Playing || s.Track.Path != "/m/a.flac" {
		t.Fatalf("after v2: %+v", s)
	}
}

func TestDroppingTheLastTrackLeavesTheQueuePlayedOut(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": short, "/m/n.flac": long})
	p.Play([]Track{{Path: "/m/a.flac", Album: "X", AlbumArtist: "A"}, {VideoID: "v1", Album: "X", AlbumArtist: "A"}}, 0)
	eventually(t, "waiting", func() bool { return p.State().Waiting })
	p.Drop("v1")
	snap, ok := p.Snapshot()
	if !ok || snap.Fraction != 1 || snap.Frame != short {
		t.Fatalf("snapshot %+v, %v", snap, ok)
	}
	p.Enqueue([]Track{{Path: "/m/n.flac"}})
	p.Toggle()
	eventually(t, "n plays", func() bool { s := p.State(); return s.Playing && s.Track.Path == "/m/n.flac" })
}

func TestDroppingTheLastTrackWithRepeatAllWraps(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"/m/a.flac": short})
	p.CycleRepeat()
	p.Play([]Track{{Path: "/m/a.flac"}, {VideoID: "v1"}}, 0)
	eventually(t, "waiting", func() bool { return p.State().Waiting })
	p.Drop("v1")
	eventually(t, "a again", func() bool { return opens(f, "/m/a.flac") >= 2 })
}

func TestDroppingBeforeTheCurrentTrack(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": long, "/m/b.flac": long})
	p.Play([]Track{{Path: "/m/a.flac"}, {VideoID: "v1"}, {Path: "/m/b.flac"}}, 2)
	if n := p.Drop("v1"); n != 1 {
		t.Fatalf("dropped %d", n)
	}
	eventually(t, "b still plays", func() bool {
		s := p.State()
		return s.Playing && s.Track.Path == "/m/b.flac" && s.Index == 1 && s.Count == 2
	})
}

func TestDroppingTheLastTrackWhilePlayingEarlierOne(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": long})
	p.Play([]Track{{Path: "/m/a.flac"}, {VideoID: "v1"}}, 0)
	p.Drop("v1")
	if s := p.State(); !s.Playing || s.Count != 1 || s.Track.Path != "/m/a.flac" {
		t.Fatalf("state %+v", s)
	}
}

func TestAPendingTrackSnapshotKeepsItsPosition(t *testing.T) {
	snap := Snapshot{Queue: []Track{{VideoID: "v1"}}, Order: []int{0}, Frame: 4800}
	p, _ := newPlayer(t, nil)
	p.Restore(snap)
	p.Toggle()
	if got, ok := p.Snapshot(); !ok || got.Frame != 4800 {
		t.Fatalf("snapshot %+v, %v", got, ok)
	}
	q, _ := newPlayer(t, nil)
	q.PlayFrom([]Track{{VideoID: "v1"}}, 0, 9600)
	if got, ok := q.Snapshot(); !ok || got.Frame != 9600 {
		t.Fatalf("snapshot %+v, %v", got, ok)
	}
}

func TestResolvingAnotherVideoKeepsTheNextPrimed(t *testing.T) {
	p, f := newPlayer(t, map[string]int64{"/m/a.flac": long, "/m/b.flac": long, "/m/v1.opus": long})
	p.Play([]Track{{Path: "/m/a.flac"}, {Path: "/m/b.flac"}, {VideoID: "v1"}}, 0)
	eventually(t, "b primed", func() bool { return opens(f, "/m/b.flac") == 1 })
	p.Resolve("v1", "/m/v1.opus")
	p.Resolve("v1", "")
	time.Sleep(30 * time.Millisecond)
	if n := opens(f, "/m/b.flac"); n != 1 {
		t.Fatalf("b opened %d times", n)
	}
}

func TestResolvingWithNoFileChangesNothing(t *testing.T) {
	p, _ := newPlayer(t, nil)
	p.Play([]Track{{VideoID: "v1"}}, 0)
	p.Resolve("v1", "")
	if s := p.State(); !s.Waiting || !s.Track.Pending() {
		t.Fatalf("state %+v", s)
	}
}

func TestMovingAroundAPendingTrack(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": long, "/m/b.flac": long, "/m/v1.opus": long})
	p.Play([]Track{{Path: "/m/a.flac"}, {VideoID: "v1"}, {Path: "/m/b.flac"}}, 0)
	p.Next()
	if s := p.State(); !s.Waiting || s.Index != 1 {
		t.Fatalf("next: %+v", s)
	}
	p.Next()
	if s := p.State(); s.Waiting || s.Track.Path != "/m/b.flac" {
		t.Fatalf("next again: %+v", s)
	}
	p.Prev()
	if s := p.State(); !s.Waiting || s.Playing || s.Index != 1 {
		t.Fatalf("prev: %+v", s)
	}
	p.Prev()
	eventually(t, "a plays", func() bool { s := p.State(); return s.Playing && !s.Waiting && s.Track.Path == "/m/a.flac" })
	p.Next()
	p.SetShuffle(true)
	if s := p.State(); !s.Waiting || s.Track.VideoID != "v1" {
		t.Fatalf("shuffle: %+v", s)
	}
	p.Resolve("v1", "/m/v1.opus")
	eventually(t, "v1 plays", func() bool { s := p.State(); return s.Playing && s.Track.Path == "/m/v1.opus" })
}

func TestDroppingARestoredWaitingTrackKeepsTheQueueCued(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": long})
	p.Restore(Snapshot{Queue: []Track{{VideoID: "v1"}, {Path: "/m/a.flac"}}, Order: []int{0, 1}, Frame: 4800})
	p.Drop("v1")
	time.Sleep(20 * time.Millisecond)
	if s := p.State(); s.Playing || s.Waiting || s.Track.Path != "/m/a.flac" || s.Elapsed != 0 {
		t.Fatalf("after drop: %+v", s)
	}
	p.Toggle()
	eventually(t, "a plays on request", func() bool { return p.State().Playing })

	// The entry that follows is itself pending: still waiting, still cued.
	q, _ := newPlayer(t, map[string]int64{"/m/v2.opus": long})
	q.Restore(Snapshot{Queue: []Track{{VideoID: "v1"}, {VideoID: "v2"}}, Order: []int{0, 1}, Frame: 4800})
	q.Drop("v1")
	if s := q.State(); !s.Waiting || s.Playing || s.Track.VideoID != "v2" {
		t.Fatalf("pending follower: %+v", s)
	}
	q.Resolve("v2", "/m/v2.opus")
	if s := q.State(); s.Playing || s.Waiting || s.Track.Path != "/m/v2.opus" || s.Elapsed != 0 {
		t.Fatalf("landed: %+v", s)
	}

	// With repeat all, the wrap is cued too.
	r, _ := newPlayer(t, map[string]int64{"/m/a.flac": long})
	r.CycleRepeat()
	r.Restore(Snapshot{Queue: []Track{{Path: "/m/a.flac"}, {VideoID: "v1"}}, Order: []int{0, 1}, Pos: 1, Repeat: RepeatAll})
	r.Drop("v1")
	time.Sleep(20 * time.Millisecond)
	if s := r.State(); s.Playing || s.Waiting || s.Track.Path != "/m/a.flac" {
		t.Fatalf("wrapped: %+v", s)
	}
}

func TestAPlayedOutDropReportsAFrameOfTheRightTrack(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/a.flac": long, "/m/b.flac": long})
	p.Play([]Track{{Path: "/m/a.flac"}, {Path: "/m/b.flac"}, {VideoID: "v1"}}, 0)
	p.PlayIndex(2)
	p.Drop("v1")
	snap, ok := p.Snapshot()
	if !ok || snap.Pos != 1 || snap.Frame != 0 {
		t.Fatalf("snapshot %+v, %v", snap, ok)
	}
}

// A restored pending song is stopped on, waiting for its download; asked to
// play, it waits without being stopped on.
func TestAWaitingTrackIsCuedUntilPlayed(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"/m/v1.opus": long})
	p.Restore(Snapshot{Queue: []Track{{VideoID: "v1"}}, Order: []int{0}})
	if s := p.State(); !s.Waiting || !s.Cued {
		t.Fatalf("restored: waiting %v, cued %v", s.Waiting, s.Cued)
	}
	p.Toggle()
	if s := p.State(); !s.Waiting || s.Cued {
		t.Fatalf("played: waiting %v, cued %v", s.Waiting, s.Cued)
	}
}

// Restore says whether it took the snapshot: not when something is queued.
func TestRestoreReportsWhetherItTookTheSnapshot(t *testing.T) {
	snap := Snapshot{Queue: queue("a"), Order: []int{0}}
	p, _ := newPlayer(t, map[string]int64{"a": long})
	if !p.Restore(snap) {
		t.Fatal("a fresh player refused the snapshot")
	}
	if p.Restore(snap) {
		t.Fatal("a player with a queue took a snapshot")
	}
	if q, _ := newPlayer(t, map[string]int64{"a": long}); q.Restore(Snapshot{}) {
		t.Fatal("an empty snapshot was taken")
	}
}

// within fails the test unless f returns within a second.
func within(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("%s waited for a file to open", what)
	}
}

// watched is a source that records being closed.
type watched struct {
	tone
	closed *atomic.Bool
}

func (w *watched) Close() error {
	w.closed.Store(true)
	return nil
}

// slowPlayer is a player whose opens of "slow" wait until release is called,
// as on a slow card, and report closing their sources to closed. Other paths
// open at once, as tones of their lengths, or long ones.
func slowPlayer(t *testing.T, lengths map[string]int64) (p *Player, release func(), closed *atomic.Bool) {
	t.Helper()
	ch := make(chan struct{})
	release = sync.OnceFunc(func() { close(ch) })
	closed = &atomic.Bool{}
	open := func(path string) (audio.Source, error) {
		if path != "slow" {
			return &tone{n: cmp.Or(lengths[path], long)}, nil
		}
		<-ch
		return &watched{tone{n: long}, closed}, nil
	}
	p = New(nullOut{}, open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { p.Close() })
	t.Cleanup(release) // runs first, so Close isn't stuck behind an open
	return p, release, closed
}

// A file slow to open holds up neither the call that plays it nor anything
// asking the player meanwhile, as the screens do every frame.
func TestASlowFileHoldsNothingUp(t *testing.T) {
	p, release, closed := slowPlayer(t, nil)
	within(t, "Play", func() { p.Play([]Track{{Path: "slow", Duration: time.Minute}, {Path: "b"}}, 0) })
	var s State
	within(t, "State", func() { s = p.State() })
	if s.Track.Path != "slow" || !s.Playing || s.Loading || s.Rate != 0 || s.Duration != time.Minute {
		t.Fatalf("while it opens: %+v", s)
	}
	within(t, "Snapshot", func() { p.Snapshot() })
	within(t, "Next", p.Next)
	eventually(t, "b to play", func() bool { s := p.State(); return s.Track.Path == "b" && s.Rate == 48000 && s.Elapsed > 0 })
	release()
	eventually(t, "the slow file, opened too late, to be closed", closed.Load)
	if s := p.State(); s.Track.Path != "b" || !s.Playing {
		t.Fatalf("after the slow file opened: %+v", s)
	}
}

// A track that ends with nothing primed opens the next off the lock too: the
// engine reports the end on its own goroutine while the screens ask for the
// state.
func TestATrackEndingOpensTheNextOffTheLock(t *testing.T) {
	p, release, _ := slowPlayer(t, map[string]int64{"a": short})
	p.Play(queue("a", "slow"), 0) // a ends while slow is still being primed
	var s State
	eventually(t, "a to end", func() bool {
		within(t, "State", func() { s = p.State() })
		return s.Index == 1
	})
	if !s.Playing || s.Rate != 0 {
		t.Fatalf("while slow opens: %+v", s)
	}
	release()
	eventually(t, "slow to play", func() bool { s := p.State(); return s.Rate == 48000 && s.Elapsed > 0 })
}

// Restoring a queue cues its track without waiting for the file to open.
func TestRestoreDoesntWaitForTheFile(t *testing.T) {
	p, release, _ := slowPlayer(t, nil)
	within(t, "Restore", func() { p.Restore(Snapshot{Queue: queue("slow"), Order: []int{0}, Frame: 48000}) })
	var s State
	within(t, "State", func() { s = p.State() })
	if s.Track.Path != "slow" || s.Playing || !s.Cued {
		t.Fatalf("while it opens: %+v", s)
	}
	if snap, _ := p.Snapshot(); snap.Frame != 48000 {
		t.Fatalf("snapshot frame %d while it opens, want 48000", snap.Frame)
	}
	release()
	settled(t, p)
	if s := p.State(); s.Playing || s.Elapsed != time.Second {
		t.Fatalf("once open: %+v, want it cued at 1 s", s)
	}
}

// Paused while it opens, a file is cued rather than played.
func TestPausedWhileAFileOpens(t *testing.T) {
	p, release, _ := slowPlayer(t, nil)
	p.Play(queue("slow"), 0)
	p.Toggle()
	if s := p.State(); s.Playing || !s.Cued {
		t.Fatalf("paused while it opens: %+v", s)
	}
	release()
	settled(t, p)
	time.Sleep(50 * time.Millisecond)
	if s := p.State(); s.Playing || s.Rate != 48000 || s.Elapsed != 0 {
		t.Fatalf("once open: %+v, want it cued", s)
	}
	p.Toggle()
	eventually(t, "it to play", func() bool { return p.State().Elapsed > 0 })
}

// With repeat all and no file that opens, each is tried once and playback
// stops, rather than going round for ever.
func TestNothingOpensWithRepeatAll(t *testing.T) {
	var tried atomic.Int32
	open := func(string) (audio.Source, error) {
		tried.Add(1)
		return nil, errors.New("unreadable")
	}
	p := New(nullOut{}, open, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { p.Close() })
	p.CycleRepeat()
	p.Play(queue("x", "y", "z"), 1)
	settled(t, p)
	time.Sleep(20 * time.Millisecond)
	if n := tried.Load(); n != 3 || p.State().Playing {
		t.Fatalf("tried %d opens, playing %v; want each tried once, then a stop", n, p.State().Playing)
	}
}

func TestStateDoesNotAllocate(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a.flac": long})
	p.Play(queue("a.flac"), 0)
	settled(t, p)
	if s := p.State(); s.Format != "FLAC" {
		t.Fatalf("format %q, want FLAC", s.Format)
	}
	if n := testing.AllocsPerRun(100, func() { p.State() }); n != 0 {
		t.Fatalf("State allocates %v times a call", n)
	}
}

func TestUpcoming(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": long, "b": long, "c": long})
	next := func(what, want string) {
		t.Helper()
		got, ok := p.Upcoming()
		if ok != (want != "") || got.Path != want {
			t.Fatalf("%s: upcoming %q, %v; want %q", what, got.Path, ok, want)
		}
	}
	next("nothing queued", "")
	p.Play(queue("a", "b", "c"), 1)
	next("in order", "c")
	p.Next()
	next("the last", "")
	p.CycleRepeat()
	next("the last, repeating all", "a")
	p.CycleRepeat()
	next("repeating one", "c")
	p.CycleRepeat()
	p.SetShuffle(true)
	q, pos := p.Queue(nil)
	next("shuffled", q[pos+1].Path)
}

func TestChapterTarget(t *testing.T) {
	starts := []time.Duration{0, 10 * time.Minute, 20 * time.Minute}
	for _, c := range []struct {
		name string
		at   time.Duration
		dir  int
		to   time.Duration
		ok   bool
	}{
		{"next from the first", time.Minute, 1, 10 * time.Minute, true},
		{"next from the middle", 15 * time.Minute, 1, 20 * time.Minute, true},
		{"next from the last", 25 * time.Minute, 1, 0, false},
		{"prev well into a chapter", 15 * time.Minute, -1, 10 * time.Minute, true},
		{"prev just into a chapter", 10*time.Minute + time.Second, -1, 0, true},
		{"prev well into the first", time.Minute, -1, 0, true},
		{"prev at the very start", time.Second, -1, 0, false},
	} {
		if to, ok := chapterTarget(starts, c.at, c.dir); to != c.to || ok != c.ok {
			t.Errorf("%s: got %v, %v; want %v, %v", c.name, to, ok, c.to, c.ok)
		}
	}
	if _, ok := chapterTarget(nil, time.Hour, 1); ok {
		t.Error("a book with no chapters has a next chapter")
	}
}

// target is where the current track was last started or sought to.
func target(p *Player) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	return duration(p.startFrame, p.curRate)
}

func TestBookMovesByChapter(t *testing.T) {
	// The test output plays far faster than real time, so what is checked is
	// where the player was sent, and chapters are hours apart.
	const hour = 48000 * 3600
	p, _ := newPlayer(t, map[string]int64{"book.m4b": 12 * hour, "other.m4b": 12 * hour})
	p.SetChapters(func(path string) []time.Duration {
		if path == "book.m4b" {
			return []time.Duration{0, 3 * time.Hour, 6 * time.Hour}
		}
		return nil
	})
	p.Play(queue("book.m4b", "other.m4b"), 0)
	settled(t, p)
	p.Next()
	if got := target(p); got != 3*time.Hour {
		t.Fatalf("Next went to %v, want the second chapter", got)
	}
	if s := p.State(); s.Track.Path != "book.m4b" {
		t.Fatalf("Next left the book for %q", s.Track.Path)
	}
	p.Seek(8 * time.Hour)
	eventually(t, "the seek", func() bool { return p.State().Elapsed >= 8*time.Hour })
	p.Next() // no chapter after the last: the next track
	if s := p.State(); s.Track.Path != "other.m4b" {
		t.Fatalf("Next at the last chapter gave %q", s.Track.Path)
	}
}

func TestSeekByMovesFromWhereItIs(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"a": 48000 * 36000})
	p.Play(queue("a"), 0)
	settled(t, p)
	p.Seek(5 * time.Hour)
	p.SeekBy(-15 * time.Minute)
	if got := target(p); got < 4*time.Hour+40*time.Minute || got > 4*time.Hour+50*time.Minute {
		t.Fatalf("15 min back from 5 h went to %v", got)
	}
	p.SeekBy(-100 * time.Hour)
	if got := target(p); got != 0 {
		t.Fatalf("seeking before the start went to %v", got)
	}
}

func TestABookKeepsItsPlaceLikeAnAlbum(t *testing.T) {
	p, _ := newPlayer(t, map[string]int64{"book.m4b": long, "song.flac": long})
	p.Play(queue("book.m4b"), 0)
	if s, ok := p.Snapshot(); !ok || !s.AlbumRun {
		t.Fatalf("a book on its own isn't kept: %+v", s)
	}
	p.Play(queue("song.flac"), 0)
	if s, _ := p.Snapshot(); s.AlbumRun {
		t.Fatal("a song on its own is kept as an album")
	}
}

func TestStateSaysWhichChapter(t *testing.T) {
	const hour = 48000 * 3600
	p, _ := newPlayer(t, map[string]int64{"book.m4b": 12 * hour, "song.flac": 12 * hour})
	p.SetChapters(func(path string) []time.Duration {
		if path == "book.m4b" {
			return []time.Duration{0, 3 * time.Hour, 6 * time.Hour}
		}
		return nil
	})
	p.Play(queue("book.m4b"), 0)
	settled(t, p)
	if s := p.State(); s.Chapters != 3 || s.Chapter != 1 {
		t.Fatalf("at the start: chapter %d of %d", s.Chapter, s.Chapters)
	}
	p.Seek(7 * time.Hour)
	eventually(t, "the last chapter", func() bool { s := p.State(); return s.Chapter == 3 })
	p.Play(queue("song.flac"), 0)
	if s := p.State(); s.Chapters != 0 || s.Chapter != 0 {
		t.Fatalf("a song is in chapter %d of %d", s.Chapter, s.Chapters)
	}
}

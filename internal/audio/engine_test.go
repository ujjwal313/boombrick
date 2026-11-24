package audio

import (
	"errors"
	"io"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ramp is a Source whose left channel counts frames.
type ramp struct {
	mu     sync.Mutex
	rate   int
	n, pos int64
	seeks  []int64
	closes int
}

func (r *ramp) SampleRate() int { return r.rate }
func (r *ramp) Frames() int64   { return r.n }

func (r *ramp) Read(buf []float32) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	k := min(int64(len(buf)/2), r.n-r.pos)
	for i := int64(0); i < k; i++ {
		buf[2*i] = float32(r.pos+i) / float32(r.n)
		buf[2*i+1] = 0
	}
	r.pos += k
	if r.pos >= r.n {
		return int(k), io.EOF
	}
	return int(k), nil
}

func (r *ramp) SeekFrame(frame int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pos = frame
	r.seeks = append(r.seeks, frame)
	return nil
}

func (r *ramp) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return nil
}

func (r *ramp) isClosed() bool { return r.closeCount() > 0 }

func (r *ramp) closeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closes
}

// sink is an Output that records what it receives. It pretends to hold up to
// 100 ms, which Drop reports and forgets. Each write takes a millisecond.
// Closed, it takes no writes until it is opened again.
type sink struct {
	mu     sync.Mutex
	opens  []int // each rate it was opened at, again after a close
	writes []int // frames per write
	frames int64
	held   int
	drops  int
	fail   int // writes left to fail
	closed bool
}

func (s *sink) Open(rate int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.opens) == 0 || s.opens[len(s.opens)-1] != rate {
		s.opens = append(s.opens, rate)
	}
	s.closed = false
	return nil
}

func (s *sink) Write(buf []int16) error {
	time.Sleep(time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("write to a closed device")
	}
	if s.fail > 0 {
		s.fail--
		return errors.New("device gone")
	}
	n := len(buf) / 2
	s.writes = append(s.writes, n)
	s.frames += int64(n)
	s.held = min(s.held+n, OutputRate/10)
	return nil
}

func (s *sink) Drop() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drops++
	held := s.held
	s.held = 0
	return held, nil
}

func (s *sink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed, s.held = true, 0
	return nil
}

func (s *sink) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *sink) openCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.opens)
}

func (s *sink) failNext() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail++
}

func (s *sink) written() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames
}

func (s *sink) dropCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drops
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func ends() (chan End, func(End)) {
	ch := make(chan End, 8)
	return ch, func(e End) { ch <- e }
}

func next(t *testing.T, ch chan End) End {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("onEnd never ran")
		return End{}
	}
}

func TestPlaysToTheEndAndReports(t *testing.T) {
	out := &sink{}
	ch, onEnd := ends()
	e := NewEngine(out, onEnd)
	defer e.Close()
	src := &ramp{rate: OutputRate, n: 5000}
	e.Play(src)
	end := next(t, ch)
	if end.Source != src || end.Next != nil || end.Frames != 5000 || end.Err != nil {
		t.Fatalf("End = %+v", end)
	}
	if out.written() != 5000 || !src.isClosed() {
		t.Fatalf("wrote %d frames, closed %v", out.written(), src.isClosed())
	}
	if st := e.Status(); st.Loaded || st.Playing {
		t.Fatalf("status after the end = %+v", st)
	}
}

func TestOpensOnceAtTheOutputRate(t *testing.T) {
	out := &sink{}
	ch, onEnd := ends()
	e := NewEngine(out, onEnd)
	defer e.Close()
	e.Play(&ramp{rate: 44100, n: 4410})
	next(t, ch)
	e.Play(&ramp{rate: 48000, n: 4800})
	next(t, ch)
	out.mu.Lock()
	defer out.mu.Unlock()
	if !slices.Equal(out.opens, []int{OutputRate}) {
		t.Fatalf("opened at %v, want only %d", out.opens, OutputRate)
	}
}

func TestResamplesToTheOutputRate(t *testing.T) {
	out := &sink{}
	ch, onEnd := ends()
	e := NewEngine(out, onEnd)
	defer e.Close()
	e.Play(&ramp{rate: 24000, n: 24000})
	next(t, ch)
	if w := out.written(); w < 47800 || w > 48000 {
		t.Fatalf("one second at 24 kHz became %d frames, want about 48000", w)
	}
}

func TestPlayDropsWhatWasQueued(t *testing.T) {
	e := NewEngine(&sink{}, nil)
	defer e.Close()
	q := &ramp{rate: OutputRate, n: 10}
	e.Queue(q)
	e.Play(&ramp{rate: OutputRate, n: 10})
	if !q.isClosed() {
		t.Fatal("Play should close a queued source it drops")
	}
}

func TestQueuedSourceFollowsWithoutAGap(t *testing.T) {
	out := &sink{}
	ch, onEnd := ends()
	e := NewEngine(out, onEnd)
	defer e.Close()
	a, b := &ramp{rate: OutputRate, n: OutputRate}, &ramp{rate: OutputRate, n: 3000}
	e.Cue(a, 0) // paused, so b is queued before a can end
	eventually(t, "the cue to settle", func() bool { return out.dropCount() == 1 })
	e.Queue(b)
	e.Resume()
	first := next(t, ch)
	if first.Source != a || first.Next != b {
		t.Fatalf("first End = %+v, want a handing over to b", first)
	}
	second := next(t, ch)
	if second.Source != b || second.Next != nil {
		t.Fatalf("second End = %+v", second)
	}
	if out.written() != OutputRate+3000 || out.dropCount() != 1 {
		t.Fatalf("wrote %d frames with %d drops; want %d and only the cue's", out.written(), out.dropCount(), OutputRate+3000)
	}
}

func TestPauseReplaysWhatTheDeviceHeld(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	e.Play(&ramp{rate: OutputRate, n: 1 << 40})
	eventually(t, "a full device buffer", func() bool { return out.written() > OutputRate/5 })
	e.Pause()
	eventually(t, "the pause to drop the buffer", func() bool { return out.dropCount() == 1 })
	written := out.written()
	eventually(t, "the position to move back", func() bool { return e.Status().Frame == written-OutputRate/10 })
	e.Resume()
	eventually(t, "the replay", func() bool { return out.written() >= written+OutputRate/10 })
	out.mu.Lock()
	defer out.mu.Unlock()
	if !slices.Contains(out.writes, OutputRate/10) {
		t.Fatalf("no write replayed the %d dropped frames: %v", OutputRate/10, out.writes[len(out.writes)-5:])
	}
}

// Release pauses and has let go of the output by the time it returns, so
// another program can open a device that takes one at a time. Resume opens it
// again and replays what the device held, as after Pause.
func TestReleaseLetsGoOfTheOutput(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	e.Play(&ramp{rate: OutputRate, n: 1 << 40})
	eventually(t, "a full device buffer", func() bool { return out.written() > OutputRate/5 })
	e.Release()
	if !out.isClosed() {
		t.Fatal("Release returned with the output still open")
	}
	if e.Status().Playing {
		t.Fatal("Release left the music playing")
	}
	written := out.written()
	if e.Status().Frame != written-OutputRate/10 {
		t.Fatalf("frame %d after Release, want %d: what the device held isn't taken off", e.Status().Frame, written-OutputRate/10)
	}
	time.Sleep(20 * time.Millisecond)
	if out.written() != written || !out.isClosed() {
		t.Fatal("the output was written to, or opened, while released")
	}
	e.Resume()
	eventually(t, "playback after Release", func() bool { return out.written() >= written+OutputRate/10 })
	if out.isClosed() || out.openCount() != 2 {
		t.Fatalf("closed %v, opened %d times; want it opened again", out.isClosed(), out.openCount())
	}
	if e.Status().Err != nil {
		t.Fatalf("Resume after Release: %v", e.Status().Err)
	}
}

// Release lets go of the output just the same when playback is already
// paused or nothing is loaded, and doesn't wait on an engine that has closed.
func TestReleaseWhenPausedStoppedOrClosed(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	e.Play(&ramp{rate: OutputRate, n: 1 << 40})
	eventually(t, "playback", func() bool { return out.written() > 0 })
	e.Pause()
	eventually(t, "the pause to drop the buffer", func() bool { return out.dropCount() == 1 })
	e.Release()
	if !out.isClosed() {
		t.Fatal("paused: Release returned with the output still open")
	}

	e.Resume()
	eventually(t, "playback after Release", func() bool { return !out.isClosed() })
	e.Stop()
	e.Release()
	if !out.isClosed() {
		t.Fatal("stopped: Release returned with the output still open")
	}

	e.Close()
	released := make(chan struct{})
	go func() { e.Release(); close(released) }()
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("Release waited on a closed engine")
	}
}

// stuck is an Output whose writes don't return until it is freed, as one to
// a wedged Bluetooth device.
type stuck struct {
	sink
	entered  chan struct{} // closed by the first write
	free     chan struct{}
	once     sync.Once
	freeOnce sync.Once
}

// newStuck returns a stuck output that is freed when the test ends, so a
// failing test doesn't leave the engine waiting in a write.
func newStuck(t *testing.T) *stuck {
	s := &stuck{entered: make(chan struct{}), free: make(chan struct{})}
	t.Cleanup(s.unstick)
	return s
}

func (s *stuck) Write(buf []int16) error {
	s.once.Do(func() { close(s.entered) })
	<-s.free
	return s.sink.Write(buf)
}

// unstick lets the write under way, and every one after, return.
func (s *stuck) unstick() { s.freeOnce.Do(func() { close(s.free) }) }

func (s *stuck) waitWrite(t *testing.T) {
	t.Helper()
	select {
	case <-s.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("nothing was written")
	}
}

// An output stuck in a write holds up neither Release nor Close: they give up
// on it after a while, so quitting and the hand-over to the background player
// can't hang. Once the write returns, the engine closes as before.
func TestReleaseAndCloseDontWaitOnAStuckOutput(t *testing.T) {
	for _, call := range []string{"Release", "Close"} {
		out := newStuck(t)
		e := NewEngine(out, nil)
		e.wait = 50 * time.Millisecond
		e.Play(&ramp{rate: OutputRate, n: 1 << 40})
		out.waitWrite(t)
		start := time.Now()
		var err error
		if call == "Release" {
			err = e.Release()
		} else {
			err = e.Close()
		}
		if !errors.Is(err, ErrStuck) {
			t.Fatalf("%s on a stuck output: %v, want %v", call, err, ErrStuck)
		}
		if took := time.Since(start); took > time.Second {
			t.Fatalf("%s took %v on a stuck output", call, took)
		}
		out.unstick()
		e.wait = 3 * time.Second
		if err := e.Close(); err != nil {
			t.Fatalf("Close once the write returned: %v", err)
		}
		if !out.isClosed() {
			t.Fatal("the output was left open")
		}
	}
}

// A Release that gave up is forgotten: when the write comes back and the music
// plays on, as after a hand-over that was undone, a later pause doesn't close
// the output.
func TestAReleaseThatGaveUpIsForgotten(t *testing.T) {
	out := newStuck(t)
	e := NewEngine(out, nil)
	defer e.Close()
	e.wait = 50 * time.Millisecond
	e.Play(&ramp{rate: OutputRate, n: 1 << 40})
	out.waitWrite(t)
	if err := e.Release(); !errors.Is(err, ErrStuck) {
		t.Fatalf("Release on a stuck output: %v", err)
	}
	e.Resume()
	out.unstick()
	eventually(t, "playback", func() bool { return out.written() > OutputRate/5 })
	e.Pause()
	eventually(t, "the pause to drop the buffer", func() bool { return out.dropCount() == 1 })
	time.Sleep(20 * time.Millisecond)
	if out.isClosed() {
		t.Fatal("a pause closed the output, as the Release that gave up asked")
	}
}

// On an engine whose Close gave up, Release returns at once, saying the output
// is still stuck.
func TestReleaseAfterACloseThatGaveUp(t *testing.T) {
	out := newStuck(t)
	e := NewEngine(out, nil)
	e.wait = 50 * time.Millisecond
	e.Play(&ramp{rate: OutputRate, n: 1 << 40})
	out.waitWrite(t)
	if err := e.Close(); !errors.Is(err, ErrStuck) {
		t.Fatalf("Close on a stuck output: %v", err)
	}
	e.wait = 3 * time.Second
	start := time.Now()
	if err := e.Release(); !errors.Is(err, ErrStuck) {
		t.Fatalf("Release after the Close: %v, want %v", err, ErrStuck)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("Release after the Close took %v", took)
	}
}

// closing is an Output whose closing fails, and which counts its closes.
type closing struct {
	sink
	err    error
	closes atomic.Int32
}

func (c *closing) Close() error {
	c.closes.Add(1)
	c.sink.Close()
	return c.err
}

// Close closes the output once and reports what closing it said.
func TestCloseReportsClosingTheOutput(t *testing.T) {
	out := &closing{err: errors.New("device gone")}
	e := NewEngine(out, nil)
	e.Play(&ramp{rate: OutputRate, n: 1 << 40})
	eventually(t, "playback", func() bool { return out.written() > 0 })
	if err := e.Close(); !errors.Is(err, out.err) {
		t.Fatalf("Close: %v, want %v", err, out.err)
	}
	if n := out.closes.Load(); n != 1 {
		t.Fatalf("the output was closed %d times", n)
	}
}

func TestCueLoadsPaused(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	src := &ramp{rate: OutputRate, n: 1 << 40}
	e.Cue(src, 1000)
	eventually(t, "the cue", func() bool { st := e.Status(); return st.Loaded && st.Frame == 1000 })
	time.Sleep(20 * time.Millisecond)
	if st := e.Status(); st.Playing || out.written() != 0 {
		t.Fatalf("cued source played: %+v, %d frames", st, out.written())
	}
	e.Resume()
	eventually(t, "playback", func() bool { return out.written() > 0 })
	src.mu.Lock()
	defer src.mu.Unlock()
	if !slices.Contains(src.seeks, 1000) {
		t.Fatalf("seeks %v, want 1000", src.seeks)
	}
}

func TestSeek(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	src := &ramp{rate: OutputRate, n: 1 << 40}
	e.Play(src)
	eventually(t, "playback", func() bool { return out.written() > 0 })
	e.SeekFrame(1 << 30)
	eventually(t, "the seek", func() bool { return e.Status().Frame >= 1<<30 })
}

func TestPlayReplacesAndStopCloses(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, func(End) { t.Error("onEnd ran for a replaced source") })
	a, b, q := &ramp{rate: OutputRate, n: 1 << 40}, &ramp{rate: OutputRate, n: 1 << 40}, &ramp{rate: OutputRate, n: 10}
	e.Play(a)
	eventually(t, "a to play", func() bool { return out.written() > 0 })
	e.Queue(q)
	e.Play(b)
	eventually(t, "a and the queued source to close", func() bool { return a.isClosed() && q.isClosed() })
	e.Stop()
	eventually(t, "b to close", b.isClosed)
	if err := e.Close(); err != nil || !out.closed {
		t.Fatalf("Close = %v, output closed %v", err, out.closed)
	}
}

func TestOutputErrorPausesAndResumeRetries(t *testing.T) {
	out := &sink{fail: 1}
	e := NewEngine(out, nil)
	defer e.Close()
	e.Play(&ramp{rate: OutputRate, n: 1 << 40})
	eventually(t, "the error", func() bool { return e.Status().Err != nil })
	if st := e.Status(); st.Playing || out.written() != 0 {
		t.Fatalf("after a failed write: %+v, %d frames", st, out.written())
	}
	e.Resume()
	eventually(t, "playback after resume", func() bool { return out.written() > 0 })
	if e.Status().Err != nil {
		t.Fatal("Resume should clear the error")
	}
}

func TestRefusesUnsupportedRates(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	low, high := &ramp{rate: 4000, n: 1 << 40}, &ramp{rate: 768000, n: 1 << 40}
	e.Play(low)
	e.Queue(high)
	if !low.isClosed() || !high.isClosed() {
		t.Fatal("sources at unsupported rates should be closed, not played")
	}
	time.Sleep(10 * time.Millisecond)
	if st := e.Status(); st.Loaded || out.written() != 0 {
		t.Fatalf("status %+v, %d frames written", st, out.written())
	}
	if SupportedRate(4000) || !SupportedRate(8000) || !SupportedRate(384000) || SupportedRate(384001) {
		t.Fatal("SupportedRate bounds are wrong")
	}
}

func TestPlayAfterCloseClosesTheSource(t *testing.T) {
	e := NewEngine(&sink{}, nil)
	e.Close()
	src := &ramp{rate: OutputRate, n: 10}
	e.Play(src)
	if !src.isClosed() {
		t.Fatal("Play after Close should close the source")
	}
}

// gated is a ramp whose reads wait until gate is closed.
type gated struct {
	*ramp
	gate chan struct{}
}

func (g *gated) Read(buf []float32) (int, error) {
	<-g.gate
	return g.ramp.Read(buf)
}

func (r *ramp) position() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pos
}

func TestHandOverAcrossRates(t *testing.T) {
	// Going from a long filter down to the 8 kHz one used to overrun the
	// resampler's output buffer.
	out := &sink{}
	ch, onEnd := ends()
	e := NewEngine(out, onEnd)
	defer e.Close()
	a, b := &ramp{rate: 96000, n: 96000}, &ramp{rate: 8000, n: 8000}
	e.Cue(a, 0)
	e.Queue(b)
	e.Resume()
	if first := next(t, ch); first.Source != a || first.Next != b {
		t.Fatalf("first End = %+v", first)
	}
	if second := next(t, ch); second.Source != b {
		t.Fatalf("second End = %+v", second)
	}
	if w := out.written(); w < 95000 || w > 97000 {
		t.Fatalf("two seconds of audio became %d frames, want about 96000", w)
	}
}

func TestEndsArriveInOrder(t *testing.T) {
	for i := 0; i < 200; i++ {
		ch, onEnd := ends()
		e := NewEngine(&sink{}, onEnd)
		a, q := &ramp{rate: OutputRate, n: 100}, &ramp{rate: OutputRate, n: int64(i % 3)}
		e.Cue(a, 0)
		e.Queue(q)
		e.Resume()
		if first := next(t, ch); first.Source != a || first.Next != q {
			t.Fatalf("round %d: first End = %+v, want a handing over to q", i, first)
		}
		if second := next(t, ch); second.Source != q {
			t.Fatalf("round %d: second End = %+v, want q", i, second)
		}
		e.Close()
	}
}

func TestPauseRightAfterAHandOver(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	a := &ramp{rate: OutputRate, n: OutputRate}
	q := &gated{&ramp{rate: OutputRate, n: 1 << 40}, make(chan struct{})}
	e.Cue(a, 0)
	eventually(t, "the cue to settle", func() bool { return out.dropCount() == 1 })
	e.Queue(q)
	e.Resume()
	eventually(t, "the hand-over", func() bool { return e.Status().Source == q })
	e.Pause()
	close(q.gate) // q's first chunk is read, then the pause lands
	eventually(t, "the pause to drop the buffer", func() bool { return out.dropCount() == 2 })
	if pos := q.position(); pos != chunk {
		t.Fatalf("q was read to %d, want %d", pos, chunk)
	}
	eventually(t, "the position to stop at the start", func() bool { return e.Status().Frame == 0 })
	e.Resume()
	eventually(t, "the position to come back", func() bool { return e.Status().Frame >= chunk })
	for i := 0; i < 20; i++ {
		st := e.Status()
		if st.Frame > q.position() {
			t.Fatalf("position %d is ahead of what was read, %d", st.Frame, q.position())
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFailedReplayIsPlayedAgain(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	src := &ramp{rate: OutputRate, n: 1 << 40}
	e.Play(src)
	eventually(t, "a full device buffer", func() bool { return out.written() > OutputRate/5 })
	e.Pause()
	eventually(t, "the pause to drop the buffer", func() bool { return out.dropCount() == 1 })
	rewound := out.written() - OutputRate/10
	eventually(t, "the position to move back", func() bool { return e.Status().Frame == rewound })
	out.failNext()
	e.Resume() // the replay's write fails
	eventually(t, "the replay to fail", func() bool { return e.Status().Err != nil })
	eventually(t, "the rewind", func() bool {
		src.mu.Lock()
		defer src.mu.Unlock()
		return slices.Contains(src.seeks, rewound)
	})
	if st := e.Status(); st.Frame != rewound {
		t.Fatalf("position %d after the failed replay, want %d", st.Frame, rewound)
	}
	e.Resume()
	eventually(t, "playback to go on", func() bool { return e.Status().Frame > rewound+chunk })
}

func TestPlayingTheQueuedSourceKeepsIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		hand func(t *testing.T, e *Engine, q *ramp, out *sink)
	}{
		{"queued twice", func(t *testing.T, e *Engine, q *ramp, out *sink) { e.Queue(q); e.Queue(q) }},
		{"queued then played", func(t *testing.T, e *Engine, q *ramp, out *sink) { e.Queue(q); e.Play(q) }},
		{"played twice", func(t *testing.T, e *Engine, q *ramp, out *sink) {
			e.Play(q)
			eventually(t, "playback", func() bool { return out.written() > 0 })
			e.Play(q)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := &sink{}
			e := NewEngine(out, nil)
			q := &ramp{rate: OutputRate, n: 1 << 40}
			tc.hand(t, e, q, out)
			time.Sleep(5 * time.Millisecond)
			if n := q.closeCount(); n != 0 {
				t.Fatalf("a source handed to the engine again was closed %d times while in use", n)
			}
			e.Close()
			if n := q.closeCount(); n != 1 {
				t.Fatalf("Close closed it %d times, want once", n)
			}
		})
	}
}

func TestQueueingThePlayingSourceIsIgnored(t *testing.T) {
	ch, onEnd := ends()
	e := NewEngine(&sink{}, onEnd)
	defer e.Close()
	q := &ramp{rate: OutputRate, n: 5000}
	e.Cue(q, 0)
	eventually(t, "the cue", func() bool { return e.Status().Source == q })
	e.Queue(q)
	e.Resume()
	if end := next(t, ch); end.Source != q || end.Next != nil {
		t.Fatalf("End = %+v; q can't follow itself", end)
	}
	time.Sleep(20 * time.Millisecond)
	if n := q.closeCount(); n != 1 {
		t.Fatalf("closed %d times, want once", n)
	}
}

func TestPlayFromStartsAtAFrame(t *testing.T) {
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	src := &ramp{rate: OutputRate, n: 1 << 40}
	e.PlayFrom(src, 5000)
	eventually(t, "playback", func() bool { return out.written() > 0 })
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.seeks) == 0 || src.seeks[0] != 5000 {
		t.Fatalf("seeks %v: the first read should come after seeking to 5000", src.seeks)
	}
	if st := e.Status(); !st.Playing || st.Frame < 5000 {
		t.Fatalf("status %+v", st)
	}
}

func TestPlayFromTheStartDoesntSeek(t *testing.T) {
	// A seek drops what the device holds, which would cut off the end of a
	// track that has just played out.
	out := &sink{}
	e := NewEngine(out, nil)
	defer e.Close()
	src := &ramp{rate: OutputRate, n: 1 << 40}
	e.PlayFrom(src, 0)
	eventually(t, "playback", func() bool { return out.written() > 0 })
	src.mu.Lock()
	defer src.mu.Unlock()
	if len(src.seeks) != 0 || out.dropCount() != 0 {
		t.Fatalf("seeks %v, %d drops; starting at the top needs neither", src.seeks, out.dropCount())
	}
}

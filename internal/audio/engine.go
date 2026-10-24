package audio

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio/resample"
)

// OutputRate is the rate the device runs at; every source is resampled to it.
const OutputRate = 48000

const (
	// chunk is how many source frames each read carries: about 21 ms at 48 kHz.
	chunk = 1024
	// maxRatio bounds how many output frames one source frame becomes (8 kHz
	// to 48 kHz).
	maxRatio = 6
	// history is how many written output frames the engine keeps, so what a
	// pause drops from the device can be played again on resume. It covers the
	// device's 100 ms buffer with room to spare.
	history = OutputRate / 4
	// resampleQuality trades CPU for accuracy on speexdsp's 0-10 scale.
	resampleQuality = 4
)

// Source rates the engine accepts: maxRatio sizes its buffers for 8 kHz.
const (
	minRate = 8000
	maxRate = 384000
)

// ErrRate is reported for sources whose sample rate the engine can't play.
var ErrRate = errors.New("audio: unsupported sample rate")

// SupportedRate reports whether the engine can play a source at rate.
func SupportedRate(rate int) bool { return rate >= minRate && rate <= maxRate }

// Status is a snapshot of the engine.
type Status struct {
	Loaded  bool   // a source is playing or paused
	Playing bool   // loaded and not paused
	Frame   int64  // the source's position, in its own frames
	Frames  int64  // the source's length, or -1
	Rate    int    // the source's sample rate
	Err     error  // the last output error; it pauses playback
	Source  Source // the loaded source, for identity checks only; nil when stopped
}

// End describes a source that finished.
type End struct {
	Source Source // the source that played out, now closed
	Next   Source // the queued source that took over without a gap, or nil
	Frames int64  // the position it ended at, in its own frames (a Cue offset counts)
	Err    error  // the read error that ended it early, or nil at a clean end
}

// Engine plays one Source at a time through an Output at OutputRate. A single
// goroutine reads a chunk, resamples it, converts it to 16-bit and writes it;
// the output's own buffer absorbs scheduling hiccups. Its buffers are
// allocated once.
type Engine struct {
	out   Output
	onEnd func(End)
	done  chan struct{}

	mu       sync.Mutex
	wake     *sync.Cond
	cur      Source // read by the loop; set only by the loop, under mu
	next     Source // handed over by Play or Cue, swapped in by the loop
	queued   Source // follows cur without a gap
	switched bool
	paused   bool
	seek     int64 // pending seek, or -1
	quiet    bool  // nothing has been written since the output last went idle
	replay   int   // output frames from history to write again before reading on
	back     int64 // source frames the pause took off Frame, which the replay restores
	fadeOut  fadeOut
	fadeEnd  fadeEnd
	closed   bool
	st       Status

	// Ends wait in a FIFO for a notifier goroutine, so onEnd sees them in the
	// order they happened and the loop never blocks on it.
	nmu     sync.Mutex
	nwake   *sync.Cond
	pending []End
	stopped bool // the loop has exited
}

// fadeOut takes the output down to silence over total output frames, left of
// them still to go, and then pauses playback. The zero fadeOut is none.
type fadeOut struct{ left, total int }

// fadeEnd takes src down over its last frames source frames, so that it ends
// in silence. src nil is none.
type fadeEnd struct {
	src    Source
	frames int64
}

// NewEngine starts the playback goroutine. onEnd runs each time a source plays
// to its end, on a goroutine of its own, one End at a time, in the order
// sources ended.
func NewEngine(out Output, onEnd func(End)) *Engine {
	e := &Engine{out: out, onEnd: onEnd, seek: -1, done: make(chan struct{}), st: Status{Frames: -1}}
	e.wake = sync.NewCond(&e.mu)
	e.nwake = sync.NewCond(&e.nmu)
	go e.loop()
	if onEnd != nil {
		go e.notify()
	}
	return e
}

// notify hands queued Ends to onEnd one at a time. Separate goroutines per End
// could overtake each other: a queued source that ends at once reports right
// behind the End that handed over to it.
func (e *Engine) notify() {
	e.nmu.Lock()
	for {
		for len(e.pending) == 0 {
			if e.stopped {
				e.nmu.Unlock()
				return
			}
			e.nwake.Wait()
		}
		end := e.pending[0]
		e.pending[0] = End{}
		e.pending = e.pending[1:]
		e.nmu.Unlock()
		e.onEnd(end)
		e.nmu.Lock()
	}
}

func (e *Engine) report(end End) {
	if e.onEnd == nil {
		return
	}
	e.nmu.Lock()
	e.pending = append(e.pending, end)
	e.nmu.Unlock()
	e.nwake.Signal()
}

// Play replaces whatever is playing with src, unpaused; nil stops. Anything
// queued is dropped. The engine closes sources once it is done with them.
func (e *Engine) Play(src Source) { e.load(src, false, -1) }

// PlayFrom is Play starting at frame. The seek happens before the first read,
// so nothing from the start of the source is heard. From frame 0 it is Play:
// a seek would drop what the device still holds of a track that played out.
func (e *Engine) PlayFrom(src Source, frame int64) {
	if frame <= 0 {
		frame = -1
	}
	e.load(src, false, frame)
}

// Cue loads src paused at frame, to be started with Resume.
func (e *Engine) Cue(src Source, frame int64) { e.load(src, true, max(frame, 0)) }

func (e *Engine) load(src Source, paused bool, seek int64) {
	if src != nil && !SupportedRate(src.SampleRate()) {
		closeSource(src)
		src = nil // callers check SupportedRate first; this only guards the buffers
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		closeSource(src)
		return
	}
	next, queued := e.next, e.queued // handed over but never started
	e.next, e.queued, e.switched, e.paused, e.seek = src, nil, true, paused, seek
	e.fadeOut, e.fadeEnd = fadeOut{}, fadeEnd{}
	e.mu.Unlock()
	if next != src {
		closeSource(next)
	}
	if queued != src {
		closeSource(queued)
	}
	e.wake.Signal()
}

// Queue sets src to follow the current source without a gap, replacing and
// closing any queued before; nil clears it. The current source can't follow
// itself, so it is ignored.
func (e *Engine) Queue(src Source) {
	if src != nil && !SupportedRate(src.SampleRate()) {
		closeSource(src)
		src = nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		closeSource(src)
		return
	}
	if src != nil && (src == e.cur || src == e.next) {
		e.mu.Unlock()
		return
	}
	old := e.queued
	e.queued = src
	e.mu.Unlock()
	if old != src {
		closeSource(old)
	}
}

// Stop stops playback.
func (e *Engine) Stop() { e.Play(nil) }

// Pause holds playback. What the device still held is played again on Resume.
// A FadeOut under way ends.
func (e *Engine) Pause() { e.set(func() { e.paused, e.fadeOut = true, fadeOut{} }) }

// Resume continues after Pause or Cue, after an output error paused playback,
// or after a FadeOut, at full volume.
func (e *Engine) Resume() { e.set(func() { e.paused, e.st.Err, e.fadeOut = false, nil, fadeOut{} }) }

// SeekFrame moves the current source to frame. A FadeOut under way ends.
func (e *Engine) SeekFrame(frame int64) {
	e.set(func() { e.seek, e.fadeOut = max(frame, 0), fadeOut{} })
}

// FadeOut fades playback down to silence over d, then pauses it, as the sleep
// timer ends. It carries on across a gapless hand-over. Pause, Resume,
// SeekFrame, CancelFade and loading a source end it, at full volume again;
// a fade already under way is replaced.
func (e *Engine) FadeOut(d time.Duration) {
	n := max(int(int64(d)*OutputRate/int64(time.Second)), 1)
	e.set(func() { e.fadeOut = fadeOut{left: n, total: n} })
}

// FadeEnd fades src out over its last d, so that it ends in silence. It
// applies while src plays, from wherever src is; a source of unknown length
// isn't faded. A nil src ends a FadeEnd under way, leaving a FadeOut be;
// loading a source and CancelFade end it too.
func (e *Engine) FadeEnd(src Source, d time.Duration) {
	if src == nil {
		e.set(func() { e.fadeEnd = fadeEnd{} })
		return
	}
	frames := int64(d) * int64(src.SampleRate()) / int64(time.Second)
	e.set(func() { e.fadeEnd = fadeEnd{src: src, frames: max(frames, 1)} })
}

// CancelFade ends FadeOut and FadeEnd, at full volume again.
func (e *Engine) CancelFade() { e.set(func() { e.fadeOut, e.fadeEnd = fadeOut{}, fadeEnd{} }) }

func (e *Engine) set(f func()) {
	e.mu.Lock()
	f()
	e.mu.Unlock()
	e.wake.Signal()
}

// Status reports what the engine is doing.
func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.st
	st.Playing = st.Loaded && !e.paused
	return st
}

// Close stops the goroutine, closes any source and closes the output.
func (e *Engine) Close() error {
	e.set(func() { e.closed = true })
	<-e.done
	return e.out.Close()
}

func (e *Engine) loop() {
	defer close(e.done)
	defer func() {
		e.nmu.Lock()
		e.stopped = true
		e.nmu.Unlock()
		e.nwake.Broadcast()
	}()
	rs := resample.New(2, resampleQuality)
	defer rs.Close()
	in := make([]float32, 2*chunk)
	out := make([]float32, 2*chunk*maxRatio+64)
	pcm := make([]int16, len(out))
	hist := newRing(2 * history)
	again := make([]int16, 2*history)
	for {
		e.mu.Lock()
		switch {
		case e.closed:
			srcs := []Source{e.cur, e.next, e.queued}
			e.cur, e.next, e.queued = nil, nil, nil
			e.mu.Unlock()
			for _, s := range srcs {
				closeSource(s)
			}
			return
		case e.switched:
			old, cur := e.cur, e.next
			e.cur, e.next, e.switched, e.replay, e.back = cur, nil, false, 0, 0
			e.st = Status{Loaded: cur != nil, Frames: -1, Source: cur}
			if cur != nil {
				e.st.Frames, e.st.Rate = cur.Frames(), cur.SampleRate()
			}
			e.mu.Unlock()
			rs.Reset()
			hist.clear()
			// A source that ended has already left the device to play out;
			// only an interrupted one is cut off. One handed over again is
			// still in use.
			if old != nil {
				if old != cur {
					closeSource(old)
				}
				e.drop()
			}
			continue
		case e.seek >= 0 && e.cur != nil:
			cur, frame := e.cur, e.seek
			e.seek, e.replay, e.back = -1, 0, 0
			e.mu.Unlock()
			err := cur.SeekFrame(frame)
			rs.Reset()
			hist.clear()
			e.drop()
			if err == nil {
				e.mu.Lock()
				if e.cur == cur {
					e.st.Frame = frame
				}
				e.mu.Unlock()
			}
			continue
		case e.cur == nil || e.paused:
			if e.paused && !e.quiet {
				e.mu.Unlock()
				dropped, _ := e.out.Drop()
				e.mu.Lock()
				// The dropped audio is written again on resume, so pausing
				// doesn't skip it.
				e.replay = min(dropped, hist.len()/2)
				// Close to the start of a track the replay can reach back
				// further than the track goes; only what was taken off is
				// put back.
				e.back = 0
				if e.st.Rate > 0 {
					e.back = min(e.st.Frame, int64(e.replay)*int64(e.st.Rate)/OutputRate)
					e.st.Frame -= e.back
				}
				e.quiet = true
				e.mu.Unlock()
				continue
			}
			e.quiet = true
			e.wake.Wait()
			e.mu.Unlock()
			continue
		}
		cur, rate, replay, back := e.cur, e.st.Rate, e.replay, e.back
		frame, fo, fe := e.st.Frame, e.fadeOut, e.fadeEnd
		e.replay, e.back, e.quiet = 0, 0, false
		e.mu.Unlock()

		if replay > 0 {
			werr := e.write(hist.tail(again[:2*replay]))
			e.mu.Lock()
			if e.cur == cur && !e.switched {
				if werr != nil {
					// Nothing was replayed: read on from the rewound frame.
					e.paused, e.st.Err = true, werr
					if e.seek < 0 {
						e.seek = e.st.Frame
					}
				} else {
					e.st.Frame += back
				}
			}
			e.mu.Unlock()
			continue
		}

		n, rerr := cur.Read(in)
		var werr error
		k := 0
		if n > 0 {
			if fe.src == cur {
				fadeTail(in[:2*n], frame, cur.Frames(), fe.frames)
			}
			k = rs.Process(rate, OutputRate, in[:2*n], out)
			if fo.total > 0 {
				fadeDown(out[:2*k], fo)
			}
			toInt16(pcm[:2*k], out[:2*k])
			if werr = e.write(pcm[:2*k]); werr == nil {
				hist.add(pcm[:2*k])
			}
		}

		e.mu.Lock()
		if e.cur != cur || e.switched {
			e.mu.Unlock()
			continue
		}
		if werr != nil {
			// The chunk didn't play: rewind to it, so Resume plays it. A seek
			// the caller asked for meanwhile wins.
			e.paused, e.st.Err = true, werr
			if e.seek < 0 {
				e.seek = e.st.Frame
			}
			e.mu.Unlock()
			continue
		}
		if ps, ok := cur.(interface{ Pos() int64 }); ok {
			e.st.Frame = ps.Pos() // a Tempo plays more or fewer frames than it uses up
		} else {
			e.st.Frame += int64(n)
		}
		if fo.total > 0 && e.fadeOut == fo {
			if left := fo.left - k; left > 0 {
				e.fadeOut.left = left
			} else {
				e.fadeOut, e.paused = fadeOut{}, true // faded out: playback stops here
			}
		}
		if rerr == nil {
			e.mu.Unlock()
			continue
		}
		end := End{Source: cur, Frames: e.st.Frame}
		if !errors.Is(rerr, io.EOF) {
			end.Err = rerr
		}
		reset := false
		if q := e.queued; q != nil {
			// Gapless: the queued source carries straight on through the same
			// resampler and device buffer. A seek aimed at the old source
			// doesn't carry over.
			reset = q.SampleRate() != rate
			e.cur, e.queued, end.Next = q, nil, q
			e.seek, e.back = -1, 0
			e.st = Status{Loaded: true, Frames: q.Frames(), Rate: q.SampleRate(), Source: q}
		} else {
			e.cur = nil
			e.st.Loaded, e.st.Source = false, nil
		}
		e.mu.Unlock()
		closeSource(cur)
		if reset {
			// Switching the running filter to a new rate leaves it holding
			// extra output from the old, longer filter; after a high rate
			// down to 8 kHz that overflows out. Same-rate hand-overs keep the
			// filter, so they stay seamless.
			rs.Reset()
		}
		e.report(end)
	}
}

// write opens the device if needed and writes pcm.
func (e *Engine) write(pcm []int16) error {
	if err := e.out.Open(OutputRate); err != nil {
		return err
	}
	return e.out.Write(pcm)
}

// drop discards audio queued in the output and marks it quiet.
func (e *Engine) drop() {
	e.out.Drop()
	e.mu.Lock()
	e.quiet = true
	e.mu.Unlock()
}

func closeSource(s Source) {
	if s != nil {
		s.Close()
	}
}

// fadeGain is the level a fade is at with left of total frames to go: falling
// slowly at first and quickly at the end, as the ear hears a fade even.
func fadeGain(left, total int64) float32 {
	if left <= 0 {
		return 0
	}
	g := float32(left) / float32(total)
	return g * g
}

// fadeTail fades the frames of buf, which start at frame first of a source
// total frames long, that fall in its last over frames.
func fadeTail(buf []float32, first, total, over int64) {
	if total <= 0 {
		return
	}
	for i := range int64(len(buf) / 2) {
		if left := total - first - i; left < over {
			g := fadeGain(left, over)
			buf[2*i] *= g
			buf[2*i+1] *= g
		}
	}
}

// fadeDown applies fade f to the output frames in buf, which come next.
func fadeDown(buf []float32, f fadeOut) {
	for i := range len(buf) / 2 {
		g := fadeGain(int64(f.left-i), int64(f.total))
		buf[2*i] *= g
		buf[2*i+1] *= g
	}
}

func toInt16(dst []int16, src []float32) {
	for i, v := range src {
		dst[i] = int16(max(-1, min(1, v)) * 32767)
	}
}

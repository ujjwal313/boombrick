package audio

import (
	"io"
	"math"
)

// Tempo bounds: the range of speeds a listener can ask for.
const (
	MinTempo = 0.5
	MaxTempo = 3.0
)

// A Tempo is a Source that plays another faster or slower without changing
// its pitch, by overlap-add of segments of the source that are chosen to join
// without a jump (WSOLA). It is made for speech, such as audiobooks.
//
// What it plays is shorter or longer than its source, but where it is is
// counted in the source's frames: Pos says it, and the engine reads Pos in
// place of counting the frames it is given, so a position means the same
// whatever the speed. Frames is the source's length. Seeking takes source
// frames, too.
type Tempo struct {
	src   Source
	speed func() float64
	rate  int

	hs, delta int       // synthesis hop, and how far a segment may move to join
	win       []float32 // Hann window of 2*hs frames
	step      int       // every step-th frame is looked at when joining

	// pos is where playback is in the source, in frames, counted as frames
	// are handed out.
	pos float64
	// passing is set while the speed is 1 and frames pass straight through.
	passing bool

	// The input: interleaved frames from source frame bufStart, with srcEnd
	// the source frame after the last one held. eof is set once the source
	// has ended; beyond it the input is silence.
	buf      []float32
	bufStart int64
	srcEnd   int64
	eof      bool

	// The stretch: where the next segment is aimed at in the source, where
	// the last one began (-1 before the first), the frames waiting to be
	// overlapped by the next, and the finished ones not yet handed out.
	aim   float64
	prev  int64
	tail  []float32
	ready []float32
	done  bool // everything has been made
}

// NewTempo makes a Source of src that plays at the speed speed returns, which
// is read as it plays so that it can change at any time.
func NewTempo(src Source, speed func() float64) *Tempo {
	rate := src.SampleRate()
	t := &Tempo{src: src, speed: speed, rate: rate, passing: true}
	t.hs = max(rate*15/1000, 8)
	t.delta = max(rate*10/1000, 4)
	t.step = 4
	t.win = make([]float32, 2*t.hs)
	for i := range t.win {
		t.win[i] = float32(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(len(t.win))))
	}
	t.tail = make([]float32, 2*2*t.hs)
	return t
}

func (t *Tempo) SampleRate() int { return t.rate }
func (t *Tempo) Frames() int64   { return t.src.Frames() }
func (t *Tempo) Close() error    { return t.src.Close() }

// Pos is where playback is in the source, in its frames.
func (t *Tempo) Pos() int64 {
	if n := t.src.Frames(); n >= 0 && t.pos > float64(n) {
		return n
	}
	return int64(t.pos)
}

// SeekFrame moves to a source frame.
func (t *Tempo) SeekFrame(frame int64) error {
	if err := t.src.SeekFrame(frame); err != nil {
		return err
	}
	t.restart(frame)
	return nil
}

// restart starts stretching afresh from source frame at, which the source is
// positioned at.
func (t *Tempo) restart(at int64) {
	t.pos, t.aim, t.prev = float64(at), float64(at), -1
	t.buf, t.bufStart, t.srcEnd, t.eof, t.done = t.buf[:0], at, at, false, false
	clear(t.tail)
	t.ready = t.ready[:0]
}

// Read fills buf with up to len(buf)/2 frames.
func (t *Tempo) Read(buf []float32) (int, error) {
	s := math.Min(math.Max(t.speed(), MinTempo), MaxTempo)
	if math.Abs(s-1) < 0.01 {
		if !t.passing {
			// Back to the source's own pace, from where the listener is.
			t.passing = true
			at := int64(t.pos)
			if err := t.src.SeekFrame(at); err != nil {
				return 0, err
			}
			t.restart(at)
		}
		n, err := t.src.Read(buf)
		t.pos += float64(n)
		return n, err
	}
	if t.passing {
		t.passing = false
		t.restart(int64(t.pos))
	}
	want := len(buf) / 2
	for len(t.ready) < 2*want && !t.done {
		if err := t.produce(s); err != nil {
			return 0, err
		}
	}
	n := min(want, len(t.ready)/2)
	copy(buf, t.ready[:2*n])
	t.ready = t.ready[:copy(t.ready, t.ready[2*n:])]
	t.pos += float64(n) * s
	if t.done && len(t.ready) == 0 {
		return n, io.EOF
	}
	return n, nil
}

// at is the sample c of source frame f, zero beyond what the input holds.
func (t *Tempo) at(f int64, c int) float32 {
	i := f - t.bufStart
	if i < 0 || i >= int64(len(t.buf)/2) {
		return 0
	}
	return t.buf[2*i+int64(c)]
}

// fill reads the source until the input reaches source frame upto, or ends.
func (t *Tempo) fill(upto int64) error {
	for !t.eof && t.srcEnd < upto {
		chunk := make([]float32, 2*4096)
		n, err := t.src.Read(chunk)
		t.buf = append(t.buf, chunk[:2*n]...)
		t.srcEnd += int64(n)
		if err == io.EOF {
			t.eof = true
		} else if err != nil {
			return err
		}
		if n == 0 && err == nil {
			break // a source that gives nothing and doesn't end
		}
	}
	return nil
}

// drop forgets input before source frame before.
func (t *Tempo) drop(before int64) {
	n := before - t.bufStart
	if n < 4096 { // not worth moving yet
		return
	}
	n = min(n, int64(len(t.buf)/2))
	t.buf = t.buf[:copy(t.buf, t.buf[2*n:])]
	t.bufStart += n
}

// join finds where, near centre, a segment best carries on from the one
// before: the start whose first half is most like what the previous
// segment's second half led to.
func (t *Tempo) join(centre int64) int64 {
	lo, hi := centre-int64(t.delta), centre+int64(t.delta)
	if lo < t.bufStart {
		lo = t.bufStart
	}
	if hi < lo {
		hi = lo
	}
	want := t.prev + int64(t.hs)
	best, bestScore := centre, math.Inf(-1)
	if centre < lo || centre > hi {
		best = lo
	}
	for p := lo; p <= hi; p++ {
		var dot, energy float64
		for i := 0; i < t.hs; i += t.step {
			a := float64(t.at(p+int64(i), 0) + t.at(p+int64(i), 1))
			b := float64(t.at(want+int64(i), 0) + t.at(want+int64(i), 1))
			dot += a * b
			energy += a * a
		}
		score := dot / math.Sqrt(energy+1e-9)
		if score > bestScore {
			best, bestScore = p, score
		}
	}
	return best
}

// produce makes one more hop of output, the next hs frames.
func (t *Tempo) produce(speed float64) error {
	centre := int64(t.aim + 0.5)
	if err := t.fill(centre + int64(t.delta) + int64(2*t.hs)); err != nil {
		return err
	}
	if t.eof && centre-int64(t.delta) >= t.srcEnd && t.prev >= 0 {
		// The input has run out: what the last segment left is the end.
		t.flush()
		t.done = true
		return nil
	}
	p := centre
	if t.prev >= 0 {
		p = t.join(centre)
	}
	// Overlap-add the segment onto the tail; the first one starts at full
	// level, as there is nothing before it to fade out.
	for i := 0; i < 2*t.hs; i++ {
		w := t.win[i]
		if t.prev < 0 && i < t.hs {
			w = 1
		}
		t.tail[2*i] += w * t.at(p+int64(i), 0)
		t.tail[2*i+1] += w * t.at(p+int64(i), 1)
	}
	t.ready = append(t.ready, t.tail[:2*t.hs]...)
	n := copy(t.tail, t.tail[2*t.hs:])
	clear(t.tail[n:])
	t.prev = p
	t.aim += float64(t.hs) * speed
	t.drop(min(p, centre-int64(t.delta)))
	return nil
}

// flush hands out the tail the last segment left, which fades to nothing.
func (t *Tempo) flush() {
	t.ready = append(t.ready, t.tail[:2*t.hs]...)
	clear(t.tail)
}

package audio

import (
	"errors"
	"io"
	"math"
	"testing"
)

// sineSource is a stereo sine of freq Hz, n frames long.
type sineSource struct {
	rate, freq int
	n, pos     int64
}

func (s *sineSource) SampleRate() int { return s.rate }
func (s *sineSource) Frames() int64   { return s.n }
func (s *sineSource) Close() error    { return nil }
func (s *sineSource) SeekFrame(f int64) error {
	s.pos = f
	return nil
}
func (s *sineSource) Read(buf []float32) (int, error) {
	k := min(int64(len(buf)/2), s.n-s.pos)
	for i := int64(0); i < k; i++ {
		v := float32(0.5 * math.Sin(2*math.Pi*float64(s.freq)*float64(s.pos+i)/float64(s.rate)))
		buf[2*i], buf[2*i+1] = v, v
	}
	s.pos += k
	if s.pos >= s.n {
		return int(k), io.EOF
	}
	return int(k), nil
}

// drain reads src to its end in chunks of the given size.
func drain(t *testing.T, src Source, chunk int) []float32 {
	t.Helper()
	var all []float32
	buf := make([]float32, 2*chunk)
	for i := 0; i < 1<<20; i++ {
		n, err := src.Read(buf)
		all = append(all, buf[:2*n]...)
		if errors.Is(err, io.EOF) {
			return all
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("never ended")
	return nil
}

// crossings counts upward zero crossings of the left channel.
func crossings(x []float32) int {
	c := 0
	for i := 2; i < len(x); i += 2 {
		if x[i-2] < 0 && x[i] >= 0 {
			c++
		}
	}
	return c
}

func TestTempoChangesLengthNotPitch(t *testing.T) {
	const rate, freq = 44100, 220
	for _, speed := range []float64{0.75, 1.25, 1.5, 2} {
		src := &sineSource{rate: rate, freq: freq, n: rate * 20}
		tp := NewTempo(src, func() float64 { return speed })
		out := drain(t, tp, 1000)
		frames := float64(len(out) / 2)
		want := float64(src.n) / speed
		if math.Abs(frames-want) > 0.03*want {
			t.Errorf("speed %v: %v frames out, want about %v", speed, frames, want)
		}
		// The pitch is the same: as many cycles a second as went in.
		hz := float64(crossings(out)) / (frames / rate)
		if math.Abs(hz-freq) > 0.03*freq {
			t.Errorf("speed %v: %.1f Hz out, want %d", speed, hz, freq)
		}
		if got := tp.Pos(); math.Abs(float64(got-src.n)) > float64(rate)/10 {
			t.Errorf("speed %v: ended at source frame %d of %d", speed, got, src.n)
		}
		for _, v := range out {
			if v > 1.05 || v < -1.05 {
				t.Fatalf("speed %v: sample %v is out of range", speed, v)
			}
		}
	}
}

func TestTempoOutputHasNoJumps(t *testing.T) {
	const rate = 44100
	src := &sineSource{rate: rate, freq: 200, n: rate * 10}
	out := drain(t, NewTempo(src, func() float64 { return 1.6 }), 777)
	// A sine at 200 Hz and amplitude 0.5 moves by at most about 0.014 a frame.
	worst := 0.0
	for i := 2; i < len(out); i += 2 {
		worst = math.Max(worst, math.Abs(float64(out[i]-out[i-2])))
	}
	if worst > 0.05 {
		t.Fatalf("the output jumps by %.3f between two frames", worst)
	}
}

func TestTempoPositionFollowsSpeedChangesAndSeeks(t *testing.T) {
	const rate = 44100
	src := &sineSource{rate: rate, freq: 300, n: rate * 600}
	speed := 1.0
	tp := NewTempo(src, func() float64 { return speed })
	buf := make([]float32, 2*rate) // a second a read
	read := func(k int) {
		for i := 0; i < k; i++ {
			if _, err := tp.Read(buf); err != nil {
				t.Fatal(err)
			}
		}
	}
	read(10)
	if p := tp.Pos(); p != 10*rate {
		t.Fatalf("after 10 s at normal speed: %d", p)
	}
	speed = 2
	read(10) // 10 s of output are 20 s of the source
	if p := tp.Pos(); math.Abs(float64(p-30*rate)) > rate/4 {
		t.Fatalf("after 10 s more at double speed: %d, want about %d", p, 30*rate)
	}
	speed = 1
	read(1)
	if got := src.pos; math.Abs(float64(got-31*rate)) > rate/2 {
		t.Fatalf("back at normal speed the source is at %d, want about %d", got, 31*rate)
	}
	if err := tp.SeekFrame(100 * rate); err != nil || tp.Pos() != 100*rate {
		t.Fatalf("seek: pos %d, %v", tp.Pos(), err)
	}
	speed = 1.5
	read(4)
	if p := tp.Pos(); math.Abs(float64(p-106*rate)) > rate/4 {
		t.Fatalf("4 s at 1.5 after a seek: %d, want about %d", p, 106*rate)
	}
	if tp.Frames() != src.n {
		t.Fatal("Frames isn't the source's")
	}
}

func TestTempoShortSources(t *testing.T) {
	for _, n := range []int64{0, 1, 100, 5000} {
		src := &sineSource{rate: 44100, freq: 200, n: n}
		out := drain(t, NewTempo(src, func() float64 { return 1.5 }), 512)
		if int64(len(out)/2) > n+4000 {
			t.Errorf("%d frames in, %d out", n, len(out)/2)
		}
	}
}

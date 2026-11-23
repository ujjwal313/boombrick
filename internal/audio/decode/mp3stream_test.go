package decode

import (
	"os"
	"testing"
)

// feed plays data through an MP3Stream 1000 bytes at a time, keeping its
// buffer at 8 KB or more as Live does, and returns the frames and the rate.
// The audio must have samples other than 0, and all of them within [-1, 1]:
// 16-bit output read as float would not.
func feed(t *testing.T, data []byte) (total, rate int) {
	t.Helper()
	s := NewMP3Stream()
	defer s.Close()
	dst := make([]float32, 2*MP3FrameMax)
	var buf []byte
	heard := false
	for len(data) > 0 || len(buf) > 0 {
		for len(buf) < 8<<10 && len(data) > 0 {
			k := min(1000, len(data))
			buf = append(buf, data[:k]...)
			data = data[k:]
		}
		used, n, r := s.Decode(buf, dst)
		if n > 0 {
			rate = r
		}
		for _, x := range dst[:2*n] {
			if !(x >= -1 && x <= 1) {
				t.Fatalf("sample %v after %d frames, outside [-1, 1]", x, total)
			}
			heard = heard || x != 0
		}
		total += n
		if used == 0 {
			if len(data) == 0 {
				break
			}
			buf = append(buf, data[:min(1000, len(data))]...)
			data = data[min(1000, len(data)):]
			continue
		}
		buf = buf[used:]
	}
	if !heard {
		t.Fatal("decoded only silence")
	}
	return total, rate
}

func TestMP3StreamDecodesFrameByFrame(t *testing.T) {
	data, err := os.ReadFile(fixture(t, "stream-44100.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	total, rate := feed(t, data)
	if rate != 44100 {
		t.Errorf("rate = %d, want 44100", rate)
	}
	if want := 5 * 44100; total < want-2304 || total > want+2304 {
		t.Errorf("total = %d frames, want %d within 2304", total, want)
	}
	junk := make([]byte, 777)
	for i := range junk {
		junk[i] = byte(i*7 + 3)
	}
	jtotal, jrate := feed(t, append(junk, data...))
	if jrate != 44100 {
		t.Errorf("rate with junk = %d, want 44100", jrate)
	}
	if d := jtotal - total; d < -1152 || d > 1152 {
		t.Errorf("total with junk = %d, want %d within 1152", jtotal, total)
	}
}

func TestMP3StreamNeedsMoreBytes(t *testing.T) {
	s := NewMP3Stream()
	defer s.Close()
	dst := make([]float32, 2*MP3FrameMax)
	for _, buf := range [][]byte{nil, {0xFF}} {
		if used, n, _ := s.Decode(buf, dst); used != 0 || n != 0 {
			t.Errorf("Decode(%d bytes) = used %d, n %d, want 0, 0", len(buf), used, n)
		}
	}
}

// A frame isn't found until the next one's header follows it: a buffer
// holding no more than the start of the stream's first frame (417 bytes at
// 128 kbit/s), or that frame and part of the next header, is used whole,
// with no audio.
func TestMP3StreamSkipsAFrameCutShort(t *testing.T) {
	data, err := os.ReadFile(fixture(t, "stream-44100.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]float32, 2*MP3FrameMax)
	for _, k := range []int{4, 300, 419} {
		s := NewMP3Stream()
		if used, n, _ := s.Decode(data[:k], dst); used != k || n != 0 {
			t.Errorf("Decode(the first %d bytes) = used %d, n %d, want %d, 0", k, used, n, k)
		}
		s.Close()
	}
}

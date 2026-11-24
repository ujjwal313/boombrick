package radio

import (
	"bytes"
	"io"
	"log/slog"
	"testing"
	"testing/iotest"
	"time"
)

// drain decodes all of d and returns the frames and the rate of the last.
// The audio must have samples other than 0, and all of them within [-1, 1]:
// 16-bit output read as float would not.
func drain(t *testing.T, d decoder) (total, rate int) {
	t.Helper()
	defer d.close()
	dst := make([]float32, 2*maxFrame)
	heard := false
	for {
		n, r, err := d.next(dst)
		if err == io.EOF {
			if !heard {
				t.Fatal("decoded only silence")
			}
			return total, rate
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range dst[:2*n] {
			if !(x >= -1 && x <= 1) {
				t.Fatalf("sample %v after %d frames, outside [-1, 1]", x, total)
			}
			heard = heard || x != 0
		}
		if n > 0 {
			total, rate = total+n, r
		}
	}
}

// The MP3 decoder keeps enough of the stream to find its frames however it
// arrives, here a byte at a time after more zeros than its buffer holds.
func TestMP3Decoder(t *testing.T) {
	file := fixtureBytes(t, "stream-44100.mp3")
	junk := make([]byte, 100<<10)
	total, rate := drain(t, newMP3Decoder(iotest.OneByteReader(bytes.NewReader(append(junk, file...)))))
	if rate != 44100 {
		t.Errorf("rate = %d, want 44100", rate)
	}
	if want := 5 * 44100; total < want-2*1152 || total > want+2*1152 {
		t.Errorf("total = %d frames, want %d within 2304", total, want)
	}
}

// At the highest bitrate, fewer frames fit in what the MP3 decoder keeps
// ahead: a stream arriving a byte at a time must still lose none against the
// same stream read whole.
func TestMP3DecoderAt320k(t *testing.T) {
	file := fixtureBytes(t, "noise-320k.mp3")
	whole, _ := drain(t, newMP3Decoder(bytes.NewReader(file)))
	bytewise, _ := drain(t, newMP3Decoder(iotest.OneByteReader(bytes.NewReader(file))))
	if whole == 0 || bytewise != whole {
		t.Errorf("a byte at a time: %d frames, read whole: %d", bytewise, whole)
	}
}

// stallingReader gives b, then waits for stop, as a station that sent no more
// than b on connecting.
type stallingReader struct {
	b    []byte
	stop chan struct{}
}

func (r *stallingReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		<-r.stop
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}

// 8 KB is enough to start on, so a slow station without a burst opens in time.
func TestMP3DecoderStartsOn8KB(t *testing.T) {
	file := fixtureBytes(t, "stream-44100.mp3")
	stop := make(chan struct{})
	d := newMP3Decoder(&stallingReader{b: file[:8<<10], stop: stop})
	defer d.close()
	got := make(chan int, 1)
	go func() {
		dst := make([]float32, 2*maxFrame)
		for {
			if n, _, err := d.next(dst); err != nil || n > 0 {
				got <- n
				return
			}
		}
	}()
	select {
	case n := <-got:
		close(stop)
		if n == 0 {
			t.Error("no audio from the first 8 KB")
		}
	case <-time.After(2 * time.Second):
		close(stop)
		<-got
		t.Error("the decoder waited for more than 8 KB")
	}
}

func TestAACDecoder(t *testing.T) {
	file := fixtureBytes(t, "stream-44100.aac")
	junk := bytes.Repeat([]byte{0x12}, 1000)
	d := newAACDecoder(iotest.OneByteReader(bytes.NewReader(append(junk, file...))), slog.New(slog.DiscardHandler))
	total, rate := drain(t, d)
	if rate != 44100 {
		t.Errorf("rate = %d, want 44100", rate)
	}
	if want := 5 * 44100; total < want-2*maxFrame || total > want+2*maxFrame {
		t.Errorf("total = %d frames, want %d within %d", total, want, 2*maxFrame)
	}
}

package radio

import (
	"io"
	"log/slog"

	faad "github.com/ujjwal313/boombrick/internal/audio/aac" // aac names the codec here
	"github.com/ujjwal313/boombrick/internal/audio/decode"
)

// decoder turns a stream's bytes into stereo frames, one frame at a time.
type decoder interface {
	// next decodes into dst (2*maxFrame samples): the frames written, 0
	// while a frame decodes to none, and their rate.
	next(dst []float32) (n, rate int, err error)
	close()
}

const maxFrame = faad.MaxFrame // more than an MP3 frame's 1152

const (
	mp3Buffer = 64 << 10
	// mp3Hold is the least the MP3 decoder keeps ahead while the stream has
	// it. minimp3 checks a frame against the ones after it, and given too
	// little may take a frame for junk and skip it: 8 KB holds two frames
	// and a frame of junk at any layer III rate, the largest being about
	// 1441 bytes (320 kbit/s at 32 kHz). It is no more, so an 8 kbit/s
	// station that sends no burst on connecting still opens in about 8 s,
	// inside openTimeout.
	mp3Hold = 8 << 10
)

// mp3Decoder decodes MP3 with minimp3, from a buffer it keeps mp3Hold or more
// in while the stream has it.
type mp3Decoder struct {
	r          io.Reader
	s          *decode.MP3Stream
	buf        []byte
	start, end int
	err        error // the stream's, returned once what is held is spent
}

func newMP3Decoder(r io.Reader) *mp3Decoder {
	return &mp3Decoder{r: r, s: decode.NewMP3Stream(), buf: make([]byte, mp3Buffer)}
}

func (d *mp3Decoder) next(dst []float32) (int, int, error) {
	if d.end-d.start < mp3Hold {
		d.fill(mp3Hold)
	}
	if d.start == d.end {
		return 0, 0, d.err
	}
	used, n, rate := d.s.Decode(d.buf[d.start:d.end], dst)
	switch {
	case used > 0:
		d.start += used
	case d.err != nil:
		return 0, 0, d.err // the stream ended in the middle of a frame
	case d.end-d.start == len(d.buf):
		d.start = d.end - 3 // no frame in all of it; a header may start in the last bytes
	default:
		d.fill(d.end - d.start + 1) // a frame starts here but isn't all there
	}
	return n, rate, nil
}

// fill reads until the buffer holds want bytes, or the stream fails.
func (d *mp3Decoder) fill(want int) {
	if len(d.buf)-d.start < want {
		d.end = copy(d.buf, d.buf[d.start:d.end])
		d.start = 0
	}
	for d.end-d.start < want && d.err == nil {
		var k int
		k, d.err = d.r.Read(d.buf[d.end:])
		d.end += k
	}
}

func (d *mp3Decoder) close() { d.s.Close() }

// aacDecoder decodes the ADTS frames of an AAC or HE-AAC stream with faad2.
type aacDecoder struct {
	r      *adtsReader
	dec    *faad.Decoder
	log    *slog.Logger
	warned bool // a frame was rejected
}

func newAACDecoder(r io.Reader, log *slog.Logger) *aacDecoder {
	return &aacDecoder{r: newADTSReader(r), log: log}
}

func (d *aacDecoder) next(dst []float32) (int, int, error) {
	frame, err := d.r.Next()
	if err != nil {
		return 0, 0, err
	}
	if d.dec == nil {
		if d.dec, err = faad.NewADTS(frame); err != nil {
			d.warn(err) // try the next frame
			return 0, 0, nil
		}
	}
	n, err := d.dec.Decode(frame, dst)
	if err != nil {
		d.warn(err) // skip the frame
		return 0, 0, nil
	}
	rate := d.dec.FrameRate()
	if rate == 0 {
		rate = d.dec.Rate()
	}
	return n, rate, nil
}

// warn logs the first frame faad2 rejects; a stream with one usually has
// more, and they are skipped.
func (d *aacDecoder) warn(err error) {
	if !d.warned {
		d.warned = true
		d.log.Info("skipping AAC frames faad2 rejects", "err", err)
	}
}

func (d *aacDecoder) close() {
	if d.dec != nil {
		d.dec.Close()
	}
}

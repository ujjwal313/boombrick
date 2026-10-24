// Package decode opens audio files as audio.Sources using the vendored dr_mp3,
// dr_flac, opusfile and faad2 decoders.
package decode

/*
#cgo CFLAGS: -O2 -DNDEBUG
#cgo LDFLAGS: -lm
#include <stdlib.h>
#include "dr_mp3.h"
#include "dr_flac.h"

// In dr.c.
drmp3* bb_mp3_open(const char* path);
int bb_mp3_seek(drmp3* m, drmp3_uint64 frame, drmp3_uint64 cur);
void bb_mp3_close(drmp3* m);
*/
import "C"

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"github.com/ujjwal313/boombrick/internal/audio"
)

// ErrUnsupported is returned for formats no decoder handles.
var ErrUnsupported = errors.New("decode: unsupported format")

var errOpen = errors.New("not a readable file of its format")

// format is how files of one extension are opened and measured.
type format struct {
	open     func(path *C.char) (*decoder, error)
	duration func(r io.ReaderAt, size int64) (time.Duration, error)
}

var formats = map[string]format{
	".mp3":  {openMP3, mp3Duration},
	".flac": {openFLAC, flacDuration},
	".opus": {openOpus, opusDuration},
	".m4a":  {openM4A, m4aDuration},
	".m4b":  {openM4A, m4aDuration},
}

func formatOf(path string) (format, bool) {
	f, ok := formats[strings.ToLower(filepath.Ext(path))]
	return f, ok
}

// Supported reports whether Open handles path's format.
func Supported(path string) bool {
	_, ok := formatOf(path)
	return ok
}

// Open opens path with the decoder its extension names.
func Open(path string) (audio.Source, error) {
	f, ok := formatOf(path)
	if !ok {
		return nil, ErrUnsupported
	}
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	d, err := f.open(cpath)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return d, nil
}

// decoder adapts a C decoder producing ch channels to a stereo Source: mono is
// copied to both sides and more channels are downmixed.
type decoder struct {
	rate   int
	ch     int
	frames int64
	exact  bool  // frames is the true length, so ending early is an error
	pos    int64 // frames read so far
	read   func(dst unsafe.Pointer, frames int) (int, error)
	seek   func(frame int64) bool
	close  func()
	tmp    []float32
}

func (d *decoder) SampleRate() int { return d.rate }
func (d *decoder) Frames() int64   { return d.frames }

func (d *decoder) Read(buf []float32) (int, error) {
	if d.close == nil {
		return 0, io.EOF
	}
	want := len(buf) / 2
	if want == 0 {
		return 0, nil
	}
	var n int
	var err error
	if d.ch == 2 {
		n, err = d.read(unsafe.Pointer(&buf[0]), want)
	} else {
		if need := want * d.ch; len(d.tmp) < need {
			d.tmp = make([]float32, need)
		}
		n, err = d.read(unsafe.Pointer(&d.tmp[0]), want)
		toStereo(buf[:2*n], d.tmp[:n*d.ch], d.ch)
	}
	d.pos += int64(n)
	if err != nil {
		return n, err
	}
	if n < want {
		if d.exact && d.frames > 0 && d.pos < d.frames {
			return n, io.ErrUnexpectedEOF
		}
		return n, io.EOF
	}
	return n, nil
}

// Downmix weights for 3 to 8 channels in WAVE order (FL FR FC LFE BL BR SL
// SR, with fewer channels dropping from the end as FLAC lays them out): the
// centre and surrounds go in at -3 dB and the LFE is left out.
const c3 = 0.7071

var downmix = map[int][2][]float32{
	3: {{1, 0, c3}, {0, 1, c3}},
	4: {{1, 0, c3, 0}, {0, 1, 0, c3}},
	5: {{1, 0, c3, c3, 0}, {0, 1, c3, 0, c3}},
	6: {{1, 0, c3, 0, c3, 0}, {0, 1, c3, 0, 0, c3}},
	7: {{1, 0, c3, 0, c3, c3, 0}, {0, 1, c3, 0, c3, 0, c3}},
	8: {{1, 0, c3, 0, c3, 0, c3, 0}, {0, 1, c3, 0, 0, c3, 0, c3}},
}

// toStereo converts frames of ch channels in src to stereo in dst. Downmixes
// are scaled by the sum of their weights so a full-scale signal on every
// channel can't clip.
func toStereo(dst, src []float32, ch int) {
	n := len(dst) / 2
	if ch == 1 {
		for i := 0; i < n; i++ {
			dst[2*i], dst[2*i+1] = src[i], src[i]
		}
		return
	}
	w, ok := downmix[ch]
	if !ok { // more than 8: keep the front pair
		for i := 0; i < n; i++ {
			dst[2*i], dst[2*i+1] = src[i*ch], src[i*ch+1]
		}
		return
	}
	var sum float32
	for _, x := range w[0] {
		sum += x
	}
	for i := 0; i < n; i++ {
		var l, r float32
		for j := 0; j < ch; j++ {
			v := src[i*ch+j]
			l += v * w[0][j]
			r += v * w[1][j]
		}
		dst[2*i], dst[2*i+1] = l/sum, r/sum
	}
}

func (d *decoder) SeekFrame(frame int64) error {
	if d.close == nil {
		return errors.New("decode: seek on a closed source")
	}
	if !d.seek(frame) {
		return fmt.Errorf("decode: seek to frame %d failed", frame)
	}
	d.pos = frame
	return nil
}

func (d *decoder) Close() error {
	if d.close != nil {
		d.close()
		d.close = nil
	}
	return nil
}

func openMP3(path *C.char) (*decoder, error) {
	m := C.bb_mp3_open(path)
	if m == nil {
		return nil, errOpen
	}
	var frames int64
	if m.totalPCMFrameCount != ^C.drmp3_uint64(0) {
		frames = int64(C.drmp3_get_pcm_frame_count(m))
	} else {
		// Without a Xing or Info header dr_mp3 would read the whole file to
		// count the frames. An estimate does: an MP3's length isn't taken as
		// exact, so reads go on to the real end.
		frames = mp3Estimate(C.GoString(path), int(m.sampleRate))
	}
	if frames <= 0 {
		frames = -1
	}
	d := &decoder{
		rate:   int(m.sampleRate),
		ch:     int(m.channels),
		frames: frames,
		read: func(dst unsafe.Pointer, n int) (int, error) {
			return int(C.drmp3_read_pcm_frames_f32(m, C.drmp3_uint64(n), (*C.float)(dst))), nil
		},
		close: func() { C.bb_mp3_close(m) },
	}
	d.seek = func(f int64) bool { return C.bb_mp3_seek(m, C.drmp3_uint64(f), C.drmp3_uint64(d.pos)) != 0 }
	return d, nil
}

// mp3Estimate is the length the library scan gives an MP3 with no Xing or
// Info header, from its bitrate, or -1.
func mp3Estimate(path string, rate int) int64 {
	f, err := os.Open(path)
	if err != nil {
		return -1
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return -1
	}
	n, r, err := mp3Length(f, info.Size())
	if err != nil || r != int64(rate) {
		return -1
	}
	return n
}

func openFLAC(path *C.char) (*decoder, error) {
	f := C.drflac_open_file(path, nil)
	if f == nil {
		return nil, errOpen
	}
	frames := int64(f.totalPCMFrameCount)
	if frames == 0 {
		frames = -1 // not stored in the stream header
	}
	return &decoder{
		rate:   int(f.sampleRate),
		ch:     int(f.channels),
		frames: frames,
		exact:  frames > 0,
		read: func(dst unsafe.Pointer, n int) (int, error) {
			return int(C.drflac_read_pcm_frames_f32(f, C.drflac_uint64(n), (*C.float)(dst))), nil
		},
		seek:  func(frame int64) bool { return C.drflac_seek_to_pcm_frame(f, C.drflac_uint64(frame)) != 0 },
		close: func() { C.drflac_close(f) },
	}, nil
}

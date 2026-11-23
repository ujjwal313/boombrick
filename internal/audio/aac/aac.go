// Package aac decodes AAC frames with faad2, vendored here.
package aac

/*
#cgo CFLAGS: -O2 -w -DNDEBUG -DLC_ONLY_DECODER -DHAVE_STDINT_H=1 -DHAVE_STRING_H=1 -DSTDC_HEADERS=1 -include ${SRCDIR}/version.h
#cgo LDFLAGS: -lm
#include "neaacdec.h"

static NeAACDecHandle bb_open(unsigned char *config, unsigned long n, unsigned long *rate, unsigned char *ch) {
	NeAACDecHandle h = NeAACDecOpen();
	if (h == NULL) return NULL;
	NeAACDecConfigurationPtr c = NeAACDecGetCurrentConfiguration(h);
	c->outputFormat = FAAD_FMT_FLOAT;
	c->downMatrix = 1;
	NeAACDecSetConfiguration(h, c);
	// Init2 returns a char: -1 reads as 255 where char is unsigned, as on arm64.
	if (NeAACDecInit2(h, config, n, rate, ch) != 0) {
		NeAACDecClose(h);
		return NULL;
	}
	return h;
}

// bb_open_adts opens a decoder for an ADTS stream from its first frame,
// which must start at the header's sync word.
static NeAACDecHandle bb_open_adts(unsigned char *frame, unsigned long n, unsigned long *rate, unsigned char *ch) {
	NeAACDecHandle h = NeAACDecOpen();
	if (h == NULL) return NULL;
	NeAACDecConfigurationPtr c = NeAACDecGetCurrentConfiguration(h);
	c->outputFormat = FAAD_FMT_FLOAT;
	c->downMatrix = 1;
	c->defObjectType = LC;
	NeAACDecSetConfiguration(h, c);
	if (NeAACDecInit(h, frame, n, rate, ch) < 0) {
		NeAACDecClose(h);
		return NULL;
	}
	return h;
}

typedef struct {
	int error;
	unsigned long samples;
	unsigned long rate;
	unsigned char channels;
} bb_result;

// bb_decode decodes one frame into out, size bytes long, and reports faad2's
// error code and the samples (all channels) and channels it produced. The
// result comes back by value so Go keeps no pointers to C's results, which
// would cost an allocation per frame.
static bb_result bb_decode(NeAACDecHandle h, unsigned char *frame, unsigned long n, float *out, unsigned long size) {
	NeAACDecFrameInfo info;
	void *buf = out;
	bb_result r;
	NeAACDecDecode2(h, &info, frame, n, &buf, size);
	r.error = info.error;
	r.samples = info.samples;
	r.channels = info.channels;
	r.rate = info.samplerate;
	return r;
}
*/
import "C"

import (
	"errors"
	"fmt"
)

// MaxFrame is the most stereo frames one AAC frame decodes to: SBR doubles
// 1024.
const MaxFrame = 2048

// maxChannels bounds the channels a stream may have before downmixing.
const maxChannels = 8

// outChannels is the most channels a frame decodes to, given the channels the
// stream's config reports. faad2 reports and outputs mono as stereo, for
// parametric stereo; other layouts get room for maxChannels, as 7.1's
// configuration reports 7.
func outChannels(ch int) int {
	if ch <= 2 {
		return 2
	}
	return maxChannels
}

// Decoder decodes one AAC stream to stereo. It is not safe for concurrent use.
type Decoder struct {
	h    C.NeAACDecHandle
	rate int
	last int // the rate of the last frame that produced audio
	out  []float32
}

// New creates a decoder for the stream an AudioSpecificConfig describes.
func New(config []byte) (*Decoder, error) {
	if len(config) < 2 {
		return nil, errors.New("aac: no decoder config")
	}
	var rate C.ulong
	var ch C.uchar
	h := C.bb_open((*C.uchar)(&config[0]), C.ulong(len(config)), &rate, &ch)
	if h != nil && rate == 0 {
		C.NeAACDecClose(h)
		h = nil
	}
	if h == nil {
		return nil, errors.New("aac: unsupported decoder config")
	}
	return &Decoder{h: h, rate: int(rate), out: make([]float32, MaxFrame*outChannels(int(ch)))}, nil
}

// NewADTS creates a decoder for an ADTS stream, as a radio station sends it,
// from its first frame, which must start at the header's sync word.
func NewADTS(frame []byte) (*Decoder, error) {
	if len(frame) < 7 {
		return nil, errors.New("aac: not an ADTS stream")
	}
	var rate C.ulong
	var ch C.uchar
	h := C.bb_open_adts((*C.uchar)(&frame[0]), C.ulong(len(frame)), &rate, &ch)
	if h != nil && rate == 0 {
		C.NeAACDecClose(h)
		h = nil
	}
	if h == nil {
		return nil, errors.New("aac: not an ADTS stream")
	}
	return &Decoder{h: h, rate: int(rate), out: make([]float32, MaxFrame*outChannels(int(ch)))}, nil
}

// FrameRate is the rate the last frame that produced audio decoded at.
func (d *Decoder) FrameRate() int { return d.last }

// Rate is the decoded sample rate, which SBR, or faad2's upsampling of
// streams at 24 kHz or less in case they carry SBR, makes double the stream's.
func (d *Decoder) Rate() int { return d.rate }

// Decode decodes one frame into dst, which must hold 2*MaxFrame samples, and
// returns the stereo frames written. Mono is copied to both sides; faad2 mixes
// 5.1 down to stereo, and other layouts keep their first two channels.
func (d *Decoder) Decode(frame []byte, dst []float32) (int, error) {
	if len(frame) == 0 {
		return 0, nil
	}
	res := C.bb_decode(d.h, (*C.uchar)(&frame[0]), C.ulong(len(frame)),
		(*C.float)(&d.out[0]), C.ulong(4*len(d.out)))
	if res.error != 0 {
		return 0, fmt.Errorf("aac: %s", C.GoString(C.NeAACDecGetErrorMessage(C.uchar(res.error))))
	}
	c := int(res.channels)
	if c == 0 {
		return 0, nil
	}
	n := min(int(res.samples)/c, MaxFrame)
	if n > 0 {
		d.last = int(res.rate)
	}
	for i := 0; i < n; i++ {
		l := d.out[i*c]
		r := l
		if c > 1 {
			r = d.out[i*c+1]
		}
		dst[2*i], dst[2*i+1] = l, r
	}
	return n, nil
}

// Reset prepares to decode from frame after a seek.
func (d *Decoder) Reset(frame int) { C.NeAACDecPostSeekReset(d.h, C.long(frame)) }

// Close frees the decoder.
func (d *Decoder) Close() {
	if d.h != nil {
		C.NeAACDecClose(d.h)
		d.h = nil
	}
}

package decode

/*
#include <stdlib.h>
#include "dr_mp3.h"
*/
import "C"

import "unsafe"

// MP3FrameMax is the most frames one MP3 frame decodes to.
const MP3FrameMax = 1152

// MP3Stream decodes MP3 frame by frame from bytes as they arrive, for a
// stream that can't be opened as a file, as a radio station's. dr.c builds
// the decoder with float output. It is not safe for concurrent use.
type MP3Stream struct {
	dec *C.drmp3dec
	pcm []float32 // one frame as minimp3 writes it, mono or stereo
}

// NewMP3Stream returns a decoder for a stream from its start. Close frees it.
func NewMP3Stream() *MP3Stream {
	d := (*C.drmp3dec)(C.calloc(1, C.sizeof_drmp3dec))
	if d == nil {
		panic("decode: no memory for an MP3 stream decoder")
	}
	C.drmp3dec_init(d)
	return &MP3Stream{dec: d, pcm: make([]float32, 2*MP3FrameMax)}
}

// Decode decodes the first frame in buf into dst, which must hold
// 2*MP3FrameMax samples, as stereo. It returns the bytes used, junk before
// the frame included, the frames written, and their rate. minimp3 finds a
// frame only when it is whole and the next frame's header follows it, or
// when it is all of buf. A buf with no frame it finds is used whole, with n
// 0, a frame cut short included; so buf should hold 8 KB while the stream
// has that much, room for two of the largest frames and a frame of junk.
// used is 0 only for a buf shorter than a frame header. A frame can decode
// to nothing while minimp3 gathers its bit reservoir.
func (m *MP3Stream) Decode(buf []byte, dst []float32) (used, n, rate int) {
	if len(buf) < 4 { // a frame header is 4 bytes
		return 0, 0, 0
	}
	var info C.drmp3dec_frame_info
	n = int(C.drmp3dec_decode_frame(m.dec, (*C.drmp3_uint8)(unsafe.Pointer(&buf[0])), C.int(len(buf)),
		unsafe.Pointer(&m.pcm[0]), &info))
	used, rate = int(info.frame_bytes), int(info.sample_rate)
	ch := max(int(info.channels), 1)
	for i := range n {
		l := m.pcm[i*ch]
		r := l
		if ch > 1 {
			r = m.pcm[i*ch+1]
		}
		dst[2*i], dst[2*i+1] = l, r
	}
	return used, n, rate
}

func (m *MP3Stream) Close() {
	if m.dec != nil {
		C.free(unsafe.Pointer(m.dec))
		m.dec = nil
	}
}

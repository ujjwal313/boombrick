package radio

import (
	"bytes"
	"io"
	"mime"
	"strings"
)

type codec uint8

const (
	unknown codec = iota
	mp3
	aac
)

func (c codec) String() string {
	switch c {
	case mp3:
		return "MP3"
	case aac:
		return "AAC"
	}
	return "unknown"
}

// codecOf reads the codec a station's Content-Type names. The second result is
// true for a playlist, or anything else that isn't audio, which can't be played
// as a stream whatever its bytes are.
func codecOf(contentType string) (c codec, playlist bool) {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(contentType))
	}
	switch mt {
	case "audio/mpeg", "audio/mp3":
		return mp3, false
	case "audio/aac", "audio/aacp", "audio/x-aac":
		return aac, false
	case "audio/x-scpls", "audio/scpls", "audio/x-mpegurl", "audio/mpegurl",
		"application/vnd.apple.mpegurl", "application/x-mpegurl",
		"application/pls+xml", "text/html":
		return unknown, true
	}
	return unknown, false
}

// adtsFrame is the length of the ADTS frame starting b, header included, or
// 0 when b doesn't start with an ADTS header: sync 0xFFF, layer 0, a
// sampling index below 13, and a length that holds the header, 7 bytes or 9
// with a CRC.
func adtsFrame(b []byte) int {
	if len(b) < 7 || b[0] != 0xFF || b[1]&0xF6 != 0xF0 || (b[2]>>2)&0x0F > 12 {
		return 0
	}
	n := int(b[3]&0x03)<<11 | int(b[4])<<3 | int(b[5])>>5
	head := 7
	if b[1]&0x01 == 0 {
		head = 9
	}
	if n < head {
		return 0
	}
	return n
}

// mp3Header reports whether b starts with an MPEG audio frame header.
func mp3Header(b []byte) bool {
	return len(b) >= 4 && b[0] == 0xFF && b[1]&0xE0 == 0xE0 && (b[1]>>1)&3 != 0 &&
		(b[1]>>3)&3 != 1 && b[2]>>4 != 0x0F && (b[2]>>2)&3 != 3
}

// mp3Bitrates are the bitrates in kbit/s by version (MPEG 1, then MPEG 2 and
// 2.5), layer and header index; index 0 is free format and 15 is invalid.
var mp3Bitrates = [2][3][14]int{
	{
		{32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448},
		{32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384},
		{32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
	},
	{
		{32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256},
		{8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
		{8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
	},
}

// mp3Frame is the length of the MPEG audio frame starting b, or 0 when b
// doesn't start with one or its bitrate is free.
func mp3Frame(b []byte) int {
	if !mp3Header(b) || b[2]>>4 == 0 {
		return 0
	}
	v := 1 // MPEG 2 and 2.5
	rates := [3]int{22050, 24000, 16000}
	switch (b[1] >> 3) & 3 {
	case 3:
		v = 0
		rates = [3]int{44100, 48000, 32000}
	case 0:
		rates = [3]int{11025, 12000, 8000}
	}
	layer := 3 - int((b[1]>>1)&3) // 0 is layer I
	rate := rates[(b[2]>>2)&3]
	bitrate := mp3Bitrates[v][layer][b[2]>>4-1] * 1000
	pad := int(b[2]>>1) & 1
	switch {
	case layer == 0:
		return (12*bitrate/rate + pad) * 4
	case layer == 2 && v == 1:
		return 72*bitrate/rate + pad
	}
	return 144*bitrate/rate + pad
}

// sniff tells MP3 from AAC by the first bytes of a stream: an ID3 tag is MP3,
// and so is an MPEG frame followed by another, or running past the end of b.
// Two ADTS frames one after the other are AAC. A header alone isn't enough,
// as 0xFF turns up in any data.
func sniff(b []byte) codec {
	if bytes.HasPrefix(b, []byte("ID3")) {
		return mp3
	}
	for i := 0; i < len(b); i++ {
		if b[i] != 0xFF {
			continue
		}
		if n := adtsFrame(b[i:]); n > 0 && i+n < len(b) && adtsFrame(b[i+n:]) > 0 {
			return aac
		}
		if n := mp3Frame(b[i:]); n > 0 && (i+n+4 > len(b) || mp3Header(b[i+n:])) {
			return mp3
		}
	}
	return unknown
}

// adtsReader splits a raw AAC stream into ADTS frames, skipping whatever comes
// before the first one and any bytes that only look like a header.
type adtsReader struct {
	r          io.Reader
	buf        [16 << 10]byte
	start, end int
}

func newADTSReader(r io.Reader) *adtsReader {
	return &adtsReader{r: r}
}

// fill reads until the buffer holds n bytes from start, or the reader fails.
func (a *adtsReader) fill(n int) error {
	if a.start > 0 {
		a.end = copy(a.buf[:], a.buf[a.start:a.end])
		a.start = 0
	}
	for a.end < n {
		m, err := a.r.Read(a.buf[a.end:])
		a.end += m
		if a.end >= n {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Next returns the next frame, header included. It is valid until the next
// call. At the clean end of the stream it returns io.EOF.
func (a *adtsReader) Next() ([]byte, error) {
	for {
		i := bytes.IndexByte(a.buf[a.start:a.end], 0xFF)
		if i < 0 {
			a.start = a.end
			if err := a.fill(7); err != nil {
				return nil, err
			}
			continue
		}
		a.start += i
		if a.end-a.start < 7 {
			if err := a.fill(7); err != nil {
				return nil, err
			}
		}
		n := adtsFrame(a.buf[a.start:a.end])
		if n == 0 {
			a.start++
			continue
		}
		// The frame and the start of the one after it, to be sure this is
		// a frame and not 0xFF in the middle of something else.
		err := a.fill(n + 7)
		if err != nil && a.end-a.start < n {
			return nil, err
		}
		if err == nil && adtsFrame(a.buf[a.start+n:a.end]) == 0 {
			a.start++
			continue
		}
		frame := a.buf[a.start : a.start+n]
		a.start += n
		return frame, nil
	}
}

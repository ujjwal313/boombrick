package decode

import (
	"io"
	"os"
	"time"
)

// Chapter is one chapter of an audiobook: where it starts and what it is
// called.
type Chapter struct {
	Start time.Duration
	Title string
}

// maxChapters bounds what a file may claim; a chpl box counts them in a byte.
const maxChapters = 255

// Chapters reads the chapter list of the MP4 file at path (the Nero "chpl"
// box, which Audible books carry), in order. A file with no chapters gives
// none and no error.
func Chapters(path string) ([]Chapter, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return readChapters(f, st.Size())
}

func readChapters(r io.ReaderAt, size int64) ([]Chapter, error) {
	var p []byte
	err := eachBox(r, 0, size, func(b box) error {
		if b.typ != "moov" {
			return nil
		}
		return eachBox(r, b.start, b.end, func(b box) error {
			if b.typ != "udta" || p != nil {
				return nil
			}
			return eachBox(r, b.start, b.end, func(b box) error {
				if b.typ != "chpl" || p != nil {
					return nil
				}
				var err error
				p, err = payload(r, b)
				return err
			})
		})
	})
	if err != nil {
		return nil, err
	}
	return parseChpl(p), nil
}

// parseChpl reads a chpl box's payload: a version and flags, four reserved
// bytes in version 1, the number of chapters, then for each its start in
// 100 ns ticks and its title, with a one-byte length. A damaged list gives
// the chapters read before the damage.
func parseChpl(p []byte) []Chapter {
	if len(p) == 0 {
		return nil
	}
	b := be{p: p}
	version := b.uint(1)
	b.bytes(3)
	if version == 1 {
		b.bytes(4)
	}
	n := int(b.uint(1))
	var out []Chapter
	for i := 0; i < min(n, maxChapters); i++ {
		start := b.uint(8)
		title := b.bytes(int(b.uint(1)))
		if b.bad || start > maxTicks*10 {
			break
		}
		out = append(out, Chapter{time.Duration(start) * 100, string(title)})
	}
	return out
}

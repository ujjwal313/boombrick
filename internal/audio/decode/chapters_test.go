package decode

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
	"time"
)

func chplBox(chs ...Chapter) []byte {
	p := []byte{1, 0, 0, 0, 0, 0, 0, 0, byte(len(chs))}
	for _, c := range chs {
		p = binary.BigEndian.AppendUint64(p, uint64(c.Start/100))
		p = append(p, byte(len(c.Title)))
		p = append(p, c.Title...)
	}
	return p
}

func mp4With(chpl []byte) []byte {
	box := func(typ string, body ...[]byte) []byte {
		b := bytes.Join(body, nil)
		return append(binary.BigEndian.AppendUint32(nil, uint32(8+len(b))), append([]byte(typ), b...)...)
	}
	return box("moov", box("udta", box("chpl", chpl)))
}

func TestChapters(t *testing.T) {
	want := []Chapter{{0, "Capitolo 1"}, {1337027 * time.Millisecond, "Capitolo 2"}, {2727879 * time.Millisecond, "È la fine"}}
	data := mp4With(chplBox(want...))
	got, err := readChapters(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chapter %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestChaptersNone(t *testing.T) {
	data := mp4With(nil)
	got, err := readChapters(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want none", got, err)
	}
}

func TestChaptersCutShort(t *testing.T) {
	p := chplBox(Chapter{0, "Uno"}, Chapter{time.Minute, "Due"})
	got := parseChpl(p[:len(p)-2]) // the last title is cut off
	if len(got) != 1 || got[0].Title != "Uno" {
		t.Fatalf("got %v, want only the first chapter", got)
	}
}

func TestChaptersOfARealBook(t *testing.T) {
	path := realBook(t)
	got, err := Chapters(path)
	if err != nil || len(got) == 0 {
		t.Fatalf("got %v, %v", got, err)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Start <= got[i-1].Start {
			t.Errorf("chapter %d starts at %v, not after %v", i, got[i].Start, got[i-1].Start)
		}
	}
}

// realBook is an audiobook to test with: the file BOOMBRICK_M4B names, or the
// test is skipped.
func realBook(t *testing.T) string {
	t.Helper()
	p := os.Getenv("BOOMBRICK_M4B")
	if p == "" {
		t.Skip("BOOMBRICK_M4B names no audiobook")
	}
	return p
}

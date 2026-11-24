package space

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// box is a cache of n bytes.
type box struct {
	mu      sync.Mutex
	n       int64
	clears  int
	measure chan struct{} // when set, Size waits for it
}

func (b *box) Size() (int64, error) {
	if b.measure != nil {
		<-b.measure
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n, nil
}

func (b *box) Clear() (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := b.n
	b.n, b.clears = 0, b.clears+1
	return n, nil
}

func wait(t *testing.T, m *Meter, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if ok() {
			return
		}
		select {
		case <-m.Ready():
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestMeasuring(t *testing.T) {
	bin := t.TempDir()
	os.MkdirAll(filepath.Join(bin, "yt-dlp"), 0o755)
	os.WriteFile(filepath.Join(bin, "yt-dlp", "program"), make([]byte, 700), 0o644)
	m := New(Options{Art: &box{n: 300}, Lyrics: &box{n: 20}, Fonts: &box{n: 4000}, Downloader: bin})
	defer m.Close()
	if _, ok := m.Sizes(); ok {
		t.Fatal("sizes before they were measured")
	}
	m.Measure()
	wait(t, m, "sizes", func() bool { _, ok := m.Sizes(); return ok })
	if s, _ := m.Sizes(); s != (Sizes{Art: 300, Lyrics: 20, Fonts: 4000, Downloader: 700}) {
		t.Fatalf("sizes %+v", s)
	}
}

func TestSizesAreNotShownWhileMeasuring(t *testing.T) {
	art := &box{n: 300}
	m := New(Options{Art: art})
	defer m.Close()
	m.Measure()
	wait(t, m, "sizes", func() bool { _, ok := m.Sizes(); return ok })
	art.measure = make(chan struct{})
	m.Measure()
	if _, ok := m.Sizes(); ok {
		t.Fatal("old sizes shown while measuring again")
	}
	close(art.measure)
	wait(t, m, "sizes", func() bool { _, ok := m.Sizes(); return ok })
}

func TestClearing(t *testing.T) {
	art, lyrics, parts, fonts := &box{n: 300}, &box{n: 20}, &box{n: 1000}, &box{n: 4000}
	m := New(Options{Art: art, Lyrics: lyrics, Parts: parts, Fonts: fonts})
	defer m.Close()
	m.Clear()
	wait(t, m, "the clear", func() bool { _, ok := m.Sizes(); return ok })
	freed, ok := m.Cleared()
	if !ok || freed != 5320 {
		t.Fatalf("Cleared = %d, %v; want 5320", freed, ok)
	}
	if _, ok := m.Cleared(); ok {
		t.Fatal("Cleared reported the same clear twice")
	}
	if s, _ := m.Sizes(); s != (Sizes{}) || art.clears != 1 || lyrics.clears != 1 || parts.clears != 1 || fonts.clears != 1 {
		t.Fatalf("after the clear: sizes %+v, clears %d %d %d %d", s, art.clears, lyrics.clears, parts.clears, fonts.clears)
	}
}

func TestFuncs(t *testing.T) {
	n := int64(50)
	c := Funcs{SizeOf: func() int64 { return n }, Empty: func() int64 { old := n; n = 0; return old }}
	if got, _ := c.Size(); got != 50 {
		t.Fatalf("Size = %d", got)
	}
	if got, _ := c.Clear(); got != 50 || n != 0 {
		t.Fatalf("Clear = %d, left %d", got, n)
	}
}

func TestCloseTwice(t *testing.T) {
	m := New(Options{})
	m.Close()
	m.Close()
	m.Measure() // after Close: nothing happens
}

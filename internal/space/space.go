// Package space measures what Boombrick keeps on the SD card besides the
// music, for Settings' Storage page, and clears its caches. The page asks
// from the UI thread and never waits: the work runs on a goroutine of its
// own, and Ready receives a value when an answer is in.
package space

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Sizes is what the Storage page lists, in bytes.
type Sizes struct {
	Art, Lyrics, Fonts, Parts, Downloader int64
}

// Cache can say how much room it takes and empty itself. *art.Cache and
// *lyrics.Service are caches.
type Cache interface {
	Size() (int64, error)
	Clear() (int64, error)
}

// Funcs is a Cache made of two functions, for one that can't fail, as the
// download queue's leftover files.
type Funcs struct {
	SizeOf func() int64
	Empty  func() int64
}

func (f Funcs) Size() (int64, error)  { return f.SizeOf(), nil }
func (f Funcs) Clear() (int64, error) { return f.Empty(), nil }

// Options say what to measure. A nil cache counts as empty.
type Options struct {
	Art, Lyrics, Fonts, Parts Cache
	Downloader                string // yt-dlp's folder: measured, never cleared
	Log                       *slog.Logger
}

// Meter measures and clears on its own goroutine.
type Meter struct {
	opts  Options
	wake  chan struct{}
	ready chan struct{}
	done  chan struct{}

	mu       sync.Mutex
	measure  bool // asked to measure
	clear    bool // asked to clear
	sizes    Sizes
	measured bool // sizes are current: nothing asked since they were taken
	freed    int64
	cleared  bool // freed is waiting to be taken
	closed   bool
}

// New starts a meter; Close stops it.
func New(opts Options) *Meter {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	m := &Meter{opts: opts, wake: make(chan struct{}, 1), ready: make(chan struct{}, 1), done: make(chan struct{})}
	go m.run()
	return m
}

// Measure asks for the sizes again.
func (m *Meter) Measure() { m.ask(func() { m.measure = true }) }

// Clear empties the art cache, the fetched lyrics, the downloaded fonts and
// the leftover partial files, then measures again. Cleared then reports what
// it freed.
func (m *Meter) Clear() { m.ask(func() { m.clear, m.measure = true, true }) }

func (m *Meter) ask(f func()) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	f()
	m.measured = false
	m.mu.Unlock()
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Sizes returns the sizes last measured; ok is false while they are being
// measured, or before they ever were.
func (m *Meter) Sizes() (Sizes, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sizes, m.measured
}

// Cleared returns the bytes the last Clear freed, once.
func (m *Meter) Cleared() (int64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cleared {
		return 0, false
	}
	m.cleared = false
	return m.freed, true
}

// Ready receives a value when sizes or a clear's result are in. It is closed
// by Close.
func (m *Meter) Ready() <-chan struct{} { return m.ready }

// Close stops the meter after the work under way and closes Ready.
func (m *Meter) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()
	close(m.wake)
	<-m.done
	close(m.ready)
}

func (m *Meter) run() {
	defer close(m.done)
	for range m.wake {
		m.mu.Lock()
		clearing, measuring := m.clear, m.measure
		m.clear, m.measure = false, false
		m.mu.Unlock()
		if clearing {
			freed := m.empty()
			m.mu.Lock()
			m.freed, m.cleared = freed, true
			m.mu.Unlock()
		}
		if measuring {
			s := m.sizesNow()
			m.mu.Lock()
			if !m.measure && !m.clear { // not asked again meanwhile
				m.sizes, m.measured = s, true
			}
			m.mu.Unlock()
		}
		select {
		case m.ready <- struct{}{}:
		default:
		}
	}
}

func (m *Meter) sizesNow() Sizes {
	var s Sizes
	s.Art = m.size("art", m.opts.Art)
	s.Lyrics = m.size("lyrics", m.opts.Lyrics)
	s.Fonts = m.size("fonts", m.opts.Fonts)
	s.Parts = m.size("partial downloads", m.opts.Parts)
	if m.opts.Downloader != "" {
		n, err := dirSize(m.opts.Downloader)
		if err != nil {
			m.opts.Log.Warn("measure the downloader", "err", err)
		}
		s.Downloader = n
	}
	return s
}

func (m *Meter) size(what string, c Cache) int64 {
	if c == nil {
		return 0
	}
	n, err := c.Size()
	if err != nil {
		m.opts.Log.Warn("measure a cache", "cache", what, "err", err)
	}
	return n
}

// empty clears the caches and reports the bytes freed.
func (m *Meter) empty() int64 {
	var freed int64
	for _, c := range []struct {
		what string
		c    Cache
	}{{"art", m.opts.Art}, {"lyrics", m.opts.Lyrics}, {"fonts", m.opts.Fonts}, {"partial downloads", m.opts.Parts}} {
		if c.c == nil {
			continue
		}
		n, err := c.c.Clear()
		if err != nil {
			m.opts.Log.Warn("clear a cache", "cache", c.what, "err", err)
		}
		freed += n
	}
	m.opts.Log.Info("cleared the caches", "freed", freed)
	return freed
}

// dirSize adds up the sizes of the files under dir; a missing dir is empty.
func dirSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n, err
}

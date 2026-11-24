// Package fonts keeps the fonts Boombrick downloads for other scripts and
// emoji, and fetches them the first time text needs one.
package fonts

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ujjwal313/boombrick/internal/netx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// Store keeps the downloaded fonts in a folder and fetches those asked for,
// one at a time on a goroutine of its own, while online. It is the text
// engine's Source and a cache for Settings' Storage page; any goroutine may
// call its methods.
type Store struct {
	opts   Options
	byName map[string]text.Download
	ready  chan struct{}
	wake   chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	on      map[string]bool     // the fonts in the folder
	pending map[string]*attempt // asked for and not fetched yet
}

// attempt is how a font asked for has fared: how often fetching it failed,
// and when to try again.
type attempt struct {
	fails int
	next  time.Time
}

// Options say where the fonts are kept and fetched from.
type Options struct {
	Dir    string // the fonts folder
	HTTP   *http.Client
	Base   string          // the URL the files are under, ending in "/": the fonts release's
	Fonts  []text.Download // the catalog
	Online func() bool     // nil: never online
	Log    *slog.Logger

	// retry is the wait after a first failure, doubling up to maxRetry, and
	// offline how often the network is looked at while it is down; tests
	// shorten them.
	retry, maxRetry, offline time.Duration
}

// Release is the URL the files of the fonts release tag are under.
func Release(tag string) string {
	return "https://github.com/ujjwal313/boombrick/releases/download/" + tag + "/"
}

// Prune empties root, the folder that holds a folder of fonts for each fonts
// release, of all but keep's. A new release recuts fonts under the same
// names, so an older release's file mustn't pass for one of its own. A root
// not made yet is left alone.
func Prune(root, keep string) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	for _, e := range entries {
		if e.Name() != keep {
			if rerr := os.RemoveAll(filepath.Join(root, e.Name())); rerr != nil && err == nil {
				err = rerr
			}
		}
	}
	return err
}

// maxFont is the most a font may unpack to: the largest, Japanese, is under
// 3 MB.
const maxFont = 32 << 20

var errTooLarge = errors.New("the font unpacks to more than 32 MB")

// New starts a store over the fonts in opts.Dir, removing what a fetch cut
// short left behind; Close stops it.
func New(opts Options) *Store {
	if opts.Online == nil {
		opts.Online = func() bool { return false }
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.retry == 0 {
		opts.retry, opts.maxRetry, opts.offline = time.Minute, time.Hour, 30*time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Store{opts: opts, byName: map[string]text.Download{}, ready: make(chan struct{}, 1),
		wake: make(chan struct{}, 1), ctx: ctx, cancel: cancel, done: make(chan struct{}),
		on: map[string]bool{}, pending: map[string]*attempt{}}
	for _, d := range opts.Fonts {
		s.byName[d.Name] = d
	}
	entries, _ := os.ReadDir(opts.Dir)
	for _, e := range entries {
		switch name := e.Name(); {
		case strings.HasSuffix(name, ".part"):
			os.Remove(filepath.Join(opts.Dir, name))
		case s.byName[name].Name != "":
			s.on[name] = true
		}
	}
	go s.run()
	return s
}

// Close stops fetching, waiting for the goroutine.
func (s *Store) Close() {
	s.cancel()
	<-s.done
}

// Has reports whether the font name is in the folder.
func (s *Store) Has(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.on[name]
}

// Open opens the font name.
func (s *Store) Open(name string) (text.File, error) { return os.Open(filepath.Join(s.opts.Dir, name)) }

// Want asks for the font name, without waiting; one in the folder, asked for
// already or not in the catalog is left alone.
func (s *Store) Want(name string) {
	s.mu.Lock()
	if s.on[name] || s.pending[name] != nil || s.byName[name].Name == "" {
		s.mu.Unlock()
		return
	}
	s.pending[name] = &attempt{}
	s.mu.Unlock()
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Bad removes the font name, which wouldn't parse, and asks for it again.
func (s *Store) Bad(name string) {
	s.mu.Lock()
	delete(s.on, name)
	s.mu.Unlock()
	s.opts.Log.Warn("a font won't parse; fetching it again", "font", name)
	os.Remove(filepath.Join(s.opts.Dir, name))
	s.Want(name)
}

// Ready receives a value when a font has arrived.
func (s *Store) Ready() <-chan struct{} { return s.ready }

// Size is the room the fonts take.
func (s *Store) Size() (int64, error) {
	var n int64
	err := s.files(func(_ string, info fs.FileInfo) { n += info.Size() })
	return n, err
}

// Clear removes the fonts and reports the bytes freed. Text drawn with one
// already loaded is unchanged until Boombrick quits; one needed later is
// fetched again.
func (s *Store) Clear() (int64, error) {
	s.mu.Lock()
	clear(s.on)
	s.mu.Unlock()
	var freed int64
	err := s.files(func(path string, info fs.FileInfo) {
		if os.Remove(path) == nil {
			freed += info.Size()
		}
	})
	return freed, err
}

// files calls f for each file in the folder; a missing folder has none.
func (s *Store) files(f func(path string, info fs.FileInfo)) error {
	entries, err := os.ReadDir(s.opts.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			f(filepath.Join(s.opts.Dir, e.Name()), info)
		}
	}
	return err
}

func (s *Store) run() {
	defer close(s.done)
	for {
		name, wait := s.due()
		if name != "" && wait <= 0 && !s.opts.Online() {
			name, wait = "", s.opts.offline
		}
		if name == "" || wait > 0 {
			var timer *time.Timer
			var at <-chan time.Time
			if wait > 0 {
				timer = time.NewTimer(wait)
				at = timer.C
			}
			select {
			case <-s.ctx.Done():
				return
			case <-s.wake:
			case <-at:
			}
			if timer != nil {
				timer.Stop()
			}
			continue
		}
		err := s.fetch(name)
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if err != nil {
			a := s.pending[name]
			a.fails++
			a.next = time.Now().Add(s.wait(a.fails))
			s.mu.Unlock()
			s.opts.Log.Warn("fetch a font", "font", name, "err", err, "fails", a.fails)
			continue
		}
		delete(s.pending, name)
		s.on[name] = true
		s.mu.Unlock()
		s.opts.Log.Info("fetched a font", "font", name)
		select {
		case s.ready <- struct{}{}:
		default:
		}
	}
}

// due is the font asked for that is due soonest, and how long until it is:
// none or less when it is due now. It is "" when none is asked for.
func (s *Store) due() (string, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name, at := "", time.Time{}
	for n, a := range s.pending {
		if name == "" || a.next.Before(at) || a.next.Equal(at) && n < name {
			name, at = n, a.next
		}
	}
	if name == "" {
		return "", 0
	}
	return name, time.Until(at)
}

// wait is how long to wait after a font's fails-th failure: retry, doubling,
// up to maxRetry.
func (s *Store) wait(fails int) time.Duration {
	d := s.opts.retry
	for i := 1; i < fails && d < s.opts.maxRetry; i++ {
		d *= 2
	}
	return min(d, s.opts.maxRetry)
}

// fetch downloads the font name, checks it against the catalog, unpacks it
// beside its place and moves it in.
func (s *Store) fetch(name string) error {
	d := s.byName[name]
	if err := os.MkdirAll(s.opts.Dir, 0o755); err != nil {
		return err
	}
	gz := filepath.Join(s.opts.Dir, name+".gz.part")
	defer os.Remove(gz)
	if err := netx.Download(s.ctx, s.opts.HTTP, s.opts.Base+name+".gz", gz, d.Sum, d.Size, nil); err != nil {
		return err
	}
	part := filepath.Join(s.opts.Dir, name+".part")
	if err := unpack(gz, part); err != nil {
		os.Remove(part)
		return err
	}
	return os.Rename(part, filepath.Join(s.opts.Dir, name))
}

// unpack writes the gzip file gz out to path and flushes it to the card: FAT
// can lose a file's data to a power cut after the rename.
func unpack(gz, path string) error {
	in, err := os.Open(gz)
	if err != nil {
		return err
	}
	defer in.Close()
	r, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(r, maxFont+1))
	if err == nil && n > maxFont {
		err = errTooLarge
	}
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

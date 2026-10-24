// Package audiobooks is the listener's Audible library on the device: it keeps
// the list of their books, and downloads a book to the card on request, where
// the Library then finds it. It is made for Boombrick's Books tab.
package audiobooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ujjwal313/boombrick/internal/audible"
)

const (
	listTimeout    = 30 * time.Second
	licenseTimeout = 30 * time.Second

	// tries is how many times a licence or a file is asked for before the
	// failure is shown; each wait is twice the one before, from Options.retry.
	tries      = 4
	retryAfter = 3 * time.Second
)

// API is what the service needs of Audible. *audible.Client is one.
type API interface {
	Library(ctx context.Context) ([]audible.Book, error)
	License(ctx context.Context, asin string) (audible.License, error)
	Fetch(ctx context.Context, l audible.License, w io.Writer, from int64, progress func(done, total int64)) error
}

// State is what is being done with a book.
type State uint8

const (
	Idle        State = iota
	Queued            // waiting for the download before it
	Downloading       // the file is coming
	Decrypting        // the file is being opened
	Failed            // the last try failed; Err says why
)

// Entry is a book of the library, and what is known of it on the device.
type Entry struct {
	audible.Book
	Path     string  // the book's file on the card, or ""
	State    State   // what a download of it is doing
	Progress float64 // of the download, 0 to 1
	Err      string  // why the last try failed
}

// Status is how the list of books stands.
type Status uint8

const (
	NotSignedIn Status = iota // no Audible registration was given
	Loading                   // the list is being fetched, and none is kept
	Offline                   // there is no list, and no way to get one
	Broken                    // fetching the list failed; see Error
	OK
)

// Options are what a Service is made of.
type Options struct {
	API    API    // nil when the device isn't signed in to Audible
	Dir    string // where books are filed, as Dir/<author>/Title [ASIN].m4b
	Cache  string // the file that keeps the list between runs
	FFmpeg string // the program that opens a downloaded book
	Online func() bool
	Log    *slog.Logger
	// Filed hears of each book once it is in Dir, with its path.
	Filed func(path string)
	// Open turns the downloaded file at in into the book at out, given its key
	// and IV. Nil uses FFmpeg.
	Open func(ctx context.Context, in, out, key, iv string) error

	retry time.Duration // the first wait before a try again; tests shorten it
}

// Service keeps the library and runs downloads, one at a time.
type Service struct {
	o     Options
	ready chan struct{}
	queue chan string
	stop  context.CancelFunc
	wg    sync.WaitGroup

	mu     sync.Mutex
	books  []audible.Book
	local  map[string]string // ASIN to the file on the card
	jobs   map[string]*job
	status Status
	err    string
}

type job struct {
	state    State
	progress float64
	err      string
}

// New makes a service and starts its downloader; Close stops it. The list kept
// from the last run is available at once.
func New(o Options) *Service {
	if o.retry == 0 {
		o.retry = retryAfter
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Online == nil {
		o.Online = func() bool { return true }
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{o: o, ready: make(chan struct{}, 1), queue: make(chan string, 64), stop: cancel,
		local: map[string]string{}, jobs: map[string]*job{}}
	if o.API == nil {
		s.status = NotSignedIn
	} else {
		s.books = s.loadCache()
		s.status = Loading
		if len(s.books) > 0 {
			s.status = OK
		}
	}
	s.scanLocal()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.run(ctx)
	}()
	return s
}

// Close stops the downloader, abandoning a download under way.
func (s *Service) Close() {
	s.stop()
	s.wg.Wait()
}

// Ready receives whenever what the service shows has changed.
func (s *Service) Ready() <-chan struct{} { return s.ready }

func (s *Service) changed() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// SignedIn reports whether the device has an Audible registration.
func (s *Service) SignedIn() bool { return s.api() != nil }

// api is the Audible API, or nil without a registration.
func (s *Service) api() API {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.o.API
}

// SetAPI gives a device that had no registration one, as signing in does, and
// reads the library with it.
func (s *Service) SetAPI(a API) {
	s.mu.Lock()
	s.o.API = a
	s.status, s.err = Loading, ""
	s.mu.Unlock()
	s.changed()
	s.Refresh()
}

// Status says how the list stands, with the reason when it is Broken.
func (s *Service) Status() (Status, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.err
}

// Entries are the library's books, newest first, with what is going on with each.
func (s *Service) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.books))
	for i, b := range s.books {
		e := Entry{Book: b, Path: s.local[b.ASIN]}
		if j := s.jobs[b.ASIN]; j != nil {
			e.State, e.Progress, e.Err = j.state, j.progress, j.err
		}
		out[i] = e
	}
	return out
}

// Refresh fetches the list of books again, in the background.
func (s *Service) Refresh() {
	if s.api() == nil {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.refresh()
	}()
}

func (s *Service) refresh() {
	if !s.o.Online() {
		s.mu.Lock()
		if len(s.books) == 0 {
			s.status = Offline
		}
		s.mu.Unlock()
		s.changed()
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	books, err := s.api().Library(ctx)
	s.mu.Lock()
	if err != nil {
		s.o.Log.Warn("audible library", "err", err)
		if len(s.books) == 0 {
			s.status, s.err = Broken, friendly(err)
		}
	} else {
		s.books, s.status, s.err = books, OK, ""
		s.saveCache(books)
	}
	s.mu.Unlock()
	s.scanLocal()
	s.changed()
}

// Download queues the book with that ASIN to be downloaded; one already on the
// card, or in the queue, is left be.
func (s *Service) Download(asin string) {
	if s.api() == nil {
		return
	}
	s.mu.Lock()
	if _, have := s.local[asin]; have {
		s.mu.Unlock()
		return
	}
	if j := s.jobs[asin]; j != nil && j.state != Failed {
		s.mu.Unlock()
		return
	}
	s.jobs[asin] = &job{state: Queued}
	s.mu.Unlock()
	select {
	case s.queue <- asin:
	default:
		s.setJob(asin, Failed, 0, "too many downloads waiting")
	}
	s.changed()
}

func (s *Service) setJob(asin string, st State, progress float64, err string) {
	s.mu.Lock()
	if j := s.jobs[asin]; j != nil {
		j.state, j.progress, j.err = st, progress, err
	}
	s.mu.Unlock()
	s.changed()
}

func (s *Service) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case asin := <-s.queue:
			s.download(ctx, asin)
		}
	}
}

// book finds a book of the library.
func (s *Service) book(asin string) (audible.Book, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.books {
		if b.ASIN == asin {
			return b, true
		}
	}
	return audible.Book{}, false
}

var unsafeName = regexp.MustCompile(`[\\/:*?"<>|]+`)

// SafeName is name made fit to be a file's on the card's FAT or exFAT.
func SafeName(name string) string {
	n := strings.Trim(unsafeName.ReplaceAllString(name, "-"), " .")
	if n == "" {
		return "?"
	}
	return n
}

// PathFor is where a book is filed.
func PathFor(dir string, b audible.Book) string {
	author := SafeName(strings.Split(b.Authors, ",")[0])
	if b.Authors == "" {
		author = "Unknown"
	}
	return filepath.Join(dir, author, SafeName(b.Title)+" ["+b.ASIN+"].m4b")
}

func (s *Service) download(ctx context.Context, asin string) {
	b, ok := s.book(asin)
	if !ok {
		s.setJob(asin, Failed, 0, "not in the library")
		return
	}
	if !s.o.Online() {
		s.setJob(asin, Failed, 0, "no network")
		return
	}
	s.setJob(asin, Downloading, 0, "")
	path, err := s.fetchBook(ctx, b)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.o.Log.Warn("audible download", "asin", asin, "err", err)
		s.setJob(asin, Failed, 0, friendly(err))
		return
	}
	s.mu.Lock()
	s.local[asin] = path
	delete(s.jobs, asin)
	s.mu.Unlock()
	s.changed()
	if s.o.Filed != nil {
		s.o.Filed(path)
	}
}

// friendly is a failure in a few words, for the screen.
func friendly(err error) string {
	var d *audible.Denied
	switch {
	case errors.As(err, &d) && d.NoPlan():
		return "Audible says this account has no plan or ownership for this book"
	case errors.As(err, &d) && len(d.Reasons) == 0 && d.Throttled:
		return "Audible says wait a while, then try again"
	case errors.As(err, &d) && len(d.Reasons) > 0:
		return d.Reasons[0]
	case errors.As(err, &d):
		return "Audible refused the download"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, audible.ErrStalled):
		return "the download was cut off: check the Wi-Fi, then try again"
	}
	var dns *net.DNSError
	var ne net.Error
	switch {
	case errors.As(err, &dns):
		return "couldn't reach Audible: check the Wi-Fi, then try again"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout(),
		strings.Contains(err.Error(), "timeout"): // an http2 or TLS timeout isn't always a net.Error
		return "Audible didn't answer in time: check the Wi-Fi, then try again"
	}
	var ue *url.Error
	if errors.As(err, &ue) { // not the address, which says nothing to the listener
		return "couldn't reach Audible: " + ue.Err.Error()
	}
	return err.Error()
}

// license asks for a book's licence, once more after a pause if the answer
// didn't come: a request that timed out wasn't granted, so asking again costs
// nothing.
func (s *Service) license(ctx context.Context, asin string) (audible.License, error) {
	var lic audible.License
	var err error
	for try := 0; ; try++ {
		lctx, cancel := context.WithTimeout(ctx, licenseTimeout)
		lic, err = s.api().License(lctx, asin)
		cancel()
		if err == nil || try == 1 || ctx.Err() != nil || !transient(err) {
			return lic, err
		}
		s.o.Log.Warn("audible licence, asking again", "asin", asin, "err", err)
		if !s.wait(ctx, s.o.retry) {
			return lic, err
		}
	}
}

// fetch downloads the file into f. When it stops short, on a timeout, a cut
// connection or a server error, it carries on from the bytes already in f,
// after a pause that doubles each time, with the licence it has; a server that
// won't resume gets the file from the start.
func (s *Service) fetch(ctx context.Context, asin string, lic audible.License, f *os.File) error {
	wait := s.o.retry
	for try := 1; ; try++ {
		from, err := f.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		err = s.api().Fetch(ctx, lic, f, from, func(done, total int64) {
			if total > 0 {
				s.progress(asin, float64(done)/float64(total))
			}
		})
		switch {
		case err == nil:
			return nil
		case errors.Is(err, audible.ErrNoResume):
			if err := f.Truncate(0); err != nil {
				return err
			}
			continue // from the start; the server's refusal isn't a failed try
		case try == tries || ctx.Err() != nil || !transient(err):
			return err
		}
		s.o.Log.Warn("audible download, carrying on", "asin", asin, "at", from, "try", try, "err", err)
		if !s.wait(ctx, wait) {
			return err
		}
		wait *= 2
	}
}

// wait pauses for d; it reports false if ctx ended first.
func (s *Service) wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// transient reports whether err is the network's doing, which asking again
// may get past: a timeout, a connection cut or never made, a stalled
// download, a short file or a server error. A refusal from Audible, a missing
// file or a full card is not.
func transient(err error) bool {
	var d *audible.Denied
	var se *audible.StatusError
	var ne net.Error
	switch {
	case err == nil, errors.As(err, &d), errors.Is(err, context.Canceled):
		return false
	case errors.As(err, &se):
		return se.Code >= 500 || se.Code == 429
	case errors.Is(err, audible.ErrStalled), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, context.DeadlineExceeded),
		errors.As(err, &ne):
		return true
	}
	// An http2 or TLS timeout isn't always a net.Error.
	return strings.Contains(err.Error(), "timeout")
}

// fetchBook downloads and opens a book, and files it with its sidecar.
func (s *Service) fetchBook(ctx context.Context, b audible.Book) (string, error) {
	lic, err := s.license(ctx, b.ASIN)
	if err != nil {
		return "", err
	}
	final := PathFor(s.o.Dir, b)
	dir := filepath.Dir(final)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	raw := filepath.Join(dir, "."+b.ASIN+".aax")
	part := filepath.Join(dir, "."+b.ASIN+".part.m4b")
	defer os.Remove(raw)
	defer os.Remove(part)
	f, err := os.Create(raw)
	if err != nil {
		return "", err
	}
	err = s.fetch(ctx, b.ASIN, lic, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	s.setJob(b.ASIN, Decrypting, 1, "")
	open := s.o.Open
	if open == nil {
		open = s.ffmpeg
	}
	if err := open(ctx, raw, part, lic.Key, lic.IV); err != nil {
		return "", err
	}
	if err := os.Rename(part, final); err != nil {
		return "", err
	}
	side, _ := json.Marshal(map[string]string{"asin": b.ASIN, "acr": lic.ACR})
	if lic.ACR != "" {
		os.WriteFile(strings.TrimSuffix(final, ".m4b")+".audible.json", side, 0o644)
	}
	return final, nil
}

// progress records how much of a book has come, redrawing at most a hundred
// times over a download.
func (s *Service) progress(asin string, p float64) {
	s.mu.Lock()
	j := s.jobs[asin]
	moved := j != nil && p-j.progress >= 0.01
	if moved {
		j.progress = p
	}
	s.mu.Unlock()
	if moved {
		s.changed()
	}
}

// ffmpeg opens in as out without changing the audio: Audible's files are
// sealed with a key, which ffmpeg takes, and come out as a plain M4B with its
// chapters and cover.
func (s *Service) ffmpeg(ctx context.Context, in, out, key, iv string) error {
	if s.o.FFmpeg == "" {
		return errors.New("no ffmpeg to open the book with")
	}
	args := []string{"-v", "error", "-y"}
	if key != "" {
		args = append(args, "-audible_key", key, "-audible_iv", iv)
	}
	args = append(args, "-i", in, "-c", "copy", out)
	if msg, err := exec.CommandContext(ctx, s.o.FFmpeg, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(msg)))
	}
	return nil
}

// scanLocal finds which books of the library are on the card.
func (s *Service) scanLocal() {
	found := map[string]string{}
	matches, _ := filepath.Glob(filepath.Join(s.o.Dir, "*", "*].m4b"))
	loose, _ := filepath.Glob(filepath.Join(s.o.Dir, "*].m4b"))
	for _, m := range append(matches, loose...) {
		if asin := audible.ASINOf(m); asin != "" {
			found[asin] = m
		}
	}
	s.mu.Lock()
	s.local = found
	s.mu.Unlock()
}

func (s *Service) loadCache() []audible.Book {
	if s.o.Cache == "" {
		return nil
	}
	b, err := os.ReadFile(s.o.Cache)
	if err != nil {
		return nil
	}
	var books []audible.Book
	if json.Unmarshal(b, &books) != nil {
		return nil
	}
	return books
}

func (s *Service) saveCache(books []audible.Book) {
	if s.o.Cache == "" {
		return
	}
	b, err := json.Marshal(books)
	if err != nil {
		return
	}
	tmp := s.o.Cache + ".tmp"
	if os.MkdirAll(filepath.Dir(s.o.Cache), 0o755) == nil && os.WriteFile(tmp, b, 0o644) == nil {
		os.Rename(tmp, s.o.Cache)
	}
}

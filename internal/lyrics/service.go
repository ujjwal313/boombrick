package lyrics

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
)

// State is how far finding a track's lyrics has got.
type State uint8

const (
	Looking      State = iota // being looked for
	Found                     // in the Lyrics returned with it
	None                      // there are none to be had
	Instrumental              // the track has no words
	Offline                   // none with the music, and looking further needs the network
	Failed                    // the network is up but the lookup failed
)

// Fetcher looks lyrics up online. *Client is one.
type Fetcher interface {
	Get(ctx context.Context, t Track) (Result, error)
}

// Options configure a Service.
type Options struct {
	DB     *sql.DB
	Fetch  Fetcher     // nil: nothing is looked up online
	Online func() bool // whether the network is up; nil means it never is
	// FileOf finds the file of a Spotify song an import downloaded, so its tags
	// and .lrc are read before LRCLIB is asked. It queries the database, so it
	// is called only on the service's goroutine. Nil: a streamed song goes
	// straight to LRCLIB.
	FileOf func(spotifyID string) (path string, ok bool)
	// Found hears the lyrics found for each track, on the service's
	// goroutine, so the fonts they need are fetched before they are shown.
	// Nil: nothing listens.
	Found func(Lyrics)
	Log   *slog.Logger

	retry time.Duration // how long before an Offline or Failed answer is looked for again; tests shorten it
}

const (
	maxEntries    = 256                // tracks whose answers are remembered
	maxQueued     = 32                 // requests waiting beyond this drop the oldest
	retryNone     = 7 * 24 * time.Hour // LRCLIB is asked again after this when it had nothing
	retryOffline  = 30 * time.Second
	lookupTimeout = 10 * time.Second
)

// Service finds the lyrics of files and of Spotify songs played as a stream:
// in their tags, in an .lrc beside them, in what LRCLIB gave before, then
// from LRCLIB while online, stopping at the first synced lyrics and otherwise
// giving the first plain ones. A streamed song looks in the tags and .lrc of
// a copy an import downloaded, if there is one. The screens
// ask from the UI thread and never wait: the work runs on a goroutine, newest
// request first, and Ready receives a value when an answer is in. Looking
// beside the music is quick and lookups are not, so every track waiting for
// the first is seen to before the next lookup.
type Service struct {
	opts   Options
	ready  chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	work    *sync.Cond
	entries map[string]*entry
	order   []string  // entries' keys, oldest first
	queue   []request // to look for beside the music, oldest first
	lookups []request // to look up online, oldest first
	asks    uint64    // requests made, to order them by
	closed  bool
}

// request is a track to look for, with when it was asked for last.
type request struct {
	t     Track
	asked uint64  // the Service's asks then
	f     finding // what was found beside the music, for a lookup
}

type entry struct {
	state  State
	lyrics Lyrics
	queued bool
	retry  time.Time // when an Offline or Failed answer, or a stopgap one, may be looked for again
	again  bool      // the answer is plain lyrics held for want of the network, better may come
}

// New starts a service.
func New(opts Options) *Service {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Online == nil {
		opts.Online = func() bool { return false }
	}
	if opts.retry == 0 {
		opts.retry = retryOffline
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{opts: opts, ready: make(chan struct{}, 1), ctx: ctx, cancel: cancel, done: make(chan struct{}),
		entries: map[string]*entry{}}
	s.work = sync.NewCond(&s.mu)
	go s.worker()
	return s
}

// Close stops the service and waits for its goroutine.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.work.Broadcast()
	<-s.done
}

// Ready receives a value when an answer is in.
func (s *Service) Ready() <-chan struct{} { return s.ready }

// Lyrics returns t's lyrics and how far finding them has got. Asking starts
// the search, or starts it again for an Offline or Failed answer 30 s old.
func (s *Service) Lyrics(t Track) (Lyrics, State) {
	if t.Key() == "" {
		return Lyrics{}, None
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.askLocked(t)
	return e.lyrics, e.state
}

// Prefetch asks for the lyrics of ts without waiting for them, the first
// soonest: the track now playing and the one after it.
func (s *Service) Prefetch(ts ...Track) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(ts) - 1; i >= 0; i-- {
		if ts[i].Key() != "" {
			s.askLocked(ts[i])
		}
	}
}

func (s *Service) askLocked(t Track) *entry {
	key := t.Key()
	e := s.entries[key]
	if e == nil {
		e = &entry{state: Looking}
		s.entries[key] = e
		s.order = append(s.order, key)
		if len(s.order) > maxEntries {
			delete(s.entries, s.order[0])
			s.order = s.order[1:]
		}
	}
	if e.queued {
		// Asked for again: it is the newest request now, wherever it waits.
		s.asks++
		for _, q := range [][]request{s.queue, s.lookups} {
			if i := slices.IndexFunc(q, func(r request) bool { return r.t.Key() == key }); i >= 0 {
				r := q[i]
				r.t, r.asked = t, s.asks
				copy(q[i:], q[i+1:])
				q[len(q)-1] = r
			}
		}
	} else if e.state == Looking || (e.state == Offline || e.state == Failed || e.again) && time.Now().After(e.retry) {
		e.queued = true
		s.asks++
		s.queue = s.trim(append(s.queue, request{t: t, asked: s.asks}))
		s.work.Signal()
	}
	return e
}

// trim drops the oldest request from q when more than maxQueued wait; its
// track is asked for again when it is next shown.
func (s *Service) trim(q []request) []request {
	if len(q) <= maxQueued {
		return q
	}
	if old := s.entries[q[0].t.Key()]; old != nil {
		old.queued = false
	}
	return q[1:]
}

func (s *Service) worker() {
	defer close(s.done)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && len(s.lookups) == 0 && !s.closed {
			s.work.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		online := len(s.queue) == 0
		var r request
		if online {
			r, s.lookups = s.lookups[len(s.lookups)-1], s.lookups[:len(s.lookups)-1]
		} else {
			r, s.queue = s.queue[len(s.queue)-1], s.queue[:len(s.queue)-1]
		}
		s.mu.Unlock()

		var a outcome
		if online {
			a = s.lookUp(r.t, &r.f)
		} else if a, online = s.beside(r.t, &r.f); online {
			s.mu.Lock()
			// Among the lookups waiting by when each was asked for, so the
			// newest still goes first.
			i := len(s.lookups)
			for i > 0 && s.lookups[i-1].asked > r.asked {
				i--
			}
			s.lookups = s.trim(slices.Insert(s.lookups, i, r))
			s.mu.Unlock()
			continue
		}
		s.mu.Lock()
		if e := s.entries[r.t.Key()]; e != nil {
			e.lyrics, e.state, e.queued, e.again = a.lyrics, a.state, false, a.again
			if a.again {
				e.retry = time.Now().Add(s.opts.retry)
			}
		}
		s.mu.Unlock()
		if a.state == Found && s.opts.Found != nil {
			s.opts.Found(a.lyrics)
		}
		select {
		case s.ready <- struct{}{}:
		default:
		}
	}
}

// outcome is what the search for a track's lyrics came to.
type outcome struct {
	lyrics Lyrics
	state  State
	again  bool // the answer is only for want of the network, even if plain lyrics stand in
}

// beside looks for t's lyrics in its tags, in an .lrc beside it and in what
// LRCLIB gave before; a streamed song looks in its downloaded copy's tags and
// .lrc, if it has one. lookup is set when only LRCLIB can tell more, with f
// holding what was found meanwhile.
func (s *Service) beside(t Track, f *finding) (a outcome, lookup bool) {
	path := t.Path
	if path == "" && t.SpotifyID != "" && s.opts.FileOf != nil {
		path, _ = s.opts.FileOf(t.SpotifyID)
	}
	for _, read := range []func(string) (string, bool){Embedded, Sidecar} {
		if path == "" {
			break
		}
		if text, ok := read(path); ok && f.consider(text) {
			return outcome{f.plain, Found, false}, false
		}
	}
	c, ok, err := s.load(t.Key())
	if err != nil {
		s.opts.Log.Warn("read lyrics", "track", t.Title, "err", err)
	}
	switch {
	case ok && (c.body != "" || c.instrumental || time.Since(time.UnixMilli(c.fetched)) < retryNone):
		return f.answer(c.body, c.instrumental), false
	case s.opts.Fetch == nil || !searchable(t):
		return f.failing(None), false
	case !s.opts.Online():
		return f.failing(Offline), false
	}
	return outcome{}, true
}

// lookUp asks LRCLIB for t's lyrics, f holding what was found beside it.
func (s *Service) lookUp(t Track, f *finding) outcome {
	if !s.opts.Online() {
		return f.failing(Offline)
	}
	ctx, cancel := context.WithTimeout(s.ctx, lookupTimeout)
	defer cancel()
	r, err := s.opts.Fetch.Get(ctx, t)
	switch {
	case errors.Is(err, ErrNotFound):
		s.save(t.Key(), "", false)
		return f.failing(None)
	case err != nil:
		if s.ctx.Err() == nil {
			s.opts.Log.Warn("lyrics lookup", "track", t.Title, "err", err)
		}
		return f.failing(Failed)
	}
	body := r.Synced
	if body == "" {
		body = r.Plain
	}
	s.save(t.Key(), body, r.Instrumental)
	return f.answer(body, r.Instrumental)
}

// searchable reports whether LRCLIB can be asked for t, which takes a title
// and an artist: a file without tags outside an artist's folder has none.
func searchable(t Track) bool {
	artist := strings.TrimSpace(t.Artist)
	return strings.TrimSpace(t.Title) != "" && artist != "" && artist != library.UnknownArtist
}

// finding is one track's search as it goes from source to source.
type finding struct {
	plain Lyrics // the first plain lyrics found, should nothing synced turn up
}

// consider takes the lyrics in text, reporting whether they are synced,
// which ends the search with them in plain.
func (f *finding) consider(text string) bool {
	l := Parse(text)
	if l.Synced {
		f.plain = l
		return true
	}
	if f.plain.Lines == nil && len(l.Lines) > 0 {
		f.plain = l
	}
	return false
}

// failing is the outcome when the search ends at st: any plain lyrics found
// instead.
func (f *finding) failing(st State) outcome {
	again := st == Offline || st == Failed
	if f.plain.Lines != nil {
		return outcome{f.plain, Found, again}
	}
	return outcome{Lyrics{}, st, again}
}

// answer is the outcome when LRCLIB gave body and instrumental, now or before.
func (f *finding) answer(body string, instrumental bool) outcome {
	if body != "" && f.consider(body) {
		return f.failing(None) // plain now holds the synced lyrics
	}
	if instrumental {
		return f.failing(Instrumental)
	}
	return f.failing(None)
}

type cached struct {
	body         string
	instrumental bool
	fetched      int64
}

// load and save keep answers in the lyrics table under a track's key, in its
// path column.
func (s *Service) load(key string) (cached, bool, error) {
	var c cached
	err := s.opts.DB.QueryRow(`SELECT body, instrumental, fetched FROM lyrics WHERE path = ?`, key).
		Scan(&c.body, &c.instrumental, &c.fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return cached{}, false, nil
	}
	return c, err == nil, err
}

func (s *Service) save(key, body string, instrumental bool) {
	_, err := s.opts.DB.Exec(`INSERT INTO lyrics (path, body, instrumental, fetched) VALUES (?, ?, ?, ?)
		ON CONFLICT (path) DO UPDATE SET body = excluded.body, instrumental = excluded.instrumental, fetched = excluded.fetched`,
		key, body, instrumental, time.Now().UnixMilli())
	if err != nil {
		s.opts.Log.Warn("save lyrics", "err", err)
	}
}

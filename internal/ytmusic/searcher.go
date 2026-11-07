package ytmusic

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"time"
)

// State is how far an answer has got.
type State uint8

const (
	// Waiting means asked for and not answered yet, including while typing
	// pauses.
	Waiting State = iota
	// Done means answered, perhaps with nothing.
	Done
	// Offline means not asked, the device being offline.
	Offline
	// Failed means the request failed, as when the connection dropped.
	Failed
	// Changed means YouTube Music answered in a shape Boombrick can't read.
	Changed
)

// Querier is YouTube Music as a Searcher asks it. *Client is one.
type Querier interface {
	Search(ctx context.Context, query string, f Filter) ([]Item, error)
	Album(ctx context.Context, id string) (AlbumPage, error)
	Artist(ctx context.Context, id string) (ArtistPage, error)
}

// Options configure a Searcher.
type Options struct {
	Client Querier
	Online func() bool // nil: always online
	Log    *slog.Logger
	// Pause is how long typing must stop before a search is sent; 0 means
	// 300 ms.
	Pause time.Duration
	// Retry is how long an answer other than Done stands before asking for
	// it again sends it again; 0 means 10 s.
	Retry time.Duration
	// Fallback searches for songs when YouTube Music's answers can't be read:
	// the download service's yt-dlp search. nil: none.
	Fallback func(ctx context.Context, query string) ([]Item, error)
}

// timeout bounds one request.
const timeout = 10 * time.Second

// fallbackTimeout bounds a search with Fallback, which has yt-dlp to start.
const fallbackTimeout = 20 * time.Second

// maxAnswers is how many answers are kept.
const maxAnswers = 32

// Searcher asks YouTube Music on a goroutine of its own, so screens never
// wait: they ask for what they show every frame and draw what has come. A
// search is sent once typing pauses, and a newer search replaces it,
// cancelling it if it was sent. Pages are asked for at once, newest first.
// The last 32 answers are kept.
type Searcher struct {
	opts   Options
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	wake   chan struct{}
	ready  chan struct{}
	closed sync.Once

	mu      sync.Mutex
	search  question           // the newest search not yet sent
	pending bool               // whether search is set
	due     time.Time          // when search may be sent
	pages   []question         // pages not yet sent, newest last
	running question           // the question being asked
	asking  bool               // whether running is out and still wanted; a newer search clears it
	stop    context.CancelFunc // cancels running
	answers map[question]*answer
	order   []question // the answers' questions, oldest first
}

// question is something a screen asks for.
type question struct {
	kind   uint8 // searchQ, albumQ or artistQ
	s      string
	filter Filter
}

const (
	searchQ uint8 = iota
	albumQ
	artistQ
)

type answer struct {
	state    State
	items    []Item
	album    AlbumPage
	artist   ArtistPage
	at       time.Time // when it came
	fallback bool      // the songs came from Fallback
}

// NewSearcher starts a searcher.
func NewSearcher(opts Options) *Searcher {
	if opts.Pause == 0 {
		opts.Pause = 300 * time.Millisecond
	}
	if opts.Retry == 0 {
		opts.Retry = 10 * time.Second
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Searcher{opts: opts, ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), ready: make(chan struct{}, 1),
		answers: map[question]*answer{}}
	s.wg.Add(1)
	go s.worker()
	return s
}

// Close stops the searcher, cancelling what it is asking, waits for it and
// closes Ready. It may be called more than once.
func (s *Searcher) Close() {
	s.closed.Do(func() {
		s.cancel()
		s.wg.Wait()
		close(s.ready)
	})
}

// Ready receives a value when an answer has come, and is closed by Close.
func (s *Searcher) Ready() <-chan struct{} { return s.ready }

// Search returns query's results under f and how far they have got. Asking
// for a query not answered yet makes it the next search, sent once typing
// pauses, or at once when the query has gone out under another filter. The
// items are shared with the searcher's cache and must not be changed.
func (s *Searcher) Search(query string, f Filter) ([]Item, State) {
	a := s.ask(question{kind: searchQ, s: query, filter: f})
	return a.items, a.state
}

// Album returns the album with browse ID id, asking for it if needed.
func (s *Searcher) Album(id string) (AlbumPage, State) {
	a := s.ask(question{kind: albumQ, s: id})
	return a.album, a.state
}

// Artist returns the artist with browse ID id, asking for it if needed.
func (s *Searcher) Artist(id string) (ArtistPage, State) {
	a := s.ask(question{kind: artistQ, s: id})
	return a.artist, a.state
}

// ask returns q's answer, or asks for it and returns Waiting.
func (s *Searcher) ask(q question) answer {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := false // whether q's query is out under another filter
	if q.kind == searchQ {
		out = s.asking && s.running.kind == searchQ && s.running.s == q.s
		// Whatever search is asked for, even one already answered, replaces
		// any other that is waiting or out.
		if s.pending && s.search != q {
			s.pending = false
		}
		if s.asking && s.running.kind == searchQ && s.running != q {
			s.stop()         // overtaken by this one
			s.asking = false // so asking for it again queues it again
		}
	}
	if a, ok := s.answers[q]; ok && (a.state == Done || now.Sub(a.at) < s.opts.Retry) {
		return *a
	}
	switch {
	case s.asking && s.running == q:
	case q.kind == searchQ:
		if !s.pending {
			s.search, s.pending, s.due = q, true, now.Add(s.opts.Pause)
			if out || s.answeredElsewhere(q) {
				s.due = now // a switch of filter, not typing
			}
			signal(s.wake)
		}
	case !slices.Contains(s.pages, q):
		s.pages = append(s.pages, q)
		signal(s.wake)
	}
	return answer{state: Waiting}
}

// answeredElsewhere reports whether search q's query has an answer under a
// filter other than q's. A stale answer under q's own says nothing: asked
// for again, it may be being typed again.
func (s *Searcher) answeredElsewhere(q question) bool {
	for f := range filterCount {
		if f == q.filter {
			continue
		}
		if _, ok := s.answers[question{kind: searchQ, s: q.s, filter: f}]; ok {
			return true
		}
	}
	return false
}

func (s *Searcher) worker() {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		q, ctx, wait, ok := s.next(time.Now())
		if ok {
			s.run(ctx, q)
			continue
		}
		var timer *time.Timer
		var due <-chan time.Time
		if wait > 0 {
			timer = time.NewTimer(wait)
			due = timer.C
		}
		select {
		case <-s.ctx.Done():
		case <-s.wake:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// next takes the question to send now, with a context that cancels it: a
// search whose pause is over, or else the newest page. Without one it says
// how long until the waiting search is due, 0 when none waits.
func (s *Searcher) next(now time.Time) (question, context.Context, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var q question
	switch {
	case s.pending && !now.Before(s.due):
		q, s.pending = s.search, false
	case len(s.pages) > 0:
		n := len(s.pages)
		q, s.pages = s.pages[n-1], s.pages[:n-1]
	case s.pending:
		return question{}, nil, s.due.Sub(now), false
	default:
		return question{}, nil, 0, false
	}
	ctx, stop := context.WithCancel(s.ctx)
	s.running, s.asking, s.stop = q, true, stop
	return q, ctx, 0, true
}

// run asks q and keeps the answer, unless q was cancelled on the way.
func (s *Searcher) run(ctx context.Context, q question) {
	a := s.fetch(ctx, q)
	s.mu.Lock()
	cancelled := ctx.Err() != nil
	s.stop()
	s.asking, s.stop = false, nil
	if !cancelled {
		s.keep(q, a)
		// The fallback finds songs alone, so its answer for All is the one
		// for Songs: switching filter doesn't run yt-dlp again.
		if a.fallback && q.filter == All {
			s.keep(question{kind: searchQ, s: q.s, filter: Songs}, a)
		}
	}
	s.mu.Unlock()
	if !cancelled {
		signal(s.ready)
	}
}

// fetch asks YouTube Music, unless the device is offline. A search for songs
// it answers in a shape that can't be read goes to Fallback.
func (s *Searcher) fetch(ctx context.Context, q question) answer {
	if s.opts.Online != nil && !s.opts.Online() {
		return answer{state: Offline}
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var a answer
	var err error
	switch q.kind {
	case searchQ:
		a.items, err = s.opts.Client.Search(rctx, q.s, q.filter)
	case albumQ:
		a.album, err = s.opts.Client.Album(rctx, q.s)
	case artistQ:
		a.artist, err = s.opts.Client.Artist(rctx, q.s)
	}
	switch {
	case err == nil:
		a.state = Done
	case errors.Is(err, ErrChanged) && q.kind == searchQ && (q.filter == All || q.filter == Songs) && s.opts.Fallback != nil:
		s.opts.Log.Error("youtube music answered in an unknown shape; searching with yt-dlp", "q", q.s, "err", err)
		a = s.fallback(ctx, q.s)
	case errors.Is(err, ErrChanged):
		s.opts.Log.Error("youtube music answered in an unknown shape", "q", q.s, "err", err)
		a.state = Changed
	default:
		if !errors.Is(err, context.Canceled) {
			s.opts.Log.Warn("youtube music", "q", q.s, "err", err)
		}
		a.state = Failed
	}
	// The screens say "No results" for this: the log says what was asked, and
	// whether yt-dlp answered in YouTube Music's place.
	if a.state == Done && q.kind == searchQ && len(a.items) == 0 {
		s.opts.Log.Info("search found nothing", "q", q.s, "filter", FilterNames[q.filter], "fallback", a.fallback)
	}
	return a
}

// fallback searches for query's songs with Fallback. When it can't, as before
// yt-dlp is installed, the search stays Changed: YouTube Music still can't be
// read.
func (s *Searcher) fallback(ctx context.Context, query string) answer {
	ctx, cancel := context.WithTimeout(ctx, fallbackTimeout)
	defer cancel()
	items, err := s.opts.Fallback(ctx, query)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.opts.Log.Warn("yt-dlp search", "q", query, "err", err)
		}
		return answer{state: Changed}
	}
	return answer{state: Done, items: items, fallback: true}
}

// keep stores q's answer, letting the oldest go past maxAnswers.
func (s *Searcher) keep(q question, a answer) {
	a.at = time.Now()
	if _, ok := s.answers[q]; ok {
		s.order = slices.DeleteFunc(s.order, func(x question) bool { return x == q })
	}
	s.answers[q] = &a
	s.order = append(s.order, q)
	if len(s.order) > maxAnswers {
		delete(s.answers, s.order[0])
		s.order = s.order[1:]
	}
}

// signal sends on c unless a value is already waiting there.
func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

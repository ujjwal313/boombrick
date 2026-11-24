package radio

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Lister is Radio Browser as the Directory asks it. *Client is one.
type Lister interface {
	Top(ctx context.Context) ([]Station, error)
	Search(ctx context.Context, name string) ([]Station, error)
	ByCountry(ctx context.Context, code string) ([]Station, error)
	ByTag(ctx context.Context, tag string) ([]Station, error)
	Countries(ctx context.Context) ([]Count, error)
	Genres(ctx context.Context) ([]Count, error)
}

const (
	// fresh is how long a list is shown before Radio Browser is asked again.
	fresh = 10 * time.Minute
	// retryAfter is how long a list whose asking failed waits before it is
	// asked again, unless Retry says to at once.
	retryAfter = 30 * time.Second
	// searchPause is how long typing must stop before a search is sent.
	searchPause = 300 * time.Millisecond
	// keepSearches is how many searches are kept: the ones looked at last.
	keepSearches = 16
	// keepLists is how many countries' and genres' station lists are kept,
	// together: the ones asked for last. Top, Countries and Genres stay.
	keepLists = 16
	// askFor bounds one asking, over every server tried.
	askFor = 30 * time.Second
	// A search's key in the lists starts with searchPrefix, a country's
	// stations' with countryPrefix and a genre's with tagPrefix; the query,
	// code or tag follows.
	searchPrefix  = "search:"
	countryPrefix = "country:"
	tagPrefix     = "tag:"
)

// Directory is Radio Browser's lists as the screens ask them: each call
// returns what is known at once and asks for what isn't, on goroutines of
// its own. Lists are kept in memory, those of countries, genres and searches
// only while asked for lately, and are asked for again once they are ten
// minutes old. A search is sent once typing pauses, and cancels the search
// sent before it.
type Directory struct {
	c     Lister
	log   *slog.Logger
	now   func() time.Time
	after func(d time.Duration, f func()) (stop func() bool) // time.AfterFunc, or a test's clock
	ready chan struct{}
	ctx   context.Context
	stop  context.CancelFunc
	wg    sync.WaitGroup
	// version is bumped with each signal.
	version atomic.Uint64

	mu    sync.Mutex
	lists map[string]*entry
	uses  uint64 // counts the calls for lists, to order them by use
	// want is the search waiting for typing to pause, "" for none, and wantAt
	// when it may be sent. Typing on replaces it and bumps typed, so a timer
	// set for an older one does nothing.
	want   string
	wantAt time.Time
	typed  uint64
	timer  func() bool        // stops the timer that sends want; nil when none is set
	halt   context.CancelFunc // cancels the search sent last
}

// entry is one list as the screens see it.
type entry struct {
	body  any // []Station or []Count
	have  bool
	at    time.Time // when body came
	err   error     // the last asking's failure, cleared by a success or by Retry
	tried time.Time // when the last asking ended, unless it was cancelled
	busy  bool      // Radio Browser is being asked
	used  uint64    // its place in the calls for lists, when last asked for
}

// NewDirectory asks c for the lists. Close it when done.
func NewDirectory(c Lister, log *slog.Logger) *Directory {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Directory{c: c, log: log, now: time.Now, after: afterFunc, ready: make(chan struct{}, 1),
		ctx: ctx, stop: stop, lists: map[string]*entry{}}
}

func afterFunc(d time.Duration, f func()) func() bool { return time.AfterFunc(d, f).Stop }

// Ready receives a value when an asking has ended, as when an answer is in.
func (d *Directory) Ready() <-chan struct{} { return d.ready }

// Version changes whenever an asking has ended, so a screen asks again only
// when what it shows may have changed.
func (d *Directory) Version() uint64 { return d.version.Load() }

// Close cancels the asking under way, and the search waiting for typing to
// pause, and waits for them. It may be called more than once; nothing is
// asked after it.
func (d *Directory) Close() {
	d.mu.Lock()
	d.stop()
	d.wait("")
	d.mu.Unlock()
	d.wg.Wait()
}

func (d *Directory) signal() {
	d.version.Add(1)
	select {
	case d.ready <- struct{}{}:
	default:
	}
}

// Top is the most listened stations. have is false until an answer has come,
// and err is the last asking's failure, even when what was had before shows.
// The lists returned are shared, and mustn't be changed.
func (d *Directory) Top() ([]Station, bool, error) {
	return asStations(d.get("top", func(ctx context.Context) (any, error) { return d.c.Top(ctx) }))
}

// Countries is the countries that have stations, as Top.
func (d *Directory) Countries() ([]Count, bool, error) {
	return asCounts(d.get("countries", func(ctx context.Context) (any, error) { return d.c.Countries(ctx) }))
}

// Genres is the most used tags, as Top.
func (d *Directory) Genres() ([]Count, bool, error) {
	return asCounts(d.get("genres", func(ctx context.Context) (any, error) { return d.c.Genres(ctx) }))
}

// ByCountry is a country's stations, by its ISO 3166-1 code, as Top.
func (d *Directory) ByCountry(code string) ([]Station, bool, error) {
	return asStations(d.get(countryPrefix+code, func(ctx context.Context) (any, error) { return d.c.ByCountry(ctx, code) }))
}

// ByTag is the stations tagged exactly tag, as Top.
func (d *Directory) ByTag(tag string) ([]Station, bool, error) {
	return asStations(d.get(tagPrefix+tag, func(ctx context.Context) (any, error) { return d.c.ByTag(ctx, tag) }))
}

// Search is the stations whose name holds q, as Top. A query is sent once it
// has stood for searchPause, as typing pauses, and cancels the search sent
// before it. Until q's answer comes have is false, even when the answer for
// an earlier query comes meanwhile: that one is kept as its own. An empty
// query asks nothing, and drops the search waiting to be sent.
func (d *Directory) Search(q string) ([]Station, bool, error) {
	q = strings.TrimSpace(q)
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.lists[searchPrefix+q]
	if e != nil {
		d.uses++
		e.used = d.uses
	}
	switch {
	case q == "" || !d.due(e):
		d.wait("")
	case q != d.want:
		d.wait(q)
	case !d.now().Before(d.wantAt):
		d.search() // the timer is late
	}
	if e == nil {
		return nil, false, nil
	}
	s, _ := e.body.([]Station)
	return s, e.have, e.err
}

// Retry asks again for what failed, on the next call for it. Until that
// asking ends, a list that failed reads as not failed, so that a screen says
// it is loading rather than that the asking before failed.
func (d *Directory) Retry() {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.lists {
		e.tried, e.err = time.Time{}, nil
	}
}

func asStations(v any, have bool, err error) ([]Station, bool, error) {
	s, _ := v.([]Station)
	return s, have, err
}

func asCounts(v any, have bool, err error) ([]Count, bool, error) {
	c, _ := v.([]Count)
	return c, have, err
}

// get returns the list under key as known, and asks for it with fetch when it
// is due. A country's or a genre's list new to the Directory may drop the one
// asked for longest ago.
func (d *Directory) get(key string, fetch func(context.Context) (any, error)) (any, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.lists[key]
	added := e == nil
	if added {
		e = &entry{}
		d.lists[key] = e
	}
	d.uses++
	e.used = d.uses
	if d.due(e) {
		d.ask(d.ctx, key, e, fetch)
	}
	if added && browsed(key) {
		d.trim(browsed, keepLists)
	}
	return e.body, e.have, e.err
}

// browsed reports whether key is a country's or a genre's stations.
func browsed(key string) bool {
	return strings.HasPrefix(key, countryPrefix) || strings.HasPrefix(key, tagPrefix)
}

func searched(key string) bool { return strings.HasPrefix(key, searchPrefix) }

// due reports whether e, nil for a list not asked for yet, is to be asked
// for: it is missing or old, it isn't being asked for, and it didn't fail
// within retryAfter. Nothing is due after Close. Called with mu held.
func (d *Directory) due(e *entry) bool {
	if d.ctx.Err() != nil {
		return false
	}
	if e == nil {
		return true
	}
	now := d.now()
	if e.busy || e.err != nil && now.Sub(e.tried) < retryAfter {
		return false
	}
	return !e.have || now.Sub(e.at) >= fresh
}

// ask starts asking for e with fetch on a goroutine, under ctx. Called with
// mu held.
func (d *Directory) ask(ctx context.Context, key string, e *entry, fetch func(context.Context) (any, error)) {
	e.busy = true
	d.wg.Go(func() { d.fill(ctx, key, e, fetch) })
}

// fill asks for e and keeps the answer. A failure keeps what was had, with the
// error. An asking cancelled, by a newer search or by Close, is no failure,
// and leaves e as it was. Its end is still signalled, unless by Close: its
// query may have been typed again while it was ending, and is only sent when
// asked for after.
func (d *Directory) fill(ctx context.Context, key string, e *entry, fetch func(context.Context) (any, error)) {
	actx, cancel := context.WithTimeout(ctx, askFor)
	v, err := fetch(actx)
	cancel()
	cancelled := err != nil && ctx.Err() != nil
	d.mu.Lock()
	e.busy = false
	if !cancelled {
		e.tried = d.now()
		if err != nil {
			e.err = err
		} else {
			e.body, e.have, e.at, e.err = v, true, e.tried, nil
		}
	}
	closed := d.ctx.Err() != nil
	d.mu.Unlock()
	if closed {
		return
	}
	if err != nil && !cancelled {
		d.log.Warn("radio browser list", "list", key, "err", err)
	}
	d.signal()
}

// wait makes q the search waiting for typing to pause, "" for none, and sets
// the timer that sends it. The timer counts in wg until it has gone off or
// been stopped, so Close waits for it too. Called with mu held.
func (d *Directory) wait(q string) {
	if q == d.want {
		return
	}
	d.want = q
	d.typed++
	if d.timer != nil && d.timer() {
		d.wg.Done() // it won't go off
	}
	d.timer = nil
	if q == "" {
		return
	}
	d.wantAt = d.now().Add(searchPause)
	n := d.typed
	d.wg.Add(1)
	d.timer = d.after(searchPause, func() {
		defer d.wg.Done()
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.typed == n && d.ctx.Err() == nil {
			d.search()
		}
	})
}

// search sends the search waiting, cancelling the one sent before it.
// Called with mu held.
func (d *Directory) search() {
	q := d.want
	d.wait("")
	if d.halt != nil {
		d.halt()
	}
	ctx, halt := context.WithCancel(d.ctx)
	d.halt = halt
	key := searchPrefix + q
	e := d.lists[key]
	if e == nil {
		e = &entry{}
		d.lists[key] = e
	}
	d.uses++
	e.used = d.uses
	d.ask(ctx, key, e, func(ctx context.Context) (any, error) { return d.c.Search(ctx, q) })
	d.trim(searched, keepSearches)
}

// trim drops the lists whose keys are of kind past the keep asked for last,
// leaving any being asked for. Called with mu held.
func (d *Directory) trim(kind func(key string) bool, keep int) {
	var keys []string
	for k := range d.lists {
		if kind(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) <= keep {
		return
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Compare(d.lists[b].used, d.lists[a].used) })
	for _, k := range keys[keep:] {
		if !d.lists[k].busy {
			delete(d.lists, k)
		}
	}
}

package radio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"
)

var _ Lister = (*Client)(nil)

// fakeLister answers each list with one station or count, named after the
// list's key and how many times it has been asked, as "top 2". It counts
// what it is asked; an asking whose key is held waits until it is let go, or
// until its context ends.
type fakeLister struct {
	mu    sync.Mutex
	err   error // every answer, when set
	asked map[string]int
	ended map[string]int // askings that have returned
	held  map[string]chan struct{}
	ctx   map[string]context.Context // each key's last asking's
}

func newFakeLister() *fakeLister {
	return &fakeLister{asked: map[string]int{}, ended: map[string]int{}, held: map[string]chan struct{}{},
		ctx: map[string]context.Context{}}
}

func (f *fakeLister) answer(ctx context.Context, key string) (string, error) {
	f.mu.Lock()
	f.asked[key]++
	n := f.asked[key]
	f.ctx[key] = ctx
	h := f.held[key]
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.ended[key]++
		f.mu.Unlock()
	}()
	if h != nil {
		select {
		case <-h:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	return fmt.Sprintf("%s %d", key, n), nil
}

func (f *fakeLister) stations(ctx context.Context, key string) ([]Station, error) {
	name, err := f.answer(ctx, key)
	if err != nil {
		return nil, err
	}
	return []Station{{ID: name, Name: name}}, nil
}

func (f *fakeLister) counts(ctx context.Context, key string) ([]Count, error) {
	name, err := f.answer(ctx, key)
	if err != nil {
		return nil, err
	}
	return []Count{{Name: name}}, nil
}

func (f *fakeLister) Top(ctx context.Context) ([]Station, error) {
	return f.stations(ctx, "top")
}

func (f *fakeLister) Search(ctx context.Context, name string) ([]Station, error) {
	return f.stations(ctx, "search:"+name)
}

func (f *fakeLister) ByCountry(ctx context.Context, code string) ([]Station, error) {
	return f.stations(ctx, "country:"+code)
}

func (f *fakeLister) ByTag(ctx context.Context, tag string) ([]Station, error) {
	return f.stations(ctx, "tag:"+tag)
}

func (f *fakeLister) Countries(ctx context.Context) ([]Count, error) {
	return f.counts(ctx, "countries")
}

func (f *fakeLister) Genres(ctx context.Context) ([]Count, error) {
	return f.counts(ctx, "genres")
}

func (f *fakeLister) times(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[key]
}

func (f *fakeLister) returned(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ended[key]
}

func (f *fakeLister) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, v := range f.asked {
		n += v
	}
	return n
}

func (f *fakeLister) lastCtx(key string) context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctx[key]
}

func (f *fakeLister) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// hold makes the askings for key wait until let.
func (f *fakeLister) hold(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held[key] = make(chan struct{})
}

func (f *fakeLister) let(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	close(f.held[key])
	delete(f.held, key)
}

// clock is a time the test moves on, with timers that go off, on the test's
// goroutine, as it passes them.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*timer
}

type timer struct {
	at   time.Time
	f    func()
	done bool // gone off or stopped
}

func (c *clock) time() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) after(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &timer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		stopped := !t.done
		t.done = true
		return stopped
	}
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, t := range c.timers {
		if !t.done && !c.now.Before(t.at) {
			t.done = true
			due = append(due, t.f)
		}
	}
	c.timers = slices.DeleteFunc(c.timers, func(t *timer) bool { return t.done })
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// directory makes a Directory asking l, on a clock the test moves.
func directory(t *testing.T, l Lister) (*Directory, *clock) {
	t.Helper()
	d := NewDirectory(l, slog.New(slog.DiscardHandler))
	clk := &clock{now: time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)}
	d.now, d.after = clk.time, clk.after
	t.Cleanup(d.Close)
	return d, clk
}

// asking reports whether the list under key is being asked for.
func asking(d *Directory, key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	e := d.lists[key]
	return e != nil && e.busy
}

// known reports whether the Directory has an entry under key: a list asked
// for, or a search sent.
func known(d *Directory, key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lists[key] != nil
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func first(s []Station) string {
	if len(s) == 0 {
		return ""
	}
	return s[0].ID
}

// Each list's first call returns nothing and asks for it once; the next
// returns the answer from memory, without asking again.
func TestDirectoryAsksOnce(t *testing.T) {
	stations := func(s []Station, have bool, err error) (string, bool, error) { return first(s), have, err }
	counts := func(c []Count, have bool, err error) (string, bool, error) {
		if len(c) == 0 {
			return "", have, err
		}
		return c[0].Name, have, err
	}
	lists := []struct {
		key string
		get func(*Directory) (string, bool, error)
	}{
		{"top", func(d *Directory) (string, bool, error) { return stations(d.Top()) }},
		{"country:IN", func(d *Directory) (string, bool, error) { return stations(d.ByCountry("IN")) }},
		{"tag:jazz", func(d *Directory) (string, bool, error) { return stations(d.ByTag("jazz")) }},
		{"countries", func(d *Directory) (string, bool, error) { return counts(d.Countries()) }},
		{"genres", func(d *Directory) (string, bool, error) { return counts(d.Genres()) }},
	}
	for _, l := range lists {
		t.Run(l.key, func(t *testing.T) {
			f := newFakeLister()
			d, _ := directory(t, f)
			v := d.Version()
			if got, have, err := l.get(d); got != "" || have || err != nil {
				t.Fatalf("first call = %q, %v, %v", got, have, err)
			}
			if !asking(d, l.key) && f.times(l.key) == 0 {
				t.Fatal("the first call didn't ask")
			}
			select {
			case <-d.Ready():
			case <-time.After(2 * time.Second):
				t.Fatal("no answer came")
			}
			if d.Version() == v {
				t.Fatal("the version didn't change with the answer")
			}
			for range 10 {
				if got, have, err := l.get(d); got != l.key+" 1" || !have || err != nil {
					t.Fatalf("next call = %q, %v, %v", got, have, err)
				}
			}
			d.Close()
			if n := f.times(l.key); n != 1 {
				t.Fatalf("asked %d times", n)
			}
		})
	}
}

// A country's or a genre's stations are their own list.
func TestDirectoryListsApart(t *testing.T) {
	f := newFakeLister()
	d, _ := directory(t, f)
	waitFor(t, "India", func() bool { _, have, _ := d.ByCountry("IN"); return have })
	if s, have, _ := d.ByCountry("DE"); s != nil || have {
		t.Fatalf("Germany shows %q before it is answered", first(s))
	}
	if s, have, _ := d.ByTag("IN"); s != nil || have {
		t.Fatalf("the tag IN shows %q before it is answered", first(s))
	}
	waitFor(t, "Germany", func() bool { s, _, _ := d.ByCountry("DE"); return first(s) == "country:DE 1" })
	if s, _, _ := d.ByCountry("IN"); first(s) != "country:IN 1" {
		t.Fatalf("India shows %q", first(s))
	}
}

// A list ten minutes old is asked for again, and shows the old answer until
// the new one comes.
func TestDirectoryAsksAgainAfterTenMinutes(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	waitFor(t, "Top", func() bool { _, have, _ := d.Top(); return have })
	f.hold("top")
	clk.advance(fresh - time.Second)
	d.Top()
	if asking(d, "top") {
		t.Fatal("asked again within ten minutes")
	}
	clk.advance(time.Second)
	for range 5 {
		if s, have, err := d.Top(); first(s) != "top 1" || !have || err != nil {
			t.Fatalf("while asking again, Top = %q, %v, %v", first(s), have, err)
		}
		if !asking(d, "top") {
			t.Fatal("not asked again after ten minutes")
		}
	}
	v := d.Version()
	f.let("top")
	waitFor(t, "the new answer", func() bool { s, _, _ := d.Top(); return first(s) == "top 2" })
	if d.Version() == v {
		t.Fatal("the version didn't change with the new answer")
	}
	d.Close()
	if n := f.times("top"); n != 2 {
		t.Fatalf("asked %d times", n)
	}
}

// A failure keeps what was had, with the error, and the list isn't asked for
// again for 30 s, unless Retry says to.
func TestDirectoryFailureKeepsWhatWasHad(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	waitFor(t, "Top", func() bool { _, have, _ := d.Top(); return have })
	f.fail(ErrUnreachable)
	clk.advance(fresh)
	waitFor(t, "the failure", func() bool { _, _, err := d.Top(); return err != nil })
	if s, have, err := d.Top(); first(s) != "top 1" || !have || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("after failing, Top = %q, %v, %v", first(s), have, err)
	}

	f.hold("top")
	clk.advance(retryAfter - time.Second)
	for range 5 {
		d.Top()
	}
	if asking(d, "top") {
		t.Fatal("asked again within 30 s of failing")
	}
	clk.advance(time.Second)
	d.Top()
	if !asking(d, "top") {
		t.Fatal("not asked again 30 s after failing")
	}
	f.let("top")
	waitFor(t, "the second failure", func() bool { return !asking(d, "top") })

	f.hold("top")
	d.Top()
	if asking(d, "top") {
		t.Fatal("asked again at once after failing again")
	}
	f.fail(nil)
	d.Retry()
	d.Top()
	if !asking(d, "top") {
		t.Fatal("Retry didn't ask again")
	}
	f.let("top")
	waitFor(t, "the answer", func() bool { s, _, err := d.Top(); return first(s) == "top 4" && err == nil })
	d.Close()
	if n := f.times("top"); n != 4 {
		t.Fatalf("asked %d times", n)
	}
}

// A list that fails before any answer has nothing, and the error.
func TestDirectoryFirstFailure(t *testing.T) {
	f := newFakeLister()
	f.fail(ErrUnreachable)
	d, _ := directory(t, f)
	waitFor(t, "the failure", func() bool { _, _, err := d.Genres(); return err != nil })
	if c, have, err := d.Genres(); c != nil || have || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Genres = %v, %v, %v", c, have, err)
	}
}

// Retry clears a failure: while the list is asked again it reads as one asked
// for the first time, with no error, until the asking fails again.
func TestDirectoryRetryClearsTheFailure(t *testing.T) {
	f := newFakeLister()
	f.fail(ErrUnreachable)
	d, _ := directory(t, f)
	waitFor(t, "the failure", func() bool { _, _, err := d.Genres(); return err != nil })
	f.hold("genres")
	d.Retry()
	if c, have, err := d.Genres(); c != nil || have || err != nil {
		t.Fatalf("asked again, Genres = %v, %v, %v; want nothing yet, and no error", c, have, err)
	}
	if !asking(d, "genres") {
		t.Fatal("Retry didn't ask again")
	}
	f.let("genres")
	waitFor(t, "the second failure", func() bool { _, _, err := d.Genres(); return errors.Is(err, ErrUnreachable) })
}

// A search is sent once its query has stood for 300 ms: typing on starts the
// wait again, and the query typed over is never sent.
func TestDirectorySearchWaitsForTyping(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	if s, have, err := d.Search("ja"); s != nil || have || err != nil {
		t.Fatalf("Search = %q, %v, %v", first(s), have, err)
	}
	clk.advance(searchPause - time.Millisecond)
	d.Search("ja")
	if known(d, "search:ja") {
		t.Fatal("sent before typing paused")
	}
	d.Search("jaz")
	clk.advance(searchPause - time.Millisecond)
	if known(d, "search:ja") || known(d, "search:jaz") {
		t.Fatal("sent while typing")
	}
	clk.advance(time.Millisecond)
	if !known(d, "search:jaz") {
		t.Fatal("not sent once typing paused")
	}
	waitFor(t, "the answer", func() bool { _, have, _ := d.Search("jaz"); return have })
	if s, _, err := d.Search("jaz"); first(s) != "search:jaz 1" || err != nil {
		t.Fatalf("Search = %q, %v", first(s), err)
	}
	d.Close()
	if f.times("search:ja") != 0 || f.times("search:jaz") != 1 {
		t.Fatalf("asked for ja %d times and jaz %d times", f.times("search:ja"), f.times("search:jaz"))
	}
}

// When the timer is late, the next call after the pause sends the search.
func TestDirectorySearchSentByTheNextCall(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	d.after = func(time.Duration, func()) func() bool { return func() bool { return true } }
	d.Search("jazz")
	clk.advance(searchPause)
	if known(d, "search:jazz") {
		t.Fatal("sent with no call")
	}
	waitFor(t, "the answer", func() bool { _, have, _ := d.Search("jazz"); return have })
}

// With the real clock and timers, a search goes out on its own once typing
// pauses; Close returns with one still waiting to go out.
func TestDirectorySearchTimer(t *testing.T) {
	f := newFakeLister()
	d := NewDirectory(f, slog.New(slog.DiscardHandler))
	t.Cleanup(d.Close)
	start := time.Now()
	d.Search("jazz")
	select {
	case <-d.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("the search never went out")
	}
	if time.Since(start) < searchPause {
		t.Fatalf("sent after %v", time.Since(start))
	}
	if s, have, _ := d.Search("jazz"); first(s) != "search:jazz 1" || !have {
		t.Fatalf("Search = %q, %v", first(s), have)
	}

	d2 := NewDirectory(f, slog.New(slog.DiscardHandler))
	d2.Search("rock")
	d2.Close()
}

// A search sent cancels the one before it. That one isn't a failure: asked
// for again, it is sent again.
func TestDirectoryNewSearchCancelsTheOld(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	f.hold("search:jazz")
	d.Search("jazz")
	clk.advance(searchPause)
	waitFor(t, "jazz to go out", func() bool { return f.times("search:jazz") == 1 })
	jazz := f.lastCtx("search:jazz")
	f.hold("search:rock")
	d.Search("rock")
	if jazz.Err() != nil {
		t.Fatal("typing cancelled the search under way")
	}
	v := d.Version()
	clk.advance(searchPause)
	if jazz.Err() == nil {
		t.Fatal("sending rock didn't cancel jazz")
	}
	// Jazz's end is signalled, though nothing came: had it been typed again
	// while ending, asking for it now sends it.
	waitFor(t, "jazz's end to be signalled", func() bool { return d.Version() != v })
	if asking(d, "search:jazz") {
		t.Fatal("signalled before jazz ended")
	}
	f.let("search:rock")
	waitFor(t, "rock", func() bool { s, _, _ := d.Search("rock"); return first(s) == "search:rock 1" })
	if s, have, err := d.Search("jazz"); s != nil || have || err != nil {
		t.Fatalf("cancelled, jazz = %q, %v, %v", first(s), have, err)
	}
	f.let("search:jazz")
	clk.advance(searchPause)
	waitFor(t, "jazz again", func() bool { s, _, _ := d.Search("jazz"); return first(s) == "search:jazz 2" })
}

// An answer for a query typed over is kept, but isn't the new query's.
func TestDirectoryOldSearchIsKept(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	f.hold("search:jazz")
	d.Search("jazz")
	clk.advance(searchPause)
	waitFor(t, "jazz to go out", func() bool { return f.times("search:jazz") == 1 })
	d.Search("rock")
	v := d.Version()
	f.let("search:jazz")
	waitFor(t, "jazz's answer", func() bool { return d.Version() != v })
	if s, have, err := d.Search("rock"); s != nil || have || err != nil {
		t.Fatalf("rock = %q, %v, %v", first(s), have, err)
	}
	if s, have, err := d.Search("jazz"); first(s) != "search:jazz 1" || !have || err != nil {
		t.Fatalf("jazz = %q, %v, %v", first(s), have, err)
	}
	d.Close()
	if f.times("search:jazz") != 1 || f.times("search:rock") != 0 {
		t.Fatalf("asked for jazz %d times and rock %d times", f.times("search:jazz"), f.times("search:rock"))
	}
}

// The searches looked at last are kept, sixteen of them; an older one is sent
// again.
func TestDirectoryKeepsSixteenSearches(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	for i := range keepSearches + 1 {
		q := fmt.Sprint("q", i)
		d.Search(q)
		clk.advance(searchPause)
		waitFor(t, q, func() bool { _, have, _ := d.Search(q); return have })
	}
	if _, have, _ := d.Search("q1"); !have {
		t.Fatal("q1 was dropped")
	}
	if _, have, _ := d.Search("q0"); have {
		t.Fatal("q0 was kept")
	}
	clk.advance(searchPause)
	waitFor(t, "q0 again", func() bool { _, have, _ := d.Search("q0"); return have })
	d.Close()
	if f.times("search:q0") != 2 || f.times("search:q1") != 1 {
		t.Fatalf("asked for q0 %d times and q1 %d times", f.times("search:q0"), f.times("search:q1"))
	}
}

// The country lists asked for last are kept, sixteen of them; an older one is
// asked for again on its next call. Top and Genres stay.
func TestDirectoryKeepsSixteenCountries(t *testing.T) {
	f := newFakeLister()
	d, _ := directory(t, f)
	waitFor(t, "Top", func() bool { _, have, _ := d.Top(); return have })
	waitFor(t, "Genres", func() bool { _, have, _ := d.Genres(); return have })
	for i := range keepLists + 1 {
		code := fmt.Sprint("C", i)
		waitFor(t, code, func() bool { _, have, _ := d.ByCountry(code); return have })
	}
	for i := 1; i <= keepLists; i++ {
		if _, have, _ := d.ByCountry(fmt.Sprint("C", i)); !have {
			t.Fatalf("C%d was dropped", i)
		}
	}
	if _, have, _ := d.ByCountry("C0"); have {
		t.Fatal("C0 was kept")
	}
	waitFor(t, "C0 again", func() bool { _, have, _ := d.ByCountry("C0"); return have })
	if _, have, _ := d.Top(); !have {
		t.Fatal("Top was dropped")
	}
	if _, have, _ := d.Genres(); !have {
		t.Fatal("Genres was dropped")
	}
	d.Close()
	if n := f.times("country:C0"); n != 2 {
		t.Fatalf("asked for C0 %d times", n)
	}
	for i := 1; i <= keepLists; i++ {
		if n := f.times(fmt.Sprint("country:C", i)); n != 1 {
			t.Fatalf("asked for C%d %d times", i, n)
		}
	}
	if f.times("top") != 1 || f.times("genres") != 1 {
		t.Fatalf("asked for Top %d times and Genres %d times", f.times("top"), f.times("genres"))
	}
}

// A genre's stations and a country's share the limit.
func TestDirectoryCountriesAndGenresShareTheLimit(t *testing.T) {
	f := newFakeLister()
	d, _ := directory(t, f)
	for i := range keepLists {
		code := fmt.Sprint("C", i)
		waitFor(t, code, func() bool { _, have, _ := d.ByCountry(code); return have })
	}
	waitFor(t, "jazz", func() bool { _, have, _ := d.ByTag("jazz"); return have })
	if _, have, _ := d.ByCountry("C1"); !have {
		t.Fatal("C1 was dropped")
	}
	if _, have, _ := d.ByTag("jazz"); !have {
		t.Fatal("jazz was dropped")
	}
	if _, have, _ := d.ByCountry("C0"); have {
		t.Fatal("C0 was kept past the limit")
	}
}

// A list being asked for isn't dropped, even past the limit.
func TestDirectoryKeepsListsBeingAsked(t *testing.T) {
	f := newFakeLister()
	d, _ := directory(t, f)
	f.hold("country:C0")
	d.ByCountry("C0")
	for i := 1; i <= keepLists; i++ {
		code := fmt.Sprint("C", i)
		waitFor(t, code, func() bool { _, have, _ := d.ByCountry(code); return have })
	}
	if !asking(d, "country:C0") {
		t.Fatal("C0 was dropped while being asked")
	}
	f.let("country:C0")
	waitFor(t, "C0", func() bool { _, have, _ := d.ByCountry("C0"); return have })
	d.Close()
	if n := f.times("country:C0"); n != 1 {
		t.Fatalf("asked for C0 %d times", n)
	}
}

// An empty query asks nothing, and emptying the query drops the search
// waiting for typing to pause.
func TestDirectoryEmptySearchAsksNothing(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	for _, q := range []string{"", "   "} {
		if s, have, err := d.Search(q); s != nil || have || err != nil {
			t.Fatalf("Search(%q) = %q, %v, %v", q, first(s), have, err)
		}
	}
	clk.advance(time.Second)
	d.Search("jazz")
	d.Search("")
	clk.advance(time.Second)
	d.Close()
	if n := f.total(); n != 0 {
		t.Fatalf("asked %d times", n)
	}
}

// Close cancels what is being asked and returns once it has ended; nothing
// is asked after it, and the search waiting for its pause is never sent.
func TestDirectoryClose(t *testing.T) {
	f := newFakeLister()
	d, clk := directory(t, f)
	f.hold("top")
	f.hold("search:jazz")
	d.Top()
	d.Search("jazz")
	clk.advance(searchPause)
	d.Search("rock")
	waitFor(t, "both askings", func() bool { return f.times("top") == 1 && f.times("search:jazz") == 1 })
	v := d.Version()
	d.Close()
	if f.returned("top") != 1 || f.returned("search:jazz") != 1 {
		t.Fatal("Close returned before its askings ended")
	}
	if f.lastCtx("top").Err() == nil || f.lastCtx("search:jazz").Err() == nil {
		t.Fatal("Close didn't cancel its askings")
	}
	clk.advance(time.Second)
	d.Top()
	d.Genres()
	d.Search("pop")
	clk.advance(time.Second)
	d.Search("pop")
	d.Close()
	if f.total() != 2 {
		t.Fatalf("asked %d times in all", f.total())
	}
	if d.Version() != v {
		t.Fatal("the cancelled askings changed the version")
	}
}

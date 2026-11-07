package lyrics

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/netx"
	"github.com/ujjwal313/boombrick/internal/store"
)

const twoLines = "[00:01.00]One\n[00:02.00]Two"

type fakeFetch struct {
	mu    sync.Mutex
	calls int
	r     Result
	err   error
}

func (f *fakeFetch) Get(ctx context.Context, t Track) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.r, f.err
}

func (f *fakeFetch) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "lyrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.DB
}

// song makes a file with no tags in dir, with an .lrc beside it holding lrc
// unless lrc is empty.
func song(t *testing.T, dir, lrc string) Track {
	t.Helper()
	path := filepath.Join(dir, "01 Rust.mp3")
	os.WriteFile(path, []byte("not audio"), 0o644)
	if lrc != "" {
		os.WriteFile(filepath.Join(dir, "01 Rust.lrc"), []byte(lrc), 0o644)
	}
	return Track{Path: path, Title: "Rust", Artist: "Odalys", Album: "Copper Season", Duration: 222 * time.Second}
}

func service(t *testing.T, db *sql.DB, f Fetcher, online bool) *Service {
	t.Helper()
	s := New(Options{DB: db, Fetch: f, Online: func() bool { return online }})
	t.Cleanup(s.Close)
	return s
}

// answer asks for tr's lyrics until the service has an answer.
func answer(t *testing.T, s *Service, tr Track) (Lyrics, State) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if l, st := s.Lyrics(tr); st != Looking {
			return l, st
		}
		select {
		case <-s.Ready():
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("no answer")
	return Lyrics{}, Looking
}

func TestLyricsBesideTheMusicComeFirst(t *testing.T) {
	f := &fakeFetch{r: Result{Synced: "[00:01.00]Online"}}
	s := service(t, openDB(t), f, true)
	l, st := answer(t, s, song(t, t.TempDir(), twoLines))
	if st != Found || !l.Synced || len(l.Lines) != 2 || f.count() != 0 {
		t.Fatalf("%v %+v, %d fetches", st, l, f.count())
	}
}

// Lyrics found are told to Found, for the fonts they need.
func TestFoundLyricsAreTold(t *testing.T) {
	found := make(chan Lyrics, 1)
	s := New(Options{DB: openDB(t), Found: func(l Lyrics) { found <- l }})
	t.Cleanup(s.Close)
	if _, st := answer(t, s, song(t, t.TempDir(), twoLines)); st != Found {
		t.Fatalf("state %v", st)
	}
	select {
	case l := <-found:
		if len(l.Lines) != 2 {
			t.Fatalf("told %d lines", len(l.Lines))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not told")
	}
}

func TestFetchedLyricsAreKept(t *testing.T) {
	db, dir := openDB(t), t.TempDir()
	f := &fakeFetch{r: Result{Synced: twoLines}}
	tr := song(t, dir, "")
	s := New(Options{DB: db, Fetch: f, Online: func() bool { return true }})
	if l, st := answer(t, s, tr); st != Found || len(l.Lines) != 2 {
		t.Fatalf("%v %+v", st, l)
	}
	s.Close()
	s2 := service(t, db, f, true)
	if l, st := answer(t, s2, tr); st != Found || len(l.Lines) != 2 || f.count() != 1 {
		t.Fatalf("second launch: %v %+v, %d fetches", st, l, f.count())
	}
}

func TestNothingOnlineIsRemembered(t *testing.T) {
	db, dir := openDB(t), t.TempDir()
	f := &fakeFetch{err: ErrNotFound}
	tr := song(t, dir, "")
	s := New(Options{DB: db, Fetch: f, Online: func() bool { return true }})
	if _, st := answer(t, s, tr); st != None {
		t.Fatalf("state %v, want None", st)
	}
	s.Close()
	s2 := service(t, db, f, true)
	if _, st := answer(t, s2, tr); st != None || f.count() != 1 {
		t.Fatalf("second launch: %v, %d fetches; want None and no new fetch", st, f.count())
	}
}

func TestOffline(t *testing.T) {
	f := &fakeFetch{r: Result{Synced: twoLines}}
	s := service(t, openDB(t), f, false)
	if _, st := answer(t, s, song(t, t.TempDir(), "")); st != Offline || f.count() != 0 {
		t.Fatalf("state %v, %d fetches", st, f.count())
	}
}

func TestSyncedOnlineBeatsPlainBesideTheMusic(t *testing.T) {
	f := &fakeFetch{r: Result{Synced: twoLines}}
	s := service(t, openDB(t), f, true)
	if l, st := answer(t, s, song(t, t.TempDir(), "Just words\nNo times")); st != Found || !l.Synced {
		t.Fatalf("%v %+v", st, l)
	}
}

func TestPlainBesideTheMusicWhenNothingBetter(t *testing.T) {
	f := &fakeFetch{err: ErrNotFound}
	s := service(t, openDB(t), f, true)
	l, st := answer(t, s, song(t, t.TempDir(), "Just words\nNo times"))
	if st != Found || l.Synced || len(l.Lines) != 2 {
		t.Fatalf("%v %+v", st, l)
	}
}

func TestInstrumental(t *testing.T) {
	f := &fakeFetch{r: Result{Instrumental: true}}
	s := service(t, openDB(t), f, true)
	if _, st := answer(t, s, song(t, t.TempDir(), "")); st != Instrumental {
		t.Fatalf("state %v", st)
	}
}

// gateFetch holds the lookup of the track titled "hold" until released and
// records the titles it is asked for, in order.
type gateFetch struct {
	mu      sync.Mutex
	titles  []string
	started chan struct{} // receives when the held lookup has begun
	release chan struct{}
	done    chan struct{} // receives as each lookup ends
}

func (g *gateFetch) Get(ctx context.Context, t Track) (Result, error) {
	g.mu.Lock()
	g.titles = append(g.titles, t.Title)
	g.mu.Unlock()
	if t.Title == "hold" {
		g.started <- struct{}{}
		<-g.release
	}
	g.done <- struct{}{}
	return Result{}, ErrNotFound
}

func TestNewestRequestRunsFirst(t *testing.T) {
	g := &gateFetch{started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}, 4)}
	s := service(t, openDB(t), g, true)
	named := func(title string) Track {
		tr := song(t, t.TempDir(), "")
		tr.Title = title
		return tr
	}
	hold, a, b, c := named("hold"), named("a"), named("b"), named("c")
	s.Lyrics(hold)
	<-g.started // the worker is busy, so what follows queues up
	s.Prefetch(a, b)
	s.Prefetch(b, c) // after a skip: b is now playing and must not wait behind a
	close(g.release)
	for range 4 { // asking again would reorder the queue, so just count the lookups
		select {
		case <-g.done:
		case <-time.After(5 * time.Second):
			t.Fatal("lookups did not finish")
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if got := g.titles; len(got) != 4 || got[1] != "b" || got[2] != "c" || got[3] != "a" {
		t.Fatalf("lookups ran in the order %v, want [hold b c a]", got)
	}
}

// A track whose lookup is under way doesn't hold up another with lyrics beside
// it: every pending local search runs before the next lookup.
func TestLyricsBesideTheMusicDontWaitForTheNetwork(t *testing.T) {
	h := &heldFetch{started: make(chan string, 4), release: make(chan struct{})}
	s := service(t, openDB(t), h, true)
	named := func(title, lrc string) Track {
		tr := song(t, t.TempDir(), lrc)
		tr.Title = title
		return tr
	}
	s.Lyrics(named("first", ""))
	<-h.started // the worker is busy, so what follows queues up
	beside := named("beside", twoLines)
	s.Lyrics(beside)
	s.Lyrics(named("online", "")) // newer, but it needs the network
	h.release <- struct{}{}
	if title := <-h.started; title != "online" {
		t.Fatalf("looked up %q", title)
	}
	// online's lookup holds the worker now.
	if l, st := s.Lyrics(beside); st != Found || len(l.Lines) != 2 {
		t.Fatalf("lyrics beside the music still %v while another track's lookup runs", st)
	}
}

// heldFetch holds each lookup until it is let go or given up, sending the
// track's title to started as it begins.
type heldFetch struct {
	started chan string
	release chan struct{}
}

func (h *heldFetch) Get(ctx context.Context, t Track) (Result, error) {
	h.started <- t.Title
	select {
	case <-h.release:
	case <-ctx.Done():
	}
	return Result{}, ErrNotFound
}

func TestLookupsGiveUpAfter10s(t *testing.T) {
	f := &deadlineFetch{}
	s := service(t, openDB(t), f, true)
	asked := time.Now()
	answer(t, s, song(t, t.TempDir(), ""))
	f.mu.Lock()
	defer f.mu.Unlock()
	if left := f.deadline.Sub(asked); left < 10*time.Second || left > 11*time.Second {
		t.Fatalf("a lookup may take %v", left)
	}
}

// deadlineFetch records the deadline its lookup was given.
type deadlineFetch struct {
	mu       sync.Mutex
	deadline time.Time
}

func (f *deadlineFetch) Get(ctx context.Context, t Track) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deadline, _ = ctx.Deadline()
	return Result{}, ErrNotFound
}

// Files with no tags and no folders to name them have nothing to look up by.
func TestNoLookupWithoutAnArtistOrTitle(t *testing.T) {
	f := &fakeFetch{r: Result{Synced: twoLines}}
	s := service(t, openDB(t), f, true)
	for _, c := range []struct{ artist, title string }{{library.UnknownArtist, "Rust"}, {"", "Rust"}, {"Odalys", " "}} {
		tr := song(t, t.TempDir(), "")
		tr.Artist, tr.Title = c.artist, c.title
		if _, st := answer(t, s, tr); st != None {
			t.Errorf("%q by %q: %v, want None", c.title, c.artist, st)
		}
	}
	if f.count() != 0 {
		t.Fatalf("%d lookups", f.count())
	}
}

func TestFailedLookupIsNotOffline(t *testing.T) {
	f := &fakeFetch{err: &netx.StatusError{Code: 500}}
	s := service(t, openDB(t), f, true)
	if _, st := answer(t, s, song(t, t.TempDir(), "")); st != Failed {
		t.Fatalf("state %v, want Failed", st)
	}
}

func TestPlainBesideTheMusicGivesWayToSyncedOnline(t *testing.T) {
	var online atomic.Bool
	f := &fakeFetch{r: Result{Synced: twoLines}}
	s := New(Options{DB: openDB(t), Fetch: f, Online: online.Load, retry: time.Nanosecond})
	t.Cleanup(s.Close)
	tr := song(t, t.TempDir(), "Just words\nNo times")
	if l, st := answer(t, s, tr); st != Found || l.Synced || f.count() != 0 {
		t.Fatalf("offline: %v %+v, %d fetches", st, l, f.count())
	}
	online.Store(true)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if l, st := s.Lyrics(tr); st == Found && l.Synced {
			return
		} else if st != Found {
			t.Fatalf("state %v while better lyrics were being fetched; the plain ones should stay", st)
		}
		select {
		case <-s.Ready():
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("synced lyrics never replaced the plain ones")
}

func TestPrefetch(t *testing.T) {
	f := &fakeFetch{r: Result{Synced: twoLines}}
	s := service(t, openDB(t), f, true)
	tr := song(t, t.TempDir(), "")
	s.Prefetch(tr, Track{})
	select {
	case <-s.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("prefetching found nothing")
	}
	if _, st := s.Lyrics(tr); st != Found {
		t.Fatalf("state after prefetching %v", st)
	}
	if _, st := s.Lyrics(Track{}); st != None {
		t.Fatalf("a track without a path: %v", st)
	}
}

func TestTrackKey(t *testing.T) {
	for _, c := range []struct {
		t    Track
		want string
	}{
		{Track{Path: "/m/1.flac", SpotifyID: "abc"}, "/m/1.flac"},
		{Track{SpotifyID: "abc"}, "spotify:abc"},
		{Track{Title: "Rust"}, ""},
	} {
		if got := c.t.Key(); got != c.want {
			t.Errorf("Key of %+v = %q, want %q", c.t, got, c.want)
		}
	}
}

func streamed() Track {
	return Track{SpotifyID: "abc", Title: "Rust", Artist: "Odalys", Album: "Copper Season", Duration: 222 * time.Second}
}

func TestStreamedTrackIsLookedUpAndCached(t *testing.T) {
	db := openDB(t)
	f := &fakeFetch{r: Result{Synced: twoLines}}
	s := service(t, db, f, true)
	if l, st := answer(t, s, streamed()); st != Found || !l.Synced {
		t.Fatalf("streamed track: %v %+v", st, l)
	}
	if f.count() != 1 {
		t.Fatalf("fetches %d", f.count())
	}
	s2 := service(t, db, f, true)
	if _, st := answer(t, s2, streamed()); st != Found {
		t.Fatalf("from the cache: %v", st)
	}
	if f.count() != 1 {
		t.Fatalf("asked again despite the cache: %d fetches", f.count())
	}
}

func TestStreamedTrackReadsItsDownloadedCopy(t *testing.T) {
	f := &fakeFetch{r: Result{Synced: "[00:01.00]Online"}}
	tr := song(t, t.TempDir(), twoLines)
	var calls atomic.Int32
	s := New(Options{DB: openDB(t), Fetch: f, Online: func() bool { return true },
		FileOf: func(id string) (string, bool) {
			calls.Add(1)
			return tr.Path, id == "abc"
		}})
	t.Cleanup(s.Close)
	l, st := answer(t, s, streamed())
	if st != Found || len(l.Lines) != 2 || l.Lines[0].Text != "One" {
		t.Fatalf("copy's lyrics: %v %+v", st, l)
	}
	if f.count() != 0 {
		t.Fatalf("fetched despite a copy with lyrics: %d", f.count())
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("FileOf called %d times, want 1", n)
	}
}

func TestStreamedTrackWithoutFileOrIDIsNone(t *testing.T) {
	f := &fakeFetch{r: Result{Synced: twoLines}}
	s := service(t, openDB(t), f, true)
	if _, st := s.Lyrics(Track{Title: "Rust", Artist: "Odalys"}); st != None {
		t.Fatalf("no path or ID: %v", st)
	}
	s.Prefetch(Track{Title: "Rust", Artist: "Odalys"})
	if f.count() != 0 {
		t.Fatalf("fetched for a track with no key: %d", f.count())
	}
}

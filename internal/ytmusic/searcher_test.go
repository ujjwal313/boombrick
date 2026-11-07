package ytmusic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeYT answers at once, or once its gate is closed, and records what it
// was asked.
type fakeYT struct {
	mu    sync.Mutex
	asked []string
	gate  chan struct{}
	err   error
	empty bool // searches find nothing
}

func (f *fakeYT) note(ctx context.Context, what string) error {
	f.mu.Lock()
	f.asked = append(f.asked, what)
	gate, err := f.gate, f.err
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (f *fakeYT) Search(ctx context.Context, query string, fl Filter) ([]Item, error) {
	if err := f.note(ctx, "search "+query+" "+FilterNames[fl]); err != nil || f.empty {
		return nil, err
	}
	return []Item{{Kind: SongItem, ID: query, Title: query}}, nil
}

func (f *fakeYT) Album(ctx context.Context, id string) (AlbumPage, error) {
	if err := f.note(ctx, "album "+id); err != nil {
		return AlbumPage{}, err
	}
	return AlbumPage{ID: id, Title: "Album " + id}, nil
}

func (f *fakeYT) Artist(ctx context.Context, id string) (ArtistPage, error) {
	if err := f.note(ctx, "artist "+id); err != nil {
		return ArtistPage{}, err
	}
	return ArtistPage{ID: id, Name: "Artist " + id}, nil
}

func (f *fakeYT) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *fakeYT) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

// eventually waits up to 2 s for ok.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func newTestSearcher(t *testing.T, f *fakeYT, online func() bool) *Searcher {
	s := NewSearcher(Options{Client: f, Online: online, Pause: 30 * time.Millisecond, Retry: 50 * time.Millisecond})
	t.Cleanup(s.Close)
	return s
}

func TestSearchWaitsForTypingToPause(t *testing.T) {
	f := &fakeYT{}
	s := NewSearcher(Options{Client: f, Pause: 200 * time.Millisecond})
	t.Cleanup(s.Close)
	for _, q := range []string{"n", "ni", "nig"} {
		if _, st := s.Search(q, All); st != Waiting {
			t.Fatalf("%q: %v", q, st)
		}
	}
	eventually(t, "the search", func() bool { _, st := s.Search("nig", All); return st == Done })
	if got := f.calls(); !slices.Equal(got, []string{"search nig All"}) {
		t.Fatalf("asked %q", got)
	}
	if items, _ := s.Search("nig", All); len(items) != 1 || items[0].ID != "nig" {
		t.Fatalf("answer %+v", items)
	}
}

func TestANewerSearchCancelsOneSent(t *testing.T) {
	f := &fakeYT{gate: make(chan struct{})}
	s := newTestSearcher(t, f, nil)
	s.Search("first", All)
	eventually(t, "the first search to go out", func() bool { return len(f.calls()) == 1 })
	s.Search("second", All)
	eventually(t, "the second search to go out", func() bool { return len(f.calls()) == 2 })
	close(f.gate)
	eventually(t, "the second answer", func() bool { _, st := s.Search("second", All); return st == Done })
	// The first was cancelled, not answered: asking again sends it again.
	if _, st := s.Search("first", All); st != Waiting {
		t.Fatalf("the cancelled search: %v", st)
	}
	eventually(t, "the first search to go out again", func() bool { return len(f.calls()) == 3 })
}

func TestAnAnsweredSearchCancelsOneSent(t *testing.T) {
	f := &fakeYT{}
	s := newTestSearcher(t, f, nil)
	s.Search("a", All)
	eventually(t, "the first answer", func() bool { _, st := s.Search("a", All); return st == Done })
	f.mu.Lock()
	f.gate = make(chan struct{})
	f.mu.Unlock()
	s.Search("ab", All)
	eventually(t, "ab to go out", func() bool { return len(f.calls()) == 2 })
	// Back to the answered query: served from the cache, and ab is cancelled.
	if _, st := s.Search("a", All); st != Done {
		t.Fatalf("a: %v", st)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(f.calls()); n != 2 {
		t.Fatalf("%d requests, want no further search", n)
	}
	f.mu.Lock()
	close(f.gate)
	f.mu.Unlock()
	// ab never answered: asking again sends it again.
	if _, st := s.Search("ab", All); st != Waiting {
		t.Fatalf("the cancelled search: %v", st)
	}
	eventually(t, "ab to be asked again", func() bool { return len(f.calls()) == 3 })
	eventually(t, "ab answered", func() bool { _, st := s.Search("ab", All); return st == Done })
}

// slowYT ignores cancellation for a while before answering a search.
type slowYT struct {
	fakeYT
	delay time.Duration
}

func (f *slowYT) Search(ctx context.Context, query string, fl Filter) ([]Item, error) {
	f.note(context.Background(), "search "+query+" "+FilterNames[fl])
	time.Sleep(f.delay)
	return []Item{{Kind: SongItem, ID: query, Title: query}}, nil
}

func TestGoingBackToACancelledSearchSendsItAgain(t *testing.T) {
	f := &slowYT{delay: 200 * time.Millisecond}
	s := NewSearcher(Options{Client: f, Pause: 20 * time.Millisecond})
	t.Cleanup(s.Close)
	s.Search("a", All)
	eventually(t, "a to go out", func() bool { return len(f.calls()) == 1 })
	s.Search("b", All) // cancels a, which goes on answering for a while
	s.Search("a", All) // back to a before b was sent
	select {
	case <-s.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("no word of a")
	}
	items, st := s.Search("a", All)
	if st != Done || len(items) != 1 || items[0].ID != "a" {
		t.Fatalf("a: %v %+v", st, items)
	}
	if got := f.calls(); !slices.Equal(got, []string{"search a All", "search a All"}) {
		t.Fatalf("asked %q", got)
	}
}

// Switching filter on a query already sent is no typing to wait out: the
// search goes at once.
func TestAFilterSwitchIsSentAtOnce(t *testing.T) {
	f := &fakeYT{gate: make(chan struct{})}
	s := NewSearcher(Options{Client: f, Pause: 20 * time.Millisecond})
	t.Cleanup(s.Close)
	s.Search("night", All)
	eventually(t, "All to go out", func() bool { return len(f.calls()) == 1 })
	s.mu.Lock()
	s.opts.Pause = time.Hour // from here, a search that waits for the pause never goes
	s.mu.Unlock()

	// All is still out: Songs replaces it, without the pause.
	s.Search("night", Songs)
	eventually(t, "Songs to go out", func() bool { return len(f.calls()) == 2 })
	f.mu.Lock()
	close(f.gate)
	f.mu.Unlock()
	eventually(t, "Songs answered", func() bool { _, st := s.Search("night", Songs); return st == Done })

	// Songs is answered: Albums goes at once too.
	eventually(t, "Albums answered", func() bool { _, st := s.Search("night", Albums); return st == Done })

	// A new query is still being typed, and waits.
	s.Search("nights", All)
	time.Sleep(100 * time.Millisecond)
	want := []string{"search night All", "search night Songs", "search night Albums"}
	if got := f.calls(); !slices.Equal(got, want) {
		t.Fatalf("asked %q, want %q", got, want)
	}
}

// A failed search asked for again under its own filter is being typed again,
// and waits out the pause; another filter of it is a switch, and doesn't.
func TestAFailedSearchAskedAgainWaits(t *testing.T) {
	f := &fakeYT{err: errors.New("connection reset")}
	s := NewSearcher(Options{Client: f, Pause: 20 * time.Millisecond, Retry: 50 * time.Millisecond})
	t.Cleanup(s.Close)
	eventually(t, "a failure", func() bool { _, st := s.Search("a", All); return st == Failed })
	f.setErr(nil)
	s.mu.Lock()
	s.opts.Pause = time.Hour
	s.mu.Unlock()
	time.Sleep(60 * time.Millisecond) // past Retry: asking again sends it again
	if _, st := s.Search("a", All); st != Waiting {
		t.Fatalf("a: %v", st)
	}
	time.Sleep(100 * time.Millisecond)
	if got := f.calls(); !slices.Equal(got, []string{"search a All"}) {
		t.Fatalf("asked %q, want the pause waited out", got)
	}
	eventually(t, "Songs answered", func() bool { _, st := s.Search("a", Songs); return st == Done })
}

func TestAnAnsweredSearchDropsOneWaiting(t *testing.T) {
	f := &fakeYT{}
	s := NewSearcher(Options{Client: f, Pause: 100 * time.Millisecond})
	t.Cleanup(s.Close)
	s.Search("a", All)
	eventually(t, "a answered", func() bool { _, st := s.Search("a", All); return st == Done })
	s.Search("ab", All)
	if _, st := s.Search("a", All); st != Done {
		t.Fatalf("a: %v", st)
	}
	time.Sleep(250 * time.Millisecond)
	if got := f.calls(); !slices.Equal(got, []string{"search a All"}) {
		t.Fatalf("asked %q", got)
	}
}

func TestClosingTwice(t *testing.T) {
	s := NewSearcher(Options{Client: &fakeYT{}})
	s.Close()
	s.Close()
}

func TestPagesAreAskedOnceAndKept(t *testing.T) {
	f := &fakeYT{}
	s := newTestSearcher(t, f, nil)
	if _, st := s.Album("MPREb_1"); st != Waiting {
		t.Fatalf("album: %v", st)
	}
	select {
	case <-s.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("no word of the answer")
	}
	s.Artist("UC1")
	eventually(t, "the album", func() bool { _, st := s.Album("MPREb_1"); return st == Done })
	eventually(t, "the artist", func() bool { _, st := s.Artist("UC1"); return st == Done })
	if a, _ := s.Album("MPREb_1"); a.Title != "Album MPREb_1" {
		t.Fatalf("album %+v", a)
	}
	got := f.calls()
	slices.Sort(got)
	if !slices.Equal(got, []string{"album MPREb_1", "artist UC1"}) {
		t.Fatalf("asked %q", got)
	}
}

func TestOfflineAndFailuresAreAskedAgain(t *testing.T) {
	var online atomic.Bool
	f := &fakeYT{}
	s := newTestSearcher(t, f, online.Load)
	eventually(t, "offline", func() bool { _, st := s.Album("A"); return st == Offline })
	if n := len(f.calls()); n != 0 {
		t.Fatalf("asked %d times while offline", n)
	}
	online.Store(true)
	eventually(t, "an answer once online", func() bool { _, st := s.Album("A"); return st == Done })

	f.setErr(errors.New("connection reset"))
	eventually(t, "a failure", func() bool { _, st := s.Album("B"); return st == Failed })
	f.setErr(fmt.Errorf("%w: no tabs", ErrChanged))
	eventually(t, "an unknown shape", func() bool { _, st := s.Album("B"); return st == Changed })
}

func TestAnswersAreBounded(t *testing.T) {
	f := &fakeYT{}
	s := newTestSearcher(t, f, nil)
	for i := range maxAnswers + 8 {
		id := strconv.Itoa(i)
		eventually(t, "album "+id, func() bool { _, st := s.Album(id); return st == Done })
	}
	s.mu.Lock()
	n := len(s.answers)
	s.mu.Unlock()
	if n != maxAnswers {
		t.Fatalf("%d answers kept", n)
	}
	if _, st := s.Album("0"); st != Waiting {
		t.Fatal("the oldest answer was kept")
	}
}

func TestSongsComeFromTheFallbackWhenSearchChanges(t *testing.T) {
	f := &fakeYT{err: fmt.Errorf("%w: no tabs", ErrChanged)}
	s := NewSearcher(Options{Client: f, Pause: time.Millisecond,
		Fallback: func(ctx context.Context, q string) ([]Item, error) {
			return []Item{{Kind: SongItem, ID: "yt1", Title: q + " (from yt-dlp)"}}, nil
		}})
	defer s.Close()
	eventually(t, "an answer", func() bool { _, st := s.Search("night drive", Songs); return st == Done })
	if items, _ := s.Search("night drive", Songs); len(items) != 1 || items[0].ID != "yt1" {
		t.Fatalf("items %+v", items)
	}
	// Albums and artists have no fallback.
	eventually(t, "albums", func() bool { _, st := s.Search("night drive", Albums); return st == Changed })
}

// Without yt-dlp the fallback fails, and search still says YouTube Music
// can't be read.
func TestAFallbackThatFailsLeavesSearchChanged(t *testing.T) {
	f := &fakeYT{err: fmt.Errorf("%w: no tabs", ErrChanged)}
	s := NewSearcher(Options{Client: f, Pause: time.Millisecond,
		Fallback: func(context.Context, string) ([]Item, error) { return nil, errors.New("yt-dlp isn't installed") }})
	defer s.Close()
	eventually(t, "an answer", func() bool { _, st := s.Search("night drive", All); return st == Changed })
}

// A search that finds nothing says so in the log, and whether yt-dlp answered
// it: "No results" on a device then tells what was asked and who answered.
func TestASearchThatFindsNothingIsLogged(t *testing.T) {
	for _, c := range []struct {
		name     string
		client   *fakeYT
		fallback bool
	}{
		{"youtube music", &fakeYT{empty: true}, false},
		{"yt-dlp", &fakeYT{err: fmt.Errorf("%w: no tabs", ErrChanged)}, true},
	} {
		var log bytes.Buffer
		s := NewSearcher(Options{Client: c.client, Pause: time.Millisecond, Log: slog.New(slog.NewTextHandler(&log, nil)),
			Fallback: func(context.Context, string) ([]Item, error) { return nil, nil }})
		t.Cleanup(s.Close)
		eventually(t, c.name+" to answer", func() bool { _, st := s.Search("hello", All); return st == Done })
		s.Close() // done writing to log
		want := "fallback=" + strconv.FormatBool(c.fallback)
		if l := log.String(); !strings.Contains(l, "found nothing") || !strings.Contains(l, "q=hello") || !strings.Contains(l, want) {
			t.Errorf("%s: the log says %q", c.name, l)
		}
	}
}

func TestCloseCancelsWhatIsSent(t *testing.T) {
	f := &fakeYT{gate: make(chan struct{})}
	s := NewSearcher(Options{Client: f, Pause: time.Millisecond})
	s.Album("A")
	eventually(t, "the request", func() bool { return len(f.calls()) == 1 })
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close waited for the request")
	}
}

// The fallback's answer for All is its answer for Songs too: switching filter
// doesn't run yt-dlp again for the same query.
func TestAFallbackAnswerForAllAnswersSongs(t *testing.T) {
	f := &fakeYT{err: fmt.Errorf("%w: no tabs", ErrChanged)}
	var runs atomic.Int32
	s := NewSearcher(Options{Client: f, Pause: time.Millisecond,
		Fallback: func(ctx context.Context, q string) ([]Item, error) {
			runs.Add(1)
			return []Item{{Kind: SongItem, ID: "yt1", Title: q}}, nil
		}})
	defer s.Close()
	eventually(t, "All answered", func() bool { _, st := s.Search("night drive", All); return st == Done })
	items, st := s.Search("night drive", Songs)
	if st != Done || len(items) != 1 || items[0].ID != "yt1" || runs.Load() != 1 {
		t.Fatalf("Songs: %+v, %v; yt-dlp ran %d times", items, st, runs.Load())
	}
}

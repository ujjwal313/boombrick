package download

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/remux"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// fakeFetcher runs batches by its steps for each song: "ok" (the default)
// lands it, "gone" fails it, "hold" starts it and waits for the batch to be
// stopped, "slow" lands it once slow is closed, "net" loses the network and
// fails with it, "down" fails with the network while Wi-Fi stays up, "panic"
// panics. It records the batches, the context of the latest, and the batches
// that began while the queue was paused.
type fakeFetcher struct {
	mu      sync.Mutex
	parts   string
	steps   map[string]string
	batches [][]string
	last    context.Context
	paused  func() bool
	drop    func()
	bad     int

	running   int           // batches under way
	searching bool          // a search is under way
	overlaps  int           // searches and batches that ran at once
	gate      chan struct{} // a search waits for this to close, when set
	slow      chan struct{} // a "slow" song waits for this to close
}

func (f *fakeFetcher) Run(ctx context.Context, ids []string, event func(Event)) error {
	f.mu.Lock()
	f.batches = append(f.batches, slices.Clone(ids))
	f.last = ctx
	if f.paused != nil && f.paused() {
		f.bad++
	}
	f.running++
	if f.searching {
		f.overlaps++
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
	}()
	for _, id := range ids {
		f.mu.Lock()
		step := f.steps[id]
		f.mu.Unlock()
		event(Event{ID: id, Done: 25, Total: 100, Speed: 2048})
		switch step {
		case "hold":
			<-ctx.Done()
			return ctx.Err()
		case "slow":
			f.mu.Lock()
			slow := f.slow
			f.mu.Unlock()
			select {
			case <-slow:
			case <-ctx.Done():
				return ctx.Err()
			}
			src := filepath.Join(f.parts, id+".webm")
			os.WriteFile(src, []byte("webm "+id), 0o644)
			event(Event{ID: id, Path: src})
		case "gone":
			event(Event{ID: id, Failure: Unavailable, Message: "Video unavailable"})
		case "net":
			f.drop()
			event(Event{ID: id, Failure: Network, Message: "Unable to download webpage"})
		case "down":
			event(Event{ID: id, Failure: Network, Message: "Unable to download webpage: Temporary failure in name resolution"})
		case "panic":
			panic("the fake gave up")
		default:
			src := filepath.Join(f.parts, id+".webm")
			os.WriteFile(src, []byte("webm "+id), 0o644)
			event(Event{ID: id, Path: src})
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// Search answers with one song named after query, once the gate is closed.
func (f *fakeFetcher) Search(ctx context.Context, query string) ([]ytmusic.Item, error) {
	f.mu.Lock()
	f.searching = true
	if f.running > 0 {
		f.overlaps++
	}
	gate := f.gate
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.searching = false
		f.mu.Unlock()
	}()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []ytmusic.Item{{Kind: ytmusic.SongItem, ID: "s1", Title: query}}, nil
}

// busy reports whether a search is under way, and how many times a search
// and a batch ran at once.
func (f *fakeFetcher) busy() (searching bool, overlaps int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searching, f.overlaps
}

func (f *fakeFetcher) set(id, step string) {
	f.mu.Lock()
	f.steps[id] = step
	f.mu.Unlock()
}

// lastCtx is the context of the latest batch.
func (f *fakeFetcher) lastCtx() context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// early is how many batches began while the queue was paused.
func (f *fakeFetcher) early() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bad
}

func (f *fakeFetcher) runs() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.batches)
}

type fakeInstaller struct {
	mu        sync.Mutex
	installed bool
	installs  int
	fail      error
	block     chan struct{} // an install waits for this to close
	entered   chan struct{} // and says when it has begun
	steps     int           // progress reports, 1 when 0
	version   string        // what Version reports
}

func (i *fakeInstaller) Program() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.installed {
		return "/bin/yt-dlp"
	}
	return ""
}

func (i *fakeInstaller) Install(ctx context.Context, progress func(done, total int64)) (bool, error) {
	i.mu.Lock()
	i.installs++
	block, entered := i.block, i.entered
	i.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.fail != nil {
		return false, i.fail
	}
	n := int64(max(i.steps, 1))
	for d := int64(1); d <= n; d++ {
		progress(d, n)
	}
	i.installed = true
	i.version = "2026.10.11"
	return true, nil
}

func (i *fakeInstaller) Version() (string, time.Time) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.version, time.Time{}
}

func (i *fakeInstaller) RecordVersion(context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.installed {
		return errors.New("yt-dlp isn't installed")
	}
	i.version = "2026.10.11"
	return nil
}

func (i *fakeInstaller) count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.installs
}

// env is a service over a database of its own, with a fake yt-dlp, a fake
// installer and a remux that copies. log and stopWait, when set, are used by
// the next start.
type env struct {
	s        *Service
	db       *sql.DB
	music    string
	f        *fakeFetcher
	inst     *fakeInstaller
	online   atomic.Bool
	free     atomic.Int64
	log      *slog.Logger
	stopWait time.Duration

	// While gated, the remux waits to be released, or for the service to close.
	gated     atomic.Bool
	entered   chan struct{}
	release   chan struct{}
	failRemux atomic.Bool // the next remux fails
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{db: openDB(t), music: filepath.Join(dir, "Music"), inst: &fakeInstaller{},
		f:       &fakeFetcher{parts: filepath.Join(dir, "parts"), steps: map[string]string{}},
		entered: make(chan struct{}, 16), release: make(chan struct{})}
	e.f.drop = func() { e.online.Store(false) }
	os.MkdirAll(e.f.parts, 0o755)
	e.online.Store(true)
	e.free.Store(32 << 30)
	e.start(t)
	return e
}

// start starts a service over e's database, as launching the app again does.
func (e *env) start(t *testing.T) {
	t.Helper()
	rows, err := loadRows(e.db)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{DB: e.db, Music: e.music, Online: e.online.Load, Log: e.log,
		Formats: library.Formats{Supported: func(string) bool { return true }, Duration: func(string) (time.Duration, error) { return 0, nil }}}
	s := newService(opts, rows, e.inst, func(string, string) fetcher { return e.f }, e.f.parts)
	e.f.paused = func() bool { return s.Status().Paused }
	s.fin.remux = func(src, dst string, _ remux.Tags) error {
		if e.gated.Load() {
			e.entered <- struct{}{}
			select {
			case <-e.release:
			case <-s.ctx.Done():
				return s.ctx.Err()
			}
		}
		if e.failRemux.Swap(false) {
			return errors.New("the remux failed")
		}
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	}
	s.space = func(string) (int64, int64, error) { return 64 << 30, e.free.Load(), nil }
	s.offlineWait, s.stopWait, s.panicWait = 10*time.Millisecond, cmp.Or(e.stopWait, 10*time.Millisecond), 10*time.Millisecond
	s.start()
	t.Cleanup(s.Close)
	e.s = s
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func (e *env) is(id string, want media.DownloadState) func() bool {
	return func() bool { st, _ := e.s.State(id); return st == want }
}

func song(id, title string, n int) ytmusic.Song {
	return ytmusic.Song{VideoID: id, Title: title, Artist: "Lumen Coast", Album: "Night Drive Theory",
		AlbumArtist: "Lumen Coast", Number: n}
}

func TestSongsLandInTheMusicFolder(t *testing.T) {
	e := newEnv(t)
	e.s.Get([]ytmusic.Song{song("v1", "Night Drive Theory", 1), song("v2", "Sodium Lights", 2)})
	eventually(t, "both saved", func() bool { return e.is("v1", media.Saved)() && e.is("v2", media.Saved)() })
	p, ok := e.s.Path("v2")
	if want := filepath.Join(e.music, "Boombrick", "Lumen Coast", "Night Drive Theory", "02 Sodium Lights.opus"); !ok || p != want {
		t.Fatalf("Path = %q, %v", p, ok)
	}
	if b, _ := os.ReadFile(p); string(b) != "webm v2" {
		t.Fatalf("the file holds %q", b)
	}
	if n := e.inst.count(); n != 1 {
		t.Fatalf("yt-dlp installed %d times", n)
	}
	var landed []string
	for _, o := range e.s.Outcomes() {
		if o.Path != "" {
			landed = append(landed, o.VideoID)
		}
	}
	slices.Sort(landed)
	if !slices.Equal(landed, []string{"v1", "v2"}) || len(e.s.Outcomes()) != 0 {
		t.Fatalf("outcomes %q, then more", landed)
	}
	// Asking again for a saved song does nothing; after a restart it is
	// still saved.
	e.s.Get([]ytmusic.Song{song("v1", "Night Drive Theory", 1)})
	e.s.Close()
	e.start(t)
	if st, p := e.s.State("v1"); st != media.Saved || p != 1 {
		t.Fatalf("after a restart: %v, %v", st, p)
	}
	if n := len(e.f.runs()); n != 1 {
		t.Fatalf("%d batches", n)
	}
}

func TestFailuresAndRetrying(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "gone")
	e.s.Get([]ytmusic.Song{song("v1", "Night Drive Theory", 1)})
	eventually(t, "the failure", e.is("v1", media.Failed))
	if o := e.s.Outcomes(); len(o) != 1 || o[0].Failure != Unavailable || o[0].Path != "" {
		t.Fatalf("outcomes %+v", o)
	}
	if st := e.s.Status(); len(st.Entries) != 1 || st.Entries[0].Failure != Unavailable {
		t.Fatalf("status %+v", st)
	}
	e.f.set("v1", "ok")
	e.s.Retry("v1", true)
	eventually(t, "the retry", e.is("v1", media.Saved))
	if n := e.inst.count(); n != 2 {
		t.Fatalf("installed %d times; Retry asked for an update", n)
	}
}

func TestFirstStartsTheBatchAgain(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "hold")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2), song("v3", "c", 3)})
	eventually(t, "v1 under way", e.is("v1", media.Downloading))
	if _, p := e.s.State("v1"); p != 0.25 {
		t.Fatalf("progress %v", p)
	}
	e.s.First("v3")
	eventually(t, "v3 saved", e.is("v3", media.Saved))
	if runs := e.f.runs(); len(runs) < 2 || !slices.Equal(runs[1], []string{"v3", "v1", "v2"}) {
		t.Fatalf("batches %q", runs)
	}
	// First on the song a batch is on leaves the batch alone.
	n := len(e.f.runs())
	eventually(t, "v1 under way again", e.is("v1", media.Downloading))
	e.s.First("v1")
	// First stops a batch before it returns, so the batch's context says.
	if e.f.lastCtx().Err() != nil || len(e.f.runs()) != n {
		t.Fatal("First on the song under way restarted the batch")
	}
}

func TestPausing(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "hold")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "v1 under way", e.is("v1", media.Downloading))
	e.s.SetPaused(true)
	eventually(t, "v1 queued", e.is("v1", media.Queued))
	if !e.s.Status().Paused {
		t.Fatal("not paused")
	}
	e.f.set("v1", "ok")
	e.s.Get([]ytmusic.Song{song("v2", "b", 2)}) // wakes the worker, which stays idle
	e.s.SetPaused(false)
	eventually(t, "both saved", func() bool { return e.is("v1", media.Saved)() && e.is("v2", media.Saved)() })
	if n := e.f.early(); n != 0 {
		t.Fatalf("%d batches began while paused", n)
	}
	if n := len(e.f.runs()); n != 2 {
		t.Fatalf("%d batches", n)
	}
}

// Held, as while YouTube is switched off, the queue begins no batch, but the
// one under way finishes; the user's pause is a thing of its own.
func TestHolding(t *testing.T) {
	e := newEnv(t)
	slow := make(chan struct{})
	e.f.mu.Lock()
	e.f.slow = slow
	e.f.mu.Unlock()
	e.f.set("v1", "slow")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "v1 under way", e.is("v1", media.Downloading))
	e.s.SetHeld(true)
	e.s.Get([]ytmusic.Song{song("v2", "b", 2)})
	close(slow)
	eventually(t, "v1 saved", e.is("v1", media.Saved))
	time.Sleep(50 * time.Millisecond)
	if !e.is("v2", media.Queued)() || len(e.f.runs()) != 1 {
		t.Fatalf("a batch began while held: %v", e.f.runs())
	}
	if e.s.Status().Paused {
		t.Fatal("holding the queue paused it")
	}
	if got := e.s.Note(); got != "YouTube is off" {
		t.Fatalf("held, the note says %q", got)
	}
	// Paused by the user meanwhile, it stays paused when the hold ends.
	e.s.SetPaused(true)
	e.s.SetHeld(false)
	time.Sleep(50 * time.Millisecond)
	if !e.is("v2", media.Queued)() || len(e.f.runs()) != 1 {
		t.Fatalf("a batch began while paused: %v", e.f.runs())
	}
	e.s.SetPaused(false)
	eventually(t, "v2 saved", e.is("v2", media.Saved))
}

func TestWaitingForWiFiAndSpace(t *testing.T) {
	e := newEnv(t)
	e.online.Store(false)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "waiting for Wi-Fi", func() bool { return e.s.Status().Waiting == waitOffline })
	e.free.Store(100 << 20)
	e.online.Store(true)
	eventually(t, "stopped for space", func() bool { return e.s.Status().Waiting == waitSpace })
	if free := e.s.Status().Storage.Free; free != 100<<20 {
		t.Fatalf("the card shows %d free", free)
	}
	if n := len(e.f.runs()); n != 0 {
		t.Fatalf("%d batches", n)
	}
	e.free.Store(32 << 30)
	eventually(t, "v1 saved", e.is("v1", media.Saved))
	if e.s.Status().Waiting != "" {
		t.Fatal("still says it waits")
	}
}

func TestADownloaderThatWontInstall(t *testing.T) {
	e := newEnv(t)
	e.inst.mu.Lock()
	e.inst.fail = errors.New("offline")
	e.inst.mu.Unlock()
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "the message", func() bool { return e.s.Status().Waiting == waitDownloader })
	e.inst.mu.Lock()
	e.inst.fail = nil
	e.inst.mu.Unlock()
	eventually(t, "v1 saved", e.is("v1", media.Saved))
}

func TestUpdateDownloader(t *testing.T) {
	e := newEnv(t)
	if d := e.s.Downloader(); d.Version != "" || d.Updating || d.Failed {
		t.Fatalf("before asking: %+v", d)
	}
	// Asked for from Settings, it installs with nothing queued.
	e.s.UpdateDownloader()
	eventually(t, "yt-dlp installed", func() bool { d := e.s.Downloader(); return d.Version == "2026.10.11" && !d.Updating })
	if n := e.inst.count(); n != 1 || e.s.Status().Waiting != "" || len(e.f.runs()) != 0 {
		t.Fatalf("%d installs, waiting %q, %d batches", n, e.s.Status().Waiting, len(e.f.runs()))
	}
	// Offline, it fails at once rather than wait for Wi-Fi.
	e.online.Store(false)
	e.s.UpdateDownloader()
	eventually(t, "the failure", func() bool { return e.s.Downloader().Failed })
	if d := e.s.Downloader(); d.Updating || e.inst.count() != 1 || e.s.Status().Waiting != "" {
		t.Fatalf("offline: %+v, %d installs, waiting %q", d, e.inst.count(), e.s.Status().Waiting)
	}
	// A failed install says so, and asking again tries again.
	e.online.Store(true)
	e.inst.mu.Lock()
	e.inst.fail = errors.New("the release is gone")
	e.inst.mu.Unlock()
	e.s.UpdateDownloader()
	if d := e.s.Downloader(); d.Failed || !d.Updating {
		t.Fatalf("asked again: %+v", d)
	}
	eventually(t, "the failure", func() bool { return e.s.Downloader().Failed })
	if d := e.s.Downloader(); d.Updating || d.Version != "2026.10.11" || e.inst.count() != 2 {
		t.Fatalf("after failing: %+v, %d installs", d, e.inst.count())
	}
}

// An update asked for by retrying a song waits for the next batch, with the
// queue paused or offline: only Settings' ask runs with nothing to download,
// and only it says how it went.
func TestARetrysUpdateWaitsForABatch(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "gone")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "the failure", e.is("v1", media.Failed))
	e.s.SetPaused(true)
	e.online.Store(false)
	e.s.Retry("v1", true)
	time.Sleep(50 * time.Millisecond)
	if d := e.s.Downloader(); d.Failed || d.Updating || e.inst.count() != 1 {
		t.Fatalf("after a retry with the queue paused: %+v, %d installs", d, e.inst.count())
	}
}

func TestAnInstallWithoutAVersionGetsOne(t *testing.T) {
	e := newEnv(t)
	e.s.Close()
	e.inst.mu.Lock()
	e.inst.installed = true
	e.inst.mu.Unlock()
	e.start(t)
	eventually(t, "the version", func() bool { return e.s.Downloader().Version == "2026.10.11" })
	if n := e.inst.count(); n != 0 {
		t.Fatalf("%d installs", n)
	}
}

func TestRemove(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "hold")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2)})
	eventually(t, "v1 under way", e.is("v1", media.Downloading))
	part := filepath.Join(e.f.parts, "v1.webm.part")
	os.WriteFile(part, []byte("half"), 0o644)
	e.s.Remove("v1")
	if st, _ := e.s.State("v1"); st != media.Available {
		t.Fatalf("removed, yet %v", st)
	}
	eventually(t, "v2 saved", e.is("v2", media.Saved))
	eventually(t, "the partial file gone", func() bool {
		_, err := os.Stat(part)
		return os.IsNotExist(err)
	})
	if o := e.s.Outcomes(); len(o) != 2 || o[0] != (Outcome{VideoID: "v1", Removed: true}) || o[1].Path == "" {
		t.Fatalf("outcomes %+v", o)
	}
	// Removing a saved song forgets it but keeps the file in the library.
	p, _ := e.s.Path("v2")
	e.s.Remove("v2")
	eventually(t, "the rows deleted", func() bool { rows, _ := loadRows(e.db); return len(rows) == 0 })
	if _, err := os.Stat(p); err != nil {
		t.Fatal("a saved song's file went with it")
	}
}

func TestStatus(t *testing.T) {
	e := newEnv(t)
	yesterday := time.Now().Add(-36 * time.Hour).UnixMilli()
	saveRows(e.db, []row{
		{song: song("old", "Old", 1), state: saved, path: "/m/old.opus", bytes: 3_000_000, finished: yesterday},
		{song: song("bad", "Bad", 2), state: failed, failure: Network, added: 1},
	})
	e.s.Close()
	e.start(t)
	eventually(t, "the storage read", func() bool { return e.s.Status().Storage.Total != 0 })
	e.s.SetPaused(true)
	e.s.Get([]ytmusic.Song{song("q1", "First queued", 3), song("q2", "Second queued", 4)})
	e.s.First("q2")
	st := e.s.Status()
	var got []string
	for _, en := range st.Entries {
		got = append(got, en.Song.VideoID)
	}
	if !slices.Equal(got, []string{"q2", "q1", "bad"}) {
		t.Fatalf("entries %q", got)
	}
	if !st.Paused || st.Storage.Free != 32<<30 || st.Storage.Total != 64<<30 {
		t.Fatalf("status %+v", st)
	}
	if again := e.s.Status(); again.Version != st.Version {
		t.Fatal("unchanged, yet rebuilt")
	}
	e.s.SetPaused(false)
	eventually(t, "both saved", func() bool { return e.is("q1", media.Saved)() && e.is("q2", media.Saved)() })
	st = e.s.Status()
	if n := len(st.Entries); n != 3 || st.Entries[2].State != media.Saved || st.Entries[2].Bytes == 0 {
		t.Fatalf("entries %+v", st.Entries)
	}
}

// Songs saved before today are on no list, so only their files are kept. They
// still answer State and Path, are left be by Get, and can be removed.
func TestOlderSavedSongsKeepOnlyTheirFiles(t *testing.T) {
	e := newEnv(t)
	e.s.Close()
	saveRows(e.db, []row{
		{song: song("old", "Old", 1), state: saved, path: "/m/old.opus", finished: time.Now().Add(-36 * time.Hour).UnixMilli()},
		{song: song("new", "New", 2), state: saved, path: "/m/new.opus", finished: time.Now().UnixMilli()},
	})
	e.start(t)
	e.s.mu.Lock()
	_, full := e.s.rows["old"]
	e.s.mu.Unlock()
	if full {
		t.Fatal("an older saved song is kept in full")
	}
	e.s.Get([]ytmusic.Song{song("old", "Old", 1)})
	if st, _ := e.s.State("old"); st != media.Saved {
		t.Fatalf("asked for again, it is %v", st)
	}
	if p, ok := e.s.Path("old"); !ok || p != "/m/old.opus" {
		t.Fatalf("Path = %q, %v", p, ok)
	}
	if en := e.s.Status().Entries; len(en) != 1 || en[0].Song.VideoID != "new" {
		t.Fatalf("entries %+v", en)
	}
	e.s.Remove("old")
	if st, _ := e.s.State("old"); st != media.Available {
		t.Fatalf("removed, yet %v", st)
	}
	if o := e.s.Outcomes(); len(o) != 1 || o[0] != (Outcome{VideoID: "old", Removed: true}) {
		t.Fatalf("outcomes %+v", o)
	}
	eventually(t, "its row deleted", func() bool { rows, _ := loadRows(e.db); return rows["old"] == nil && rows["new"] != nil })
}

// slowInstall makes the first install wait until the returned func is called,
// and returns a channel that says when it has begun.
func (e *env) slowInstall() (begun <-chan struct{}, release func()) {
	block, entered := make(chan struct{}), make(chan struct{}, 4)
	e.inst.mu.Lock()
	e.inst.block, e.inst.entered = block, entered
	e.inst.mu.Unlock()
	return entered, func() { close(block) }
}

func wait(t *testing.T, c <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// files is the number of files filed in the Music folder.
func (e *env) files() int {
	n := 0
	filepath.WalkDir(e.music, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func TestPausingDuringTheInstall(t *testing.T) {
	e := newEnv(t)
	begun, release := e.slowInstall()
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2)})
	wait(t, begun, "the install")
	e.s.SetPaused(true)
	release()
	eventually(t, "yt-dlp installed", func() bool { return e.inst.Program() != "" })
	// Unpausing is what lets a batch begin; one that began before is caught.
	e.s.SetPaused(false)
	eventually(t, "both saved", func() bool { return e.is("v1", media.Saved)() && e.is("v2", media.Saved)() })
	if n := e.f.early(); n != 0 {
		t.Fatalf("%d batches began while paused", n)
	}
}

func TestFirstDuringTheInstall(t *testing.T) {
	e := newEnv(t)
	begun, release := e.slowInstall()
	e.s.Get([]ytmusic.Song{song("a", "a", 1), song("b", "b", 2), song("c", "c", 3)})
	wait(t, begun, "the install")
	e.s.First("c")
	release()
	eventually(t, "all saved", func() bool { return e.is("a", media.Saved)() && e.is("c", media.Saved)() && e.is("b", media.Saved)() })
	if runs := e.f.runs(); !slices.Equal(runs[0], []string{"c", "a", "b"}) {
		t.Fatalf("batches %q", runs)
	}
}

func TestRemoveDuringTheInstall(t *testing.T) {
	e := newEnv(t)
	begun, release := e.slowInstall()
	e.s.Get([]ytmusic.Song{song("a", "a", 1), song("b", "b", 2)})
	wait(t, begun, "the install")
	e.s.Remove("a")
	release()
	eventually(t, "b saved", e.is("b", media.Saved))
	if runs := e.f.runs(); !slices.Equal(runs[0], []string{"b"}) {
		t.Fatalf("batches %q", runs)
	}
	if _, err := os.Stat(filepath.Join(e.f.parts, "a.webm")); !os.IsNotExist(err) {
		t.Fatal("the song removed was downloaded")
	}
}

func TestCallsAfterClose(t *testing.T) {
	e := newEnv(t)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "v1 saved", e.is("v1", media.Saved))
	e.s.Close()
	e.s.Get([]ytmusic.Song{song("v2", "b", 2)})
	e.s.First("v2")
	e.s.Retry("v1", true)
	e.s.Remove("v1")
	e.s.SetPaused(true)
	e.s.Status()
	e.s.Outcomes()
	if _, err := e.s.Search(context.Background(), "night drive"); err == nil {
		t.Fatal("searched after Close")
	}
	select {
	case <-e.s.Ready():
	case <-time.After(time.Second):
		t.Fatal("Ready is not closed")
	}
	if st, _ := e.s.State("v1"); st != media.Saved {
		t.Fatalf("a call after Close changed v1 to %v", st)
	}
}

func TestRemoveWhileFinishing(t *testing.T) {
	e := newEnv(t)
	e.gated.Store(true)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	wait(t, e.entered, "the remux")
	e.s.Remove("v1")
	src := filepath.Join(e.f.parts, "v1.webm")
	if _, err := os.Stat(src); err != nil {
		t.Fatal("Remove deleted the file being finished")
	}
	close(e.release)
	eventually(t, "the song gone", func() bool {
		_, err := os.Stat(src)
		return os.IsNotExist(err) && e.files() == 0
	})
	if o := e.s.Outcomes(); len(o) != 1 || o[0] != (Outcome{VideoID: "v1", Removed: true}) {
		t.Fatalf("outcomes %+v", o)
	}
	if st, _ := e.s.State("v1"); st != media.Available {
		t.Fatalf("v1 is %v", st)
	}
}

func TestRemoveThenGetWhileFinishing(t *testing.T) {
	e := newEnv(t)
	e.gated.Store(true)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	wait(t, e.entered, "the remux")
	e.s.Remove("v1")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	close(e.release)
	eventually(t, "the new row saved", e.is("v1", media.Saved))
	if p, _ := e.s.Path("v1"); filepath.Base(p) != "01 a.opus" || e.files() != 1 {
		t.Fatalf("the song is at %q, with %d files", p, e.files())
	}
	if o := e.s.Outcomes(); len(o) != 2 || !o[0].Removed || o[1].Path == "" {
		t.Fatalf("outcomes %+v", o)
	}
}

func TestCloseLeavesLandedSongsQueued(t *testing.T) {
	e := newEnv(t)
	e.gated.Store(true)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2)})
	wait(t, e.entered, "the remux")
	eventually(t, "v2 landed", func() bool {
		e.s.mu.Lock()
		defer e.s.mu.Unlock()
		return e.s.rows["v2"].landed
	})
	e.s.Close()
	rows, err := loadRows(e.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"v1", "v2"} {
		if r := rows[id]; r == nil || r.state != queued || r.failure != NoFailure {
			t.Fatalf("%s after Close: %+v", id, r)
		}
	}
	if o := e.s.Outcomes(); len(o) != 0 {
		t.Fatalf("outcomes %+v", o)
	}
}

func TestLosingWiFiBetweenSongs(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"v1", "v2", "v3"} {
		e.f.set(id, "net")
	}
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2), song("v3", "c", 3)})
	eventually(t, "waiting for Wi-Fi", func() bool { return e.s.Status().Waiting == waitOffline })
	for _, id := range []string{"v1", "v2", "v3"} {
		if st, _ := e.s.State(id); st != media.Queued {
			t.Fatalf("%s is %v", id, st)
		}
	}
	if o := e.s.Outcomes(); len(o) != 0 {
		t.Fatalf("outcomes %+v", o)
	}
	for _, id := range []string{"v1", "v2", "v3"} {
		e.f.set(id, "ok")
	}
	e.online.Store(true)
	eventually(t, "all saved", func() bool {
		return e.is("v1", media.Saved)() && e.is("v2", media.Saved)() && e.is("v3", media.Saved)()
	})
}

func TestAFailedUpdateKeepsDownloading(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "gone")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "the failure", e.is("v1", media.Failed))
	e.inst.mu.Lock()
	e.inst.fail = errors.New("offline")
	e.inst.mu.Unlock()
	e.f.set("v1", "ok")
	e.s.Retry("v1", true)
	eventually(t, "v1 saved", e.is("v1", media.Saved))
	if w := e.s.Status().Waiting; w != "" {
		t.Fatalf("waiting: %q", w)
	}
	// The update is not asked for again.
	e.s.Get([]ytmusic.Song{song("v2", "b", 2)})
	eventually(t, "v2 saved", e.is("v2", media.Saved))
	if n := e.inst.count(); n != 2 {
		t.Fatalf("installed %d times", n)
	}
}

func TestLeadingIsTheNextSongNotDoneWith(t *testing.T) {
	e := newEnv(t)
	e.s.SetPaused(true)
	e.s.Get([]ytmusic.Song{song("a", "a", 1), song("b", "b", 2), song("c", "c", 3)})
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	// a failed in this batch and was asked for again: the batch is past it.
	e.s.batch, e.s.settled = []string{"a", "b", "c"}, map[string]bool{"a": true}
	defer func() { e.s.batch, e.s.settled = nil, nil }()
	if e.s.leadsLocked("a") || !e.s.leadsLocked("b") || e.s.leadsLocked("c") {
		t.Fatal("the song to lead is b")
	}
}

func TestOrderNeverRepeatsOrGoesBack(t *testing.T) {
	e := newEnv(t)
	later := time.Now().Add(time.Hour).UnixMilli() // a clock that has since gone back
	saveRows(e.db, []row{{song: song("old", "Old", 1), state: failed, failure: Network, added: later, priority: later + 5}})
	e.s.Close()
	e.start(t)
	e.s.SetPaused(true)
	e.s.Get([]ytmusic.Song{song("a", "a", 1), song("b", "b", 2)})
	e.s.First("a")
	e.s.mu.Lock()
	a, b := *e.s.rows["a"], *e.s.rows["b"]
	e.s.mu.Unlock()
	if a.added <= later+5 || b.added <= a.added || a.priority <= later+5 {
		t.Fatalf("added %d, %d; priority %d, after %d", a.added, b.added, a.priority, later+5)
	}
}

func TestAPanicInABatchFailsItsLeadSong(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "panic")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2)})
	eventually(t, "both settled", func() bool { return e.is("v1", media.Failed)() && e.is("v2", media.Saved)() })
	if o := e.s.Outcomes(); len(o) != 2 || o[0].VideoID != "v1" || o[0].Failure != Other {
		t.Fatalf("outcomes %+v", o)
	}
}

func TestAPanicThatKeepsComingDoesNotRetryForever(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "panic")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "v1 failed", e.is("v1", media.Failed))
	time.Sleep(100 * time.Millisecond) // ten rests
	if n := len(e.f.runs()); n > 3 {
		t.Fatalf("%d batches", n)
	}
}

func TestAPanicWhileFilingIsOneFailure(t *testing.T) {
	e := newEnv(t)
	e.s.fin.remux = func(string, string, remux.Tags) error { panic("the remux gave up") }
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "v1 failed", e.is("v1", media.Failed))
	if o := e.s.Outcomes(); len(o) != 1 || o[0].Failure != Other {
		t.Fatalf("outcomes %+v", o)
	}
}

func TestRemoveWhileTheFinisherWrites(t *testing.T) {
	e := newEnv(t)
	e.gated.Store(true)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	wait(t, e.entered, "the remux")
	e.s.write.Lock() // the finisher's write waits here
	close(e.release)
	eventually(t, "v1 saved in memory", e.is("v1", media.Saved))
	done := make(chan struct{})
	go func() { e.s.Remove("v1"); close(done) }()
	e.s.write.Unlock()
	wait(t, done, "Remove")
	e.s.Close() // the finisher is done with its write
	if rows, _ := loadRows(e.db); len(rows) != 0 {
		t.Fatalf("%d rows came back", len(rows))
	}
}

func TestSearchNeedsTheDownloader(t *testing.T) {
	e := newEnv(t)
	if _, err := e.s.Search(context.Background(), "night drive"); err == nil {
		t.Fatal("searched without yt-dlp")
	}
}

// yt-dlp runs one at a time: a search stops the batch under way, which starts
// again after it.
func TestSearchStopsTheBatch(t *testing.T) {
	e := newEnv(t)
	e.f.set("v1", "hold")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2)})
	eventually(t, "v1 under way", e.is("v1", media.Downloading))
	e.f.set("v1", "ok")
	items, err := e.s.Search(context.Background(), "night drive")
	if err != nil || len(items) != 1 || items[0].Title != "night drive" {
		t.Fatalf("search: %+v, %v", items, err)
	}
	eventually(t, "both saved", func() bool { return e.is("v1", media.Saved)() && e.is("v2", media.Saved)() })
	if _, n := e.f.busy(); n != 0 {
		t.Fatalf("a search and a batch ran at once %d times", n)
	}
	if runs := e.f.runs(); len(runs) != 2 || !slices.Equal(runs[1], []string{"v1", "v2"}) {
		t.Fatalf("batches %q", runs)
	}
}

// A batch waits for a search under way.
func TestABatchWaitsForASearch(t *testing.T) {
	e := newEnv(t)
	e.inst.mu.Lock()
	e.inst.installed = true
	e.inst.mu.Unlock()
	e.f.gate = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := e.s.Search(context.Background(), "night drive")
		done <- err
	}()
	eventually(t, "the search under way", func() bool { on, _ := e.f.busy(); return on })
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	time.Sleep(50 * time.Millisecond)
	if n := len(e.f.runs()); n != 0 {
		t.Fatalf("%d batches began during the search", n)
	}
	close(e.f.gate)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the search")
	}
	eventually(t, "v1 saved", e.is("v1", media.Saved))
	if _, n := e.f.busy(); n != 0 {
		t.Fatalf("a search and a batch ran at once %d times", n)
	}
}

// A search given up while it waits for yt-dlp says so.
func TestASearchGivenUpWhileItWaits(t *testing.T) {
	e := newEnv(t)
	e.inst.mu.Lock()
	e.inst.installed = true
	e.inst.mu.Unlock()
	e.s.tool <- struct{}{} // yt-dlp is busy
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.s.Search(ctx, "night drive"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	<-e.s.tool
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	eventually(t, "v1 saved", e.is("v1", media.Saved))
}

func TestStatusIsThrottledButShowsLandingsAndFailures(t *testing.T) {
	e := newEnv(t)
	e.gated.Store(true) // a landed file stays unfinished
	e.s.SetPaused(true)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2), song("v3", "c", 3)})
	e.s.event(Event{ID: "v1", Done: 10, Total: 100}) // the first progress shows
	first := e.s.Status()
	if first.Entries[0].State != media.Downloading {
		t.Fatalf("entries %+v", first.Entries)
	}
	for i := int64(20); i < 25; i++ {
		e.s.event(Event{ID: "v1", Done: i, Total: 100})
	}
	if e.s.Status().Version != first.Version {
		t.Fatal("progress rebuilt the status every time")
	}
	if _, p := e.s.State("v1"); p != 0.24 {
		t.Fatalf("State has progress %v", p)
	}
	e.s.event(Event{ID: "v2", Failure: Unavailable, Message: "gone"})
	st := e.s.Status()
	if st.Version == first.Version || !slices.ContainsFunc(st.Entries, func(en Entry) bool {
		return en.Song.VideoID == "v2" && en.State == media.Failed
	}) {
		t.Fatalf("a failure is not shown: %+v", st.Entries)
	}
	e.s.event(Event{ID: "v3", Path: filepath.Join(e.f.parts, "v3.webm")})
	again := e.s.Status()
	if again.Version == st.Version || !slices.ContainsFunc(again.Entries, func(en Entry) bool {
		return en.Song.VideoID == "v3" && en.State == media.Downloading && en.Progress == 1
	}) {
		t.Fatalf("a landing is not shown: %+v", again.Entries)
	}
}

// The queue's calls change memory only, for a goroutine of the service's to
// write: a write held up behind another, as a scan's, doesn't hold them up.
func TestCallsDontWaitForTheDatabase(t *testing.T) {
	e := newEnv(t)
	e.s.Close()
	saveRows(e.db, []row{{song: song("bad", "Bad", 3), state: failed, failure: Network, added: 1}})
	e.start(t)
	e.s.SetPaused(true)
	tx, err := e.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE downloads SET message = 'held' WHERE video_id = 'bad'`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2)})
		e.s.First("v2")
		e.s.Retry("bad", false)
		e.s.Remove("v1")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the calls waited for the database")
	}
	tx.Rollback()
	eventually(t, "the rows written", func() bool {
		rows, _ := loadRows(e.db)
		return len(rows) == 2 && rows["v2"] != nil && rows["v2"].priority != 0 && rows["bad"] != nil &&
			rows["bad"].state == queued
	})
}

// A song played right after it was removed, while its partial files wait to
// be deleted, goes first once they are, rather than after the batch that
// started without it.
func TestARemovedSongPlayedAgainGoesFirst(t *testing.T) {
	e := newEnv(t)
	e.f.set("a", "hold")
	e.f.set("b", "hold")
	e.s.Get([]ytmusic.Song{song("a", "x", 1), song("b", "x", 2)})
	e.s.First("a")
	eventually(t, "a downloading", func() bool { return e.leads("a") })
	e.s.write.Lock() // the writer can't delete a's partial files yet
	e.s.Remove("a")
	e.s.Get([]ytmusic.Song{song("a", "x", 1)})
	e.s.First("a")
	eventually(t, "a batch without a", func() bool { return e.leads("b") })
	e.s.write.Unlock()
	eventually(t, "a batch led by a", func() bool { return e.leads("a") })
}

// leads reports whether the last batch started began with id.
func (e *env) leads(id string) bool {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	n := len(e.f.batches)
	return n > 0 && len(e.f.batches[n-1]) > 0 && e.f.batches[n-1][0] == id
}

// A write that fails, as on a card that is full for a moment, is tried again
// by itself, not only with the queue's next change.
func TestFailedWritesAreTriedAgain(t *testing.T) {
	e := newEnv(t)
	e.s.writeWait = 20 * time.Millisecond
	if _, err := e.db.Exec(`CREATE TABLE full (x); INSERT INTO full VALUES (1);
		CREATE TRIGGER full_insert BEFORE INSERT ON downloads WHEN EXISTS (SELECT 1 FROM full)
		BEGIN SELECT RAISE(ABORT, 'disk full'); END`); err != nil {
		t.Fatal(err)
	}
	e.f.set("a", "hold")
	e.s.Get([]ytmusic.Song{song("a", "x", 1)})
	time.Sleep(60 * time.Millisecond) // writes fail meanwhile, and are tried again
	if rows, err := loadRows(e.db); err != nil || len(rows) != 0 {
		t.Fatalf("rows %v, %v while writes fail", rows, err)
	}
	if _, err := e.db.Exec(`DELETE FROM full`); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the row written", func() bool {
		rows, err := loadRows(e.db)
		return err == nil && rows["a"] != nil
	})
}

// Close writes what the queue's calls changed and wasn't written yet.
func TestCloseWritesWhatIsPending(t *testing.T) {
	e := newEnv(t)
	for i := range 20 {
		id := fmt.Sprintf("s%d", i)
		e.f.set(id, "hold")
		e.s.Get([]ytmusic.Song{song(id, "a", 1)})
		e.s.First(id)
		e.s.Remove(fmt.Sprintf("s%d", i-1))
		e.s.Close()
		rows, err := loadRows(e.db)
		if err != nil || len(rows) != 1 || rows[id] == nil || rows[id].priority == 0 {
			t.Fatalf("round %d: rows %v, %v", i, rows, err)
		}
		e.start(t)
	}
}

// Installing yt-dlp reports its progress a few times a second at most, as
// downloads do.
func TestInstallProgressIsThrottled(t *testing.T) {
	e := newEnv(t)
	e.s.SetPaused(true)
	e.inst.mu.Lock()
	e.inst.steps = 1000
	e.inst.mu.Unlock()
	before := e.s.Status().Version
	if err := e.s.installYtdlp(); err != nil {
		t.Fatal(err)
	}
	if st := e.s.Status(); st.Version-before > 5 || st.Installing {
		t.Fatalf("the status changed %d times", st.Version-before)
	}
}

// Saved songs whose files are gone from the library, as when they were
// deleted on a PC, are forgotten, so they can be downloaded again. A file
// still there, though not indexed, is kept.
func TestPruneForgetsSongsWhoseFilesAreGone(t *testing.T) {
	e := newEnv(t)
	e.s.Close()
	os.MkdirAll(e.music, 0o755)
	kept, unindexed := filepath.Join(e.music, "kept.opus"), filepath.Join(e.music, "unindexed.opus")
	os.WriteFile(kept, []byte("opus"), 0o644)
	os.WriteFile(unindexed, []byte("opus"), 0o644)
	saveRows(e.db, []row{
		{song: song("kept", "Kept", 1), state: saved, path: kept, finished: 1},
		{song: song("lost", "Lost", 2), state: saved, path: filepath.Join(e.music, "lost.opus"), finished: 2},
		{song: song("unindexed", "Unindexed", 3), state: saved, path: unindexed, finished: 3},
		{song: song("q1", "Queued", 4), state: queued, added: 1},
		{song: song("f1", "Failed", 5), state: failed, failure: Network, added: 2},
	})
	if _, err := e.db.Exec(`INSERT INTO tracks (path, size, mtime, artist, album, title, number, added)
		VALUES (?, 4, 0, '', '', '', 0, 0)`, kept); err != nil {
		t.Fatal(err)
	}
	e.f.set("q1", "hold")
	e.start(t)
	before := e.s.Status().Version
	e.s.Prune()
	eventually(t, "the lost song forgotten", e.is("lost", media.Available))
	if _, ok := e.s.Path("lost"); ok || e.s.Status().Version == before {
		t.Fatal("still saved, or the status says nothing")
	}
	for id, want := range map[string]media.DownloadState{"kept": media.Saved, "unindexed": media.Saved, "f1": media.Failed} {
		if st, _ := e.s.State(id); st != want {
			t.Fatalf("%s is %v", id, st)
		}
	}
	if st, _ := e.s.State("q1"); st == media.Available {
		t.Fatal("the queued song was forgotten")
	}
	rows, _ := loadRows(e.db)
	if len(rows) != 4 || rows["lost"] != nil {
		t.Fatalf("rows %v", rows)
	}
}

// The SD card's numbers count the library as a scan leaves it. The first
// scan runs alongside the service's start, which measures the library before
// the scan has filled it, so Prune, called as each scan ends, measures it
// again.
func TestPruneMeasuresTheLibraryAgain(t *testing.T) {
	e := newEnv(t)
	eventually(t, "the card measured", func() bool { return e.s.Status().Storage.Total > 0 })
	if got := e.s.Status().Storage.Music; got != 0 {
		t.Fatalf("an empty library counts %d bytes", got)
	}
	if _, err := e.db.Exec(`INSERT INTO tracks (path, size, mtime, artist, album, title, number, added)
		VALUES (?, 5000, 0, '', '', '', 0, 0)`, filepath.Join(e.music, "scanned.flac")); err != nil {
		t.Fatal(err)
	}
	e.s.Prune()
	eventually(t, "the scanned song counted", func() bool { return e.s.Status().Storage.Music == 5000 })
}

// With Wi-Fi up but no internet, a song fails as Network only on its third
// try in a row: each failure stops the batch, keeping the rest queued, and
// the queue rests before trying again.
func TestNoInternetFailsASongOnItsThirdTry(t *testing.T) {
	e := newEnv(t)
	e.s.Close()
	e.stopWait = 100 * time.Millisecond
	e.start(t)
	e.f.set("v1", "down")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1), song("v2", "b", 2)})
	eventually(t, "the rest", func() bool { return e.s.Status().Waiting == waitNetwork })
	if n := e.s.Note(); n != "Couldn’t connect" {
		t.Fatalf("resting, Now Playing says %q", n)
	}
	if st, _ := e.s.State("v1"); st != media.Queued || len(e.f.runs()) != 1 || len(e.s.Outcomes()) != 0 {
		t.Fatalf("after one failure v1 is %v, with %d batches", st, len(e.f.runs()))
	}
	eventually(t, "v2 saved", e.is("v2", media.Saved))
	if st, _ := e.s.State("v1"); st != media.Failed {
		t.Fatalf("v1 is %v", st)
	}
	runs := e.f.runs()
	if len(runs) != 4 || !slices.Equal(runs[3], []string{"v2"}) {
		t.Fatalf("batches %q", runs)
	}
	for _, r := range runs[:3] {
		if !slices.Equal(r, []string{"v1", "v2"}) {
			t.Fatalf("batches %q", runs)
		}
	}
	if o := e.s.Outcomes(); len(o) != 2 || o[0].VideoID != "v1" || o[0].Failure != Network {
		t.Fatalf("outcomes %+v", o)
	}
}

// A crash can leave the partial files of songs no longer queued, and remuxes
// cut short. The next start deletes them, and nothing else.
func TestLeftoversGoAtStart(t *testing.T) {
	e := newEnv(t)
	e.s.Close()
	saveRows(e.db, []row{
		{song: song("q1", "Queued", 1), state: queued, added: 1},
		{song: song("f1", "Failed", 2), state: failed, failure: Network, added: 2},
		{song: song("s1", "Saved", 3), state: saved, path: "/m/s1.opus", finished: 3},
	})
	album := filepath.Join(e.music, "Boombrick", "Lumen Coast", "Night Drive Theory")
	other := filepath.Join(e.music, "Odalys")
	os.MkdirAll(album, 0o755)
	os.MkdirAll(other, 0o755)
	stays := map[string]bool{
		filepath.Join(e.f.parts, "q1.251.webm.part"):    true,
		filepath.Join(e.f.parts, "f1.250.webm.part"):    true,
		filepath.Join(e.f.parts, "f1.251.webm"):         true,
		filepath.Join(e.f.parts, "s1.251.webm.part"):    false,
		filepath.Join(e.f.parts, "gone1.251.webm.part"): false,
		filepath.Join(e.f.parts, "gone2.251.webm"):      false,
		filepath.Join(album, "01 a.opus.part"):          false,
		filepath.Join(album, "02 b.opus"):               true,
		filepath.Join(album, "cover.jpg.part"):          true,
		filepath.Join(other, "03 c.opus.part"):          true,
	}
	for p := range stays {
		os.WriteFile(p, []byte("x"), 0o644)
	}
	e.f.set("q1", "hold")
	e.start(t)
	there := func(p string) bool { _, err := os.Stat(p); return err == nil }
	eventually(t, "the leftovers gone", func() bool {
		for p, stay := range stays {
			if !stay && there(p) {
				return false
			}
		}
		return true
	})
	for p, stay := range stays {
		if stay && !there(p) {
			t.Fatalf("%s went", p)
		}
	}
}

// panicky is a log that panics on msg, once.
type panicky struct {
	msg  string
	done atomic.Bool
}

func (h *panicky) Enabled(context.Context, slog.Level) bool { return true }
func (h *panicky) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *panicky) WithGroup(string) slog.Handler            { return h }
func (h *panicky) Handle(_ context.Context, r slog.Record) error {
	if r.Message == h.msg && h.done.CompareAndSwap(false, true) {
		panic("the log gave up")
	}
	return nil
}

// A panic while a song is settled lets go of it: a song asked for again while
// it was finished goes on.
func TestAPanicWhileSettlingLetsTheSongGo(t *testing.T) {
	e := newEnv(t)
	e.s.Close()
	e.log = slog.New(&panicky{msg: "finish a download"})
	e.start(t)
	e.gated.Store(true)
	e.failRemux.Store(true)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	wait(t, e.entered, "the remux")
	e.s.Remove("v1")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)}) // waits for the file being finished
	close(e.release)
	eventually(t, "v1 saved", e.is("v1", media.Saved))
}

// Now Playing says in a few words why a song it waits on isn't downloading.
func TestNote(t *testing.T) {
	e := newEnv(t)
	begun, release := e.slowInstall()
	e.f.set("v1", "hold")
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	wait(t, begun, "the install")
	if n := e.s.Note(); n != "Getting the downloader" {
		t.Fatalf("installing: %q", n)
	}
	e.s.SetPaused(true)
	if n := e.s.Note(); n != "Downloads paused" {
		t.Fatalf("paused: %q", n)
	}
	release()
	eventually(t, "yt-dlp installed", func() bool { return e.inst.Program() != "" })
	e.online.Store(false)
	e.s.SetPaused(false)
	eventually(t, "waiting for Wi-Fi", func() bool { return e.s.Note() == "Waiting for Wi-Fi" })
	e.free.Store(100 << 20)
	e.online.Store(true)
	eventually(t, "the card full", func() bool { return e.s.Note() == "Card full" })
	e.free.Store(32 << 30)
	eventually(t, "v1 under way", e.is("v1", media.Downloading))
	if n := e.s.Note(); n != "" {
		t.Fatalf("downloading: %q", n)
	}
}

// Busy says while a batch downloads or a song is being filed.
func TestBusy(t *testing.T) {
	e := newEnv(t)
	if e.s.Busy() {
		t.Fatal("busy with nothing to do")
	}
	e.gated.Store(true)
	e.s.Get([]ytmusic.Song{song("v1", "a", 1)})
	wait(t, e.entered, "the remux")
	if !e.s.Busy() {
		t.Fatal("not busy while a song is filed")
	}
	close(e.release)
	eventually(t, "v1 saved", e.is("v1", media.Saved))
	eventually(t, "idle", func() bool { return !e.s.Busy() })
}

// A saved song removed keeps its file in the library. Asked for again, its
// download replaces that file; removed again while it is filed, the file and
// its track stay, as they did after the first Remove.
func TestRemovedWhileReplacingItsOwnFileKeepsIt(t *testing.T) {
	tone := filepath.Join("..", "remux", "testdata", "tone.webm")
	if _, err := os.Stat(tone); err != nil {
		t.Skipf("%s is missing; run make fixtures", tone)
	}
	e := newEnv(t)
	sg := song("v1", "a", 1)
	p := place(e.music, sg)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := remux.File(tone, p, remux.Tags{Title: sg.Title, Extra: [][2]string{{youTubeIDTag, "v1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := library.Add(e.db, e.music, p, e.s.fin.formats); err != nil {
		t.Fatal(err)
	}
	e.gated.Store(true)
	e.s.Get([]ytmusic.Song{sg})
	wait(t, e.entered, "the remux")
	e.s.Remove("v1")
	close(e.release)
	eventually(t, "the song settled", func() bool { return !e.s.Busy() })
	e.s.Close() // waits for the settling to end
	if _, err := os.Stat(p); err != nil || e.files() != 1 {
		t.Fatalf("the song's file: %v; %d files", err, e.files())
	}
	var n int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM tracks WHERE path = ?`, p).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d tracks for the file, %v", n, err)
	}
}

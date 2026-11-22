package importer

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/store"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// fakeYT answers searches from its map and records them; err, when set,
// answers every one.
type fakeYT struct {
	mu      sync.Mutex
	answers map[string][]ytmusic.Item
	err     error
	asked   []string
}

func (f *fakeYT) search(_ context.Context, q string, _ ytmusic.Filter) ([]ytmusic.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, q)
	return f.answers[q], f.err
}

func (f *fakeYT) queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

func (f *fakeYT) set(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

// queue is the download queue as the importer sees it: Get files a row in
// the downloads table, as the real one does, and records the songs.
type queue struct {
	db      *sql.DB
	mu      sync.Mutex
	got     []ytmusic.Song
	removed []string
	saved   map[string]bool
}

func (q *queue) get(songs []ytmusic.Song) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, s := range songs {
		q.got = append(q.got, s)
		q.db.Exec(`INSERT INTO downloads (video_id, title, artist, album, album_id, album_artist, number, year,
			duration_ms, thumb, state, failure, message, path, bytes, added, priority, finished, spotify_id, isrc)
			VALUES (?, ?, '', '', '', '', 0, 0, 0, '', 0, 0, '', '', 0, 0, 0, 0, ?, ?)`, s.VideoID, s.Title, s.SpotifyID, s.ISRC)
	}
}

func (q *queue) state(id string) (media.DownloadState, float32) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.saved[id] {
		return media.Saved, 1
	}
	return media.Queued, 0
}

func (q *queue) remove(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.removed = append(q.removed, id)
	q.db.Exec(`DELETE FROM downloads WHERE video_id = ?`, id)
}

func (q *queue) songs() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []string
	for _, s := range q.got {
		out = append(out, s.VideoID+":"+s.SpotifyID+":"+s.ISRC)
	}
	return out
}

func openDB(t testing.TB) *sql.DB {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "boombrick.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.DB
}

// bare is an importer on db that matches nothing itself: the test settles
// its songs, and flushes.
func bare(db *sql.DB) *Importer {
	return &Importer{o: Options{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, now: time.Now}
}

// written counts the songs' rows written from now on; each call says how many
// were since the last.
func written(t testing.TB, db *sql.DB) func() int {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE writes (n INTEGER NOT NULL)`,
		`INSERT INTO writes VALUES (0)`,
		`CREATE TRIGGER inserted AFTER INSERT ON import_items BEGIN UPDATE writes SET n = n + 1; END`,
		`CREATE TRIGGER updated AFTER UPDATE ON import_items BEGIN UPDATE writes SET n = n + 1; END`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT n FROM writes`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE writes SET n = 0`); err != nil {
			t.Fatal(err)
		}
		return n
	}
}

// start makes an importer on db, asking yt, with the waits shortened.
func start(t *testing.T, db *sql.DB, yt *fakeYT, online *atomic.Bool) (*Importer, *queue) {
	t.Helper()
	q := &queue{db: db, saved: map[string]bool{}}
	if online == nil {
		online = &atomic.Bool{}
		online.Store(true)
	}
	im, err := New(Options{DB: db, Search: yt.search, Get: q.get, State: q.state, Remove: q.remove, Online: online.Load,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Pause: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	im.waits = [2]time.Duration{5 * time.Millisecond, 5 * time.Millisecond}
	t.Cleanup(im.Close)
	return im, q
}

// settled waits until no song of import uri waits to be matched.
func settled(t *testing.T, im *Importer, uri string) Job {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(time.Millisecond) {
		if j, ok := im.Job(uri); ok && j.Count(Waiting) == 0 {
			return j
		}
		if time.Now().After(deadline) {
			t.Fatal("still matching")
		}
	}
}

func states(j Job) []State {
	var out []State
	for _, it := range j.Items {
		out = append(out, it.State)
	}
	return out
}

const road = "spotify:playlist:5ABHKGoOzxkaa28ttQV9sE"

// What YouTube Music answers: Sapphire's ISRC finds nothing like it, its
// artist and title find it; Terey papa's ISRC finds it; Stan is found only 8 s
// longer; nothing is found for the last.
func answers() map[string][]ytmusic.Item {
	return map[string][]ytmusic.Item{
		"GBAHS2500262":        {song("e", "BBpanzu", "Bang Bang Bang", "", dur(3, 1))},
		"Ed Sheeran Sapphire": {song("a", "Ed Sheeran", "Sapphire", "Sapphire", dur(3, 0)), song("c", "Pianofy", "Sapphire", "A Piano Tribute to Ed Sheeran", dur(2, 53))},
		"ES71G2502682":        {song("i", "OG Lucifer & Calm", "Terey papa", "", dur(3, 5))},
		"Eminem Stan":         {song("l", "Eminem", "Stan", "The Marshall Mathers LP", dur(6, 52)), song("k", "Eminem", "Stan (Live)", "", dur(6, 50))},
	}
}

var nowhere = spotify.Track{ID: "0nowhere0000000000000", Title: "Nowhere", Artists: []string{"Nobody"}, Duration: 200000}

func TestImportMatchesQueuesAndAsks(t *testing.T) {
	yt := &fakeYT{answers: answers()}
	im, q := start(t, openDB(t), yt, nil)
	im.Import(road, "Road trip", []spotify.Track{sapphire, tereyPapa, stan, nowhere, sapphire})
	j := settled(t, im, road)
	if got := states(j); !slices.Equal(got, []State{Matched, Matched, Unsure, NotFound}) {
		t.Fatalf("states %v", got)
	}
	if got := q.songs(); !slices.Equal(got, []string{"a:4Q0qVhFQa7j6jRKzo3HDmP:GBAHS2500262", "i:7vIXP7osY1jcyIpX9hCeLt:ES71G2502682"}) {
		t.Fatalf("queued %v", got)
	}
	want := []string{"GBAHS2500262", "Ed Sheeran Sapphire", "ES71G2502682", "Eminem Stan", "Nobody Nowhere"}
	if got := yt.queries(); !slices.Equal(got, want) {
		t.Fatalf("searched %q", got)
	}
	if c := j.Items[2].Candidates; len(c) != 2 || c[0].Song.VideoID != "l" {
		t.Fatalf("Stan's candidates %+v", c)
	}
}

// A song the library has already isn't searched for.
func TestSongsThereAreFilesOfAreLeftAlone(t *testing.T) {
	db := openDB(t)
	if _, err := db.Exec(`INSERT INTO tracks (path, size, mtime, artist, album, title, number, added, spotify_id)
		VALUES ('/m/Boombrick/Ed Sheeran/Sapphire/01 Sapphire.opus', 1, 1, 'Ed Sheeran', 'Sapphire', 'Sapphire', 1, 1, ?)`, sapphire.ID); err != nil {
		t.Fatal(err)
	}
	yt := &fakeYT{answers: answers()}
	im, _ := start(t, db, yt, nil)
	im.Import(road, "Road trip", []spotify.Track{sapphire})
	if j := settled(t, im, road); j.Items[0].State != Have || len(yt.queries()) != 0 {
		t.Fatalf("state %v, searched %q", j.Items[0].State, yt.queries())
	}
}

// Imports carry on after a restart as they were; imported again, only the
// new songs are searched for, and songs gone from the playlist are dropped.
func TestImportsAreKept(t *testing.T) {
	db := openDB(t)
	yt := &fakeYT{answers: answers()}
	im, _ := start(t, db, yt, nil)
	im.Import(road, "Road trip", []spotify.Track{sapphire, stan})
	before := settled(t, im, road)
	im.Close()

	im2, _ := start(t, db, yt, nil)
	after, ok := im2.Job(road)
	if !ok || after.Name != "Road trip" || !slices.Equal(states(after), states(before)) ||
		after.Items[1].Candidates[0].Song.VideoID != "l" {
		t.Fatalf("after a restart %+v", after)
	}
	n := len(yt.queries())
	im2.Import(road, "Road trip", []spotify.Track{stan, tereyPapa})
	j := settled(t, im2, road)
	if got := states(j); !slices.Equal(got, []State{Unsure, Matched}) {
		t.Fatalf("imported again: %v", got)
	}
	if got := yt.queries()[n:]; !slices.Equal(got, []string{"ES71G2502682"}) {
		t.Fatalf("searched again %q", got)
	}
}

// Use queues the candidate chosen; Skip takes a queued song out again.
func TestUseAndSkip(t *testing.T) {
	yt := &fakeYT{answers: answers()}
	im, q := start(t, openDB(t), yt, nil)
	im.Import(road, "Road trip", []spotify.Track{sapphire, stan})
	settled(t, im, road)
	v := im.Version()
	im.Use(road, stan.ID, 1)
	im.Skip(road, sapphire.ID)
	if im.Version() < v+2 {
		t.Fatalf("version %d after %d: Use and Skip each change it", im.Version(), v)
	}
	j, _ := im.Job(road)
	if got := states(j); !slices.Equal(got, []State{Skipped, Accepted}) || j.Items[1].Chosen != 1 {
		t.Fatalf("states %v, chosen %d", got, j.Items[1].Chosen)
	}
	if got := q.songs(); len(got) != 2 || got[1] != "k:3UmaczJpikHgJFyBTAJVoz:" {
		t.Fatalf("queued %v", got)
	}
	if !slices.Equal(q.removed, []string{"a"}) {
		t.Fatalf("removed %v", q.removed)
	}
}

// Cancelling skips what is left to match and takes queued songs out, but not
// those saved.
func TestCancel(t *testing.T) {
	yt := &fakeYT{answers: answers()}
	im, q := start(t, openDB(t), yt, nil)
	im.Import(road, "Road trip", []spotify.Track{sapphire, tereyPapa})
	settled(t, im, road)
	q.saved["i"] = true
	im.Cancel(road)
	j, _ := im.Job(road)
	if got := states(j); !slices.Equal(got, []State{Skipped, Matched}) || !slices.Equal(q.removed, []string{"a"}) {
		t.Fatalf("states %v, removed %v", got, q.removed)
	}
}

// Two imports can share a song's download, as a playlist's and the song's own
// do: the second finds it queued and counts on it, so cancelling the first
// leaves it in the queue.
func TestASharedDownloadStaysForTheOtherImport(t *testing.T) {
	yt := &fakeYT{answers: answers()}
	im, q := start(t, openDB(t), yt, nil)
	im.Import(road, "Road trip", []spotify.Track{sapphire, tereyPapa})
	settled(t, im, road)
	one := "spotify:track:" + sapphire.ID
	im.Import(one, "Sapphire", []spotify.Track{sapphire})
	if j := settled(t, im, one); j.Items[0].State != Have {
		t.Fatalf("the song alone: %v", states(j))
	}
	im.Cancel(road)
	if !slices.Equal(q.removed, []string{"i"}) {
		t.Fatalf("cancelling the playlist removed %v", q.removed)
	}
}

// Two imports that accepted the same download keep it while either has it.
func TestADownloadAcceptedTwiceStaysWhileOneHasIt(t *testing.T) {
	yt := &fakeYT{answers: answers()}
	im, q := start(t, openDB(t), yt, nil)
	const other = "spotify:playlist:0ther0000000000000000"
	im.Import(road, "Road trip", []spotify.Track{stan})
	im.Import(other, "Other", []spotify.Track{stan})
	settled(t, im, road)
	settled(t, im, other)
	im.Use(road, stan.ID, 0)
	im.Use(other, stan.ID, 0)
	im.Cancel(road)
	if len(q.removed) != 0 {
		t.Fatalf("cancelling one import removed %v", q.removed)
	}
	im.Skip(other, stan.ID)
	if !slices.Equal(q.removed, []string{"l"}) {
		t.Fatalf("with no import left having it, removed %v", q.removed)
	}
}

// A pass writes only the songs that changed, and a song dropped from the
// playlist while it was being matched isn't written back.
func TestOnlyWhatChangedIsWritten(t *testing.T) {
	db := openDB(t)
	rows := written(t, db)
	im := bare(db)
	im.Import(road, "Road trip", []spotify.Track{sapphire, tereyPapa, stan})
	im.flush()
	if n := rows(); n != 3 {
		t.Fatalf("the import wrote %d songs", n)
	}
	j, e := im.item(road, stan.ID)
	im.settle(j, e, Unsure, []Candidate{{Song: ytmusic.Song{VideoID: "l", Title: "Stan"}, Score: 0.7}})
	im.flush()
	if n := rows(); n != 1 {
		t.Fatalf("a song matched wrote %d", n)
	}
	if im.flush(); rows() != 0 {
		t.Fatal("a pass with nothing changed wrote")
	}

	_, gone := im.item(road, tereyPapa.ID)
	im.Import(road, "Road trip", []spotify.Track{sapphire, stan})
	im.flush()
	rows()
	im.settle(j, gone, Matched, []Candidate{{Song: ytmusic.Song{VideoID: "i"}, Score: 1}})
	if im.flush(); rows() != 0 {
		t.Fatal("a dropped song was written")
	}

	again := bare(db)
	if err := again.load(); err != nil {
		t.Fatal(err)
	}
	got, _ := again.Job(road)
	if !slices.Equal(states(got), []State{Waiting, Unsure}) || got.Items[1].Candidates[0].Song.VideoID != "l" {
		t.Fatalf("read back %+v", got)
	}
}

// Changes gives the songs that changed since a version, or all of them when
// the playlist was imported again since.
func TestChanges(t *testing.T) {
	im := bare(openDB(t))
	if _, ok := im.Changes(road, 0); ok {
		t.Fatal("changes of a playlist never imported")
	}
	im.Import(road, "Road trip", []spotify.Track{sapphire, tereyPapa, stan})
	c, _ := im.Changes(road, 0)
	if !c.All || len(c.Items) != 3 || c.Version != im.Version() {
		t.Fatalf("first changes %+v", c)
	}
	im.Skip(road, tereyPapa.ID)
	c, _ = im.Changes(road, c.Version)
	if c.All || !slices.Equal(c.At, []int{1}) || len(c.Items) != 1 || c.Items[0].State != Skipped {
		t.Fatalf("after a skip %+v", c)
	}
	if c, _ = im.Changes(road, c.Version); c.All || len(c.Items) != 0 {
		t.Fatalf("with nothing changed %+v", c)
	}
	im.Import(road, "Road trip", []spotify.Track{stan, sapphire})
	if c, _ = im.Changes(road, c.Version); !c.All || len(c.Items) != 2 || c.Items[0].Track.ID != stan.ID {
		t.Fatalf("imported again %+v", c)
	}
}

// BenchmarkFlush is a pass after one song of a 2,000-song import is matched.
func BenchmarkFlush(b *testing.B) {
	im := bare(openDB(b))
	ts := make([]spotify.Track, 2000)
	for i := range ts {
		ts[i] = stan
		ts[i].ID = strconv.Itoa(i)
	}
	im.Import(road, "Big", ts)
	j := im.find(road)
	cands := make([]Candidate, keep)
	for i := range cands {
		cands[i] = Candidate{Song: ytmusic.Song{VideoID: strconv.Itoa(i), Title: "Stan", Artist: "Eminem",
			Album: "The Marshall Mathers LP", Duration: dur(6, 44), Thumb: "https://lh3.googleusercontent.com/abcdef=w120-h120"}, Score: 0.9}
	}
	for _, e := range j.items {
		im.settle(j, e, Matched, cands)
	}
	im.flush()
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		e := j.items[i%len(j.items)]
		im.mu.Lock()
		e.Chosen = (e.Chosen + 1) % keep
		im.touch(j, e)
		im.mu.Unlock()
		im.flush()
		i++
	}
}

// YouTube Music in a shape that can't be read stops matching until the next
// import; offline, nothing is asked until online again.
func TestChangedAndOffline(t *testing.T) {
	yt := &fakeYT{answers: answers(), err: ytmusic.ErrChanged}
	im, _ := start(t, openDB(t), yt, nil)
	im.Import(road, "Road trip", []spotify.Track{tereyPapa})
	for deadline := time.Now().Add(3 * time.Second); !errors.Is(im.Err(), ytmusic.ErrChanged); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("matching didn't stop")
		}
	}
	time.Sleep(20 * time.Millisecond)
	if n := len(yt.queries()); n != 1 {
		t.Fatalf("searched %d times after it stopped", n)
	}
	yt.set(nil)
	im.Import(road, "Road trip", []spotify.Track{tereyPapa})
	if j := settled(t, im, road); j.Items[0].State != Matched || im.Err() != nil {
		t.Fatalf("imported again: %v, %v", j.Items[0].State, im.Err())
	}

	online := &atomic.Bool{}
	yt2 := &fakeYT{answers: answers()}
	im2, _ := start(t, openDB(t), yt2, online)
	im2.Import(road, "Road trip", []spotify.Track{tereyPapa})
	time.Sleep(30 * time.Millisecond)
	if n := len(yt2.queries()); n != 0 {
		t.Fatalf("searched %d times offline", n)
	}
	online.Store(true)
	if j := settled(t, im2, road); j.Items[0].State != Matched {
		t.Fatalf("back online: %v", j.Items[0].State)
	}
}

// Switched off, as while Spotify or YouTube is, the importer matches nothing
// and doesn't look again until it is switched on.
func TestSwitchedOffMatchesNothing(t *testing.T) {
	yt := &fakeYT{answers: answers()}
	im, _ := start(t, openDB(t), yt, nil)
	im.SetEnabled(false)
	im.Import(road, "Road trip", []spotify.Track{tereyPapa})
	time.Sleep(30 * time.Millisecond)
	if n := len(yt.queries()); n != 0 {
		t.Fatalf("searched %d times switched off", n)
	}
	if j, _ := im.Job(road); j.Count(Waiting) != 1 {
		t.Fatal("the song should wait to be matched")
	}
	im.SetEnabled(true)
	if j := settled(t, im, road); j.Items[0].State != Matched {
		t.Fatalf("switched on: %v", j.Items[0].State)
	}
}

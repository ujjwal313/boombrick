package resume

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/store"
)

// Save stores s with its queue, as a first save does.
func Save(db *sql.DB, s player.Snapshot) error { return save(db, s, true) }

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "resume.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.DB
}

func snapshot() player.Snapshot {
	return player.Snapshot{
		Queue: []player.Track{
			{Path: "/m/A/X/01.flac", Title: "One", Artist: "A", AlbumArtist: "A", Album: "X", Gain: -3.5, Peak: 0.8, HasGain: true,
				AlbumGain: -4.25, AlbumPeak: 0.9, HasAlbumGain: true, Duration: 3 * time.Minute},
			{Path: "/m/A/X/02.flac", Title: "Two", Artist: "A", AlbumArtist: "A", Album: "X"},
		},
		Order: []int{1, 0}, Pos: 1, Frame: 12345, Fraction: 0.25, AlbumRun: true, Shuffle: true, Repeat: player.RepeatAll,
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	db := openDB(t)
	if _, ok, err := Load(db); ok || err != nil {
		t.Fatalf("Load of an empty database = %v, %v", ok, err)
	}
	want := snapshot()
	if err := Save(db, want); err != nil {
		t.Fatal(err)
	}
	want.Queue = want.Queue[:1] // saving again with a shorter queue replaces it
	want.Order, want.Pos = []int{0}, 0
	if err := Save(db, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(db)
	if err != nil || !ok {
		t.Fatalf("Load = %v, %v", ok, err)
	}
	want.Fraction = 0     // not part of the saved queue
	want.AlbumRun = false // nor is the album run
	want.Version = 0      // nor is the version
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v\nwant   %+v", got, want)
	}
}

func TestSaverRewritesTheQueueOnlyWhenItChanged(t *testing.T) {
	db := openDB(t)
	sv := NewSaver(db)
	s := snapshot()
	s.Version = 1
	if err := sv.Save(s); err != nil {
		t.Fatal(err)
	}
	// Clear the saved queue behind the saver's back: an unchanged version
	// mustn't rewrite it, a new one must.
	if _, err := db.Exec(`DELETE FROM queue`); err != nil {
		t.Fatal(err)
	}
	s.Frame = 99
	if err := sv.Save(s); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := Load(db); ok {
		t.Fatal("an unchanged queue was written again")
	}
	s.Version = 2
	if err := sv.Save(s); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := Load(db); err != nil || !ok || got.Frame != 99 || len(got.Queue) != 2 {
		t.Fatalf("Load after a new version = %+v, %v, %v", got, ok, err)
	}
}

// The play order is written with the queue, when the version changes; the
// position is written every time.
func TestSaverWritesTheOrderWithTheQueue(t *testing.T) {
	db := openDB(t)
	sv := NewSaver(db)
	saved := func() (order string, frame int64) {
		t.Helper()
		if err := db.QueryRow(`SELECT play_order, frame FROM playback_state WHERE id = 1`).Scan(&order, &frame); err != nil {
			t.Fatal(err)
		}
		return order, frame
	}
	s := snapshot()
	s.Version = 1
	if err := sv.Save(s); err != nil {
		t.Fatal(err)
	}
	if order, _ := saved(); order != "1,0" {
		t.Fatalf("order %q after the first save", order)
	}
	// An order that differs under the same version shows whether it is written.
	s.Order, s.Frame = []int{0, 1}, 99
	if err := sv.Save(s); err != nil {
		t.Fatal(err)
	}
	if order, frame := saved(); order != "1,0" || frame != 99 {
		t.Fatalf("same version: order %q, frame %d; want the order left, the frame written", order, frame)
	}
	s.Version = 2
	if err := sv.Save(s); err != nil {
		t.Fatal(err)
	}
	if order, _ := saved(); order != "0,1" {
		t.Fatalf("order %q after a new version", order)
	}
}

func TestOddSnapshotsSaveAndLoadAsNothing(t *testing.T) {
	q := snapshot().Queue
	for _, s := range []player.Snapshot{
		{},
		{Pos: 3, Order: []int{0}},
		{Queue: q, Order: []int{0, 1}, Pos: 2},
		{Queue: q, Order: []int{0, 2}, Pos: 1},
		{Queue: q, Order: []int{0}, Pos: 0},
		{Queue: q, Order: []int{1, 0}, Pos: -1},
	} {
		db := openDB(t)
		if err := Save(db, s); err != nil {
			t.Fatalf("Save(%+v) = %v", s, err)
		}
		if _, ok, err := Load(db); ok || err != nil {
			t.Fatalf("Load after saving %d tracks in order %v at %d = %v, %v; want nothing", len(s.Queue), s.Order, s.Pos, ok, err)
		}
	}
}

func TestLoadRejectsABadOrder(t *testing.T) {
	db := openDB(t)
	if err := Save(db, snapshot()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE playback_state SET play_order = '1,x'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Load(db); err == nil || ok {
		t.Fatalf("Load with a non-numeric play order = %v, %v; want an error", ok, err)
	}
}

func TestRecentPutsTheLatestFirst(t *testing.T) {
	db := openDB(t)
	s := snapshot()
	s.Order, s.Pos = []int{0, 1}, 0
	for _, album := range []string{"X", "Y", "Z", "X"} {
		s.Queue[0].Album = album
		if err := Save(db, s); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // well within a second of each other
	}
	got, err := Recent(db, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Album != "X" || got[1].Album != "Z" {
		t.Fatalf("Recent(2) = %+v; want X, then Z", got)
	}
	if d := time.Since(got[0].Updated); d < 0 || d > time.Minute {
		t.Fatalf("X was updated %v ago", d)
	}
}

func TestProgressWithoutAnAlbumArtistIsKeptUnderTheArtist(t *testing.T) {
	db := openDB(t)
	s := snapshot()
	s.Queue[0].AlbumArtist, s.Queue[0].Artist = "", "B"
	s.Order, s.Pos = []int{0, 1}, 0
	if err := Save(db, s); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(db, 10)
	if err != nil || len(got) != 1 || got[0].Artist != "B" || got[0].Album != "X" {
		t.Fatalf("Recent = %+v, %v; want X under B", got, err)
	}
}

func TestRecentListsAlbumsInProgress(t *testing.T) {
	db := openDB(t)
	s := snapshot()
	if err := Save(db, s); err != nil {
		t.Fatal(err)
	}
	s.Queue[0].Album, s.Queue[0].Path, s.Order, s.Pos = "Y", "/m/A/Y/01.flac", []int{0, 1}, 0
	s.Fraction = 0.99 // finished albums drop off
	if err := Save(db, s); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(db, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Album != "X" || got[0].Artist != "A" || got[0].Path != "/m/A/X/01.flac" || got[0].Frame != 12345 {
		t.Fatalf("Recent = %+v", got)
	}
}

// A song that wasn't queued as part of its album leaves no progress, though
// the queue and position are saved.
func TestSongsOutsideAnAlbumRunLeaveNoProgress(t *testing.T) {
	db := openDB(t)
	s := snapshot()
	s.AlbumRun = false
	if err := Save(db, s); err != nil {
		t.Fatal(err)
	}
	if got, err := Recent(db, 10); err != nil || len(got) != 0 {
		t.Fatalf("Recent = %+v, %v; want none", got, err)
	}
	if got, ok, err := Load(db); err != nil || !ok || got.Frame != s.Frame {
		t.Fatalf("Load = %+v, %v, %v; want the queue saved", got, ok, err)
	}
}

func TestRecentOmitsAnAlbumPlayedOut(t *testing.T) {
	db := openDB(t)
	s := snapshot()
	s.Fraction = 1
	if err := Save(db, s); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(db, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("Recent = %+v, %v; want none", got, err)
	}
}

func TestPendingTracksAreKept(t *testing.T) {
	db := openDB(t)
	s := player.Snapshot{Queue: []player.Track{
		{Title: "Night Drive Theory", Artist: "Lumen Coast", AlbumArtist: "Lumen Coast", Album: "Night Drive Theory", VideoID: "v1"},
		{Path: "/m/a.flac", Title: "a"},
	}, Order: []int{0, 1}}
	if err := Save(db, s); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(db)
	if err != nil || !ok || !reflect.DeepEqual(got.Queue, s.Queue) {
		t.Fatalf("loaded %+v, %v, %v", got.Queue, ok, err)
	}
}

// A streamed track is kept by its Spotify ID; with no file to carry on
// from, it leaves its album's progress alone.
func TestStreamedTracksAreKept(t *testing.T) {
	db := openDB(t)
	s := player.Snapshot{Queue: []player.Track{
		{Title: "Stan", Artist: "Eminem", AlbumArtist: "Eminem", Album: "The Marshall Mathers LP", SpotifyID: "3UmaczJpikHgJFyBTAJVoz", Duration: 404 * time.Second,
			Cover: "https://i.scdn.co/image/ab67616d0000b273dbb3dd82da45b7d7f31b1b42"},
		{Title: "Kim", Artist: "Eminem", AlbumArtist: "Eminem", Album: "The Marshall Mathers LP", SpotifyID: "4mBTgXbiaP0mLanQhIKAxQ"},
	}, Order: []int{0, 1}, Frame: 4410, AlbumRun: true, Fraction: 0.25}
	if err := Save(db, s); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(db)
	if err != nil || !ok || !reflect.DeepEqual(got.Queue, s.Queue) || got.Frame != 4410 {
		t.Fatalf("loaded %+v, %v, %v", got, ok, err)
	}
	if p, err := Recent(db, 10); err != nil || len(p) != 0 {
		t.Fatalf("progress %+v, %v; want none", p, err)
	}
}

func TestAPendingTrackLeavesAlbumProgressAlone(t *testing.T) {
	db := openDB(t)
	album := func(t player.Track) player.Track {
		t.Album, t.AlbumArtist = "Night Drive Theory", "Lumen Coast"
		return t
	}
	first := player.Snapshot{Queue: []player.Track{album(player.Track{Path: "/m/a.flac"})}, Order: []int{0}, AlbumRun: true, Frame: 100, Fraction: 0.5}
	if err := Save(db, first); err != nil {
		t.Fatal(err)
	}
	second := player.Snapshot{Queue: []player.Track{album(player.Track{VideoID: "v1"})}, Order: []int{0}, AlbumRun: true, Fraction: 0.5}
	if err := Save(db, second); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(db, 10)
	if err != nil || len(got) != 1 || got[0].Path != "/m/a.flac" || got[0].Frame != 100 {
		t.Fatalf("progress %+v, %v", got, err)
	}
}

func TestStationsAreKept(t *testing.T) {
	db := openDB(t)
	s := player.Snapshot{Queue: []player.Track{
		{Title: "Radio X", Artist: "India", Cover: "https://x/logo.png", StationID: "u1", Stream: "http://s/x"},
	}, Order: []int{0}}
	if err := Save(db, s); err != nil {
		t.Fatal(err)
	}
	got, ok, err := Load(db)
	if err != nil || !ok || !reflect.DeepEqual(got.Queue, s.Queue) {
		t.Fatalf("loaded %+v, %v, %v", got, ok, err)
	}
	if !got.Queue[0].Live() {
		t.Fatal("the station isn't live after loading")
	}
}

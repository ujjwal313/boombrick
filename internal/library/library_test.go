package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/store"
)

func audioFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".mp3" || ext == ".flac"
}

// formats indexes audioFile paths, giving every file a 90 s length.
var formats = Formats{
	Supported: audioFile,
	Duration:  func(string) (time.Duration, error) { return 90 * time.Second, nil },
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "lib.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.DB
}

func touch(t *testing.T, root, rel, content string) string {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParsePath(t *testing.T) {
	root := filepath.FromSlash("/m")
	cases := []struct {
		rel                  string
		artist, album, title string
		disc, number         int
	}{
		{"Odalys/Copper Season/03 Rust.flac", "Odalys", "Copper Season", "Rust", 0, 3},
		{"Odalys/Copper Season/07 - Rust.mp3", "Odalys", "Copper Season", "Rust", 0, 7},
		{"Odalys/Copper Season/1-12. Rust.mp3", "Odalys", "Copper Season", "Rust", 1, 12},
		{"Odalys/Copper Season/CD2/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season/Disc 1/01 Ember.mp3", "Odalys", "Copper Season", "Ember", 1, 1},
		{"Odalys/Copper Season/Disc 1 - Embers/01 Ember.mp3", "Odalys", "Copper Season", "Ember", 1, 1},
		{"Odalys/Copper Season/CD2 (Bonus)/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season/Disc 2 of 2/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season/[Disc 2]/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season/Disc.2/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season/CD_2/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season (Disc 2)/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season [CD2]/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season - Disc 2/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season/Disc 1, Embers/01 Ember.mp3", "Odalys", "Copper Season", "Ember", 1, 1},
		{"Odalys/Copper Season/Disc 1 - Embers/01 Ember.mp3", "Odalys", "Copper Season", "Ember", 1, 1},
		{"Odalys/Copper Season　(Disc 2)/03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		// The disc the folders give beats a file name's, as a ripper names
		// every disc of a set tagged 1/1 "1-03".
		{"Odalys/Copper Season CD2/1-03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		{"Odalys/Copper Season/CD2/1-03 Rust.flac", "Odalys", "Copper Season", "Rust", 2, 3},
		// Not discs: a name that goes on after the number with only a space, or
		// after a dot ("Disc 2.0"); a CD numbered as one of a collection; a disc
		// without a number; a word that starts like one; a year.
		{"Odalys/CD 1 Classics/01 Ember.mp3", "Odalys", "CD 1 Classics", "Ember", 0, 1},
		{"Odalys/Disc 2.0 Remixes/01 Ember.mp3", "Odalys", "Disc 2.0 Remixes", "Ember", 0, 1},
		{"Odalys/CD1.5/01 Ember.mp3", "Odalys", "CD1.5", "Ember", 0, 1},
		{"Odalys/CD 01 - Abbey Road/01 Ember.mp3", "Odalys", "CD 01 - Abbey Road", "Ember", 0, 1},
		{"Odalys/Copper Season (Bonus Disc)/01 Ember.mp3", "Odalys", "Copper Season (Bonus Disc)", "Ember", 0, 1},
		{"Odalys/Discovery/01 Ember.mp3", "Odalys", "Discovery", "Ember", 0, 1},
		{"Odalys/Hits CD 2001/01 Ember.mp3", "Odalys", "Hits CD 2001", "Ember", 0, 1},
		{"Boombrick/Kenji Arata/Afterhours/02 Neon.flac", "Kenji Arata", "Afterhours", "Neon", 0, 2},
		{"Copper Season/Rust.mp3", "Unknown artist", "Copper Season", "Rust", 0, 0},
		{"Rust.mp3", "Unknown artist", "Unknown album", "Rust", 0, 0},
		{"Odalys/Live/2001 A Space Odyssey.mp3", "Odalys", "Live", "2001 A Space Odyssey", 0, 0},
		{"Odalys/Live/01.mp3", "Odalys", "Live", "01", 0, 0},
	}
	for _, c := range cases {
		a, al, ti, d, n := ParsePath(root, filepath.Join(root, filepath.FromSlash(c.rel)))
		if a != c.artist || al != c.album || ti != c.title || d != c.disc || n != c.number {
			t.Errorf("%s: got %q %q %q %d %d", c.rel, a, al, ti, d, n)
		}
	}
}

func TestScanIndexesAndUpdates(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	touch(t, root, "Odalys/Copper Season/02 Rust.flac", "x")
	touch(t, root, "Odalys/Copper Season/01 Ember.flac", "x")
	touch(t, root, "Kenji Arata/Afterhours/01 Neon.mp3", "x")
	touch(t, root, "loose.mp3", "x")
	touch(t, root, "Odalys/Copper Season/cover.jpg", "x")
	touch(t, root, "Odalys/Copper Season/._01 Ember.flac", "x")
	touch(t, root, ".Trashes/old.mp3", "x")

	st, err := Scan(context.Background(), db, root, formats)
	if err != nil {
		t.Fatal(err)
	}
	if st != (Stats{Added: 4, Total: 4}) {
		t.Fatalf("first scan = %+v", st)
	}

	albums, err := Albums(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 3 {
		t.Fatalf("%d albums, want 3: %+v", len(albums), albums)
	}
	var copper Album
	for _, a := range albums {
		if a.Title == "Copper Season" {
			copper = a
		}
	}
	if copper.Artist != "Odalys" || len(copper.Tracks) != 2 || copper.Tracks[0].Title != "Ember" || copper.Tracks[1].Number != 2 {
		t.Fatalf("Copper Season = %+v", copper)
	}

	if st, _ := Scan(context.Background(), db, root, formats); st != (Stats{Total: 4}) {
		t.Fatalf("unchanged rescan = %+v", st)
	}

	touch(t, root, "loose.mp3", "longer")
	os.Remove(filepath.Join(root, "Kenji Arata", "Afterhours", "01 Neon.mp3"))
	if st, _ := Scan(context.Background(), db, root, formats); st != (Stats{Updated: 1, Removed: 1, Total: 3}) {
		t.Fatalf("rescan after a change and a removal = %+v", st)
	}
}

func TestNewestAlbumsFirst(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	touch(t, root, "A/Old/01 x.mp3", "x")
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // added times are in whole seconds
	touch(t, root, "B/New/01 y.mp3", "x")
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, _ := Albums(db)
	if len(albums) != 2 || albums[0].Title != "New" {
		t.Fatalf("albums = %+v, want New first", albums)
	}
}

// Albums found by one scan share their added time; the one with the newest
// files comes first.
func TestAlbumsAddedTogetherNewestFilesFirst(t *testing.T) {
	for _, newer := range []string{"A", "B"} {
		db := openDB(t)
		root := t.TempDir()
		// Track order puts A's track first, so A is grouped first.
		a := touch(t, root, "A/Old/01 a.mp3", "x")
		b := touch(t, root, "B/New/01 b.mp3", "x")
		b2 := touch(t, root, "B/New/02 c.mp3", "x")
		old, recent := time.Now().Add(-48*time.Hour), time.Now().Add(-time.Hour)
		for _, f := range []string{a, b, b2} {
			if err := os.Chtimes(f, old, old); err != nil {
				t.Fatal(err)
			}
		}
		latest, want := b2, "New"
		if newer == "A" {
			latest, want = a, "Old"
		}
		if err := os.Chtimes(latest, recent, recent); err != nil {
			t.Fatal(err)
		}
		if _, err := Scan(context.Background(), db, root, formats); err != nil {
			t.Fatal(err)
		}
		albums, err := Albums(db)
		if err != nil || len(albums) != 2 || albums[0].Title != want {
			t.Fatalf("albums = %+v, %v; want %s first", albums, err, want)
		}
	}
}

func TestDiscFoldersFormOneAlbum(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	touch(t, root, "Odalys/Copper Season/CD2/01 Rust.flac", "x")
	touch(t, root, "Odalys/Copper Season/CD1/02 Ember.flac", "x")
	touch(t, root, "Odalys/Copper Season/CD1/01 Ash.flac", "x")
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, err := Albums(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(albums) != 1 || albums[0].Artist != "Odalys" || albums[0].Title != "Copper Season" {
		t.Fatalf("albums = %+v, want one Copper Season", albums)
	}
	var titles []string
	for _, tr := range albums[0].Tracks {
		titles = append(titles, tr.Title)
	}
	if got := strings.Join(titles, ","); got != "Ash,Ember,Rust" {
		t.Fatalf("track order = %s", got)
	}
}

func TestUnreadableRootKeepsIndex(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	touch(t, root, "Odalys/Copper Season/01 Ember.flac", "x")
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	st, err := Scan(context.Background(), db, filepath.Join(root, "gone"), formats)
	if err != nil || st != (Stats{}) {
		t.Fatalf("Scan of a missing root = %+v, %v", st, err)
	}
	albums, _ := Albums(db)
	if len(albums) != 1 || len(albums[0].Tracks) != 1 {
		t.Fatalf("index changed: %+v", albums)
	}
}

func TestMissingRootOnEmptyIndex(t *testing.T) {
	st, err := Scan(context.Background(), openDB(t), filepath.Join(t.TempDir(), "Music"), formats)
	if err != nil || st != (Stats{}) {
		t.Fatalf("Scan of a missing root = %+v, %v", st, err)
	}
}

func TestCancelledScanLeavesIndexUnchanged(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	touch(t, root, "Odalys/Copper Season/01 Ember.flac", "x")
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	touch(t, root, "Kenji Arata/Afterhours/01 Neon.mp3", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Scan(ctx, db, root, formats); !errors.Is(err, context.Canceled) {
		t.Fatalf("Scan with a cancelled context = %v, want context.Canceled", err)
	}
	albums, _ := Albums(db)
	if len(albums) != 1 || albums[0].Title != "Copper Season" {
		t.Fatalf("index changed by a cancelled scan: %+v", albums)
	}
}

func TestScanReadsTags(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	data, err := os.ReadFile(taggedFixture(t, "tagged.flac"))
	if err != nil {
		t.Fatal(err)
	}
	// Folder names that disagree with the tags: the tags win.
	touch(t, root, "Somebody/Something/09 Wrong.flac", string(data))
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, _ := Albums(db)
	if len(albums) != 1 {
		t.Fatalf("albums = %+v", albums)
	}
	a, tr := albums[0], albums[0].Tracks[0]
	if a.Artist != "Various Artists" || a.Title != "Copper Season" || a.Year != 2019 ||
		tr.Title != "Rust" || tr.Artist != "Odalys" || tr.Disc != 2 || tr.Number != 3 || !tr.HasGain {
		t.Fatalf("album %+v, track %+v", a, tr)
	}
}

// id3 builds the smallest MP3 the tag reader accepts: an ID3v2.3 header and
// the given text frames, with no audio.
func id3(frames map[string]string) string {
	var body []byte
	for id, text := range frames {
		n := len(text) + 1
		body = append(body, id...)
		body = append(body, byte(n>>24), byte(n>>16), byte(n>>8), byte(n), 0, 0, 0)
		body = append(body, text...)
	}
	n := len(body)
	return "ID3\x03\x00\x00" + string([]byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}) + string(body)
}

// A scan keeps each file's genres, and a rescan of a retagged file replaces
// them.
func TestScanStoresGenres(t *testing.T) {
	db, root := openDB(t), t.TempDir()
	path := touch(t, root, "Odalys/Copper Season/01 Rust.mp3", id3(map[string]string{"TCON": "Rock;Pop"}))
	touch(t, root, "Odalys/Copper Season/02 Ember.mp3", id3(map[string]string{"TIT2": "Ember"}))
	genres := func() [][]string {
		t.Helper()
		if _, err := Scan(context.Background(), db, root, formats); err != nil {
			t.Fatal(err)
		}
		albums, err := Albums(db)
		if err != nil || len(albums) != 1 {
			t.Fatalf("albums %+v, %v", albums, err)
		}
		var gs [][]string
		for _, tr := range albums[0].Tracks {
			gs = append(gs, tr.Genres)
		}
		return gs
	}
	if gs := genres(); !reflect.DeepEqual(gs, [][]string{{"Rock", "Pop"}, nil}) {
		t.Fatalf("genres %q", gs)
	}
	os.WriteFile(path, []byte(id3(map[string]string{"TCON": "Jazz"})), 0o644)
	os.Chtimes(path, time.Now(), time.Now().Add(time.Hour))
	if gs := genres(); !reflect.DeepEqual(gs, [][]string{{"Jazz"}, nil}) {
		t.Fatalf("genres after retagging %q", gs)
	}
}

func TestTagsKeepFolderDisc(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	// A track number is tagged but no disc number: the folders still say which disc.
	touch(t, root, "Odalys/Copper Season/CD2/x.mp3", id3(map[string]string{"TRCK": "1/9", "TIT2": "Rust"}))
	touch(t, root, "Odalys/Copper Season/CD1/y.mp3", id3(map[string]string{"TRCK": "2/9", "TIT2": "Ember"}))
	touch(t, root, "Odalys/Copper Season/CD1/z.mp3", id3(map[string]string{"TRCK": "1/9", "TIT2": "Ash"}))
	// A disc is tagged without a track number: the name and folder give the track.
	touch(t, root, "Odalys/Copper Season/CD2/02 Gold.mp3", id3(map[string]string{"TPOS": "2/2"}))
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, _ := Albums(db)
	if len(albums) != 1 {
		t.Fatalf("albums = %+v", albums)
	}
	var got []string
	for _, tr := range albums[0].Tracks {
		got = append(got, fmt.Sprintf("%d-%d %s", tr.Disc, tr.Number, tr.Title))
	}
	if want := "1-1 Ash,1-2 Ember,2-1 Rust,2-2 Gold"; strings.Join(got, ",") != want {
		t.Fatalf("tracks = %v, want %s", got, want)
	}
}

// oneAlbum scans root and returns its only album's title and tracks as
// "disc-number title".
func oneAlbum(t *testing.T, db *sql.DB, root string) (string, string) {
	t.Helper()
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, err := Albums(db)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums = %+v, %v; want one", albums, err)
	}
	var got []string
	for _, tr := range albums[0].Tracks {
		got = append(got, fmt.Sprintf("%d-%d %s", tr.Disc, tr.Number, tr.Title))
	}
	return albums[0].Title, strings.Join(got, ",")
}

// An album tag that names the disc, as "Copper Season (Disc 1)", is the album
// without it, and gives the disc when no disc is tagged.
func TestDiscInAlbumTag(t *testing.T) {
	db, root := openDB(t), t.TempDir()
	tags := func(album, track, title string) string {
		return id3(map[string]string{"TALB": album, "TPE2": "Odalys", "TRCK": track, "TIT2": title})
	}
	touch(t, root, "Odalys/Copper Season/x.mp3", tags("Copper Season [CD2]", "1", "Rust"))
	touch(t, root, "Odalys/Copper Season/y.mp3", tags("Copper Season (Disc 1)", "2", "Ember"))
	touch(t, root, "Odalys/Copper Season/z.mp3", tags("Copper Season (Disc 1)", "1", "Ash"))
	title, tracks := oneAlbum(t, db, root)
	if title != "Copper Season" || tracks != "1-1 Ash,1-2 Ember,2-1 Rust" {
		t.Fatalf("album %q: %s", title, tracks)
	}
}

// The disc an album's name or a disc folder gives beats a disc tag: rippers
// tag every disc of a set 1/1.
func TestNamedDiscBeatsDiscTag(t *testing.T) {
	for _, c := range []struct{ name, disc1, disc2, album1, album2 string }{
		{"album tags", "Odalys/Copper Season/x.mp3", "Odalys/Copper Season/y.mp3",
			"Copper Season (Disc 1)", "Copper Season (Disc 2)"},
		{"disc folders", "Odalys/Copper Season/CD1/x.mp3", "Odalys/Copper Season/CD2/y.mp3",
			"Copper Season", "Copper Season"},
	} {
		t.Run(c.name, func(t *testing.T) {
			db, root := openDB(t), t.TempDir()
			tags := func(album, title string) string {
				return id3(map[string]string{"TALB": album, "TPE2": "Odalys", "TPOS": "1/1", "TRCK": "1", "TIT2": title})
			}
			touch(t, root, c.disc2, tags(c.album2, "Rust"))
			touch(t, root, c.disc1, tags(c.album1, "Ash"))
			title, tracks := oneAlbum(t, db, root)
			if title != "Copper Season" || tracks != "1-1 Ash,2-1 Rust" {
				t.Fatalf("album %q: %s", title, tracks)
			}
		})
	}
}

// Sibling folders of an album's discs, "Copper Season (Disc 1)" and
// "Copper Season (Disc 2)", are one album without an album-artist tag, even
// when one disc has a guest.
func TestSiblingDiscFolders(t *testing.T) {
	db, root := openDB(t), t.TempDir()
	touch(t, root, "Odalys/Copper Season (Disc 1)/01 Ash.mp3", "x")
	touch(t, root, "Odalys/Copper Season (Disc 2)/01 Rust.mp3",
		id3(map[string]string{"TPE1": "Odalys feat. Kenji Arata", "TIT2": "Rust", "TRCK": "1"}))
	title, tracks := oneAlbum(t, db, root)
	if title != "Copper Season" || tracks != "1-1 Ash,2-1 Rust" {
		t.Fatalf("album %q: %s", title, tracks)
	}
}

// A folder's album name loses its disc once: a tagged file without an album
// tag keeps the rest of the name.
func TestFolderAlbumSplitOnce(t *testing.T) {
	db, root := openDB(t), t.TempDir()
	touch(t, root, "Odalys/Hits CD1 (Disc 2)/x.mp3", id3(map[string]string{"TIT2": "Ember", "TRCK": "1"}))
	title, tracks := oneAlbum(t, db, root)
	if title != "Hits CD1" || tracks != "2-1 Ember" {
		t.Fatalf("album %q: %s", title, tracks)
	}
}

func TestSplitDisc(t *testing.T) {
	for _, c := range []struct {
		name, album string
		disc        int
	}{
		{"Copper Season (Disc 1)", "Copper Season", 1},
		{"Copper Season [CD 2]", "Copper Season", 2},
		{"Copper Season (Disc 1 of 2)", "Copper Season", 1},
		{"Copper Season - Disc 2", "Copper Season", 2},
		{"Copper Season: Disc 3", "Copper Season", 3},
		{"Copper Season, Disc 3", "Copper Season", 3},
		{"Copper Season CD2", "Copper Season", 2},
		{"Copper Season Disc.2", "Copper Season", 2},
		{"Copper Season (Disc 1)", "Copper Season", 1},
		{"アルバム（Disc 2）", "アルバム", 2},
		{"アルバム　[DISC 1]", "アルバム", 1},
		{"アルバム【CD1】", "アルバム", 1},
		// Left as they are.
		{"Copper Season (Bonus Disc)", "Copper Season (Bonus Disc)", 0},
		{"Disc 2", "Disc 2", 0},
		{"Hits CD 2001", "Hits CD 2001", 0},
		{"Copper Season (Disc 2) [Remastered]", "Copper Season (Disc 2) [Remastered]", 0},
		{"Copper Season (Disc 0)", "Copper Season (Disc 0)", 0},
	} {
		if album, disc := splitDisc(c.name); album != c.album || disc != c.disc {
			t.Errorf("splitDisc(%q) = %q, %d; want %q, %d", c.name, album, disc, c.album, c.disc)
		}
	}
}

// Disc folders named with their titles keep each disc's tracks together.
func TestDiscFoldersWithTitles(t *testing.T) {
	db, root := openDB(t), t.TempDir()
	touch(t, root, "Odalys/Copper Season/Disc 2 - Rust/01 Rust.flac", "x")
	touch(t, root, "Odalys/Copper Season/Disc 1 - Embers/02 Ember.flac", "x")
	touch(t, root, "Odalys/Copper Season/Disc 1 - Embers/01 Ash.flac", "x")
	title, tracks := oneAlbum(t, db, root)
	if title != "Copper Season" || tracks != "1-1 Ash,1-2 Ember,2-1 Rust" {
		t.Fatalf("album %q: %s", title, tracks)
	}
}

func TestPartialTagsFallBackToFolders(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	touch(t, root, "Odalys/Copper Season/04 Rust.mp3", id3(map[string]string{"TIT2": "Rust (Live)"}))
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, _ := Albums(db)
	if len(albums) != 1 {
		t.Fatalf("albums = %+v", albums)
	}
	a, tr := albums[0], albums[0].Tracks[0]
	if a.Artist != "Odalys" || a.Title != "Copper Season" || tr.Title != "Rust (Live)" || tr.Number != 4 || tr.HasGain {
		t.Fatalf("album %+v, track %+v", a, tr)
	}
}

func TestAlbumArtistGrouping(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	// Without an album artist the track artist groups; with one it wins.
	touch(t, root, "a/Mix/01.mp3", id3(map[string]string{"TRCK": "1", "TPE1": "Odalys", "TIT2": "One", "TALB": "Mix"}))
	touch(t, root, "b/Mix/02.mp3", id3(map[string]string{"TRCK": "2", "TPE1": "Odalys", "TIT2": "Two", "TALB": "Mix"}))
	touch(t, root, "c/Mix/03.mp3", id3(map[string]string{"TRCK": "3", "TPE1": "Kenji Arata", "TPE2": "Odalys", "TIT2": "Three", "TALB": "Mix"}))
	touch(t, root, "d/Mix/04.mp3", id3(map[string]string{"TPE1": "Kenji Arata", "TIT2": "Four", "TALB": "Mix"}))
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, _ := Albums(db)
	got := map[string]string{}
	for _, a := range albums {
		var titles []string
		for _, tr := range a.Tracks {
			titles = append(titles, tr.Title)
		}
		got[a.Artist] = strings.Join(titles, ",")
	}
	if len(albums) != 2 || got["Odalys"] != "One,Two,Three" || got["Kenji Arata"] != "Four" {
		t.Fatalf("albums = %v", got)
	}
}

func TestAlbumsWithoutAnAlbumArtistGroupByFolder(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	// A compilation: one folder, one album tag, different track artists.
	touch(t, root, "Mix/Hits/01.mp3", id3(map[string]string{"TIT2": "One", "TPE1": "Odalys", "TALB": "Hits", "TRCK": "1"}))
	touch(t, root, "Mix/Hits/02.mp3", id3(map[string]string{"TIT2": "Two", "TPE1": "Kenji Arata", "TALB": "Hits", "TRCK": "2"}))
	// Same album title in another folder, one artist: a different album.
	touch(t, root, "Other/Hits/01.mp3", id3(map[string]string{"TIT2": "Three", "TPE1": "Marisol Vega", "TALB": "Hits", "TRCK": "1"}))
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, err := Albums(db)
	if err != nil || len(albums) != 2 {
		t.Fatalf("albums = %+v, %v; want 2", albums, err)
	}
	byArtist := map[string]Album{}
	for _, a := range albums {
		byArtist[a.Artist] = a
	}
	mix, ok := byArtist["Various Artists"]
	if !ok || len(mix.Tracks) != 2 || mix.Tracks[0].Title != "One" || mix.Tracks[1].AlbumArtist != "Various Artists" {
		t.Fatalf("compilation = %+v", mix)
	}
	if solo, ok := byArtist["Marisol Vega"]; !ok || len(solo.Tracks) != 1 {
		t.Fatalf("the other folder's album = %+v", byArtist)
	}
}

func TestScanHoldsNoWriteLockWhileReading(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	for _, n := range []string{"01 a.mp3", "02 b.mp3", "03 c.mp3"} {
		touch(t, root, "A/B/"+n, id3(map[string]string{"TIT2": n}))
	}
	// supported runs during the walk, between files being read; a write from
	// here would wait out the busy timeout if the scan held a transaction.
	var werr error
	supported := func(path string) bool {
		if _, err := db.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('k', 'v')`); err != nil {
			werr = err
		}
		return audioFile(path)
	}
	start := time.Now()
	if _, err := Scan(context.Background(), db, root, Formats{Supported: supported, Duration: formats.Duration}); err != nil {
		t.Fatal(err)
	}
	if werr != nil || time.Since(start) > 2*time.Second {
		t.Fatalf("a write during the scan: %v after %v", werr, time.Since(start))
	}
}

func TestScanStoresLengths(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	touch(t, root, "A/X/01 One.mp3", "1")
	touch(t, root, "A/X/02 Two.mp3", "2")
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	albums, err := Albums(db)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums = %+v, %v", albums, err)
	}
	if a := albums[0]; a.Tracks[0].Duration != 90*time.Second || a.Duration != 180*time.Second {
		t.Fatalf("track %v, album %v; want 1m30s and 3m0s", a.Tracks[0].Duration, a.Duration)
	}
}

func TestScanWritesInBatches(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	for i := range 5 {
		touch(t, root, fmt.Sprintf("A/B/%02d x.mp3", i+1), "x")
	}
	var seen []int
	wrote := func(st Stats) {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM tracks`).Scan(&n); err != nil {
			t.Error(err)
		}
		if n != st.Added {
			t.Errorf("after a batch the table holds %d tracks, Stats says %d", n, st.Added)
		}
		seen = append(seen, st.Added)
	}
	st, err := scan(context.Background(), db, root, formats, 2, wrote)
	if err != nil {
		t.Fatal(err)
	}
	if st.Added != 5 || !slices.Equal(seen, []int{2, 4}) {
		t.Fatalf("added %d, batches reported %v; want 5 and [2 4]", st.Added, seen)
	}
}

func TestCancelledScanKeepsWrittenBatchesAndRemovesNothing(t *testing.T) {
	db := openDB(t)
	root := t.TempDir()
	old := touch(t, root, "Old/Gone/01 a.mp3", "x")
	if _, err := Scan(context.Background(), db, root, formats); err != nil {
		t.Fatal(err)
	}
	os.Remove(old)
	for i := range 4 {
		touch(t, root, fmt.Sprintf("New/Album/%02d x.mp3", i+1), "x")
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := scan(ctx, db, root, formats, 2, func(Stats) { cancel() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("scan cancelled after a batch = %v, want context.Canceled", err)
	}
	var paths []string
	rows, _ := db.Query(`SELECT path FROM tracks ORDER BY path`)
	for rows.Next() {
		var p string
		rows.Scan(&p)
		paths = append(paths, filepath.ToSlash(strings.TrimPrefix(p, root)))
	}
	rows.Close()
	if !slices.Equal(paths, []string{"/New/Album/01 x.mp3", "/New/Album/02 x.mp3", "/Old/Gone/01 a.mp3"}) {
		t.Fatalf("tracks after a cancelled scan: %v", paths)
	}
}

func TestAlbumDirSkipsDiscFolders(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{"/m/A/B/01.mp3", "/m/A/B"},
		{"/m/A/B/CD2/01.mp3", "/m/A/B"},
		{"/m/A/B/Disc 1/01.mp3", "/m/A/B"},
		{"/m/A/B/Disc 1 - Embers/01.mp3", "/m/A/B"},
		{"/m/A/B/[CD 2]/01.mp3", "/m/A/B"},
		{"/m/A/CD 1 Classics/01.mp3", "/m/A/CD 1 Classics"},
		{"/m/A/CD 01 - Abbey Road/01.mp3", "/m/A/CD 01 - Abbey Road"},
	} {
		if got := filepath.ToSlash(AlbumDir(filepath.FromSlash(c.path))); got != c.want {
			t.Errorf("AlbumDir(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestAddIndexesOneFile(t *testing.T) {
	db, root := openDB(t), t.TempDir()
	path := touch(t, root, "Boombrick/Lumen Coast/Night Drive Theory/01 Night Drive Theory.flac", "not really flac")
	if err := Add(db, root, path, formats); err != nil {
		t.Fatal(err)
	}
	albums, err := Albums(db)
	if err != nil || len(albums) != 1 {
		t.Fatalf("albums %+v, %v", albums, err)
	}
	if a := albums[0]; a.Artist != "Lumen Coast" || a.Title != "Night Drive Theory" || len(a.Tracks) != 1 ||
		a.Tracks[0].Number != 1 || a.Tracks[0].Duration != 90*time.Second {
		t.Fatalf("album %+v", a)
	}
	// A scan after it finds it as it left it.
	st, err := Scan(context.Background(), db, root, formats)
	if err != nil || st.Added != 0 || st.Updated != 0 || st.Total != 1 {
		t.Fatalf("scan %+v, %v", st, err)
	}
	if err := Add(db, root, filepath.Join(root, "missing.flac"), formats); err == nil {
		t.Fatal("a missing file was added")
	}
}

// Texts lists every name the library shows, once, whatever its script.
func TestTexts(t *testing.T) {
	db := openDB(t)
	changes := []change{
		{"/m/a.flac", 1, 1, Track{Title: "東京事変の新しいアルバム", Artist: "東京事変", Album: "大地", Genres: []string{"J-Pop", "Rock"}}},
		{"/m/b.flac", 1, 1, Track{Title: "مرحبا", Artist: "東京事変", AlbumArtist: "Odalys", Album: "大地"}},
	}
	if err := writeChanges(db, changes, nil, 1); err != nil {
		t.Fatal(err)
	}
	got, err := Texts(db)
	slices.Sort(got)
	want := []string{"J-Pop;Rock", "Odalys", "مرحبا", "大地", "東京事変", "東京事変の新しいアルバム"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("Texts = %q, %v; want %q", got, err, want)
	}
}

// The library knows the files an import downloaded by their Spotify IDs.
func TestFilesBySpotifyID(t *testing.T) {
	db := openDB(t)
	changes := []change{
		{"/m/Boombrick/Ed Sheeran/Sapphire/01 Sapphire.opus", 1, 1, Track{Title: "Sapphire", SpotifyID: "4Q0qVhFQa7j6jRKzo3HDmP"}},
		{"/m/Odalys/Copper Season/01 Rust.flac", 1, 1, Track{Title: "Rust"}},
	}
	if err := writeChanges(db, changes, nil, 1); err != nil {
		t.Fatal(err)
	}
	path, ok, err := FileOf(db, "4Q0qVhFQa7j6jRKzo3HDmP")
	if err != nil || !ok || path != changes[0].path {
		t.Fatalf("FileOf = %q, %v, %v", path, ok, err)
	}
	if _, ok, err := FileOf(db, "2IRZnDFmlqMuOrYOLnZZyc"); ok || err != nil {
		t.Fatalf("a song never downloaded: %v, %v", ok, err)
	}
	if _, ok, _ := FileOf(db, ""); ok {
		t.Fatal("no ID found a file")
	}
	var id, parent, unused int
	var plan string
	if err := db.QueryRow(`EXPLAIN QUERY PLAN `+fileOfQuery, "x").Scan(&id, &parent, &unused, &plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "tracks_spotify") {
		t.Fatalf("FileOf's plan is %q, not a search of tracks_spotify", plan)
	}
}

package download

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/store"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "boombrick.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.DB
}

func TestRowsKeep(t *testing.T) {
	db := openDB(t)
	a := row{song: ytmusic.Song{VideoID: "v1", Title: "Night Drive Theory", Artist: "Lumen Coast", Album: "Night Drive Theory",
		AlbumID: "MPREb_nd", AlbumArtist: "Lumen Coast", Number: 1, Year: 2023, Duration: 231 * time.Second,
		Thumb: "https://example.com/nd"}, state: queued, added: 100, priority: 5}
	b := row{song: ytmusic.Song{VideoID: "v2", Title: "Sodium Lights"}, state: failed, failure: NeedsUpdate,
		message: "HTTP Error 403", added: 101}
	if err := saveRows(db, []row{a, b}); err != nil {
		t.Fatal(err)
	}
	a.state, a.path, a.bytes, a.finished = saved, "/m/Boombrick/a.opus", 4_500_000, 200
	if err := saveRows(db, []row{a}); err != nil {
		t.Fatal(err)
	}
	got, err := loadRows(db)
	if err != nil || len(got) != 2 || !reflect.DeepEqual(*got["v1"], a) || !reflect.DeepEqual(*got["v2"], b) {
		t.Fatalf("loaded %+v, %v", got, err)
	}
	if err := saveRows(db, nil, "v2"); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadRows(db); len(got) != 1 {
		t.Fatalf("%d rows after a delete", len(got))
	}
	if err := saveRows(db, []row{b}, "v1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := loadRows(db); len(got) != 1 || got["v2"] == nil {
		t.Fatalf("a write and a delete together left %v", got)
	}
	if _, err := db.Exec(`INSERT INTO tracks (path, size, mtime, artist, album, title, number, added) VALUES
		('/m/1.opus', 1000, 0, '', '', '', 0, 0), ('/m/2.opus', 2500, 0, '', '', '', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if n, err := musicBytes(db); err != nil || n != 3500 {
		t.Fatalf("music bytes %d, %v", n, err)
	}
	// A file marked to be read again, -1 bytes until the scan gets to it,
	// adds nothing.
	if _, err := db.Exec(`INSERT INTO tracks (path, size, mtime, artist, album, title, number, added) VALUES
		('/m/3.opus', -1, 0, '', '', '', 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if n, err := musicBytes(db); err != nil || n != 3500 {
		t.Fatalf("music bytes with a file to read again %d, %v", n, err)
	}
}

package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOpenAppliesMigrationsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boombrick.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	var version int
	if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}

	var mode string
	if err := s.DB.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	var pages int
	if err := s.DB.QueryRow(`PRAGMA cache_size`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if pages != -512 {
		t.Fatalf("cache_size = %d, want -512", pages)
	}

	if _, err := s.DB.Exec(`INSERT INTO settings (key, value) VALUES ('k', 'v')`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening must not re-run migrations or lose data.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v string
	if err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = 'k'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "v" {
		t.Fatalf("value = %q", v)
	}
}

func TestMigrationThreeKeepsTracks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boombrick.db")
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{migrations[0], migrations[1], `PRAGMA user_version = 2`,
		`INSERT INTO tracks (path, size, mtime, artist, album, title, number, added)
			VALUES ('/m/a.mp3', 10, 1700000000, 'Odalys', 'Copper Season', 'Rust', 1003, 5)`} {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The size is migration 14's -1 by now (TestGenresMigrationRereadsTracks).
	var artist, albumArtist string
	var mtime, disc, number, year int
	var gain, peak sql.NullFloat64
	err = s.DB.QueryRow(`SELECT artist, album_artist, mtime, disc, number, year, gain, peak FROM tracks`).
		Scan(&artist, &albumArtist, &mtime, &disc, &number, &year, &gain, &peak)
	if err != nil {
		t.Fatal(err)
	}
	if artist != "Odalys" || albumArtist != "" || number != 1003 || disc != 0 || year != 0 ||
		gain.Valid || peak.Valid {
		t.Fatalf("row = %q %q disc %d number %d year %d gain %v peak %v",
			artist, albumArtist, disc, number, year, gain, peak)
	}
	// Migration 3 zeroes it so the next scan re-reads the file; 15 moves it on
	// a second.
	if mtime != 1 {
		t.Fatalf("mtime = %d, want 1 so the next scan re-reads the file", mtime)
	}
	for _, table := range []string{"queue", "playback_state", "progress"} {
		if _, err := s.DB.Exec(`SELECT 1 FROM ` + table); err != nil {
			t.Errorf("%s: %v", table, err)
		}
	}
}

// Migration 14 adds the genres column and has the next scan read every file's
// tags again to fill it.
func TestGenresMigrationRereadsTracks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boombrick.db")
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range append(migrations[:13:13], `PRAGMA user_version = 13`,
		`INSERT INTO tracks (path, size, mtime, artist, album, title, number, added)
			VALUES ('/m/a.mp3', 10, 1700000000, 'Odalys', 'Copper Season', 'Rust', 1, 5)`) {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var genres string
	var size, mtime int
	if err := s.DB.QueryRow(`SELECT genres, size, mtime FROM tracks`).Scan(&genres, &size, &mtime); err != nil {
		t.Fatal(err)
	}
	// No file is -1 bytes, so the next scan re-reads it. The mtime keeps its
	// order in Recent while that scan runs: migration 15 moves every one on
	// a second.
	if genres != "" || size != -1 || mtime != 1700000001 {
		t.Fatalf("genres %q, size %d, mtime %d; want none yet, -1 and 1700000001", genres, size, mtime)
	}
}

// Migration 15 has the next scan read every file again, for the disc numbers
// and album names a disc folder or an album's name gives. It moves each
// file's time on a second rather than spoiling its size, which the Downloads
// tab adds up while the scan runs.
func TestDiscMigrationRereadsTracks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boombrick.db")
	old, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range append(migrations[:14:14], `PRAGMA user_version = 14`,
		`INSERT INTO tracks (path, size, mtime, artist, album, title, number, added)
			VALUES ('/m/a.mp3', 10, 1700000000, 'Odalys', 'Copper Season (Disc 1)', 'Rust', 1, 5)`) {
		if _, err := old.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var size, mtime int
	if err := s.DB.QueryRow(`SELECT size, mtime FROM tracks`).Scan(&size, &mtime); err != nil {
		t.Fatal(err)
	}
	if size != 10 || mtime != 1700000001 {
		t.Fatalf("size %d, mtime %d; want 10 and 1700000001", size, mtime)
	}
}

func TestArtTable(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "boombrick.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO art (key, file, source, track, track_mtime, dir_mtime, source_mtime, hue, chroma, colour, looked_up)
		VALUES ('Odalys' || char(31) || 'Copper Season', 'abc', '/m/cover.jpg', '/m/01.mp3', 1, 2, 3, 35.5, 0.12, 1, 0)`); err != nil {
		t.Fatal(err)
	}
	var hue float64
	var colour bool
	if err := s.DB.QueryRow(`SELECT hue, colour FROM art`).Scan(&hue, &colour); err != nil || hue != 35.5 || !colour {
		t.Fatalf("read back hue %v colour %v: %v", hue, colour, err)
	}
}

func TestLyricsTable(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "boombrick.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO lyrics (path, body, instrumental, fetched) VALUES ('/m/1.flac', '[00:01.00]Rust', 0, 5)`); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := s.DB.QueryRow(`SELECT body FROM lyrics`).Scan(&body); err != nil || body != "[00:01.00]Rust" {
		t.Fatalf("read back %q: %v", body, err)
	}
}

func TestDownloadsTable(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "boombrick.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.Exec(`INSERT INTO downloads (video_id, title, artist, album, album_id, album_artist, number, year,
		duration_ms, thumb, state, failure, message, path, bytes, added, priority, finished)
		VALUES ('v1', 'Night Drive Theory', 'Lumen Coast', '', '', '', 0, 0, 231000, '', 0, 0, '', '', 0, 1, 0, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO queue (pos, path, title, artist, album_artist, album, gain, peak, has_gain, video_id)
		VALUES (0, '', 'Night Drive Theory', 'Lumen Coast', '', '', 0, 0, 0, 'v1')`); err != nil {
		t.Fatal(err)
	}
}

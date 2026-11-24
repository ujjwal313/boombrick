package settings

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/store"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st.DB
}

func load(t *testing.T, db *sql.DB) *Settings {
	t.Helper()
	s, err := Load(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestDefaults(t *testing.T) {
	s := load(t, openDB(t))
	if got := s.Int(StreamingKbps); got != 160 {
		t.Errorf("StreamingKbps = %d, want 160", got)
	}
	if !s.Bool(PreferDownloaded) {
		t.Error("PreferDownloaded should default to true")
	}
	if got := s.String(DownloadQuality); got != "best" {
		t.Errorf("DownloadQuality = %q, want best", got)
	}
	// Installs from before the switches keep both integrations.
	if !s.Bool(SpotifyOn) || !s.Bool(YouTubeOn) {
		t.Error("the integrations should default to on")
	}
	if !s.Bool(RadioOn) {
		t.Error("Radio should default to on")
	}
	s.SetBool(RadioOn, false)
	if s.Bool(RadioOn) {
		t.Error("Radio should read false once switched off")
	}
}

func TestSetPersists(t *testing.T) {
	db := openDB(t)
	s := load(t, db)
	s.Set(StreamingKbps, "320")
	s.SetBool(PreferDownloaded, false)
	if got := s.Int(StreamingKbps); got != 320 {
		t.Errorf("StreamingKbps = %d before it is written, want 320", got)
	}
	s.Close()

	again := load(t, db)
	if got := again.Int(StreamingKbps); got != 320 {
		t.Errorf("StreamingKbps = %d after reload, want 320", got)
	}
	if again.Bool(PreferDownloaded) {
		t.Error("PreferDownloaded should be false after reload")
	}
}

func TestSetDoesNotWaitForTheDatabase(t *testing.T) {
	db := openDB(t)
	s := load(t, db)
	// Another writer holds the database, as a library scan's batch does.
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES ('other', 'x')`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	s.Set(DownloadQuality, "smaller")
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("Set took %v while the database was busy", d)
	}
	if got := s.String(DownloadQuality); got != "smaller" {
		t.Errorf("DownloadQuality = %q, want smaller at once", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if got := load(t, db).String(DownloadQuality); got != "smaller" {
		t.Errorf("DownloadQuality = %q after reload, want smaller", got)
	}
}

// records counts the log's records.
type records struct{ n atomic.Int32 }

func (r *records) Enabled(context.Context, slog.Level) bool  { return true }
func (r *records) Handle(context.Context, slog.Record) error { r.n.Add(1); return nil }
func (r *records) WithAttrs([]slog.Attr) slog.Handler        { return r }
func (r *records) WithGroup(string) slog.Handler             { return r }

// A setting whose write fails stays waiting, and is written with the next.
func TestAFailedWriteIsTriedAgain(t *testing.T) {
	db := openDB(t)
	logged := &records{}
	s, err := Load(db, slog.New(logged))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := db.Exec(`CREATE TRIGGER no_settings BEFORE INSERT ON settings BEGIN SELECT RAISE(ABORT, 'the card is read-only'); END`); err != nil {
		t.Fatal(err)
	}
	s.Set(DownloadQuality, "smaller")
	for deadline := time.Now().Add(3 * time.Second); logged.n.Load() == 0; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the write never failed")
		}
	}
	if _, err := db.Exec(`DROP TRIGGER no_settings`); err != nil {
		t.Fatal(err)
	}
	s.SetBool(VolumeLevelling, false)
	s.Close()
	again := load(t, db)
	if again.String(DownloadQuality) != "smaller" || again.Bool(VolumeLevelling) {
		t.Fatalf("after reload: quality %q, levelling %v", again.String(DownloadQuality), again.Bool(VolumeLevelling))
	}
}

func TestInvalidValueFallsBackToDefault(t *testing.T) {
	s := load(t, openDB(t))
	s.Set(StreamingKbps, "lots")
	if got := s.Int(StreamingKbps); got != 160 {
		t.Errorf("StreamingKbps = %d, want default 160", got)
	}
}

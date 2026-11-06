package store

import (
	"database/sql"
	"fmt"
)

// migrations run in order; PRAGMA user_version records how many have been
// applied. Append new entries and never edit ones that have shipped.
var migrations = []string{
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	) WITHOUT ROWID`,
	`CREATE TABLE tracks (
		id     INTEGER PRIMARY KEY,
		path   TEXT NOT NULL UNIQUE,
		size   INTEGER NOT NULL,
		mtime  INTEGER NOT NULL,
		artist TEXT NOT NULL,
		album  TEXT NOT NULL,
		title  TEXT NOT NULL,
		number INTEGER NOT NULL,
		added  INTEGER NOT NULL
	);
	CREATE INDEX tracks_album ON tracks (artist, album)`,
	`ALTER TABLE tracks ADD COLUMN album_artist TEXT NOT NULL DEFAULT '';
	ALTER TABLE tracks ADD COLUMN disc INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE tracks ADD COLUMN year INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE tracks ADD COLUMN gain REAL;
	ALTER TABLE tracks ADD COLUMN peak REAL;
	UPDATE tracks SET mtime = 0;
	CREATE TABLE queue (
		pos          INTEGER PRIMARY KEY,
		path         TEXT NOT NULL,
		title        TEXT NOT NULL,
		artist       TEXT NOT NULL,
		album_artist TEXT NOT NULL,
		album        TEXT NOT NULL,
		gain         REAL NOT NULL,
		peak         REAL NOT NULL,
		has_gain     INTEGER NOT NULL
	);
	CREATE TABLE playback_state (
		id         INTEGER PRIMARY KEY CHECK (id = 1),
		pos        INTEGER NOT NULL,
		frame      INTEGER NOT NULL,
		shuffle    INTEGER NOT NULL,
		repeat     INTEGER NOT NULL,
		play_order TEXT NOT NULL
	);
	CREATE TABLE progress (
		artist   TEXT NOT NULL,
		album    TEXT NOT NULL,
		path     TEXT NOT NULL,
		frame    INTEGER NOT NULL,
		fraction REAL NOT NULL,
		updated  INTEGER NOT NULL,
		PRIMARY KEY (artist, album)
	)`,
	`ALTER TABLE tracks ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE tracks ADD COLUMN album_gain REAL;
	ALTER TABLE tracks ADD COLUMN album_peak REAL;
	UPDATE tracks SET mtime = 0;
	ALTER TABLE queue ADD COLUMN duration_ms INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE queue ADD COLUMN album_gain REAL NOT NULL DEFAULT 0;
	ALTER TABLE queue ADD COLUMN album_peak REAL NOT NULL DEFAULT 0;
	ALTER TABLE queue ADD COLUMN has_album_gain INTEGER NOT NULL DEFAULT 0;
	UPDATE progress SET updated = updated * 1000`,
	`CREATE TABLE art (
		key          TEXT PRIMARY KEY,
		file         TEXT NOT NULL,
		source       TEXT NOT NULL,
		track        TEXT NOT NULL,
		track_mtime  INTEGER NOT NULL,
		dir_mtime    INTEGER NOT NULL,
		source_mtime INTEGER NOT NULL,
		hue          REAL NOT NULL,
		chroma       REAL NOT NULL,
		colour       INTEGER NOT NULL,
		looked_up    INTEGER NOT NULL
	)`,
	`CREATE TABLE lyrics (
		path         TEXT PRIMARY KEY,
		body         TEXT NOT NULL,
		instrumental INTEGER NOT NULL,
		fetched      INTEGER NOT NULL
	)`,
	`ALTER TABLE queue ADD COLUMN video_id TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE downloads (
		video_id     TEXT PRIMARY KEY,
		title        TEXT NOT NULL,
		artist       TEXT NOT NULL,
		album        TEXT NOT NULL,
		album_id     TEXT NOT NULL,
		album_artist TEXT NOT NULL,
		number       INTEGER NOT NULL,
		year         INTEGER NOT NULL,
		duration_ms  INTEGER NOT NULL,
		thumb        TEXT NOT NULL,
		state        INTEGER NOT NULL,
		failure      INTEGER NOT NULL,
		message      TEXT NOT NULL,
		path         TEXT NOT NULL,
		bytes        INTEGER NOT NULL,
		added        INTEGER NOT NULL,
		priority     INTEGER NOT NULL,
		finished     INTEGER NOT NULL
	) WITHOUT ROWID`,
	`ALTER TABLE queue ADD COLUMN spotify_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE queue ADD COLUMN cover TEXT NOT NULL DEFAULT '';
	CREATE TABLE spotify_cache (
		key     TEXT PRIMARY KEY,
		body    TEXT NOT NULL,
		fetched INTEGER NOT NULL
	) WITHOUT ROWID`,
	`ALTER TABLE downloads ADD COLUMN spotify_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE downloads ADD COLUMN isrc TEXT NOT NULL DEFAULT '';
	ALTER TABLE tracks ADD COLUMN spotify_id TEXT NOT NULL DEFAULT '';
	CREATE INDEX tracks_spotify ON tracks (spotify_id) WHERE spotify_id != '';
	CREATE TABLE import_jobs (
		uri     TEXT PRIMARY KEY,
		name    TEXT NOT NULL,
		created INTEGER NOT NULL
	) WITHOUT ROWID;
	CREATE TABLE import_items (
		uri        TEXT NOT NULL,
		spotify_id TEXT NOT NULL,
		pos        INTEGER NOT NULL,
		track      TEXT NOT NULL,
		state      INTEGER NOT NULL,
		chosen     INTEGER NOT NULL,
		candidates TEXT NOT NULL,
		PRIMARY KEY (uri, spotify_id)
	) WITHOUT ROWID`,
	// Background play: whether music was playing, and the sleep timer, as a
	// hand-over between Boombrick and its background player leaves them.
	`ALTER TABLE playback_state ADD COLUMN playing INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE playback_state ADD COLUMN sleep_mode INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE playback_state ADD COLUMN sleep_at INTEGER NOT NULL DEFAULT 0`,
	// An import looks songs up in the downloads by Spotify ID. No query
	// reads tracks by artist and album.
	`CREATE INDEX downloads_spotify ON downloads (spotify_id) WHERE spotify_id != '';
	DROP INDEX tracks_album`,
	// A song's genres, ";"-separated. The next scan reads every file's tags
	// again to fill them in: no file is -1 bytes. The mtime stays, as it orders
	// albums added by one scan in Recent while this one runs.
	`ALTER TABLE tracks ADD COLUMN genres TEXT NOT NULL DEFAULT '';
	UPDATE tracks SET size = -1`,
	// Discs named by their folders ("Disc 1 - Embers") and by their albums'
	// names ("Copper Season (Disc 2)"): the next scan reads every file again,
	// as each stored time is now a second off. The sizes stay right for the
	// Downloads tab while the scan runs, and Recent keeps its order.
	`UPDATE tracks SET mtime = mtime + 1`,
}

func migrate(db *sql.DB) error {
	var applied int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&applied); err != nil {
		return err
	}
	for i := applied; i < len(migrations); i++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

package download

import (
	"database/sql"
	"time"

	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// Row states in the downloads table. A download under way isn't stored as
// such: after a restart its row is still queued and its partial file
// resumes.
const (
	queued = iota
	failed
	saved
)

// row is a song in the downloads table.
type row struct {
	song     ytmusic.Song
	state    int
	failure  Failure
	message  string
	path     string // a saved song's file
	bytes    int64
	added    int64 // ms: the queue's order
	priority int64 // ms, when last asked to come first; the highest goes first
	finished int64 // ms, when saved
	landed   bool  // in memory only: its file has landed and is being finished
}

const rowColumns = `video_id, title, artist, album, album_id, album_artist, number, year, duration_ms, thumb,
	state, failure, message, path, bytes, added, priority, finished, spotify_id, isrc`

// loadRows reads the table, by video ID.
func loadRows(db *sql.DB) (map[string]*row, error) {
	rs, err := db.Query(`SELECT ` + rowColumns + ` FROM downloads`)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	rows := map[string]*row{}
	for rs.Next() {
		r := &row{}
		var ms int64
		if err := rs.Scan(&r.song.VideoID, &r.song.Title, &r.song.Artist, &r.song.Album, &r.song.AlbumID,
			&r.song.AlbumArtist, &r.song.Number, &r.song.Year, &ms, &r.song.Thumb, &r.state, &r.failure, &r.message,
			&r.path, &r.bytes, &r.added, &r.priority, &r.finished, &r.song.SpotifyID, &r.song.ISRC); err != nil {
			return nil, err
		}
		r.song.Duration = time.Duration(ms) * time.Millisecond
		rows[r.song.VideoID] = r
	}
	return rows, rs.Err()
}

// saveRows writes rows, new or changed, and deletes those of the songs gone,
// in one transaction.
func saveRows(db *sql.DB, rows []row, gone ...string) error {
	if len(rows) == 0 && len(gone) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(`INSERT INTO downloads (` + rowColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (video_id) DO UPDATE SET title = excluded.title, artist = excluded.artist, album = excluded.album,
			album_id = excluded.album_id, album_artist = excluded.album_artist, number = excluded.number,
			year = excluded.year, duration_ms = excluded.duration_ms, thumb = excluded.thumb, state = excluded.state,
			failure = excluded.failure, message = excluded.message, path = excluded.path, bytes = excluded.bytes,
			added = excluded.added, priority = excluded.priority, finished = excluded.finished,
			spotify_id = excluded.spotify_id, isrc = excluded.isrc`)
	if err != nil {
		return err
	}
	defer st.Close()
	for _, r := range rows {
		s := r.song
		if _, err := st.Exec(s.VideoID, s.Title, s.Artist, s.Album, s.AlbumID, s.AlbumArtist, s.Number, s.Year,
			s.Duration.Milliseconds(), s.Thumb, r.state, r.failure, r.message, r.path, r.bytes, r.added, r.priority,
			r.finished, s.SpotifyID, s.ISRC); err != nil {
			return err
		}
	}
	del, err := tx.Prepare(`DELETE FROM downloads WHERE video_id = ?`)
	if err != nil {
		return err
	}
	defer del.Close()
	for _, id := range gone {
		if _, err := del.Exec(id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// unindexed is the saved songs whose files the library has no track for: their
// IDs and paths.
func unindexed(db *sql.DB) ([]row, error) {
	rs, err := db.Query(`SELECT video_id, path FROM downloads AS d
		WHERE state = ? AND NOT EXISTS (SELECT 1 FROM tracks WHERE path = d.path)`, saved)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.song.VideoID, &r.path); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

// musicBytes is the size of the library's files, for the Downloads tab's
// storage card. A file an update marked to be read again is -1 bytes until
// the scan gets to it, and adds nothing.
func musicBytes(db *sql.DB) (int64, error) {
	var n int64
	err := db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM tracks WHERE size > 0`).Scan(&n)
	return n, err
}

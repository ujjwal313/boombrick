// Package resume saves the play queue and position so playback carries on
// after a restart, and remembers albums the listener is part-way through.
package resume

import (
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
)

// finished is how far through an album counts as done listening.
const finished = 0.98

// Saver saves snapshots as playback goes on, rewriting the saved queue only
// when it has changed since the last save. It is not safe for concurrent use.
type Saver struct {
	db      *sql.DB
	version uint64
	saved   bool
}

// NewSaver creates a saver writing to db.
func NewSaver(db *sql.DB) *Saver { return &Saver{db: db} }

// Save stores s.
func (sv *Saver) Save(s player.Snapshot) error {
	if err := save(sv.db, s, !sv.saved || s.Version != sv.version); err != nil {
		return err
	}
	sv.version, sv.saved = s.Version, true
	return nil
}

// save stores s, rewriting the queue's rows and its play order only if queue
// is set: the order changes only with the queue's version. The rest of the
// playback state is written either way, and so is the album's progress when
// the album was queued as one: a song played from the list of songs, or added
// on its own, says nothing about how far through its album the listener is.
func save(db *sql.DB, s player.Snapshot, queue bool) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	set := `pos = excluded.pos, frame = excluded.frame, shuffle = excluded.shuffle, repeat = excluded.repeat`
	var order []string
	if queue {
		if err := saveQueue(tx, s.Queue); err != nil {
			return err
		}
		order = make([]string, len(s.Order))
		for i, v := range s.Order {
			order[i] = strconv.Itoa(v)
		}
		set += `, play_order = excluded.play_order`
	}
	if _, err := tx.Exec(`INSERT INTO playback_state (id, pos, frame, shuffle, repeat, play_order) VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET `+set,
		s.Pos, s.Frame, s.Shuffle, int(s.Repeat), strings.Join(order, ",")); err != nil {
		return err
	}
	if s.AlbumRun && s.Pos >= 0 && s.Pos < len(s.Order) && s.Order[s.Pos] >= 0 && s.Order[s.Pos] < len(s.Queue) &&
		s.Queue[s.Order[s.Pos]].Path != "" { // a song downloading or streamed has no file to carry on from
		cur := s.Queue[s.Order[s.Pos]]
		artist := cur.AlbumArtist
		if artist == "" {
			artist = cur.Artist
		}
		if _, err := tx.Exec(`INSERT INTO progress (artist, album, path, frame, fraction, updated) VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (artist, album) DO UPDATE SET path = excluded.path, frame = excluded.frame,
				fraction = excluded.fraction, updated = excluded.updated`,
			artist, cur.Album, cur.Path, s.Frame, s.Fraction, time.Now().UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// saveQueue replaces the saved queue's rows with queue.
func saveQueue(tx *sql.Tx, queue []player.Track) error {
	if _, err := tx.Exec(`DELETE FROM queue`); err != nil {
		return err
	}
	ins, err := tx.Prepare(`INSERT INTO queue (pos, path, title, artist, album_artist, album, gain, peak, has_gain, album_gain, album_peak, has_album_gain, duration_ms, video_id, spotify_id, cover, station_id, stream_url)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for i, t := range queue {
		if _, err := ins.Exec(i, t.Path, t.Title, t.Artist, t.AlbumArtist, t.Album, t.Gain, t.Peak, t.HasGain,
			t.AlbumGain, t.AlbumPeak, t.HasAlbumGain, t.Duration.Milliseconds(), t.VideoID, t.SpotifyID, t.Cover, t.StationID, t.Stream); err != nil {
			return err
		}
	}
	return nil
}

// Load returns the saved queue; ok is false when there is none, or when its
// order doesn't fit it.
func Load(db *sql.DB) (s player.Snapshot, ok bool, err error) {
	var order string
	var repeat int
	err = db.QueryRow(`SELECT pos, frame, shuffle, repeat, play_order FROM playback_state WHERE id = 1`).
		Scan(&s.Pos, &s.Frame, &s.Shuffle, &repeat, &order)
	if err == sql.ErrNoRows {
		return player.Snapshot{}, false, nil
	}
	if err != nil {
		return player.Snapshot{}, false, err
	}
	s.Repeat = player.Repeat(repeat)
	for _, f := range strings.Split(order, ",") {
		if f == "" {
			continue
		}
		v, err := strconv.Atoi(f)
		if err != nil {
			return player.Snapshot{}, false, err
		}
		s.Order = append(s.Order, v)
	}
	rows, err := db.Query(`SELECT path, title, artist, album_artist, album, gain, peak, has_gain,
		album_gain, album_peak, has_album_gain, duration_ms, video_id, spotify_id, cover, station_id, stream_url FROM queue ORDER BY pos`)
	if err != nil {
		return player.Snapshot{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var t player.Track
		var ms int64
		if err := rows.Scan(&t.Path, &t.Title, &t.Artist, &t.AlbumArtist, &t.Album, &t.Gain, &t.Peak, &t.HasGain,
			&t.AlbumGain, &t.AlbumPeak, &t.HasAlbumGain, &ms, &t.VideoID, &t.SpotifyID, &t.Cover, &t.StationID, &t.Stream); err != nil {
			return player.Snapshot{}, false, err
		}
		t.Duration = time.Duration(ms) * time.Millisecond
		s.Queue = append(s.Queue, t)
	}
	if err := rows.Err(); err != nil {
		return player.Snapshot{}, false, err
	}
	if !consistent(s) {
		return player.Snapshot{}, false, nil
	}
	return s, true, nil
}

// consistent reports whether s has a queue whose order and position fit it.
func consistent(s player.Snapshot) bool {
	if len(s.Queue) == 0 || len(s.Order) != len(s.Queue) || s.Pos < 0 || s.Pos >= len(s.Order) {
		return false
	}
	for _, i := range s.Order {
		if i < 0 || i >= len(s.Queue) {
			return false
		}
	}
	return true
}

// Progress is an album the listener is part-way through.
type Progress struct {
	Artist, Album, Path string
	Frame               int64
	Fraction            float64
	Updated             time.Time
}

// Recent lists unfinished albums, most recently played first.
func Recent(db *sql.DB, limit int) ([]Progress, error) {
	rows, err := db.Query(`SELECT artist, album, path, frame, fraction, updated FROM progress
		WHERE fraction < ? ORDER BY updated DESC LIMIT ?`, finished, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Progress
	for rows.Next() {
		var p Progress
		var updated int64
		if err := rows.Scan(&p.Artist, &p.Album, &p.Path, &p.Frame, &p.Fraction, &updated); err != nil {
			return nil, err
		}
		p.Updated = time.UnixMilli(updated)
		out = append(out, p)
	}
	return out, rows.Err()
}

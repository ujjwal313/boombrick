// Package library indexes the music on the SD card into the tracks table and
// answers questions about it.
package library

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Track is one indexed file.
type Track struct {
	ID                                int64
	Path                              string
	Artist, AlbumArtist, Album, Title string
	Disc, Number, Year                int
	Gain, Peak                        float64 // ReplayGain track gain (dB) and peak
	HasGain                           bool
	AlbumGain, AlbumPeak              float64 // ReplayGain album gain (dB) and peak
	HasAlbumGain                      bool
	Duration                          time.Duration // 0 when unknown
	SpotifyID                         string        // the Spotify song an import downloaded this as
	Genres                            []string      // nil when untagged
}

// Album is the tracks of one album, in disc and track order. Artist is the
// album artist, or the track artist when no album artist is tagged.
type Album struct {
	Artist, Title string
	Year          int
	Tracks        []Track
	Duration      time.Duration // the tracks' lengths added up
	added         int64
	mtime         int64 // the newest file's, to order albums added by one scan
}

// bookExt is the extension of an audiobook: a file with it, on its own or in
// parts, is a book, not an album.
const bookExt = ".m4b"

// IsBook reports whether the track is an audiobook.
func (t Track) IsBook() bool { return strings.EqualFold(filepath.Ext(t.Path), bookExt) }

// IsBook reports whether the album is an audiobook: it has tracks, and all
// of them are books.
func (a Album) IsBook() bool {
	for _, t := range a.Tracks {
		if !t.IsBook() {
			return false
		}
	}
	return len(a.Tracks) > 0
}

// Stats summarises a scan.
type Stats struct {
	Added, Updated, Removed, Total int
}

// Formats is what the scanner needs from the decoders: which files it can
// play, and how long each one is.
type Formats struct {
	Supported func(path string) bool
	Duration  func(path string) (time.Duration, error)
}

// batchSize is how many changed files a scan writes in one transaction. The
// first scan of a big card then shows albums while it runs.
const batchSize = 500

const upsertTrack = `INSERT INTO tracks (path, size, mtime, artist, album_artist, album, title, disc, number, year, gain, peak, album_gain, album_peak, duration_ms, added, spotify_id, genres)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime, artist = excluded.artist,
		album_artist = excluded.album_artist, album = excluded.album, title = excluded.title, disc = excluded.disc,
		number = excluded.number, year = excluded.year, gain = excluded.gain, peak = excluded.peak,
		album_gain = excluded.album_gain, album_peak = excluded.album_peak,
		duration_ms = excluded.duration_ms, spotify_id = excluded.spotify_id, genres = excluded.genres`

// Scan walks root and brings the tracks table in line with it: new and changed
// files are indexed, rows for missing files removed. Only files f supports
// are indexed; hidden files and folders are skipped. A root that can't
// be read leaves the index untouched. Files are read without a transaction
// open, since tags on a slow card take a while, and changes are written in
// short transactions of up to 500 files. Rows for missing files go only once
// the whole walk has finished. Cancelling ctx stops the walk, keeps the
// batches already written, removes nothing and returns the context's error.
func Scan(ctx context.Context, db *sql.DB, root string, f Formats) (Stats, error) {
	return scan(ctx, db, root, f, batchSize, nil)
}

// ScanWithProgress is Scan, calling wrote with the counts so far each time a
// batch has been written before the end.
func ScanWithProgress(ctx context.Context, db *sql.DB, root string, f Formats, wrote func(Stats)) (Stats, error) {
	return scan(ctx, db, root, f, batchSize, wrote)
}

// Add indexes the file at path, under root, as the next scan would, for a
// file Boombrick has just written there.
func Add(db *sql.DB, root, path string, f Formats) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	c := change{path, info.Size(), info.ModTime().Unix(), describe(root, path, f)}
	return writeChanges(db, []change{c}, nil, time.Now().Unix())
}

func scan(ctx context.Context, db *sql.DB, root string, f Formats, batch int, wrote func(Stats)) (Stats, error) {
	// An unreadable root (card not mounted, folder missing) says nothing about
	// what is indexed, so leave the table alone.
	if _, err := os.ReadDir(root); err != nil {
		return Stats{}, nil
	}
	known := map[string][2]int64{}
	rows, err := db.Query(`SELECT path, size, mtime FROM tracks`)
	if err != nil {
		return Stats{}, err
	}
	for rows.Next() {
		var path string
		var size, mtime int64
		if err := rows.Scan(&path, &size, &mtime); err != nil {
			rows.Close()
			return Stats{}, err
		}
		known[path] = [2]int64{size, mtime}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Stats{}, err
	}

	now := time.Now().Unix()
	var st Stats
	var changes []change
	seen := map[string]bool{}
	var unreadable []string // folders the walk could not list
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if d != nil && d.IsDir() {
				unreadable = append(unreadable, path)
			} else {
				seen[path] = true // keep the row of a file we could not stat
			}
			return nil
		}
		if path != root && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !f.Supported(path) {
			return nil
		}
		seen[path] = true
		info, err := d.Info()
		if err != nil {
			return nil
		}
		size, mtime := info.Size(), info.ModTime().Unix()
		old, indexed := known[path]
		if indexed && old == [2]int64{size, mtime} {
			return nil
		}
		changes = append(changes, change{path, size, mtime, describe(root, path, f)})
		if indexed {
			st.Updated++
		} else {
			st.Added++
		}
		if len(changes) >= batch {
			if err := writeChanges(db, changes, nil, now); err != nil {
				return err
			}
			changes = changes[:0]
			if wrote != nil {
				wrote(st)
			}
		}
		return nil
	})
	if err != nil {
		return Stats{}, err
	}

	var gone []string
	for path := range known {
		if !seen[path] && !under(path, unreadable) {
			gone = append(gone, path)
		}
	}
	if err := writeChanges(db, changes, gone, now); err != nil {
		return Stats{}, err
	}
	st.Removed = len(gone)
	st.Total = len(seen)
	return st, nil
}

// change is a file the scan found new or changed.
type change struct {
	path        string
	size, mtime int64
	t           Track
}

// writeChanges stores changes, stamped as added at now, and deletes the rows
// for the paths in gone, in one transaction.
func writeChanges(db *sql.DB, changes []change, gone []string, now int64) error {
	if len(changes) == 0 && len(gone) == 0 {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	upsert, err := tx.Prepare(upsertTrack)
	if err != nil {
		return err
	}
	defer upsert.Close()
	for _, c := range changes {
		t := c.t
		if _, err := upsert.Exec(c.path, c.size, c.mtime, t.Artist, t.AlbumArtist, t.Album, t.Title, t.Disc, t.Number,
			t.Year, nullable(t.Gain, t.HasGain), nullable(t.Peak, t.HasGain), nullable(t.AlbumGain, t.HasAlbumGain),
			nullable(t.AlbumPeak, t.HasAlbumGain), t.Duration.Milliseconds(), now, t.SpotifyID,
			strings.Join(t.Genres, ";")); err != nil {
			return err
		}
	}
	for _, path := range gone {
		if _, err := tx.Exec(`DELETE FROM tracks WHERE path = ?`, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// describe is a file's metadata: its tags where it has them, its place in the
// folder tree otherwise.
func describe(root, path string, f Formats) Track {
	artist, album, title, disc, number := ParsePath(root, path)
	t := Track{Artist: artist, Album: album, Title: title, Disc: disc, Number: number}
	t.Duration, _ = f.Duration(path) // an unreadable length stays 0
	tags, err := ReadTags(path)
	if err != nil {
		return t
	}
	for _, s := range []struct {
		dst *string
		v   string
	}{
		{&t.Title, tags.Title}, {&t.Artist, tags.Artist}, {&t.Album, tags.Album}, {&t.AlbumArtist, tags.AlbumArtist},
	} {
		if s.v != "" {
			*s.dst = s.v
		}
	}
	if tags.Track > 0 {
		t.Number = tags.Track
	}
	if tags.Disc > 0 {
		t.Disc = tags.Disc
	}
	t.Year, t.Gain, t.Peak, t.HasGain = tags.Year, tags.Gain, tags.Peak, tags.HasGain
	t.AlbumGain, t.AlbumPeak, t.HasAlbumGain = tags.AlbumGain, tags.AlbumPeak, tags.HasAlbumGain
	t.SpotifyID, t.Genres = tags.SpotifyID, tags.Genres
	return t
}

// fileOfQuery finds a track by its Spotify ID. SQLite uses the partial index
// tracks_spotify only when the query repeats the index's condition.
const fileOfQuery = `SELECT path FROM tracks WHERE spotify_id = ? AND spotify_id != '' LIMIT 1`

// FileOf is the path of the library's file of Spotify song id, as an import
// downloaded it; ok is false when there is none.
func FileOf(db *sql.DB, id string) (path string, ok bool, err error) {
	if id == "" {
		return "", false, nil
	}
	err = db.QueryRow(fileOfQuery, id).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return path, err == nil, err
}

func nullable(v float64, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

// under reports whether path is inside one of dirs.
func under(path string, dirs []string) bool {
	for _, d := range dirs {
		if strings.HasPrefix(path, d+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// discFolder matches folder names like "CD1", "CD 2", "Disc 2" and "disk3".
var discFolder = regexp.MustCompile(`(?i)^(?:cd|disc|disk)\s*(\d+)$`)

// The names ParsePath gives a file with no folder to take them from. The art
// cache doesn't search online for them.
const (
	UnknownArtist = "Unknown artist"
	UnknownAlbum  = "Unknown album"
)

// ParsePath derives metadata from where a file sits: Artist/Album/NN Title.ext.
// A missing level becomes "Unknown artist" or "Unknown album"; deeper trees use
// the two folders closest to the file. A disc folder (CD1, Disc 2) between the
// album and the file is skipped and its number becomes the disc, unless the
// file name gives one ("1-07 Title"). Without either, disc is 0.
func ParsePath(root, path string) (artist, album, title string, disc, number int) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	dirs, file := parts[:len(parts)-1], parts[len(parts)-1]
	folderDisc := 0
	if len(dirs) > 0 {
		if m := discFolder.FindStringSubmatch(dirs[len(dirs)-1]); m != nil {
			folderDisc, _ = strconv.Atoi(m[1])
			dirs = dirs[:len(dirs)-1]
		}
	}
	artist, album = UnknownArtist, UnknownAlbum
	switch {
	case len(dirs) >= 2:
		artist, album = dirs[len(dirs)-2], dirs[len(dirs)-1]
	case len(dirs) == 1:
		album = dirs[0]
	}
	disc, number, title = splitNumber(strings.TrimSuffix(file, filepath.Ext(file)))
	if disc == 0 {
		disc = folderDisc
	}
	return artist, album, title, disc, number
}

// splitNumber splits a leading track number of up to three digits from a file
// name: "07 Title", "07 - Title", "07. Title" and a disc-track "1-07 Title" all
// give 7 and "Title", the last with disc 1 (the others have disc 0). Names that
// are only a number keep it as the title.
func splitNumber(name string) (disc, number int, title string) {
	digits := func(i int) int {
		j := i
		for j < len(name) && name[j] >= '0' && name[j] <= '9' {
			j++
		}
		return j
	}
	end := digits(0)
	if end == 0 || end > 3 {
		return 0, 0, name
	}
	num := name[:end]
	if end+1 < len(name) && name[end] == '-' {
		if e := digits(end + 1); e > end+1 && e-end-1 <= 3 {
			disc, _ = strconv.Atoi(num)
			num, end = name[end+1:e], e
		}
	}
	rest := strings.TrimLeft(name[end:], " .-_")
	if rest == "" || rest == name[end:] {
		return 0, 0, name
	}
	number, _ = strconv.Atoi(num)
	return disc, number, rest
}

// variousArtists names an album without an album-artist tag whose tracks have
// different artists.
const variousArtists = "Various Artists"

// Albums lists every album, most recently added first, with its tracks in disc
// and track order; albums added by the same scan, as all are by the first,
// come newest file first. Tracks group by album-artist tag and album; tracks
// without an album artist group by album and folder, so a compilation missing
// the tag stays one album. An album's Artist is its album artist, its tracks'
// shared artist, or "Various Artists", and its tracks' AlbumArtist is set to
// match so every part of the app keys the album the same way.
func Albums(db *sql.DB) ([]Album, error) {
	rows, err := db.Query(`SELECT path, artist, album_artist, album, title, disc, number, year, gain, peak, album_gain, album_peak,
			duration_ms, added, mtime, spotify_id, genres
		FROM tracks ORDER BY disc, number, title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type key struct{ artist, dir, album string }
	index := map[key]int{}
	var albums []Album
	for rows.Next() {
		t, added, mtime, err := scanTrack(rows)
		if err != nil {
			return nil, err
		}
		k := key{artist: t.AlbumArtist, album: t.Album}
		if t.AlbumArtist == "" {
			k.dir = AlbumDir(t.Path)
		}
		i, ok := index[k]
		if !ok {
			i = len(albums)
			index[k] = i
			albums = append(albums, Album{Artist: t.AlbumArtist, Title: t.Album})
		}
		a := &albums[i]
		a.Tracks = append(a.Tracks, t)
		a.added, a.mtime = max(a.added, added), max(a.mtime, mtime)
		a.Year = max(a.Year, t.Year)
		a.Duration += t.Duration
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Groups that end up with the same artist and title are one album: the index
	// and the player find an album by that pair.
	merged := albums[:0]
	byName := map[[2]string]int{}
	for _, a := range albums {
		if a.Artist == "" {
			a.Artist = sharedArtist(a.Tracks)
		}
		for j := range a.Tracks {
			a.Tracks[j].AlbumArtist = a.Artist
		}
		name := [2]string{a.Artist, a.Title}
		i, ok := byName[name]
		if !ok {
			byName[name] = len(merged)
			merged = append(merged, a)
			continue
		}
		m := &merged[i]
		m.Tracks = append(m.Tracks, a.Tracks...)
		sort.SliceStable(m.Tracks, func(x, y int) bool {
			p, q := m.Tracks[x], m.Tracks[y]
			if p.Disc != q.Disc {
				return p.Disc < q.Disc
			}
			if p.Number != q.Number {
				return p.Number < q.Number
			}
			return p.Title < q.Title
		})
		m.added, m.mtime = max(m.added, a.added), max(m.mtime, a.mtime)
		m.Year = max(m.Year, a.Year)
		m.Duration += a.Duration
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].added != merged[j].added {
			return merged[i].added > merged[j].added
		}
		return merged[i].mtime > merged[j].mtime
	})
	return merged, nil
}

// AlbumDir is the folder an album's files sit in, not counting a disc folder
// (CD1, Disc 2) inside it.
func AlbumDir(path string) string {
	dir := filepath.Dir(path)
	if discFolder.MatchString(filepath.Base(dir)) {
		dir = filepath.Dir(dir)
	}
	return dir
}

// scanTrack reads one row of the Albums query: a track, when it was added and
// its file's modification time.
func scanTrack(rows *sql.Rows) (t Track, added, mtime int64, err error) {
	var gain, peak, albumGain, albumPeak sql.NullFloat64
	var ms int64
	var genres string
	if err := rows.Scan(&t.Path, &t.Artist, &t.AlbumArtist, &t.Album, &t.Title, &t.Disc, &t.Number,
		&t.Year, &gain, &peak, &albumGain, &albumPeak, &ms, &added, &mtime, &t.SpotifyID, &genres); err != nil {
		return Track{}, 0, 0, err
	}
	if genres != "" {
		t.Genres = strings.Split(genres, ";")
	}
	t.Gain, t.Peak, t.HasGain = gain.Float64, peak.Float64, gain.Valid
	t.AlbumGain, t.AlbumPeak, t.HasAlbumGain = albumGain.Float64, albumPeak.Float64, albumGain.Valid
	t.Duration = time.Duration(ms) * time.Millisecond
	return t, added, mtime, nil
}

// sharedArtist is the tracks' artist if they all have the same one.
func sharedArtist(ts []Track) string {
	for _, t := range ts[1:] {
		if t.Artist != ts[0].Artist {
			return variousArtists
		}
	}
	return ts[0].Artist
}

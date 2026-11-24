package importer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// Item is one song of an import.
type Item struct {
	Track      spotify.Track
	State      State
	Candidates []Candidate // the best first
	Chosen     int         // the candidate queued, once Matched or Accepted
}

// Job is one playlist's import, its songs in the playlist's order.
type Job struct {
	URI, Name string
	Items     []Item
}

// Count is how many of the job's songs are in state s.
func (j Job) Count(s State) int {
	n := 0
	for _, it := range j.Items {
		if it.State == s {
			n++
		}
	}
	return n
}

// Changes is what changed of an import after a version: the songs that did,
// at their places in it, or all of them when they were listed again.
type Changes struct {
	Version uint64 // the version it is as of, to ask after the next time
	All     bool   // Items is all the import's songs
	At      []int  // otherwise, where each of Items is in the import
	Items   []Item
}

// Options configure the importer.
type Options struct {
	DB *sql.DB
	// Search asks YouTube Music; (*ytmusic.Client).Search is it.
	Search func(ctx context.Context, query string, f ytmusic.Filter) ([]ytmusic.Item, error)
	// Get queues downloads, State says how far one has got, and Remove takes
	// one out of the queue: the download queue's.
	Get    func(songs []ytmusic.Song)
	State  func(videoID string) (media.DownloadState, float32)
	Remove func(videoID string)
	Online func() bool
	Log    *slog.Logger
	// Pause is the wait between searches, a second unless set: YouTube
	// Music is asked at the pace a person would.
	Pause time.Duration
	// Off starts it switched off, as with Spotify or YouTube off (see
	// SetEnabled).
	Off bool
}

// The waits while matching can't go on: offline, and after a search failed.
const (
	offlineWait = 5 * time.Second
	failedWait  = 30 * time.Second
)

// Importer keeps the imports and matches their songs, one at a time, on a
// goroutine of its own, which also writes them to the store. Its other
// methods only touch what it holds in memory, so the screens may call them.
type Importer struct {
	o     Options
	ctx   context.Context
	stop  context.CancelFunc
	wake  chan struct{}
	ready chan struct{}
	done  chan struct{}
	now   func() time.Time
	waits [2]time.Duration // offline, and after a failed search; tests shorten them
	off   atomic.Bool      // switched off: nothing is matched (see SetEnabled)

	mu   sync.Mutex
	jobs []*job
	err  error // why matching stopped, until the next Import
	// version changes whenever an import does, so a screen asks for one
	// again only when it has changed.
	version uint64
}

type job struct {
	uri, name string
	created   time.Time
	items     []*entry
	recheck   bool   // imported again: its matched songs' files are looked for again
	listed    uint64 // the version its songs were last listed in
	changed   uint64 // the version it last changed in
	// What flush writes: the job and all its songs once they are listed
	// again, and otherwise only the songs that changed.
	relist  bool
	unsaved []*entry
}

// entry is a song of a job as the importer keeps it.
type entry struct {
	Item
	pos     int    // its place in the job
	changed uint64 // the version it last changed in
	unsaved bool   // in its job's unsaved
	dropped bool   // no longer in the playlist, so not to be written
}

// New loads the imports from the store and starts matching.
func New(o Options) (*Importer, error) {
	if o.Pause == 0 {
		o.Pause = time.Second
	}
	ctx, stop := context.WithCancel(context.Background())
	im := &Importer{o: o, ctx: ctx, stop: stop, wake: make(chan struct{}, 1), ready: make(chan struct{}, 1),
		done: make(chan struct{}), now: time.Now, waits: [2]time.Duration{offlineWait, failedWait}}
	if err := im.load(); err != nil {
		stop()
		return nil, err
	}
	im.off.Store(o.Off)
	go im.run()
	return im, nil
}

// SetEnabled switches matching on or off, as Spotify and YouTube are: while
// it is off nothing is matched, and the songs wait. A song being matched is
// finished.
func (im *Importer) SetEnabled(on bool) {
	im.off.Store(!on)
	signal(im.wake)
}

// Ready receives a value when an import has moved on.
func (im *Importer) Ready() <-chan struct{} { return im.ready }

// Close stops matching and writes what is left to write.
func (im *Importer) Close() {
	im.stop()
	<-im.done
	im.flush()
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// Import imports playlist uri, called name, with tracks: songs new to it are
// matched, and songs no longer in it are dropped. Imported again, its
// matched songs whose files have gone are matched again.
func (im *Importer) Import(uri, name string, tracks []spotify.Track) {
	im.mu.Lock()
	defer im.mu.Unlock()
	j := im.find(uri)
	if j == nil {
		j = &job{uri: uri, created: im.now()}
		im.jobs = append(im.jobs, j)
	}
	old := map[string]*entry{}
	for _, e := range j.items {
		old[e.Track.ID] = e
	}
	listed := map[string]bool{}
	j.name, j.items, j.recheck = name, nil, true
	for _, t := range tracks {
		if t.ID == "" || listed[t.ID] {
			continue // a song twice in a playlist is imported once
		}
		listed[t.ID] = true
		e := old[t.ID]
		if e == nil {
			e = &entry{}
		}
		e.Track, e.pos = t, len(j.items)
		j.items = append(j.items, e)
	}
	for id, e := range old {
		if !listed[id] {
			e.dropped = true
		}
	}
	im.err = nil
	im.version++
	j.listed, j.changed, j.relist = im.version, im.version, true
	signal(im.wake)
}

// Job is import uri as it stands; ok is false for a playlist never imported.
func (im *Importer) Job(uri string) (Job, bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	j := im.find(uri)
	if j == nil {
		return Job{}, false
	}
	out := Job{URI: j.uri, Name: j.name, Items: make([]Item, len(j.items))}
	for i, e := range j.items {
		out.Items[i] = e.Item
		out.Items[i].Candidates = slices.Clone(e.Candidates)
	}
	return out, true
}

// Changes is what changed of import uri after version since, so that a
// screen showing it need make again only the rows of the songs that did. All
// its songs are given when since is 0 or they were listed again after it.
// The songs share their candidates with the importer, which never changes
// them; nor may the caller. ok is false for a playlist never imported.
func (im *Importer) Changes(uri string, since uint64) (c Changes, ok bool) {
	im.mu.Lock()
	defer im.mu.Unlock()
	j := im.find(uri)
	if j == nil {
		return Changes{}, false
	}
	c.Version = im.version
	switch {
	case since == 0 || j.listed > since:
		c.All, c.Items = true, make([]Item, len(j.items))
		for i, e := range j.items {
			c.Items[i] = e.Item
		}
	case j.changed > since:
		for i, e := range j.items {
			if e.changed > since {
				c.At = append(c.At, i)
				c.Items = append(c.Items, e.Item)
			}
		}
	}
	return c, true
}

// Version changes whenever an import does.
func (im *Importer) Version() uint64 {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.version
}

// Err is why matching stopped, as when YouTube Music answers in a shape it
// can't read; nil while it goes on. Importing again tries again.
func (im *Importer) Err() error {
	im.mu.Lock()
	defer im.mu.Unlock()
	return im.err
}

// Use accepts candidate c of song id in import uri, and queues it.
func (im *Importer) Use(uri, id string, c int) {
	im.mu.Lock()
	j, e := im.item(uri, id)
	if e == nil || c < 0 || c >= len(e.Candidates) {
		im.mu.Unlock()
		return
	}
	old := ""
	if e.State == Matched || e.State == Accepted {
		old = e.Candidates[e.Chosen].Song.VideoID
	}
	e.State, e.Chosen = Accepted, c
	song := songOf(e.Track, e.Candidates[c])
	im.touch(j, e)
	im.mu.Unlock()
	if old != "" && old != song.VideoID {
		im.remove(removal{video: old}) // another candidate was queued for it
	}
	im.o.Get([]ytmusic.Song{song})
	signal(im.wake)
}

// Skip leaves song id of import uri out, taking it out of the download queue
// if it was queued and hasn't been saved.
func (im *Importer) Skip(uri, id string) {
	im.mu.Lock()
	j, e := im.item(uri, id)
	if e == nil {
		im.mu.Unlock()
		return
	}
	queued := ""
	if e.State == Matched || e.State == Accepted {
		queued = e.Candidates[e.Chosen].Song.VideoID
	}
	e.State = Skipped
	im.touch(j, e)
	im.mu.Unlock()
	if queued != "" {
		im.remove(removal{queued, id})
	}
	signal(im.wake)
}

// Cancel stops import uri: songs not matched yet are skipped, and those queued
// and not saved leave the download queue.
func (im *Importer) Cancel(uri string) {
	im.mu.Lock()
	j := im.find(uri)
	if j == nil {
		im.mu.Unlock()
		return
	}
	var queued []removal
	for _, e := range j.items {
		switch e.State {
		case Waiting:
			e.State = Skipped
			im.touch(j, e)
		case Matched, Accepted:
			v := e.Candidates[e.Chosen].Song.VideoID
			if st, _ := im.o.State(v); st != media.Saved {
				queued = append(queued, removal{v, e.Track.ID})
				e.State = Skipped
				im.touch(j, e)
			}
		}
	}
	im.mu.Unlock()
	im.remove(queued...)
	signal(im.wake)
}

// removal is a download to take out of the queue: its video, and the Spotify
// song it was for, or "" when the song stays queued as another video.
type removal struct{ video, song string }

// remove takes downloads out of the queue, except those saved and those
// another import still counts on: imports can share a song's download, as a
// playlist's and the song's own do. One that has the song queued counts on
// the video, and one that found it queued (Have) on the song.
func (im *Importer) remove(rs ...removal) {
	if len(rs) == 0 {
		return
	}
	videos, songs := map[string]bool{}, map[string]bool{}
	im.mu.Lock()
	for _, j := range im.jobs {
		for _, e := range j.items {
			switch e.State {
			case Matched, Accepted:
				videos[e.Candidates[e.Chosen].Song.VideoID] = true
			case Have:
				songs[e.Track.ID] = true
			}
		}
	}
	im.mu.Unlock()
	for _, r := range rs {
		if videos[r.video] || r.song != "" && songs[r.song] {
			continue
		}
		if st, _ := im.o.State(r.video); st != media.Saved {
			im.o.Remove(r.video)
		}
	}
}

func (im *Importer) find(uri string) *job {
	for _, j := range im.jobs {
		if j.uri == uri {
			return j
		}
	}
	return nil
}

func (im *Importer) item(uri, id string) (*job, *entry) {
	if j := im.find(uri); j != nil {
		for _, e := range j.items {
			if e.Track.ID == id {
				return j, e
			}
		}
	}
	return nil, nil
}

// touch notes that song e of job j has changed, for the screens and to be
// written. Called with mu held.
func (im *Importer) touch(j *job, e *entry) {
	im.version++
	e.changed, j.changed = im.version, im.version
	if !e.unsaved && !e.dropped {
		e.unsaved = true
		j.unsaved = append(j.unsaved, e)
	}
}

// songOf is candidate c as the download of Spotify song t.
func songOf(t spotify.Track, c Candidate) ytmusic.Song {
	s := c.Song
	s.SpotifyID, s.ISRC = t.ID, t.ISRC
	return s
}

// run matches waiting songs one at a time until Close, writing the imports
// as they change.
func (im *Importer) run() {
	defer close(im.done)
	for {
		im.flush()
		im.rechecks()
		j, e := im.next()
		if e == nil {
			if !im.sleep(-1) {
				return
			}
			continue
		}
		if im.off.Load() {
			if !im.sleep(-1) { // until switched on
				return
			}
			continue
		}
		if im.o.Online != nil && !im.o.Online() {
			if !im.sleep(im.waits[0]) {
				return
			}
			continue
		}
		wait, ok := im.match(j, e)
		if !ok || !im.sleep(wait) {
			return
		}
	}
}

// sleep waits d, or until woken when d is negative; false once closed.
func (im *Importer) sleep(d time.Duration) bool {
	var after <-chan time.Time
	if d >= 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		after = t.C
	}
	select {
	case <-after:
	case <-im.wake:
	case <-im.ctx.Done():
		return false
	}
	return true
}

// next is the first song waiting to be matched, oldest import first, unless
// matching has stopped.
func (im *Importer) next() (*job, *entry) {
	im.mu.Lock()
	defer im.mu.Unlock()
	if im.err != nil {
		return nil, nil
	}
	for _, j := range im.jobs {
		for _, e := range j.items {
			if e.State == Waiting {
				return j, e
			}
		}
	}
	return nil, nil
}

// rechecks matches again the songs of imports made again whose files have
// gone since.
func (im *Importer) rechecks() {
	type song struct {
		j  *job
		e  *entry
		id string
	}
	im.mu.Lock()
	var check []song
	for _, j := range im.jobs {
		if !j.recheck {
			continue
		}
		j.recheck = false
		for _, e := range j.items {
			if e.State == Matched || e.State == Accepted || e.State == Have {
				check = append(check, song{j, e, e.Track.ID})
			}
		}
	}
	im.mu.Unlock()
	for _, s := range check {
		if !im.have(s.id) {
			im.mu.Lock()
			s.e.State = Waiting
			im.touch(s.j, s.e)
			im.mu.Unlock()
		}
	}
}

// have reports whether the library or the download queue has Spotify song
// id already.
func (im *Importer) have(id string) bool {
	var n int
	// The conditions repeat the partial indexes', so that SQLite uses them.
	err := im.o.DB.QueryRow(`SELECT (SELECT COUNT(*) FROM tracks WHERE spotify_id = ? AND spotify_id != '') +
		(SELECT COUNT(*) FROM downloads WHERE spotify_id = ? AND spotify_id != '')`, id, id).Scan(&n)
	if err != nil {
		im.o.Log.Warn("look for an imported song", "err", err)
	}
	return n > 0
}

// match matches one song: by its ISRC, then by its artist and title when that
// found nothing sure. A sure match is queued. It returns how long to wait
// before the next, and false once closed.
func (im *Importer) match(j *job, e *entry) (time.Duration, bool) {
	im.mu.Lock()
	t := e.Track
	im.mu.Unlock()
	if im.have(t.ID) {
		im.settle(j, e, Have, nil)
		return 0, true
	}
	var cands []Candidate
	if t.ISRC != "" {
		items, err := im.o.Search(im.ctx, t.ISRC, ytmusic.Songs)
		if wait, ok := im.failed(err); err != nil {
			return wait, ok
		}
		cands = rank(t, nil, items)
		if len(cands) == 0 || cands[0].Score < sureScore {
			if !im.sleep(im.o.Pause) {
				return 0, false
			}
		}
	}
	if len(cands) == 0 || cands[0].Score < sureScore {
		q := t.Title
		if len(t.Artists) > 0 {
			q = t.Artists[0] + " " + t.Title
		}
		items, err := im.o.Search(im.ctx, q, ytmusic.Songs)
		if wait, ok := im.failed(err); err != nil {
			return wait, ok
		}
		cands = rank(t, cands, items)
	}
	st := NotFound
	if len(cands) > 0 {
		st = classify(cands[0].Score)
	}
	if im.settle(j, e, st, cands) && st == Matched {
		im.o.Get([]ytmusic.Song{songOf(t, cands[0])})
	}
	return im.o.Pause, true
}

// failed deals with a search that failed: YouTube Music in a shape it can't
// read stops matching; anything else is tried again in a while. It returns
// how long to wait, and false once closed.
func (im *Importer) failed(err error) (time.Duration, bool) {
	switch {
	case err == nil:
		return 0, true
	case im.ctx.Err() != nil:
		return 0, false
	case errors.Is(err, ytmusic.ErrChanged):
		im.o.Log.Warn("import stopped", "err", err)
		im.mu.Lock()
		im.err = err
		im.version++
		im.mu.Unlock()
		signal(im.ready)
		return 0, true
	}
	im.o.Log.Warn("import search", "err", err)
	return im.waits[1], true
}

// settle gives a song its state and candidates, and reports whether it did:
// not if it was skipped meanwhile.
func (im *Importer) settle(j *job, e *entry, st State, cands []Candidate) bool {
	im.mu.Lock()
	ok := e.State == Waiting
	if ok {
		e.State, e.Candidates, e.Chosen = st, cands, 0
		im.touch(j, e)
	}
	im.mu.Unlock()
	signal(im.ready)
	return ok
}

// load reads the imports from the store.
func (im *Importer) load() error {
	rows, err := im.o.DB.Query(`SELECT uri, name, created FROM import_jobs ORDER BY created`)
	if err != nil {
		return err
	}
	for rows.Next() {
		j := &job{}
		var ms int64
		if err := rows.Scan(&j.uri, &j.name, &ms); err != nil {
			rows.Close()
			return err
		}
		j.created = time.UnixMilli(ms)
		im.jobs = append(im.jobs, j)
	}
	rows.Close()
	for _, j := range im.jobs {
		if err := im.loadItems(j); err != nil {
			return err
		}
	}
	return nil
}

func (im *Importer) loadItems(j *job) error {
	rows, err := im.o.DB.Query(`SELECT track, state, chosen, candidates FROM import_items WHERE uri = ? ORDER BY pos`, j.uri)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var track, cands string
		e := &entry{pos: len(j.items)}
		if err := rows.Scan(&track, &e.State, &e.Chosen, &cands); err != nil {
			return err
		}
		if json.Unmarshal([]byte(track), &e.Track) != nil || json.Unmarshal([]byte(cands), &e.Candidates) != nil {
			continue
		}
		j.items = append(j.items, e)
	}
	return rows.Err()
}

// flush writes what changed of the imports: a job listed again whole, and
// otherwise only its songs that changed. What can't be written is written
// whole the next time.
func (im *Importer) flush() {
	type row struct {
		it           Item
		pos          int
		track, cands string
	}
	type write struct {
		j         *job
		whole     bool
		uri, name string
		created   int64
		rows      []row
	}
	im.mu.Lock()
	var ws []write
	for _, j := range im.jobs {
		if !j.relist && len(j.unsaved) == 0 {
			continue
		}
		w := write{j: j, whole: j.relist, uri: j.uri, name: j.name, created: j.created.UnixMilli()}
		es := j.unsaved
		if j.relist {
			es = j.items
		}
		w.rows = make([]row, len(es))
		for i, e := range es {
			w.rows[i] = row{it: e.Item, pos: e.pos}
		}
		for _, e := range j.unsaved {
			e.unsaved = false
		}
		j.relist, j.unsaved = false, nil
		ws = append(ws, w)
	}
	im.mu.Unlock()
	if len(ws) == 0 {
		return
	}
	// Marshalled before the transaction, which then holds the database no
	// longer than the writing takes.
	for _, w := range ws {
		for i := range w.rows {
			r := &w.rows[i]
			track, _ := json.Marshal(r.it.Track)
			cands, _ := json.Marshal(r.it.Candidates)
			r.track, r.cands = string(track), string(cands)
		}
	}
	err := func() error {
		tx, err := im.o.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		// A song only changed keeps its place, which only listing again moves.
		put, err := tx.Prepare(`INSERT INTO import_items (uri, spotify_id, pos, track, state, chosen, candidates)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT (uri, spotify_id) DO UPDATE SET
			state = excluded.state, chosen = excluded.chosen, candidates = excluded.candidates`)
		if err != nil {
			return err
		}
		defer put.Close()
		for _, w := range ws {
			if w.whole {
				if _, err := tx.Exec(`INSERT INTO import_jobs (uri, name, created) VALUES (?, ?, ?)
					ON CONFLICT (uri) DO UPDATE SET name = excluded.name`, w.uri, w.name, w.created); err != nil {
					return err
				}
				if _, err := tx.Exec(`DELETE FROM import_items WHERE uri = ?`, w.uri); err != nil {
					return err
				}
			}
			for _, r := range w.rows {
				if _, err := put.Exec(w.uri, r.it.Track.ID, r.pos, r.track, int(r.it.State), r.it.Chosen, r.cands); err != nil {
					return err
				}
			}
		}
		return tx.Commit()
	}()
	if err != nil {
		im.o.Log.Warn("save imports", "err", err)
		im.mu.Lock()
		for _, w := range ws {
			w.j.relist = true
		}
		im.mu.Unlock()
	}
}

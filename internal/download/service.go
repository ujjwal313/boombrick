package download

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/remux"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// Options configure a Service.
type Options struct {
	DB      *sql.DB
	Music   string // the Music folder: songs are filed in Music/Boombrick
	Data    string // the app's folder: yt-dlp goes in Data/bin, partial files in Data/cache/downloads
	QJS     string // QuickJS, yt-dlp's JavaScript runtime
	HTTP    *http.Client
	Albums  Albums // nil: no track numbers from album pages
	Lyrics  Lyrics // nil: no lyrics in the tags
	Formats library.Formats
	Online  func() bool   // nil: always online
	Quality func() string // the download quality setting; "smaller" puts Opus 250 first
	Hold    bool          // start held, as with YouTube switched off (see SetHeld)
	Log     *slog.Logger
}

// Entry is a download as the Downloads tab shows it.
type Entry struct {
	Song     ytmusic.Song
	State    media.DownloadState
	Progress float32 // 0 to 1, while downloading
	Speed    float64 // bytes a second, while downloading
	Failure  Failure
	Bytes    int64 // a saved song's size
}

// Storage is the SD card's space, in bytes; Music is the library's files.
type Storage struct {
	Total, Free, Music int64
}

// Status is the queue as the Downloads tab shows it.
type Status struct {
	// Entries are the songs under way, then queued, failed, and saved today.
	Entries    []Entry
	Installing bool    // yt-dlp is being downloaded
	Install    float32 // how far, 0 to 1
	Paused     bool
	Waiting    string // why queued songs aren't downloading, or ""
	Storage    Storage
	Version    uint64 // changes whenever any of this does
}

// Outcome is a download that ended: its file landed (Path), it failed, or it
// was removed.
type Outcome struct {
	VideoID string
	Path    string
	Failure Failure
	Removed bool
}

// Why queued songs wait, as the Downloads tab says it.
const (
	waitOffline    = "Waiting for Wi-Fi"
	waitSpace      = "Stopped: the SD card has less than 200 MB free"
	waitDownloader = "Couldn’t get the downloader. Trying again in a minute."
	waitNetwork    = "Couldn’t connect. Trying again in a minute."
)

const (
	batchSize  = 20
	minFree    = 200 << 20 // free space under which downloads wait
	installFor = 10 * time.Minute
	tellEvery  = 250 * time.Millisecond // progress wakes the UI at most this often
	netTries   = 3                      // a song fails as Network on its third such failure in a row
)

// fetcher downloads a batch, and searches. *Runner is one.
type fetcher interface {
	Run(ctx context.Context, ids []string, event func(Event)) error
	Search(ctx context.Context, query string) ([]ytmusic.Item, error)
}

// installer keeps yt-dlp. *Installer is one.
type installer interface {
	Program() string
	Install(ctx context.Context, progress func(done, total int64)) (bool, error)
	Version() (string, time.Time)
	RecordVersion(ctx context.Context) error
}

// Downloader is yt-dlp as Settings shows it.
type Downloader struct {
	Version   string    // "" when it isn't installed, or its version isn't known
	Installed time.Time // when it was installed
	Updating  bool
	Progress  float32 // how far an install has got, 0 to 1
	Failed    bool    // the last install failed
}

// landed is a downloaded file waiting to be finished. It keeps the row it was
// downloaded for: a song removed and asked for again is a row of its own.
type landed struct {
	row  *row
	song ytmusic.Song
	src  string
}

var errPanic = errors.New("the finisher failed unexpectedly")

// Service keeps the download queue. Batches run on one goroutine and what
// lands is finished on another, so a song is tagged and filed while the next
// downloads. The queue's calls change memory only; a third goroutine writes
// what they changed, so a caller on the UI thread never waits for the card.
type Service struct {
	opts        Options
	log         *slog.Logger
	inst        installer
	runner      func(program, formats string) fetcher
	fin         *finisher
	space       func(path string) (total, free int64, err error)
	parts       string
	offlineWait time.Duration // how often to look again while offline
	stopWait    time.Duration // and while stopped for space or the downloader
	panicWait   time.Duration // how long a loop rests after a panic
	writeWait   time.Duration // how long rows that couldn't be written wait to be tried again
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	closing     sync.Once
	write       sync.Mutex    // row writes go one at a time, so the last state wins
	wake        chan struct{} // there may be work
	land        chan struct{} // a file has landed
	flushes     chan struct{} // there may be something to write
	ready       chan struct{}
	tool        chan struct{} // held while yt-dlp is installed or run: one at a time

	mu          sync.Mutex
	rows        map[string]*row
	filed       map[string]string // songs saved before today, which no list shows: only their files
	dirty       map[string]bool   // songs whose rows the calls changed or forgot, to write
	scrap       map[string]bool   // songs removed whose partial files go; they wait for that
	prune       bool              // forget saved songs whose files are gone, when next writing
	live        map[string]Event  // the latest progress of the song under way
	landing     []landed          // files waiting to be finished
	finishing   string            // the file being finished
	finishingID string            // and its song
	batch       []string
	settled     map[string]bool // the batch's songs that have landed or failed
	stopBatch   context.CancelFunc
	netFails    map[string]int // each song's Network failures in a row
	netStopped  bool           // the batch was stopped for a Network failure
	netRest     time.Time      // no batch starts before this, after a Network failure
	searching   int            // searches waiting for yt-dlp or running it; batches wait for them
	queueVer    uint64         // changes whenever the queue or the pause does
	clock       int64          // the last added or priority stamp given out
	closed      bool           // Close was called
	shut        bool           // Ready is closed
	retrying    bool           // a write failed, and the writer will try again
	paused      bool
	hold        bool // YouTube is switched off: no batch begins (see SetHeld)
	update      bool // update yt-dlp before the next batch, or now if asked from Settings
	installing  bool
	install     float32
	asked       bool      // Settings asked for an install, which runs with nothing to download
	toolFailed  bool      // the last install failed
	toolVersion string    // yt-dlp's version, read off the UI thread
	toolAt      time.Time // and when it was installed
	waiting     string
	storage     Storage
	outcomes    []Outcome
	version     uint64
	status      Status // built at statusOf
	statusOf    uint64
	told        time.Time // when progress last changed the status
}

// New starts the service over the downloads table in opts.DB.
func New(opts Options) (*Service, error) {
	rows, err := loadRows(opts.DB)
	if err != nil {
		return nil, err
	}
	bin := filepath.Join(opts.Data, "bin")
	parts := filepath.Join(opts.Data, "cache", "downloads")
	s := newService(opts, rows, NewInstaller(opts.HTTP, bin), func(program, formats string) fetcher {
		return &Runner{Program: program, QJS: opts.QJS, Cache: filepath.Join(bin, "cache"), Parts: parts, Formats: formats, Log: opts.Log}
	}, parts)
	s.start()
	return s, nil
}

func newService(opts Options, rows map[string]*row, inst installer, runner func(string, string) fetcher, parts string) *Service {
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var clock int64
	filed := map[string]string{}
	since := today()
	for id, r := range rows {
		clock = max(clock, r.added, r.priority)
		if r.state == saved && r.finished < since {
			filed[id] = r.path
			delete(rows, id)
		}
	}
	return &Service{opts: opts, log: opts.Log, inst: inst, runner: runner, parts: parts, space: diskSpace,
		offlineWait: 15 * time.Second, stopWait: time.Minute, panicWait: time.Second, writeWait: 5 * time.Second,
		ctx: ctx, cancel: cancel,
		wake: make(chan struct{}, 1), land: make(chan struct{}, 1), flushes: make(chan struct{}, 1),
		ready: make(chan struct{}, 1), tool: make(chan struct{}, 1), rows: rows, filed: filed, dirty: map[string]bool{},
		scrap: map[string]bool{}, live: map[string]Event{}, netFails: map[string]int{}, clock: clock, version: 1, hold: opts.Hold,
		fin: &finisher{db: opts.DB, music: opts.Music, http: opts.HTTP, albums: opts.Albums, lyrics: opts.Lyrics,
			formats: opts.Formats, remux: remux.File, log: opts.Log}}
}

func (s *Service) start() {
	s.wg.Add(3)
	go s.worker()
	go s.finisher()
	go s.writer()
}

// Close stops downloading, killing yt-dlp, waits for the goroutines, writes
// what the calls changed and closes Ready. Songs under way stay queued for
// next time. A search's yt-dlp is ended by its caller's ctx, not by Close:
// main closes the searcher first. Calls after Close do nothing.
func (s *Service) Close() {
	s.closing.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.cancel()
		s.wg.Wait()
		s.guard("writer", s.flush)
		s.mu.Lock()
		s.shut = true
		close(s.ready)
		s.mu.Unlock()
	})
}

// Ready receives a value when something the screens show has changed.
func (s *Service) Ready() <-chan struct{} { return s.ready }

// notify wakes Ready, unless it is closed.
func (s *Service) notify() {
	s.mu.Lock()
	if !s.shut {
		signal(s.ready)
	}
	s.mu.Unlock()
}

// stampLocked is a time for ordering the queue: later than any given before,
// even if the clock goes back.
func (s *Service) stampLocked() int64 {
	s.clock = max(time.Now().UnixMilli(), s.clock+1)
	return s.clock
}

// queueChangedLocked notes a change to the queue or the pause, which a batch
// about to start must notice.
func (s *Service) queueChangedLocked() {
	s.queueVer++
	s.version++
}

// wakeAll says that a call changed the queue: the writer has rows to write,
// the worker may have work and the screens something to show.
func (s *Service) wakeAll() {
	signal(s.flushes)
	signal(s.wake)
	s.notify()
}

// Get queues songs to download, in order. Songs already queued or saved are
// left as they are; failed ones are queued again.
func (s *Service) Get(songs []ytmusic.Song) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	for _, sg := range songs {
		if _, ok := s.filed[sg.VideoID]; ok {
			continue
		}
		r, ok := s.rows[sg.VideoID]
		switch {
		case !ok:
			r = &row{song: sg, state: queued, added: s.stampLocked()}
			s.rows[sg.VideoID] = r
		case r.state == failed:
			r.state, r.failure, r.message = queued, NoFailure, ""
		default:
			continue
		}
		s.dirty[sg.VideoID] = true
	}
	s.queueChangedLocked()
	s.mu.Unlock()
	s.wakeAll()
}

// First puts the song first in the queue, as when it is about to play. A
// batch on another song is stopped to start again with this one, and
// yt-dlp resumes the partial file it leaves.
func (s *Service) First(videoID string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if r, ok := s.rows[videoID]; ok && r.state != saved && !r.landed {
		r.priority = s.stampLocked()
		r.state, r.failure, r.message = queued, NoFailure, ""
		s.dirty[videoID] = true
		s.netRest = time.Time{} // asked for now, not after a rest
		if s.stopBatch != nil && !s.leadsLocked(videoID) {
			s.stopBatch()
		}
		s.queueChangedLocked()
	}
	s.mu.Unlock()
	s.wakeAll()
}

// leadsLocked reports whether the batch running is downloading videoID, or
// will start on it next.
func (s *Service) leadsLocked(videoID string) bool {
	if len(s.live) > 0 {
		_, ok := s.live[videoID]
		return ok
	}
	for _, id := range s.batch {
		if s.settled[id] {
			continue
		}
		if r := s.rows[id]; r != nil && r.state == queued && !r.landed {
			return id == videoID
		}
	}
	return false
}

// Retry queues a failed song again, first; with update, yt-dlp is updated
// before the next batch.
func (s *Service) Retry(videoID string, update bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if r, ok := s.rows[videoID]; ok && r.state == failed {
		r.state, r.failure, r.message, r.priority = queued, NoFailure, "", s.stampLocked()
		s.dirty[videoID] = true
		s.update = s.update || update
		s.netRest = time.Time{}
		s.queueChangedLocked()
	}
	s.mu.Unlock()
	s.wakeAll()
}

// Remove forgets a song. One not yet saved stops downloading and its partial
// file goes; a saved song's file stays in the library. Playback waiting on
// the song hears of it through Outcomes.
func (s *Service) Remove(videoID string) {
	s.mu.Lock()
	r, ok := s.rows[videoID]
	_, filed := s.filed[videoID]
	if !ok && !filed || s.closed {
		s.mu.Unlock()
		return
	}
	delete(s.rows, videoID)
	delete(s.filed, videoID)
	delete(s.live, videoID)
	delete(s.netFails, videoID)
	if ok && r.state != saved {
		if s.stopBatch != nil && slices.Contains(s.batch, videoID) && r.state == queued && !r.landed {
			s.stopBatch()
		}
		s.scrap[videoID] = true
	}
	s.dirty[videoID] = true
	s.outcomes = append(s.outcomes, Outcome{VideoID: videoID, Removed: true})
	s.queueChangedLocked()
	s.mu.Unlock()
	s.wakeAll()
}

// SetPaused pauses or resumes the queue; pausing stops the batch under way.
func (s *Service) SetPaused(on bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.paused = on
	if on && s.stopBatch != nil {
		s.stopBatch()
	}
	s.queueChangedLocked()
	s.mu.Unlock()
	signal(s.wake)
	s.notify()
}

// SetHeld holds or releases the queue, as YouTube is switched off and on: no
// batch begins while it is held, but one under way finishes. Holding is
// separate from the user's pause, which it leaves as it is.
func (s *Service) SetHeld(on bool) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.hold = on
	s.queueChangedLocked()
	s.mu.Unlock()
	signal(s.wake)
	s.notify()
}

// Search searches YouTube with yt-dlp, for when YouTube Music's own search
// can't be read. It needs yt-dlp installed. yt-dlp runs one at a time, so a
// batch under way is stopped for it and starts again after, resuming its
// partial file.
func (s *Service) Search(ctx context.Context, query string) ([]ytmusic.Item, error) {
	if s.inst.Program() == "" {
		return nil, errors.New("yt-dlp isn't installed")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("the download service is closed")
	}
	s.searching++
	if s.stopBatch != nil {
		s.stopBatch()
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.searching--
		s.mu.Unlock()
		signal(s.wake)
	}()
	if err := s.take(ctx); err != nil {
		return nil, err
	}
	defer s.give()
	program := s.inst.Program() // an update may have run meanwhile
	if program == "" {
		return nil, errors.New("yt-dlp isn't installed")
	}
	return s.runner(program, "").Search(ctx, query)
}

// take waits for yt-dlp to be free, and holds it until give.
func (s *Service) take(ctx context.Context) error {
	select {
	case s.tool <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

func (s *Service) give() { <-s.tool }

// State is how far the song with videoID has got, and how far through its
// download it is.
func (s *Service) State(videoID string) (media.DownloadState, float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.filed[videoID]; ok {
		return media.Saved, 1
	}
	return s.stateLocked(s.rows[videoID])
}

func (s *Service) stateLocked(r *row) (media.DownloadState, float32) {
	switch {
	case r == nil:
		return media.Available, 0
	case r.state == saved:
		return media.Saved, 1
	case r.state == failed:
		return media.Failed, 0
	case r.landed:
		return media.Downloading, 1
	}
	if e, ok := s.live[r.song.VideoID]; ok {
		return media.Downloading, fraction(e)
	}
	return media.Queued, 0
}

func fraction(e Event) float32 {
	if e.Total <= 0 {
		return 0
	}
	return min(float32(e.Done)/float32(e.Total), 1)
}

// Prune forgets saved songs whose files the library no longer has, as when
// they were deleted on a PC, so that they can be downloaded again, and
// measures the library again for the SD card's numbers. It works on the
// service's own goroutine; call it when a library scan ends.
func (s *Service) Prune() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.prune = true
	s.mu.Unlock()
	signal(s.flushes)
}

// Note is why queued songs aren't downloading, in the few words Now Playing
// has room for, or "".
func (s *Service) Note() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.hold:
		return "YouTube is off"
	case s.paused:
		return "Downloads paused"
	case s.installing || s.waiting == waitDownloader:
		return "Getting the downloader"
	case s.waiting == waitOffline:
		return "Waiting for Wi-Fi"
	case s.waiting == waitSpace:
		return "Card full"
	case s.waiting == waitNetwork:
		return "Couldn’t connect"
	}
	return ""
}

// Downloader reports yt-dlp's version and any install under way.
func (s *Service) Downloader() Downloader {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Downloader{Version: s.toolVersion, Installed: s.toolAt, Updating: s.installing || s.asked,
		Progress: s.install, Failed: s.toolFailed}
}

// UpdateDownloader installs yt-dlp, or updates it to its latest release, as
// soon as no batch runs, even with nothing queued.
func (s *Service) UpdateDownloader() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.update, s.asked, s.toolFailed = true, true, false
	s.changedLocked()
	s.mu.Unlock()
	signal(s.wake)
	s.notify()
}

// Version changes whenever a download's state or the queue does; it is
// Status().Version without building the status.
func (s *Service) Version() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.version
}

// Paused reports whether the queue is paused.
func (s *Service) Paused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// Busy reports whether a batch is downloading or files are being filed.
func (s *Service) Busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.batch != nil || len(s.landing) > 0 || s.finishingID != ""
}

// Path is a saved song's file.
func (s *Service) Path(videoID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.rows[videoID]; ok && r.state == saved {
		return r.path, true
	}
	path, ok := s.filed[videoID]
	return path, ok
}

// Outcomes returns the downloads that ended since it was last called.
func (s *Service) Outcomes() []Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.outcomes
	s.outcomes = nil
	return o
}

// Status is the queue as the Downloads tab shows it, built again only when
// something has changed.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statusOf == s.version {
		return s.status
	}
	since := today()
	type item struct {
		e Entry
		r *row
	}
	var items []item
	for _, r := range s.rows {
		if r.state == saved && r.finished < since {
			continue
		}
		e := Entry{Song: r.song, Failure: r.failure, Bytes: r.bytes}
		e.State, e.Progress = s.stateLocked(r)
		if e.State == media.Downloading {
			e.Speed = s.live[r.song.VideoID].Speed
		}
		items = append(items, item{e, r})
	}
	rank := [...]int{media.Downloading: 0, media.Queued: 1, media.Failed: 2, media.Saved: 3, media.Available: 4}
	slices.SortFunc(items, func(a, b item) int {
		if c := cmp.Compare(rank[a.e.State], rank[b.e.State]); c != 0 {
			return c
		}
		if a.e.State == media.Saved {
			return cmp.Compare(b.r.finished, a.r.finished)
		}
		return cmp.Or(cmp.Compare(b.r.priority, a.r.priority), cmp.Compare(a.r.added, b.r.added),
			strings.Compare(a.r.song.VideoID, b.r.song.VideoID))
	})
	entries := make([]Entry, len(items))
	for i, it := range items {
		entries[i] = it.e
	}
	s.status = Status{Entries: entries, Installing: s.installing, Install: s.install, Paused: s.paused, Waiting: s.waiting,
		Storage: s.storage, Version: s.version}
	s.statusOf = s.version
	return s.status
}

// today is when the day began, in ms: the Downloads tab lists the songs
// saved since.
func today() int64 {
	y, m, d := time.Now().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local).UnixMilli()
}

func (s *Service) changedLocked() { s.version++ }

// save writes rows as they are now, not as they were when asked: writes go one
// at a time, so the last state wins, and a row forgotten meanwhile isn't
// brought back.
func (s *Service) save(rows ...*row) {
	if len(rows) == 0 {
		return
	}
	s.write.Lock()
	defer s.write.Unlock()
	var now []row
	s.mu.Lock()
	for _, r := range rows {
		if s.rows[r.song.VideoID] == r {
			now = append(now, *r)
		}
	}
	s.mu.Unlock()
	if err := saveRows(s.opts.DB, now); err != nil {
		s.log.Warn("save downloads", "err", err)
		s.mu.Lock()
		for _, r := range now {
			s.dirty[r.song.VideoID] = true
		}
		s.mu.Unlock()
		s.retryWrites()
	}
}

// retryWrites has the writer write again a little later, after a write
// failed: once, however many fail meanwhile.
func (s *Service) retryWrites() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.retrying || s.closed {
		return
	}
	s.retrying = true
	time.AfterFunc(s.writeWait, func() {
		s.mu.Lock()
		s.retrying = false
		s.mu.Unlock()
		signal(s.flushes)
	})
}

// writer writes what the queue's calls change, as they change it.
func (s *Service) writer() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.flushes:
		}
		s.guard("writer", s.flush)
	}
}

// flush writes the rows the calls changed or forgot, deletes the partial
// files of songs removed and, when asked after a scan, forgets saved songs
// whose files are gone and measures the library again.
func (s *Service) flush() {
	scrap, prune := s.writeDirty()
	if len(scrap) > 0 {
		s.dropParts(scrap)
	}
	if prune {
		s.pruneLost()
		s.refreshStorage()
	}
}

// writeDirty writes the rows the calls changed as they are now, and deletes
// those they forgot, in one transaction, which is tried again a little later
// if it fails. It returns the songs whose partial files go, and whether to
// prune.
func (s *Service) writeDirty() (scrap []string, prune bool) {
	s.write.Lock()
	defer s.write.Unlock()
	var now []row
	var gone []string
	s.mu.Lock()
	for id := range s.dirty {
		if r, ok := s.rows[id]; ok {
			now = append(now, *r)
		} else {
			gone = append(gone, id)
		}
	}
	clear(s.dirty)
	for id := range s.scrap {
		scrap = append(scrap, id)
	}
	prune, s.prune = s.prune, false
	s.mu.Unlock()
	if err := saveRows(s.opts.DB, now, gone...); err != nil {
		s.log.Warn("save downloads", "err", err)
		s.mu.Lock()
		for _, r := range now {
			s.dirty[r.song.VideoID] = true
		}
		for _, id := range gone {
			s.dirty[id] = true
		}
		s.mu.Unlock()
		s.retryWrites()
	}
	return scrap, prune
}

// dropParts deletes the partial files of the songs removed, except those the
// finisher has or is about to work on, which are its to delete. The songs
// can then be downloaded again: one asked for first again meanwhile, as when
// it is played right after it was removed, stops the batch that started
// without it, as First does.
func (s *Service) dropParts(ids []string) {
	for _, id := range ids {
		parts, _ := filepath.Glob(filepath.Join(s.parts, id+".*"))
		s.mu.Lock()
		keep := map[string]bool{s.finishing: true}
		for _, l := range s.landing {
			keep[l.src] = true
		}
		s.mu.Unlock()
		for _, p := range parts {
			if !keep[p] {
				os.Remove(p)
			}
		}
	}
	s.mu.Lock()
	for _, id := range ids {
		delete(s.scrap, id)
	}
	if q := s.queuedLocked(); len(q) > 0 && slices.Contains(ids, q[0]) && s.stopBatch != nil && !s.leadsLocked(q[0]) {
		s.stopBatch()
	}
	s.mu.Unlock()
	signal(s.wake)
}

// pruneLost forgets saved songs the library no longer has, whose files are
// gone, as when they were deleted on a PC.
func (s *Service) pruneLost() {
	s.write.Lock()
	defer s.write.Unlock()
	lost, err := unindexed(s.opts.DB)
	if err != nil {
		s.log.Warn("find downloads the library lost", "err", err)
		return
	}
	var gone []row
	var ids []string
	for _, r := range lost {
		if _, err := os.Stat(r.path); !os.IsNotExist(err) {
			continue // still there, only not indexed yet
		}
		gone = append(gone, r)
		ids = append(ids, r.song.VideoID)
	}
	if len(gone) == 0 {
		return
	}
	if err := saveRows(s.opts.DB, nil, ids...); err != nil {
		s.log.Warn("forget downloads the library lost", "err", err)
		return
	}
	s.mu.Lock()
	for _, g := range gone {
		id := g.song.VideoID
		if r := s.rows[id]; r != nil && r.state == saved && r.path == g.path {
			delete(s.rows, id)
		}
		if p, ok := s.filed[id]; ok && p == g.path {
			delete(s.filed, id)
		}
	}
	s.changedLocked()
	s.mu.Unlock()
	s.log.Info("forgot downloads whose files are gone", "songs", len(gone))
	s.notify()
}

func (s *Service) setWaiting(why string) {
	s.mu.Lock()
	changed := s.waiting != why
	if changed {
		s.waiting = why
		s.changedLocked()
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
}

// guard runs f, logging a panic with its stack instead of crashing the app. It
// reports whether f panicked.
func (s *Service) guard(loop string, f func()) (panicked bool) {
	defer func() {
		if v := recover(); v != nil {
			s.log.Error("recovered", "loop", loop, "panic", v, "stack", string(debug.Stack()))
			panicked = true
		}
	}()
	f()
	return false
}

// rest waits a moment after a panic, so a loop that keeps panicking doesn't
// spin.
func (s *Service) rest(d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-s.ctx.Done():
	case <-t.C:
	}
}

func (s *Service) worker() {
	defer s.wg.Done()
	s.guard("worker", s.tidy)
	s.guard("worker", s.refreshStorage)
	s.guard("worker", s.recordVersion)
	rest := s.panicWait
	for s.ctx.Err() == nil {
		var ids []string
		if s.guard("worker", func() { s.pass(&ids) }) {
			s.failLead(ids)
			s.rest(rest)
			rest = min(rest*2, s.stopWait) // a panic that keeps coming is waited out for longer
		} else {
			rest = s.panicWait
		}
	}
}

// tidy deletes what a crash can leave: the partial files of songs no longer
// queued or failed, and remuxes cut short in Music/Boombrick. It runs before
// the first batch, while nothing else writes there.
func (s *Service) tidy() {
	files, _ := filepath.Glob(filepath.Join(s.parts, "*"))
	var stale []string
	s.mu.Lock()
	for _, f := range files {
		id, _, _ := strings.Cut(filepath.Base(f), ".")
		if r := s.rows[id]; r == nil || r.state == saved {
			stale = append(stale, f)
		}
	}
	s.mu.Unlock()
	for _, f := range stale {
		os.Remove(f)
	}
	filepath.WalkDir(filepath.Join(s.opts.Music, "Boombrick"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".opus.part") {
			os.Remove(p)
		}
		return nil
	})
}

// recordVersion reads yt-dlp's version for Settings, first asking an install
// made before versions were kept what its version is.
func (s *Service) recordVersion() {
	if v, _ := s.inst.Version(); v == "" && s.inst.Program() != "" && s.take(s.ctx) == nil {
		if err := s.inst.RecordVersion(s.ctx); err != nil {
			s.log.Warn("read yt-dlp's version", "err", err)
		}
		s.give()
	}
	s.readVersion()
}

// readVersion keeps the installed yt-dlp's version, so Settings never reads
// it from the card.
func (s *Service) readVersion() {
	v, at := s.inst.Version()
	s.mu.Lock()
	s.toolVersion, s.toolAt = v, at
	s.changedLocked()
	s.mu.Unlock()
	s.notify()
}

// pass looks for work and does it, or waits for some. ids is the batch it runs.
func (s *Service) pass(ids *[]string) {
	batch, ver, retry := s.next()
	if len(batch) == 0 {
		s.sleep(retry)
		return
	}
	*ids = batch
	s.run(batch, ver)
}

// failLead fails the song a batch that panicked was on, so the queue moves
// past it instead of trying it again for good.
func (s *Service) failLead(ids []string) {
	var save []*row
	s.mu.Lock()
	for _, id := range ids {
		if r := s.rows[id]; r != nil && r.state == queued && !r.landed {
			r.state, r.failure, r.message = failed, Other, "Something went wrong"
			s.outcomes = append(s.outcomes, Outcome{VideoID: id, Failure: Other})
			save = append(save, r)
			s.changedLocked()
			break
		}
	}
	s.mu.Unlock()
	s.save(save...)
	s.notify()
}

// sleep waits for work, for closing, or for d when d > 0.
func (s *Service) sleep(d time.Duration) {
	var after <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		after = t.C
	}
	select {
	case <-s.ctx.Done():
	case <-s.wake:
	case <-after:
	}
}

// next is the batch to download now, and the queue's version it was taken at.
// Without a batch it says why queued songs wait, and how long until it looks
// again, 0 meaning until something changes.
func (s *Service) next() ([]string, uint64, time.Duration) {
	s.mu.Lock()
	ids := s.queuedLocked()
	ver := s.queueVer
	paused, update, asked, searching := s.paused || s.hold, s.update, s.asked, s.searching > 0
	rest := time.Until(s.netRest)
	s.mu.Unlock()
	// Nothing to download, though Settings may have asked for yt-dlp.
	idle := len(ids) == 0 || paused
	switch {
	case idle && !asked:
		s.setWaiting("")
		return nil, 0, 0
	case searching:
		return nil, 0, 0 // the search wakes the queue when it is done
	case s.opts.Online != nil && !s.opts.Online() && idle:
		// Settings asked, and says it failed rather than wait for Wi-Fi.
		s.mu.Lock()
		s.asked, s.toolFailed = false, true
		s.changedLocked()
		s.mu.Unlock()
		s.notify()
		return nil, 0, 0
	case s.opts.Online != nil && !s.opts.Online():
		s.setWaiting(waitOffline)
		return nil, 0, s.offlineWait
	case rest > 0 && !idle:
		s.setWaiting(waitNetwork)
		return nil, 0, rest
	}
	if free, ok := s.readSpace(); ok && free < minFree && !idle {
		s.setWaiting(waitSpace)
		return nil, 0, s.stopWait
	}
	if s.inst.Program() == "" || update {
		if err := s.installYtdlp(); err != nil {
			s.log.Warn("install yt-dlp", "err", err)
			switch {
			case idle:
				// Asked for from Settings, which says it failed: not tried again
				// until asked again.
				s.mu.Lock()
				s.update = false
				s.mu.Unlock()
			case s.inst.Program() == "":
				s.setWaiting(waitDownloader)
				return nil, 0, s.stopWait
			default:
				// A failed update doesn't stop downloads with the yt-dlp there is.
				s.mu.Lock()
				s.update = false
				s.mu.Unlock()
			}
		}
	}
	s.setWaiting("")
	if idle {
		return nil, 0, 0
	}
	return ids[:min(len(ids), batchSize)], ver, 0
}

// heldLocked reports whether a file of the song is being finished or waits to
// be, or its partial files wait to be deleted: asked for again, it waits for
// that to be done with.
func (s *Service) heldLocked(videoID string) bool {
	return s.finishingID == videoID || s.scrap[videoID] ||
		slices.ContainsFunc(s.landing, func(l landed) bool { return l.song.VideoID == videoID })
}

// queuedLocked is the queue in order: highest priority, then oldest.
func (s *Service) queuedLocked() []string {
	var rs []*row
	for _, r := range s.rows {
		if r.state == queued && !r.landed && !s.heldLocked(r.song.VideoID) {
			rs = append(rs, r)
		}
	}
	slices.SortFunc(rs, func(a, b *row) int {
		return cmp.Or(cmp.Compare(b.priority, a.priority), cmp.Compare(a.added, b.added),
			strings.Compare(a.song.VideoID, b.song.VideoID))
	})
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.song.VideoID
	}
	return ids
}

func (s *Service) installYtdlp() error {
	if err := s.take(s.ctx); err != nil {
		return err
	}
	defer s.give()
	s.mu.Lock()
	s.installing, s.install = true, 0
	s.changedLocked()
	s.mu.Unlock()
	s.notify()
	ctx, cancel := context.WithTimeout(s.ctx, installFor)
	defer cancel()
	var did bool
	var err error
	returned := false
	defer func() {
		v, at := s.inst.Version()
		s.mu.Lock()
		s.installing = false
		if returned && err == nil {
			s.update = false
		}
		s.toolFailed = !returned || err != nil
		s.asked = false // what Settings asked for is done, or has failed
		s.toolVersion, s.toolAt = v, at
		s.changedLocked()
		s.mu.Unlock()
		s.notify()
	}()
	did, err = s.inst.Install(ctx, func(done, total int64) {
		if total <= 0 {
			return
		}
		// Like a download's, the progress shows a few times a second.
		s.mu.Lock()
		s.install = float32(done) / float32(total)
		tell := done >= total || time.Since(s.told) >= tellEvery
		if tell {
			s.told = time.Now()
			s.changedLocked()
		}
		s.mu.Unlock()
		if tell {
			s.notify()
		}
	})
	returned = true
	if did {
		s.log.Info("installed yt-dlp", "program", s.inst.Program())
	}
	return err
}

// run downloads a batch, unless the queue or the pause changed since ids were
// taken, installing yt-dlp can take minutes, or a search wants yt-dlp. One
// stopped on purpose leaves its songs queued.
func (s *Service) run(ids []string, ver uint64) {
	if s.take(s.ctx) != nil {
		return
	}
	defer s.give()
	ctx, stop := context.WithCancel(s.ctx)
	defer stop()
	s.mu.Lock()
	if s.closed || s.paused || s.hold || s.queueVer != ver || s.searching > 0 {
		s.mu.Unlock()
		return
	}
	s.batch, s.stopBatch, s.settled, s.netStopped = ids, stop, map[string]bool{}, false
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		clear(s.live)
		s.batch, s.stopBatch, s.settled = nil, nil, nil
		s.changedLocked()
		s.mu.Unlock()
		s.notify()
	}()
	go s.watch(ctx, stop)
	formats := "251/250/249"
	if s.opts.Quality != nil && s.opts.Quality() == "smaller" {
		formats = "250/249/251"
	}
	err := s.runner(s.inst.Program(), formats).Run(ctx, ids, s.event)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("download batch", "err", err)
	}
}

// watch stops the batch if the device goes offline, looking every 5 s.
func (s *Service) watch(ctx context.Context, stop context.CancelFunc) {
	if s.opts.Online == nil {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !s.opts.Online() {
				stop()
				return
			}
		}
	}
}

// event takes what yt-dlp reported about a song of the batch.
func (s *Service) event(e Event) {
	var save []*row
	var orphan string
	// Without a network every song of the rest of the batch fails at once:
	// stop and keep them queued instead. With Wi-Fi up but no internet each
	// fails the same way, so a song fails as Network only on its third try in
	// a row, the queue resting between tries.
	offline := e.Failure == Network && s.opts.Online != nil && !s.opts.Online()
	progress, bump := false, true
	s.mu.Lock()
	r, ok := s.rows[e.ID]
	_, had := s.live[e.ID]
	if ok && e.Failure == Network && !offline && !s.netStopped {
		s.netFails[e.ID]++
		s.netRest = time.Now().Add(s.stopWait)
	}
	switch {
	case !ok: // removed while it downloaded
		orphan = e.Path
	case e.Path != "":
		delete(s.live, e.ID)
		delete(s.netFails, e.ID)
		r.landed = true
		if s.settled != nil {
			s.settled[e.ID] = true
		}
		s.landing = append(s.landing, landed{r, r.song, e.Path})
		signal(s.land)
	case e.Failure == Network && (offline || s.netStopped || s.netFails[e.ID] < netTries):
		delete(s.live, e.ID)
		s.netStopped = true
		if s.stopBatch != nil {
			s.stopBatch()
		}
	case e.Failure != NoFailure:
		delete(s.live, e.ID)
		delete(s.netFails, e.ID)
		if s.settled != nil {
			s.settled[e.ID] = true
		}
		r.state, r.failure, r.message = failed, e.Failure, e.Message
		s.outcomes = append(s.outcomes, Outcome{VideoID: e.ID, Failure: e.Failure})
		save = append(save, r)
		if e.Failure == Network && s.stopBatch != nil {
			s.netStopped = true
			s.stopBatch()
		}
	default:
		s.live[e.ID] = e
		progress = true
	}
	// Progress changes the status a few times a second, not on every line;
	// State has it at once.
	tell := !progress || !had || time.Since(s.told) >= tellEvery
	if progress {
		bump = tell
	}
	if bump {
		s.changedLocked()
	}
	if tell {
		s.told = time.Now()
	}
	s.mu.Unlock()
	if tell {
		s.notify()
	}
	s.save(save...)
	if orphan != "" {
		os.Remove(orphan)
	}
	if e.Path != "" {
		// Free space is looked at again before the next file.
		if free, ok := s.readSpace(); ok && free < minFree {
			s.mu.Lock()
			if s.stopBatch != nil {
				s.stopBatch()
			}
			s.mu.Unlock()
		}
	}
}

func (s *Service) finisher() {
	defer s.wg.Done()
	for s.ctx.Err() == nil {
		var l landed
		var have bool
		panicked := s.guard("finisher", func() {
			if l, have = s.takeLanded(); !have {
				select {
				case <-s.ctx.Done():
				case <-s.land:
				}
				return
			}
			s.finishOne(l)
		})
		if panicked && have {
			s.mu.Lock()
			if s.finishing == l.src {
				s.finishing, s.finishingID = "", ""
			}
			s.mu.Unlock()
		}
		if have {
			signal(s.wake) // a song asked for again while this one was finished can go on
		}
		if panicked {
			s.rest(s.panicWait)
		}
	}
}

// finishOne files a landed song, if it is still wanted.
func (s *Service) finishOne(l landed) {
	if !s.wanted(l) {
		s.discard(l)
		return
	}
	var path string
	var size int64
	var replaced bool
	var err error
	if s.guard("finisher", func() { path, size, replaced, err = s.fin.finish(s.ctx, l.song, l.src) }) {
		err = errPanic
	}
	s.settle(l, path, size, replaced, err)
}

// wanted reports whether the row l was downloaded for is still there.
func (s *Service) wanted(l landed) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rows[l.song.VideoID] == l.row
}

// discard deletes the file of a song removed before it was finished, unless a
// newer download landed on the same file.
func (s *Service) discard(l landed) {
	s.mu.Lock()
	s.finishing, s.finishingID = "", ""
	reused := slices.ContainsFunc(s.landing, func(o landed) bool { return o.src == l.src })
	s.mu.Unlock()
	if !reused {
		os.Remove(l.src)
	}
}

// settle takes the result of finishing l: the song is saved, or failed. A song
// removed meanwhile is dropped from the library again, unless its file
// replaced one of its own: a Remove leaves a saved song's file, so that file
// was the user's before this download, and stays. One not finished because
// the service is closing stays queued.
func (s *Service) settle(l landed, path string, size int64, replaced bool, err error) {
	if err != nil && s.ctx.Err() != nil {
		return // the song is queued still: yt-dlp finds its file again
	}
	if err != nil {
		s.log.Warn("finish a download", "id", l.song.VideoID, "err", err)
	}
	var save []*row
	s.mu.Lock()
	s.finishing, s.finishingID = "", ""
	r := s.rows[l.song.VideoID]
	current := r == l.row
	if current {
		r.landed = false
		if err != nil {
			r.state, r.failure, r.message = failed, Other, err.Error()
			s.outcomes = append(s.outcomes, Outcome{VideoID: r.song.VideoID, Failure: Other})
		} else {
			r.state, r.path, r.bytes, r.finished = saved, path, size, time.Now().UnixMilli()
			r.failure, r.message = NoFailure, ""
			s.outcomes = append(s.outcomes, Outcome{VideoID: r.song.VideoID, Path: path})
		}
		save = append(save, r)
	}
	s.changedLocked()
	s.mu.Unlock()
	switch {
	case current:
		s.save(save...)
	case err == nil && !replaced:
		s.unfile(path)
	case err != nil:
		s.discard(l)
	}
	s.refreshStorage()
}

// unfile takes back a song that was filed but is no longer wanted.
func (s *Service) unfile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.log.Warn("remove a song forgotten while it was filed", "path", path, "err", err)
	}
	if _, err := s.opts.DB.Exec(`DELETE FROM tracks WHERE path = ?`, path); err != nil {
		s.log.Warn("forget a song forgotten while it was filed", "path", path, "err", err)
	}
	// The folders it was in go too, if nothing else is in them.
	for dir, i := filepath.Dir(path), 0; i < 2; dir, i = filepath.Dir(dir), i+1 {
		if os.Remove(dir) != nil {
			break
		}
	}
}

// takeLanded takes the next file to finish. It is the one being finished, so
// Remove leaves it be.
func (s *Service) takeLanded() (landed, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.landing) == 0 {
		return landed{}, false
	}
	l := s.landing[0]
	s.landing = s.landing[1:]
	s.finishing, s.finishingID = l.src, l.song.VideoID
	return l, true
}

// readSpace reads the card's free space for the Downloads tab and for the
// decision to go on.
func (s *Service) readSpace() (free int64, ok bool) {
	total, free, err := s.space(s.opts.Music)
	if err != nil {
		return 0, false
	}
	s.mu.Lock()
	changed := s.storage.Total != total || s.storage.Free != free
	if changed {
		s.storage.Total, s.storage.Free = total, free
		s.changedLocked()
	}
	s.mu.Unlock()
	if changed {
		s.notify()
	}
	return free, true
}

// refreshStorage reads the card's space and the library's size, off the UI
// thread, for the Downloads tab.
func (s *Service) refreshStorage() {
	total, free, err := s.space(s.opts.Music)
	if err != nil {
		s.log.Warn("read free space", "err", err)
	}
	music, err := musicBytes(s.opts.DB)
	if err != nil {
		s.log.Warn("read the library's size", "err", err)
	}
	s.mu.Lock()
	s.storage = Storage{Total: total, Free: free, Music: music}
	s.changedLocked()
	s.mu.Unlock()
	s.notify()
}

// diskSpace is the size and free space of the filesystem holding path, or
// its nearest folder that exists.
func diskSpace(path string) (total, free int64, err error) {
	for {
		var st syscall.Statfs_t
		if err = syscall.Statfs(path, &st); err == nil {
			return int64(st.Blocks) * int64(st.Frsize), int64(st.Bavail) * int64(st.Frsize), nil
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, 0, err
		}
		path = parent
	}
}

// signal sends on c unless a value is already waiting there.
func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

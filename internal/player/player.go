// Package player owns the play queue and drives the audio engine: play, pause,
// next, previous, shuffle and repeat.
package player

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
)

// Track is one file in the queue.
type Track struct {
	Path                              string
	Title, Artist, AlbumArtist, Album string
	Gain, Peak                        float64 // ReplayGain track gain (dB) and peak
	HasGain                           bool
	AlbumGain, AlbumPeak              float64 // ReplayGain album gain (dB) and peak
	HasAlbumGain                      bool
	Duration                          time.Duration // 0 when unknown
	// VideoID is a YouTube Music song's video ID. With no Path yet the song is
	// still downloading: playback waits on it, and Resolve gives it its file.
	VideoID string
	// SpotifyID is a Spotify track's ID. With no Path the track is streamed
	// (see SetStreaming), and Cover links to its album's picture.
	SpotifyID string
	Cover     string
}

// IsBook reports whether t is an audiobook: Next and Prev move by chapter in
// it, and its place is kept as an album's is, though it is one file.
func (t Track) IsBook() bool { return strings.EqualFold(filepath.Ext(t.Path), ".m4b") }

// Pending reports whether t is a song still downloading.
func (t Track) Pending() bool { return t.Path == "" && t.VideoID != "" }

// Streamed reports whether t is a Spotify track played as a stream.
func (t Track) Streamed() bool { return t.Path == "" && t.SpotifyID != "" }

// key names t's audio: its file, or its stream.
func (t Track) key() string {
	if t.Streamed() {
		return "spotify:" + t.SpotifyID
	}
	return t.Path
}

// Repeat is the repeat mode.
type Repeat uint8

const (
	RepeatOff Repeat = iota // play the queue once
	RepeatAll               // wrap to the first track after the last
	RepeatOne               // play the current track again and again
)

// State is a snapshot for the UI.
type State struct {
	Queued            bool  // the queue isn't empty
	Track             Track // the current track; zero when nothing is queued
	Index, Count      int   // position in play order, from 0, and queue length
	Playing           bool
	Waiting           bool // the current track is still downloading; it plays when it lands
	Cued              bool // the current track is stopped on: it plays only when asked
	Loading           bool // the current track is a stream being opened
	Stalled           bool // the current stream broke, and is being picked up again
	Kbps              int  // the current stream's quality, 0 for a file
	Elapsed, Duration time.Duration
	Rate              int    // the current file's sample rate, 0 if unknown
	Format            string // from the file extension: "MP3", "FLAC"
	// Chapter and Chapters are, in a book with chapters, the one playing
	// (from 1) and how many there are; both 0 otherwise.
	Chapter, Chapters int
	Shuffle           bool
	Repeat            Repeat
	Err               error  // the last output error, which paused playback
	Version           uint64 // changes whenever the queue or its order does
}

// Opener opens a track's file for decoding. It is never called with the
// player's lock held.
type Opener func(path string) (audio.Source, error)

// restartAfter is how far into a track Prev restarts it instead of going back.
const restartAfter = 3 * time.Second

// levellingOffset lifts ReplayGain's -18 LUFS reference to the -14 LUFS the
// player aims for; peak protection keeps loud tracks from clipping.
const levellingOffset = 4

// Player owns the queue and drives an audio engine. It is safe for concurrent
// use: the UI calls it, and the engine reports track ends from its goroutine.
type Player struct {
	engine *audio.Engine
	open   Opener
	log    *slog.Logger

	mu      sync.Mutex
	queue   []Track
	order   []int  // play order: indexes into queue
	pos     int    // index into order
	version uint64 // bumped whenever the queue or its order changes
	cur     audio.Source
	// curRate and curFrames describe cur until the engine reports it loaded.
	curRate   int
	curFrames int64
	framesOf  string // the path curFrames is the length of
	// next is the track primed to follow cur without a gap, at nextPos.
	next              audio.Source
	nextPos, nextRate int
	nextIdx           int  // next's entry in queue, kept right across edits
	nextAlbum         bool // whether next was levelled by album
	nextFrames        int64
	gen               uint64 // bumped whenever what should follow cur changes
	empties           int    // tracks in a row that produced no audio
	cued              bool   // cur was restored paused and hasn't been resumed
	startFrame        int64  // where cur was started, cued or last sought to
	playedOut         bool   // the queue ended and cur was dropped
	waiting           bool   // the current track is pending: nothing is open until it lands
	loading           bool   // the current track is being opened, a file or a stream
	format, formatOf  string // State's Format, and the path it was worked out for
	levelling, closed bool
	wg                sync.WaitGroup // priming goroutines
	shuffle           bool
	repeat            Repeat
	rng               *rand.Rand
	// The sleep timer: what it is set to, a timed one's timer and the
	// generation that timer belongs to, when the fade it began ends, and
	// whether the engine fades out the end of cur, where it stops playback.
	sleep      Sleep
	sleepTimer stopper
	sleepGen   uint64
	fadeEnds   time.Time
	endFade    bool
	after      func(time.Duration, func()) stopper // time.AfterFunc; tests stand in for it
	now        func() time.Time
	// Streaming: how streams open and who hears of their troubles (see
	// SetStreaming), when the listener last skipped, and the opens under way.
	stream      StreamOpener
	notify      func(Notice)
	skipped     time.Time
	retried     bool // the current stream was refused and waited for once
	failures    int  // streams in a row that wouldn't open
	cancelOpen  context.CancelFunc
	cancelPrime context.CancelFunc
	retryTimer  stopper       // tries a refused stream again
	primeTimer  stopper       // opens the stream to follow once cur nears its end
	speed       atomic.Uint64 // the books' speed, as a float64's bits
	// chapters says where the chapters of a book start; nil if it can't.
	chapters func(path string) []time.Duration
}

// New creates a player that plays through out and opens files with open.
func New(out audio.Output, open Opener, log *slog.Logger) *Player {
	p := &Player{open: open, log: log, rng: rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0)),
		after: afterFunc, now: time.Now}
	p.SetBookSpeed(1)
	p.engine = audio.NewEngine(out, p.ended)
	return p
}

// Close stops playback, waits for priming to finish and releases the output.
func (p *Player) Close() error {
	p.mu.Lock()
	p.closed = true
	if p.sleepTimer != nil {
		p.sleepTimer.Stop()
	}
	p.cancelOpensLocked()
	p.mu.Unlock()
	p.wg.Wait()
	return p.engine.Close()
}

// SetLevelling turns ReplayGain levelling on or off for tracks opened from now.
func (p *Player) SetLevelling(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on == p.levelling {
		return
	}
	p.levelling = on
	if p.cur != nil {
		p.primeLocked() // the primed track was opened with the old setting
	}
}

// levelGain is the gain and peak to level t with: its album's when album is
// set and the track has one, its own otherwise.
func levelGain(t Track, album bool) (db, peak float64, ok bool) {
	switch {
	case album && t.HasAlbumGain:
		return t.AlbumGain + levellingOffset, t.AlbumPeak, true
	case t.HasGain:
		return t.Gain + levellingOffset, t.Peak, true
	}
	return 0, 0, false
}

// albumModeLocked reports whether the queue is one album played in order, so
// tracks keep their album's levels and gapless joins don't jump.
func (p *Player) albumModeLocked() bool {
	if p.shuffle || len(p.queue) == 0 {
		return false
	}
	first := p.queue[0]
	for _, t := range p.queue[1:] {
		if t.AlbumArtist != first.AlbumArtist || t.Album != first.Album {
			return false
		}
	}
	return first.Album != ""
}

// openTrack opens t, wrapped in its ReplayGain when levelling is on, the
// album's if album is set.
func (p *Player) openTrack(t Track, levelling, album bool) (audio.Source, error) {
	src, err := p.open(t.Path)
	if err != nil {
		return nil, err
	}
	if rate := src.SampleRate(); !audio.SupportedRate(rate) {
		src.Close()
		return nil, fmt.Errorf("%w: %d Hz", audio.ErrRate, rate)
	}
	if db, peak, ok := levelGain(t, album); levelling && ok {
		src = audio.WithGain(src, db, peak)
	}
	if t.IsBook() {
		src = audio.NewTempo(src, p.bookSpeed)
	}
	return src, nil
}

// bookSpeed is the speed books play at, read as they play.
func (p *Player) bookSpeed() float64 { return math.Float64frombits(p.speed.Load()) }

// SetBookSpeed sets how fast books play, 1 being their own pace, at once and
// without changing their pitch. It is kept between books, not between runs.
func (p *Player) SetBookSpeed(s float64) {
	p.speed.Store(math.Float64bits(min(max(s, audio.MinTempo), audio.MaxTempo)))
}

// BookSpeed is how fast books play.
func (p *Player) BookSpeed() float64 { return p.bookSpeed() }

// Play replaces the queue with tracks and starts at tracks[start].
func (p *Player) Play(tracks []Track, start int) { p.PlayFrom(tracks, start, 0) }

// PlayFrom replaces the queue with tracks and starts tracks[start] at frame,
// to carry on with an album where it was left.
func (p *Player) PlayFrom(tracks []Track, start int, frame int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keepPlayingLocked()
	p.version++
	p.empties = 0 // the listener chose this; earlier silent tracks don't count
	if len(tracks) == 0 {
		p.queue, p.order, p.pos = nil, nil, 0
		p.stopLocked()
		return
	}
	p.queue = slices.Clone(tracks)
	start = min(max(start, 0), len(tracks)-1)
	p.order = p.orderLocked(start)
	p.pos = start
	if p.shuffle {
		p.pos = 0
	}
	p.startAtLocked(frame)
}

// orderLocked is the play order: the queue in order, or shuffled with first
// at the front.
func (p *Player) orderLocked(first int) []int {
	order := make([]int, len(p.queue))
	for i := range order {
		order[i] = i
	}
	if p.shuffle {
		p.rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		i := 0
		for order[i] != first {
			i++
		}
		order[0], order[i] = order[i], order[0]
	}
	return order
}

// startLocked plays the track at pos from its start.
func (p *Player) startLocked() { p.startAtLocked(0) }

// startAtLocked plays the track at pos from frame, once it has opened or, if
// it is still downloading, once it lands. Files that won't open are moved on
// past (see openFileLocked).
func (p *Player) startAtLocked(frame int64) {
	p.stopLocked()
	p.playedOut = false
	p.loadLocked(frame, len(p.order))
}

// loadLocked opens the track at pos from frame, or waits for its download.
// Opening happens on a goroutine, so neither the caller nor anything else
// that takes the lock waits on a slow card or the network. tries bounds how
// many files in a row are tried when they won't open.
func (p *Player) loadLocked(frame int64, tries int) {
	t := p.queue[p.order[p.pos]]
	switch {
	case t.Pending():
		p.waiting, p.startFrame = true, max(frame, 0)
	case t.Streamed() && p.stream != nil:
		p.openLocked(frame)
	default:
		p.openFileLocked(frame, tries)
	}
}

// openFileLocked opens the file at pos on a goroutine and plays it from
// frame, or cues it there if playback is cued by then. If it won't open and
// playback is cued, playback stops on it, and play tries it again; otherwise
// the tracks after it are started from their tops instead, until tries files
// in all have failed. The order wraps only with repeat all.
func (p *Player) openFileLocked(frame int64, tries int) {
	if p.closed {
		return
	}
	p.gen++ // anything opening for what was playing is stale
	p.cancelOpensLocked()
	p.loading, p.startFrame = true, max(frame, 0)
	gen, t, levelling, album := p.gen, p.queue[p.order[p.pos]], p.levelling, p.albumModeLocked()
	p.wg.Go(func() {
		src, err := p.openTrack(t, levelling, album)
		p.mu.Lock()
		defer p.mu.Unlock()
		if gen != p.gen || p.closed {
			if src != nil {
				src.Close()
			}
			return
		}
		p.loading = false
		if err != nil {
			p.unplayableLocked(t, err, tries)
			return
		}
		p.failures = 0
		p.cur, p.curRate, p.curFrames = src, src.SampleRate(), src.Frames()
		p.framesOf = t.key()
		if p.cued {
			p.engine.Cue(src, frame)
		} else {
			p.engine.PlayFrom(src, frame)
		}
		p.primeLocked()
	})
}

// unplayableLocked moves on from the file t at pos, which wouldn't open, as
// openFileLocked describes; tries counted it.
func (p *Player) unplayableLocked(t Track, err error, tries int) {
	if p.cued {
		p.log.Warn("can't open track", "path", t.Path, "err", err)
		return
	}
	p.log.Warn("skipping unplayable track", "path", t.Path, "err", err)
	switch {
	case tries > 1 && p.pos+1 < len(p.order):
		p.pos++
	case tries > 1 && p.repeat == RepeatAll:
		p.pos = 0
	default:
		p.stopLocked() // nothing opens, or the queue has ended
		return
	}
	p.loadLocked(0, tries-1)
}

func (p *Player) stopLocked() {
	p.cur, p.next, p.cued = nil, nil, false
	p.waiting, p.loading, p.retried = false, false, false
	p.startFrame = 0
	p.gen++
	p.cancelOpensLocked()
	p.engine.Stop()
}

// followingLocked is the position after pos in play order, if any.
func (p *Player) followingLocked(pos int) (int, bool) {
	switch {
	case p.repeat == RepeatOne:
		return pos, true
	case pos+1 < len(p.order):
		return pos + 1, true
	case p.repeat == RepeatAll:
		return 0, true
	}
	return 0, false
}

// primeLocked opens the track after cur on a goroutine of its own and queues it
// in the engine, so it follows without a gap. Anything primed before is
// dropped.
func (p *Player) primeLocked() {
	p.gen++
	p.next = nil
	p.cancelPrimeLocked()
	p.engine.Queue(nil)
	if p.cur != nil && p.sleepsAfterLocked() {
		p.engine.FadeEnd(p.cur, sleepFade) // the sleep timer stops playback at its end
		p.endFade = true
		return
	}
	if p.endFade {
		p.engine.FadeEnd(nil, 0) // a track follows now, as one was added
		p.endFade = false
	}
	pos, ok := p.followingLocked(p.pos)
	if !ok || p.cur == nil || p.closed {
		return
	}
	idx, gen, levelling, album := p.order[pos], p.gen, p.levelling, p.albumModeLocked()
	t := p.queue[idx]
	if t.Pending() {
		return // primed when it lands
	}
	if t.Streamed() {
		if p.stream != nil {
			p.primeStreamLocked(gen, pos, idx, t, levelling, album)
		}
		return
	}
	p.wg.Go(func() {
		src, err := p.openTrack(t, levelling, album)
		if err != nil {
			return // tried again, and skipped, when cur ends
		}
		rate, frames := src.SampleRate(), src.Frames()
		p.mu.Lock()
		defer p.mu.Unlock()
		if gen != p.gen || p.closed {
			src.Close()
			return
		}
		p.next, p.nextPos, p.nextIdx, p.nextAlbum, p.nextRate, p.nextFrames = src, pos, idx, album, rate, frames
		p.engine.Queue(src)
	})
}

// ended moves on after a source played to its end.
func (p *Player) ended(end audio.End) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || end.Source != p.cur {
		return // replaced while it was finishing, or the player is shutting down
	}
	t := p.queue[p.order[p.pos]]
	if end.Err != nil {
		p.log.Warn("track ended early", "path", t.Path, "err", end.Err)
	}
	if end.Frames == 0 {
		p.empties++
	} else {
		p.empties = 0
	}
	if p.empties > 0 && (p.repeat == RepeatOne || p.empties >= len(p.queue)) {
		p.log.Warn("stopping: tracks play no audio", "path", t.Path)
		p.empties = 0
		p.stopLocked()
		return
	}
	if end.Next == nil && (p.sleepsAfterLocked() || p.fadingLocked()) {
		// The sleep timer stops playback here, with the next track cued: after
		// the track it ends with, or at a change of track during a timed fade.
		p.sleepEndLocked()
		return
	}
	if end.Next != nil && end.Next == p.next {
		// The primed track took over without a gap.
		p.cur, p.pos, p.curRate, p.curFrames, p.startFrame = p.next, p.nextPos, p.nextRate, p.nextFrames, 0
		p.framesOf = p.queue[p.order[p.pos]].key()
		p.primeLocked()
		return
	}
	pos, ok := p.followingLocked(p.pos)
	if !ok {
		p.cur, p.next, p.playedOut = nil, nil, true // the queue stays, stopped on its last track
		return
	}
	p.pos = pos
	p.startLocked()
}

// Toggle pauses or resumes, or plays the current track again once the queue
// has played out. During the sleep timer's fade it keeps playing instead, at
// full volume again.
func (p *Player) Toggle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.keepPlayingLocked() {
		return
	}
	switch {
	case p.waiting:
		p.cued = false // it plays as soon as it lands
	case p.loading:
		p.cued = !p.cued // it plays, or is cued, when it opens
	case len(p.queue) == 0:
	case p.cur == nil && p.cued:
		p.startAtLocked(p.startFrame) // a stream restored, or stopped on
	case p.cur == nil:
		p.startLocked()
	case p.playingLocked():
		p.engine.Pause()
	default:
		p.cued = false
		p.engine.Resume()
	}
}

// SetChapters lets Next and Prev move through a book by chapter: f gives the
// start of each chapter of the book at path, in order, or none. It may be
// called with the player's lock held, so it must not call the player.
func (p *Player) SetChapters(f func(path string) []time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.chapters = f
}

// chapterTarget is where Next (dir > 0) or Prev (dir < 0) takes a book at
// place at, given where its chapters start: the next chapter, or the start of
// this one, or the one before when it has only just begun. ok is false when
// there is no chapter that way, and the track should change instead.
func chapterTarget(starts []time.Duration, at time.Duration, dir int) (to time.Duration, ok bool) {
	i := 0
	for j, s := range starts {
		if s <= at {
			i = j
		}
	}
	switch {
	case len(starts) == 0:
		return 0, false
	case dir > 0 && i+1 < len(starts):
		return starts[i+1], true
	case dir > 0:
		return 0, false
	case i > 0 && at-starts[i] <= restartAfter:
		return starts[i-1], true
	case at > restartAfter:
		return starts[i], true
	}
	return 0, false
}

// chapterStepLocked moves the current book by a chapter, as chapterTarget
// says. It reports false when the current track isn't a book with chapters,
// or has no chapter that way.
func (p *Player) chapterStepLocked(dir int) bool {
	if p.chapters == nil || p.cur == nil || len(p.queue) == 0 {
		return false
	}
	t := p.queue[p.order[p.pos]]
	if !t.IsBook() {
		return false
	}
	to, ok := chapterTarget(p.chapters(t.Path), p.placeLocked(), dir)
	if ok {
		p.seekLocked(to)
	}
	return ok
}

// Next skips to the next track, wrapping at the end only with repeat all. In
// a book it skips to the next chapter.
func (p *Player) Next() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keepPlayingLocked()
	if p.chapterStepLocked(1) {
		return
	}
	p.empties, p.skipped = 0, p.now()
	switch {
	case len(p.queue) == 0:
		return
	case p.pos+1 < len(p.order):
		p.pos++
	case p.repeat == RepeatAll:
		p.pos = 0
	default:
		return
	}
	p.startLocked()
}

// Prev goes back a track, or restarts the current one once it has played for
// a few seconds or when it is the first. In a book it goes back a chapter, or
// to the start of this one once it has played for a few seconds.
func (p *Player) Prev() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keepPlayingLocked()
	if p.chapterStepLocked(-1) {
		return
	}
	p.empties, p.skipped = 0, p.now()
	if len(p.queue) == 0 {
		return
	}
	if p.pos > 0 && (p.cur == nil || p.elapsedLocked() <= restartAfter) {
		p.pos--
	}
	p.startLocked()
}

// Seek moves the current track to d from its start, clamped to its length.
// It does nothing when no track is loaded.
func (p *Player) Seek(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keepPlayingLocked()
	p.seekLocked(d)
}

// SeekBy moves the current track by d, which may be negative, from where it
// is, clamped to its start and end.
func (p *Player) SeekBy(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keepPlayingLocked()
	if p.cur == nil || p.curRate <= 0 {
		return
	}
	p.seekLocked(max(p.placeLocked()+d, 0))
}

func (p *Player) seekLocked(d time.Duration) {
	if p.cur == nil || p.curRate <= 0 {
		return
	}
	f := max(int64(d)*int64(p.curRate)/int64(time.Second), 0)
	if p.curFrames > 0 {
		f = min(f, p.curFrames)
	}
	p.startFrame = f
	p.engine.SeekFrame(f)
	if pos, ok := p.followingLocked(p.pos); ok && p.next == nil && p.queue[p.order[pos]].Streamed() {
		p.primeLocked() // the stream to follow opens sooner, or later
	}
}

// SetShuffle turns shuffle on or off without interrupting the current track.
func (p *Player) SetShuffle(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if on == p.shuffle {
		return
	}
	p.shuffle = on
	if len(p.queue) == 0 {
		return
	}
	cur := p.order[p.pos]
	p.order = p.orderLocked(cur)
	p.pos = cur
	if on {
		p.pos = 0
	}
	p.editedLocked()
}

// CycleRepeat steps through off, all and one.
func (p *Player) CycleRepeat() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.repeat = (p.repeat + 1) % 3
	if p.cur != nil {
		p.primeLocked()
	}
}

// Enqueue adds tracks to the end of the play order. Nothing starts playing if
// nothing was queued; Toggle starts the first. After the queue has played out,
// Toggle starts the first track added.
func (p *Player) Enqueue(tracks []Track) {
	if len(tracks) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.queue)
	if p.playedOut {
		// Toggle starts the new music rather than the old last track.
		p.pos, p.playedOut = len(p.order), false
	}
	p.queue = append(p.queue, tracks...)
	for i := range tracks {
		p.order = append(p.order, n+i)
	}
	p.editedLocked()
}

// Queue appends the queue in play order to dst and returns it, with the
// current track's position in it.
func (p *Player) Queue(dst []Track) ([]Track, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, i := range p.order {
		dst = append(dst, p.queue[i])
	}
	return dst, p.pos
}

// Upcoming reports the track to play after the current one, as playback
// moves on to it: the next in play order, the first again after the last
// with repeat all, or the current one with repeat one. It reports false when
// nothing follows.
func (p *Player) Upcoming() (Track, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.order) == 0 {
		return Track{}, false
	}
	pos, ok := p.followingLocked(p.pos)
	if !ok {
		return Track{}, false
	}
	return p.queue[p.order[pos]], true
}

// PlayIndex plays the track at position i of the play order.
func (p *Player) PlayIndex(i int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.order) {
		return
	}
	p.keepPlayingLocked()
	p.pos, p.empties, p.skipped = i, 0, p.now()
	p.startLocked()
}

// Remove takes the track at position i of the play order out of the queue.
// The current track can't be removed.
func (p *Player) Remove(i int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i < 0 || i >= len(p.order) || i == p.pos {
		return
	}
	p.deleteLocked(i)
	p.editedLocked()
}

// deleteLocked takes the entry at position i of the play order out of the
// queue. pos and the primed entry's index stay on the entries they were on;
// pos moves on to the next entry if it was i's.
func (p *Player) deleteLocked(i int) {
	q := p.order[i]
	switch {
	case q == p.nextIdx:
		p.nextIdx = -1 // the primed entry is gone
	case q < p.nextIdx:
		p.nextIdx--
	}
	p.queue = slices.Delete(p.queue, q, q+1)
	p.order = slices.Delete(p.order, i, i+1)
	for j, v := range p.order {
		if v > q {
			p.order[j] = v - 1
		}
	}
	if i < p.pos {
		p.pos--
	}
}

// Move moves the track at position from of the play order to position to.
// Only tracks after the current one move, and only among themselves.
func (p *Player) Move(from, to int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.order)
	if from == to || from <= p.pos || to <= p.pos || from >= n || to >= n {
		return
	}
	v := p.order[from]
	p.order = slices.Insert(slices.Delete(p.order, from, from+1), to, v)
	p.editedLocked()
}

// Resolve gives the queue's entries waiting for videoID's download their
// file. Playback waiting on one starts it, or cues it when the queue was
// restored and hasn't been played; one due next is primed.
func (p *Player) Resolve(videoID, path string) {
	if path == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	found := false
	for i := range p.queue {
		if t := &p.queue[i]; t.Pending() && t.VideoID == videoID {
			t.Path, found = path, true
		}
	}
	if !found {
		return
	}
	p.version++
	switch {
	case p.waiting && p.queue[p.order[p.pos]].VideoID == videoID:
		if p.cued {
			p.cueLocked()
		} else {
			p.startAtLocked(p.startFrame)
		}
	case p.cur != nil:
		p.editedLocked() // keeps the next track's prime if it is still right
	}
}

// Drop takes the entries waiting for videoID's download out of the queue,
// as when it failed, and reports how many there were. Playback waiting on
// one moves on to what follows it.
func (p *Player) Drop(videoID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return 0
	}
	cur := p.queue[p.order[p.pos]]
	waitedOn := p.waiting && cur.Pending() && cur.VideoID == videoID
	n := 0
	removedCur := false
	cued := p.cued
	// landLocked moves on to the entry at pos: it starts it, or cues it from the
	// top when the queue was restored and hasn't been played.
	landLocked := func() {
		if !cued {
			p.startLocked()
			return
		}
		p.stopLocked()
		p.cued, p.playedOut = true, false
		if p.queue[p.order[p.pos]].Pending() {
			p.waiting = true // cued when it lands
			return
		}
		p.cueLocked()
	}
	for i := len(p.order) - 1; i >= 0; i-- {
		if t := p.queue[p.order[i]]; !t.Pending() || t.VideoID != videoID {
			continue
		}
		removedCur = removedCur || i == p.pos
		p.deleteLocked(i)
		n++
	}
	if n == 0 {
		return 0
	}
	switch {
	case len(p.order) == 0:
		p.pos, p.playedOut = 0, false
		p.stopLocked()
		p.version++
	case removedCur && p.pos >= len(p.order):
		// pos was the last entry. Wrap with repeat all if playback was waiting
		// on it; otherwise leave the queue as one that has played out.
		p.version++
		if waitedOn && p.repeat == RepeatAll {
			p.pos = 0
			landLocked()
			break
		}
		p.pos = len(p.order) - 1
		p.stopLocked()
		p.playedOut = true
		if p.queue[p.order[p.pos]].key() != p.framesOf {
			p.curFrames = 0 // the last file opened isn't this entry's
		}
	case waitedOn:
		// pos is now what followed the dropped entry.
		p.version++
		landLocked()
	default:
		p.editedLocked()
	}
	return n
}

// editedLocked records a change to the queue. The track to follow the current
// one is primed again unless it is the entry already primed, at the same
// levels, in which case only its position is updated.
func (p *Player) editedLocked() {
	p.version++
	if p.cur == nil {
		return
	}
	if p.next != nil {
		if pos, ok := p.followingLocked(p.pos); ok && p.order[pos] == p.nextIdx && p.albumModeLocked() == p.nextAlbum {
			p.nextPos = pos
			return
		}
	}
	p.primeLocked()
}

// State reports what is playing.
func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := State{Shuffle: p.shuffle, Repeat: p.repeat, Count: len(p.queue), Version: p.version}
	if len(p.queue) == 0 {
		return s
	}
	t := p.queue[p.order[p.pos]]
	s.Queued, s.Track, s.Index = true, t, p.pos
	s.Waiting, s.Loading, s.Cued = p.waiting, p.loading && t.Streamed(), p.cued
	if t.Path != p.formatOf {
		p.format, p.formatOf = strings.ToUpper(strings.TrimPrefix(filepath.Ext(t.Path), ".")), t.Path
	}
	s.Format = p.format
	if p.loading {
		s.Playing, s.Duration = !p.cued, t.Duration // the length known before it opens
	}
	if p.cur == nil {
		return s
	}
	if k, ok := p.cur.(interface{ Kbps() int }); ok {
		s.Kbps = k.Kbps()
	}
	if b, ok := p.cur.(interface{ Stalled() bool }); ok {
		s.Stalled = b.Stalled()
	}
	st := p.engine.Status()
	if st.Source != p.cur {
		// The engine hasn't picked the new source up yet.
		s.Playing, s.Rate = !p.cued, p.curRate
		if p.curRate > 0 {
			s.Elapsed = duration(p.startFrame, p.curRate)
			if p.curFrames > 0 {
				s.Duration = duration(p.curFrames, p.curRate)
			}
		}
		p.chapterLocked(&s, t)
		return s
	}
	s.Playing, s.Rate, s.Err = st.Playing, st.Rate, st.Err
	if st.Rate > 0 {
		frame := max(st.Frame, p.startFrame) // the engine hasn't reached the start yet
		s.Elapsed = duration(frame, st.Rate)
		if st.Frames > 0 {
			s.Duration = duration(st.Frames, st.Rate)
		}
	}
	p.chapterLocked(&s, t)
	return s
}

// chapterLocked fills in which chapter of the book t, the current track, s is
// in, from s.Elapsed.
func (p *Player) chapterLocked(s *State, t Track) {
	if p.chapters == nil || !t.IsBook() {
		return
	}
	starts := p.chapters(t.Path)
	if len(starts) == 0 {
		return
	}
	s.Chapters, s.Chapter = len(starts), 1
	for i, c := range starts {
		if c <= s.Elapsed {
			s.Chapter = i + 1
		}
	}
}

// playingLocked reports whether cur is playing. A source the engine hasn't
// picked up yet counts as playing, as Play starts it unpaused, unless it was
// restored paused.
func (p *Player) playingLocked() bool {
	st := p.engine.Status()
	return st.Source != p.cur && !p.cued || st.Source == p.cur && st.Playing
}

func (p *Player) elapsedLocked() time.Duration {
	st := p.engine.Status()
	if st.Source != p.cur || st.Rate == 0 {
		return 0
	}
	return duration(st.Frame, st.Rate)
}

// placeLocked is how far into cur playback is, as State shows it: not behind
// where cur was started or last sought to, which the engine may not have
// reached yet. cur must be set and its rate known.
func (p *Player) placeLocked() time.Duration {
	st := p.engine.Status()
	if st.Source != p.cur || st.Rate == 0 {
		return duration(p.startFrame, p.curRate)
	}
	return duration(max(st.Frame, p.startFrame), st.Rate)
}

func duration(frames int64, rate int) time.Duration {
	return time.Duration(frames) * time.Second / time.Duration(rate)
}

// Snapshot is what is saved to carry on after a restart.
type Snapshot struct {
	Queue    []Track
	Order    []int
	Pos      int
	Frame    int64   // position in the current track, in its frames
	Fraction float64 // how far through the current track's album, 0 to 1
	// AlbumRun is set when the current track's album was queued as an album:
	// its tracks sit together in the queue, and there is more than one.
	// Fraction says nothing about a song queued from a list of songs.
	AlbumRun bool
	Shuffle  bool
	Repeat   Repeat
	Version  uint64 // changes whenever the queue or its order does
}

// Snapshot reports the queue and position; ok is false when nothing is queued.
func (p *Player) Snapshot() (Snapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return Snapshot{}, false
	}
	s := Snapshot{Queue: slices.Clone(p.queue), Order: slices.Clone(p.order), Pos: p.pos, Shuffle: p.shuffle, Repeat: p.repeat, Version: p.version}
	var elapsed, total float64
	if st := p.engine.Status(); p.cur != nil && st.Source == p.cur {
		s.Frame = st.Frame
		if st.Frames > 0 {
			elapsed, total = float64(st.Frame), float64(st.Frames)
		}
	}
	if (p.cur != nil || p.cued || p.waiting || p.loading) && s.Frame < p.startFrame {
		// Started, cued or sought, and the engine hasn't got there yet.
		s.Frame = p.startFrame
		if p.curFrames > 0 && !p.waiting && !p.loading {
			elapsed, total = float64(s.Frame), float64(p.curFrames)
		}
	}
	s.AlbumRun = p.albumRunLocked() || p.queue[p.order[p.pos]].IsBook()
	if p.playedOut {
		s.Frame, s.Fraction = max(p.curFrames, 0), 1
		return s, true
	}
	s.Fraction = p.albumFractionLocked(elapsed, total)
	return s, true
}

// sameAlbum reports whether t and u are tracks of one album.
func sameAlbum(t, u Track) bool {
	return t.Album != "" && t.AlbumArtist == u.AlbumArtist && t.Album == u.Album
}

// albumFractionLocked is how far through the current track's album the
// listener is, counting that album's tracks played before it, in play order,
// so a shuffled album moves on as it plays. A track without an album is one
// of its own.
func (p *Player) albumFractionLocked(elapsed, total float64) float64 {
	t := p.queue[p.order[p.pos]]
	before, n := 0, 1
	if t.Album != "" {
		n = 0
		for _, q := range p.queue {
			if sameAlbum(t, q) {
				n++
			}
		}
		for _, i := range p.order[:p.pos] {
			if sameAlbum(t, p.queue[i]) {
				before++
			}
		}
	}
	f := float64(before)
	if total > 0 {
		f += elapsed / total
	}
	return f / float64(n)
}

// albumRunLocked reports whether the current track's album is one unbroken
// run of more than one entry in the queue, as queueing an album leaves it.
func (p *Player) albumRunLocked() bool {
	t := p.queue[p.order[p.pos]]
	first, last, n := -1, -1, 0
	for i, q := range p.queue {
		if sameAlbum(t, q) {
			if first < 0 {
				first = i
			}
			last = i
			n++
		}
	}
	return n > 1 && last-first+1 == n
}

// Restore loads a saved snapshot paused at its position, unless something has
// been queued since launch. It reports whether it took the snapshot.
func (p *Player) Restore(s Snapshot) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) > 0 || len(s.Queue) == 0 || len(s.Order) != len(s.Queue) || s.Pos < 0 || s.Pos >= len(s.Order) {
		return false
	}
	for _, i := range s.Order {
		if i < 0 || i >= len(s.Queue) {
			return false
		}
	}
	p.queue, p.order, p.pos = slices.Clone(s.Queue), slices.Clone(s.Order), s.Pos
	p.shuffle, p.repeat = s.Shuffle, s.Repeat
	p.version++
	t := p.queue[p.order[p.pos]]
	// Kept even if the track won't open, so the next save doesn't lose the position.
	p.cued, p.startFrame, p.playedOut = true, max(s.Frame, 0), false
	if t.Pending() {
		p.waiting = true // cued when it lands
		return true
	}
	if t.Streamed() {
		return true // opened when played: no key is asked for before then
	}
	p.cueLocked()
	return true
}

// cueLocked opens the current track paused at startFrame, as a restored
// queue starts; playback is already cued. One that won't open leaves the
// queue stopped there.
func (p *Player) cueLocked() {
	p.waiting = false
	if p.queue[p.order[p.pos]].Streamed() {
		return // opened when played
	}
	p.openFileLocked(p.startFrame, len(p.order))
}

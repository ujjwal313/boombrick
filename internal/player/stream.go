package player

import (
	"context"
	"errors"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
	"github.com/ujjwal313/boombrick/internal/spotify"
)

// StreamOpener opens a streamed track from frame, at Spotify's rate,
// levelled if level is set. It may take a while; it is never called with the
// player's lock held.
type StreamOpener func(ctx context.Context, t Track, frame int64, level bool) (audio.Source, error)

// Notice is news about streaming the listener should hear of.
type Notice struct {
	Kind NoticeKind
	Wait time.Duration // for Throttled, until the track is tried again
}

// NoticeKind is what a Notice is about.
type NoticeKind uint8

const (
	// Throttled: Spotify refused for now; the track is tried again after
	// the wait.
	Throttled NoticeKind = iota + 1
	// StillThrottled: refused again after the wait; playback stopped on the
	// track.
	StillThrottled
	// Unavailable: a track that can't be played here was skipped.
	Unavailable
	// Offline: a streamed track was skipped, being offline.
	Offline
	// Failed: a streamed track that failed to open was skipped.
	Failed
	// SignedOut: a streamed track was skipped, the Spotify session having
	// ended.
	SignedOut
	// NeedsPremium: a streamed track was skipped, Spotify streaming to
	// Premium accounts alone.
	NeedsPremium
)

const (
	// settle is how long after a skip a stream waits before it is opened,
	// so skipping through songs opens only the one landed on.
	settle = 500 * time.Millisecond
	// streamLead is how long before the current track ends a streamed track
	// to follow it is opened: Spotify gives a key for each, and asking for
	// them faster than songs are heard gets them refused.
	streamLead = 20 * time.Second
)

// SetStreaming lets the player stream tracks with no file but a Spotify ID
// through open, telling notify what the listener should hear of. notify is
// called with the player's lock held: it mustn't block or call the player.
func (p *Player) SetStreaming(open StreamOpener, notify func(Notice)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stream, p.notify = open, notify
}

func (p *Player) tellLocked(n Notice) {
	if p.notify != nil {
		p.notify(n)
	}
}

// openLocked opens the streamed track at pos from frame on a goroutine,
// after the settling time if the listener has just skipped, and plays it, or
// cues it when playback was paused meanwhile.
func (p *Player) openLocked(frame int64) {
	p.loading, p.startFrame = true, max(frame, 0)
	gen, t, level := p.gen, p.queue[p.order[p.pos]], p.levelling
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelOpen = cancel
	wait := p.skipped.Add(settle).Sub(p.now())
	p.wg.Go(func() {
		defer cancel()
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return
			}
		}
		src, err := p.stream(ctx, t, frame, level)
		p.mu.Lock()
		defer p.mu.Unlock()
		if gen != p.gen || p.closed {
			if src != nil {
				src.Close()
			}
			return
		}
		p.loading, p.cancelOpen = false, nil
		if err != nil {
			p.refusedLocked(err, frame)
			return
		}
		p.retried, p.failures = false, 0
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

// refusedLocked deals with the streamed track at pos not opening from frame.
func (p *Player) refusedLocked(err error, frame int64) {
	var th *spotify.Throttled
	switch {
	case errors.As(err, &th) && !p.retried:
		// Asking again at once is what gets keys refused: wait, then try once
		// more.
		p.retried, p.loading = true, true
		p.tellLocked(Notice{Kind: Throttled, Wait: th.Wait})
		gen := p.gen
		p.retryTimer = p.after(th.Wait, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if gen == p.gen && p.loading && !p.closed {
				p.openLocked(frame)
			}
		})
	case errors.As(err, &th):
		p.retried, p.cued = false, true // play tries again
		p.tellLocked(Notice{Kind: StillThrottled})
	default:
		// Moving on: the files in the queue still play.
		kind := Failed
		switch {
		case errors.Is(err, spotify.ErrUnavailable):
			kind = Unavailable
		case errors.Is(err, spotify.ErrOffline):
			kind = Offline
		case errors.Is(err, spotify.ErrSignedOut):
			kind = SignedOut
		case errors.Is(err, spotify.ErrPremium):
			kind = NeedsPremium
		}
		p.log.Warn("skipping stream", "id", p.queue[p.order[p.pos]].SpotifyID, "err", err)
		p.tellLocked(Notice{Kind: kind})
		p.failures++
		// A stream cued while it loaded, paused by the listener or stopped on by
		// the sleep timer, moves on but isn't played: the next track is cued in
		// its place, and play starts it.
		switch {
		case p.failures >= len(p.order):
			p.failures, p.cued = 0, true // nothing in the queue opens: stop here
		case p.pos+1 < len(p.order):
			p.pos++
			p.moveOnLocked()
		case p.repeat == RepeatAll:
			p.pos = 0
			p.moveOnLocked()
		default:
			p.cued = true // the queue's end: stop on its last track
		}
	}
}

// moveOnLocked plays the track at pos from its start after one that failed to
// open, or, when playback is cued, leaves it cued there from its start.
func (p *Player) moveOnLocked() {
	if p.cued {
		p.failures, p.startFrame = 0, 0
		return
	}
	p.startLocked()
}

// primeStreamLocked opens the streamed track at pos to follow cur once cur
// has streamLead left to play, so streams open at the pace they're heard.
func (p *Player) primeStreamLocked(gen uint64, pos, idx int, t Track, level, album bool) {
	if left := p.leftLocked(); left > streamLead {
		p.primeTimer = p.after(left-streamLead, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if gen == p.gen && !p.closed {
				p.primeStreamLocked(gen, pos, idx, t, level, album)
			}
		})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancelPrime = cancel
	p.wg.Go(func() {
		defer cancel()
		src, err := p.stream(ctx, t, 0, level)
		if err != nil {
			return // tried again, and dealt with, when cur ends
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if gen != p.gen || p.closed {
			src.Close()
			return
		}
		p.next, p.nextPos, p.nextIdx, p.nextAlbum, p.nextRate, p.nextFrames = src, pos, idx, album, src.SampleRate(), src.Frames()
		p.engine.Queue(src)
	})
}

// leftLocked is how long cur has left to play, 0 when that isn't known. A
// seek the engine hasn't made yet counts as made when it is further on.
func (p *Player) leftLocked() time.Duration {
	frame, frames, rate := p.startFrame, p.curFrames, p.curRate
	if st := p.engine.Status(); st.Source == p.cur && st.Rate > 0 {
		frame, frames, rate = max(st.Frame, p.startFrame), st.Frames, st.Rate
	}
	if rate <= 0 || frames <= 0 || frame >= frames {
		return 0
	}
	n, r := frames-frame, int64(rate)
	return time.Duration(n/r)*time.Second + duration(n%r, rate) // in two, as n can be huge
}

// cancelOpensLocked stops the stream opens under way or waiting to start.
func (p *Player) cancelOpensLocked() {
	if p.cancelOpen != nil {
		p.cancelOpen()
		p.cancelOpen = nil
	}
	if p.retryTimer != nil {
		p.retryTimer.Stop()
		p.retryTimer = nil
	}
	p.cancelPrimeLocked()
}

// cancelPrimeLocked stops opening the stream to follow cur, or waiting to.
func (p *Player) cancelPrimeLocked() {
	if p.cancelPrime != nil {
		p.cancelPrime()
		p.cancelPrime = nil
	}
	if p.primeTimer != nil {
		p.primeTimer.Stop()
		p.primeTimer = nil
	}
}

package player

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
)

// LiveOpener opens a radio station's stream. ctx ends when the open is no
// longer wanted, and is cancelled once it returns: the source must play on
// without it.
type LiveOpener func(ctx context.Context, t Track) (audio.Source, error)

// SetLive lets the player play stations through open. What the listener
// should hear of, as a station that won't connect, goes to the notify
// SetStreaming was given.
func (p *Player) SetLive(open LiveOpener) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.live = open
}

var errNoLive = errors.New("player: stations can't be played here")

// liveLocked reports whether the current track is a station.
func (p *Player) liveLocked() bool {
	return len(p.order) > 0 && p.queue[p.order[p.pos]].Live()
}

// openLiveLocked connects to the station at pos on a goroutine, after the
// settling time if the listener has just skipped, so holding R1 through a
// list connects only to the station it stops on. One that won't connect
// leaves playback cued on it: it was chosen, and Next is a press away.
func (p *Player) openLiveLocked() {
	if p.closed {
		return
	}
	p.gen++
	p.cancelOpensLocked()
	p.loading, p.startFrame = true, 0
	if p.sleep.Mode == SleepTrack || p.sleep.Mode == SleepAlbum {
		p.stopSleepLocked() // a station never ends, so neither would wait
		p.tellLocked(Notice{Kind: SleepTimerOff})
	}
	gen, t, open := p.gen, p.queue[p.order[p.pos]], p.live
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
		var src audio.Source
		err := errNoLive
		if open != nil {
			src, err = open(ctx, t)
		}
		if err == nil && !audio.SupportedRate(src.SampleRate()) {
			// The engine would drop it and play nothing, as it does a file's.
			rate := src.SampleRate()
			src.Close()
			src, err = nil, fmt.Errorf("%w: %d Hz", audio.ErrRate, rate)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		if gen != p.gen || p.closed {
			if src != nil {
				src.Close()
			}
			return
		}
		p.loading, p.cancelOpen = false, nil
		switch {
		case err != nil:
			p.log.Warn("station won't play", "station", t.Title, "err", err)
			p.cued = true
			p.tellLocked(Notice{Kind: CouldNotPlay, Name: t.Title})
		case p.cued:
			src.Close() // paused while it connected: play connects again
		default:
			p.cur, p.curRate, p.curFrames = src, src.SampleRate(), src.Frames()
			p.framesOf = t.key()
			p.engine.PlayFrom(src, 0)
		}
	})
}

// liveEndedLocked deals with the station playing failing: back at another
// rate it is opened again, without asking the gate, as a drop doesn't cut a
// station off; gone for good, playback stops on it. During the sleep timer's
// fade a station back at another rate stops there too, as a change of track
// does.
func (p *Player) liveEndedLocked(t Track, err error) {
	rate := errors.Is(err, audio.ErrRateChanged)
	if rate && !p.fadingLocked() {
		p.log.Info("station came back at another rate", "station", t.Title)
		p.stopLocked()
		p.openLiveLocked()
		return
	}
	p.stopLocked()
	p.cued, p.fadeEnds = true, time.Time{} // play plays, rather than ending a fade
	if rate {
		p.log.Info("station came back at another rate during the sleep timer's fade", "station", t.Title)
		return
	}
	p.log.Warn("lost station", "station", t.Title, "err", err)
	p.tellLocked(Notice{Kind: Lost, Name: t.Title})
}

// closeAfterFadeLocked closes the station playing once the sleep timer's
// fade, just begun, is over: paused, it would hold its stream open. The fade
// pauses the engine at its end, but a stream that stalls holds the fade back,
// so the stream is closed when the fade's time is up all the same, unless
// the listener played on during it.
func (p *Player) closeAfterFadeLocked() {
	cur, ends := p.cur, p.fadeEnds
	p.after(sleepFade, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cur == cur && !p.closed && (!p.playingLocked() || p.fadeEnds.Equal(ends)) {
			p.stopLocked()
			p.cued, p.fadeEnds = true, time.Time{}
		}
	})
}

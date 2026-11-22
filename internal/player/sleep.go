package player

import "time"

// SleepMode is when the sleep timer stops playback.
type SleepMode uint8

const (
	SleepOff   SleepMode = iota
	Sleep15              // 15 minutes after it was set
	Sleep30              // 30 minutes after
	Sleep45              // 45 minutes after
	Sleep60              // an hour after
	SleepTrack           // at the end of the track playing
	SleepAlbum           // at the end of the album playing
)

// SleepModes are the modes in the order the screens offer them.
var SleepModes = [...]SleepMode{SleepOff, Sleep15, Sleep30, Sleep45, Sleep60, SleepTrack, SleepAlbum}

// Timed reports whether m stops playback after a time, not at the end of a
// track.
func (m SleepMode) Timed() bool { return m >= Sleep15 && m <= Sleep60 }

// Duration is how long a timed mode runs.
func (m SleepMode) Duration() time.Duration {
	if !m.Timed() {
		return 0
	}
	return time.Duration(15*int(m)) * time.Minute
}

// sleepFade is how long the sleep timer takes to fade playback out.
const sleepFade = 10 * time.Second

// Sleep is the sleep timer as the screens show it.
type Sleep struct {
	Mode SleepMode
	At   time.Time // when a timed mode stops playback
}

// stopper is a timer that can be stopped; *time.Timer is one.
type stopper interface{ Stop() bool }

func afterFunc(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) }

// SetSleep sets the sleep timer, starting it again if it was already set: a
// timed mode fades playback out and pauses it when its time is up; the end of
// a track or an album fades that end out, and playback pauses there with the
// queue on the next track. A timer that has done so is off.
func (p *Player) SetSleep(m SleepMode) {
	p.mu.Lock()
	defer p.mu.Unlock()
	atEnd := p.sleep.Mode == SleepTrack || p.sleep.Mode == SleepAlbum
	p.stopSleepLocked()
	p.sleep = Sleep{Mode: m}
	if m.Timed() {
		d := m.Duration()
		p.sleep.At = p.now().Add(d)
		gen := p.sleepGen
		p.sleepTimer = p.after(d-sleepFade, func() { p.sleepFire(gen) })
	}
	if (atEnd || !m.Timed() && m != SleepOff) && p.cur != nil {
		p.primeLocked() // the end of this track fades, or the next follows again
	}
}

// Sleep reports the sleep timer.
func (p *Player) Sleep() Sleep {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sleep
}

// ResumeSleep sets the sleep timer as another of Boombrick's processes had
// it: a timed one stops playback at s.At, so its time left carries over, and
// the end of a track or an album is set as SetSleep sets it. Call it once
// playback has started: a time already within the fade fades at once, and
// with nothing playing there is nothing to stop.
func (p *Player) ResumeSleep(s Sleep) {
	if !s.Mode.Timed() {
		p.SetSleep(s.Mode)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopSleepLocked()
	// The end is kept as now plus the time left, which carries a monotonic
	// reading that a clock step can't skew.
	left := s.At.Sub(p.now())
	p.sleep = Sleep{Mode: s.Mode, At: p.now().Add(left)}
	gen := p.sleepGen
	p.sleepTimer = p.after(max(left-sleepFade, 0), func() { p.sleepFire(gen) })
}

// FadeOut fades the music out over d and pauses it, as the background player
// stops when Boombrick takes the music back. It does nothing unless music
// plays.
func (p *Player) FadeOut(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil || !p.playingLocked() {
		return
	}
	p.engine.FadeOut(d)
	p.fadeEnds = p.now().Add(d)
}

// stopSleepLocked turns the timer off and ends a fade it began, at full
// volume again.
func (p *Player) stopSleepLocked() {
	p.sleepGen++
	if p.sleepTimer != nil {
		p.sleepTimer.Stop()
		p.sleepTimer = nil
	}
	p.sleep, p.fadeEnds, p.endFade = Sleep{}, time.Time{}, false
	p.engine.CancelFade()
}

// sleepFire begins the fade of a timed sleep, sleepFade before its time. The
// timer is off from then; the engine pauses playback when the fade is done. A
// track playback waits on, for its download or to open, lands paused.
func (p *Player) sleepFire(gen uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gen != p.sleepGen || p.closed {
		return
	}
	p.sleepTimer, p.sleep = nil, Sleep{}
	if p.waiting || p.loading {
		p.cued = true
		return
	}
	if p.cur == nil || !p.playingLocked() {
		return // nothing to stop
	}
	p.engine.FadeOut(sleepFade)
	p.fadeEnds = p.now().Add(sleepFade)
}

// fadingLocked reports whether a timed sleep's fade is under way.
func (p *Player) fadingLocked() bool {
	return !p.fadeEnds.IsZero() && p.now().Before(p.fadeEnds)
}

// keepPlayingLocked ends the sleep timer's fade when the listener acts during
// it, playing on at full volume, and reports whether there was one: a timed
// sleep's fade, or the fade at the end of the track an end-of-track or
// end-of-album timer stops after, which turns that timer off.
func (p *Player) keepPlayingLocked() bool {
	switch {
	case p.fadingLocked():
		p.fadeEnds = time.Time{}
		p.engine.Resume() // ending the fade, and playing again if its last frames paused the engine
		return true
	case p.endFade && p.nearEndLocked(sleepFade):
		p.stopSleepLocked()
		p.primeLocked() // the next track follows again
		return true
	}
	return false
}

// nearEndLocked reports whether cur is playing within d of its end.
func (p *Player) nearEndLocked(d time.Duration) bool {
	st := p.engine.Status()
	if p.cur == nil || st.Source != p.cur || st.Rate == 0 || p.curFrames <= 0 {
		return false
	}
	return duration(p.curFrames-st.Frame, st.Rate) <= d
}

// sleepsAfterLocked reports whether the sleep timer stops playback at the end
// of the current track: always for SleepTrack, and for SleepAlbum when the
// track that follows in play order is of another album, comes round again
// from the start, or there is none.
func (p *Player) sleepsAfterLocked() bool {
	switch p.sleep.Mode {
	case SleepTrack:
		return true
	case SleepAlbum:
		pos, ok := p.followingLocked(p.pos)
		return !ok || pos <= p.pos || !sameAlbum(p.queue[p.order[p.pos]], p.queue[p.order[pos]])
	}
	return false
}

// sleepEndLocked stops at the end of the track that just ended, as the sleep
// timer asks: the queue moves on to the next track, cued, and the timer is
// off.
func (p *Player) sleepEndLocked() {
	p.sleep, p.fadeEnds, p.endFade = Sleep{}, time.Time{}, false
	p.cur, p.next = nil, nil
	pos, ok := p.followingLocked(p.pos)
	if !ok {
		p.playedOut = true
		return
	}
	p.pos, p.cued, p.startFrame = pos, true, 0
	if p.queue[p.order[p.pos]].Pending() {
		p.waitCuedLocked()
		return
	}
	p.cueLocked()
}

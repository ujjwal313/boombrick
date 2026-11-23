package screens

import (
	"fmt"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
)

// sleepLabels name the sleep timer's modes as the quick menu and Settings
// offer them.
var sleepLabels = [...]string{
	player.SleepOff:   "Off",
	player.Sleep15:    "15 min",
	player.Sleep30:    "30 min",
	player.Sleep45:    "45 min",
	player.Sleep60:    "60 min",
	player.SleepTrack: "End of track",
	player.SleepAlbum: "End of album",
}

// SleepLabel names the sleep timer's mode m.
func SleepLabel(m player.SleepMode) string {
	if int(m) < len(sleepLabels) {
		return sleepLabels[m]
	}
	return ""
}

// SleepValue is what the sleep timer's row says at now: its mode, and for a
// timed one how long is left, as "30 min · 18:22 left", counting part of a
// second as a whole one.
func SleepValue(s player.Sleep, now time.Time) string {
	if !s.Mode.Timed() {
		return SleepLabel(s.Mode)
	}
	left := max(s.At.Sub(now)+time.Second-1, 0).Truncate(time.Second)
	m, sec := int(left/time.Minute), int(left%time.Minute/time.Second)
	return fmt.Sprintf("%s · %d:%02d left", SleepLabel(s.Mode), m, sec)
}

// SleepDue is when what SleepValue says next changes after t: when a timed
// timer's time left passes a whole second.
func SleepDue(s player.Sleep, t time.Time) (time.Time, bool) {
	left := s.At.Sub(t)
	if !s.Mode.Timed() || left <= 0 {
		return time.Time{}, false
	}
	step := left % time.Second
	if step == 0 {
		step = time.Second
	}
	return t.Add(step), true
}

// NextSleep is the mode after m in the order offered, back to Off after the
// last; step -1 goes the other way. While a station plays (live) it passes
// over End of track and End of album, which a station never reaches.
func NextSleep(m player.SleepMode, step int, live bool) player.SleepMode {
	n := len(player.SleepModes)
	for i, x := range player.SleepModes {
		if x != m {
			continue
		}
		for range n {
			i = ((i+step)%n + n) % n
			if next := player.SleepModes[i]; SleepOffered(next, live) {
				return next
			}
		}
	}
	return player.SleepOff
}

// SleepOffered reports whether the timer offers mode m: while a station plays
// (live) it doesn't offer End of track or End of album.
func SleepOffered(m player.SleepMode, live bool) bool {
	return !live || m != player.SleepTrack && m != player.SleepAlbum
}

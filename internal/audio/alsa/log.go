package alsa

import (
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
)

// stuckAfter is how long a call into ALSA may take before the log says so. A
// write returns within the device's 100 ms buffer, and the rest take less;
// it is shorter than the 2 s the engine waits for the output to let go, so the
// log names the call before quitting gives up on it.
const stuckAfter = time.Second

// watch notes in the log a call into ALSA that hasn't returned after a while,
// and how long it took if it does, so a report shows where playback stuck.
// Calls are made one at a time.
type watch struct {
	log   *slog.Logger
	after time.Duration
	timer *time.Timer

	mu    sync.Mutex
	call  string // the call under way, or ""
	start time.Time
	late  bool // the log has said the call is stuck
}

func newWatch(log *slog.Logger, after time.Duration) *watch {
	w := &watch{log: log, after: after}
	w.timer = time.AfterFunc(time.Hour, w.fire)
	w.timer.Stop()
	return w
}

// begin starts watching call.
func (w *watch) begin(call string) {
	w.mu.Lock()
	w.call, w.start, w.late = call, time.Now(), false
	w.mu.Unlock()
	w.timer.Reset(w.after)
}

// end stops watching the call under way.
func (w *watch) end() {
	w.timer.Stop()
	w.mu.Lock()
	call, took, late := w.call, time.Since(w.start), w.late
	w.call, w.late = "", false
	w.mu.Unlock()
	if late {
		w.log.Info("the sound card answered", "call", call, "after", took.Round(time.Millisecond))
	}
}

// fire runs when a call has taken too long. One that has just begun, after
// the timer fired for the one before, isn't late.
func (w *watch) fire() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.call == "" || w.late || time.Since(w.start) < w.after {
		return
	}
	w.late = true
	w.log.Warn("the sound card hasn't answered", "call", w.call, "after", w.after)
}

// address matches a Bluetooth address, as in the D-Bus path of a device
// bluealsa plays to; the log leaves it out.
var address = regexp.MustCompile(`(?i)[0-9a-f]{2}([:_][0-9a-f]{2}){5}`)

// chain shortens what snd_pcm_dump reports to the layers the device is made
// of, as "Plug PCM: Rate conversion PCM (44100, sformat=S16_LE)", and what
// each says of itself, as a Bluetooth codec: its lines that aren't indented,
// without each layer's setup or any Bluetooth address.
func chain(dump string) string {
	var parts []string
	for line := range strings.Lines(dump) {
		line = strings.TrimRight(line, "\r\n")
		if line == "" || line[0] == ' ' || line[0] == '\t' || line == "Its setup is:" {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "Slave: "))
		parts = append(parts, address.ReplaceAllString(line, "XX"))
	}
	s := strings.Join(parts, " | ")
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

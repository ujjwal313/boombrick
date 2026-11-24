package alsa

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// lines is a log the tests read while the watch may write to it.
type lines struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lines) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// A call that takes too long is noted, and so is its return; one that
// doesn't, isn't.
func TestWatchNotesASlowCall(t *testing.T) {
	var out lines
	w := newWatch(slog.New(slog.NewTextHandler(&out, nil)), 100*time.Millisecond)

	w.begin("drop")
	w.end()
	w.begin("write")
	time.Sleep(500 * time.Millisecond)
	w.end()
	w.begin("close")
	w.end()
	time.Sleep(200 * time.Millisecond) // any late timer has run

	got := out.String()
	stuck := strings.Index(got, `msg="the sound card hasn't answered" call=write after=100ms`)
	answered := strings.Index(got, `msg="the sound card answered" call=write`)
	if stuck < 0 || answered < stuck {
		t.Fatalf("the slow write wasn't noted, then its return:\n%s", got)
	}
	if strings.Contains(got, "call=drop") || strings.Contains(got, "call=close") {
		t.Fatalf("a quick call was noted:\n%s", got)
	}
}

// The chain keeps each layer's own lines, leaves out its setup and the
// Bluetooth address.
func TestChain(t *testing.T) {
	dump := `Plug PCM: Rate conversion PCM (44100, sformat=S16_LE)
Converter: linear-interpolation
Its setup is:
  stream       : PLAYBACK
  rate         : 48000
Slave: BlueALSA PCM: /org/bluealsa/hci0/dev_A1_B2_C3_D4_E5_F6/a2dpsrc/sink
BlueALSA BlueZ device: /org/bluez/hci0/dev_A1_B2_C3_D4_E5_F6
BlueALSA Bluetooth codec: AAC
Its setup is:
  stream       : PLAYBACK
`
	want := "Plug PCM: Rate conversion PCM (44100, sformat=S16_LE) | Converter: linear-interpolation | " +
		"BlueALSA PCM: /org/bluealsa/hci0/dev_XX/a2dpsrc/sink | BlueALSA BlueZ device: /org/bluez/hci0/dev_XX | " +
		"BlueALSA Bluetooth codec: AAC"
	if got := chain(dump); got != want {
		t.Fatalf("chain:\n got %s\nwant %s", got, want)
	}
	if got := chain(""); got != "" {
		t.Fatalf("an empty dump made %q", got)
	}
}

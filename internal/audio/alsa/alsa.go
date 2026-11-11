// Package alsa writes audio to the default ALSA device, which NextUI points at
// the speaker, headphones, a USB DAC or Bluetooth.
package alsa

/*
#cgo LDFLAGS: -lasound
#include <errno.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>
#include <alsa/asoundlib.h>

// The device keeps 100 ms queued, enough to ride out scheduling hiccups.
static int bb_open(snd_pcm_t **pcm, unsigned int rate) {
	int err = snd_pcm_open(pcm, "default", SND_PCM_STREAM_PLAYBACK, 0);
	if (err < 0) return err;
	err = snd_pcm_set_params(*pcm, SND_PCM_FORMAT_S16_LE, SND_PCM_ACCESS_RW_INTERLEAVED, 2, rate, 1, 100000);
	if (err < 0) {
		snd_pcm_close(*pcm);
		*pcm = NULL;
	}
	return err;
}

static long long bb_now_ms(void) {
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return ts.tv_sec * 1000LL + ts.tv_nsec / 1000000;
}

// bb_write plays frames from buf, recovering from underruns. It writes only
// what the device has room for, so a write never blocks, and gives up with
// -ETIMEDOUT when the device has taken no audio for timeout ms, as a wedged
// Bluetooth one can, instead of holding the write.
static int bb_write(snd_pcm_t *pcm, const short *buf, snd_pcm_uframes_t frames, int timeout) {
	long long deadline = bb_now_ms() + timeout;
	while (frames > 0) {
		snd_pcm_sframes_t n = snd_pcm_avail_update(pcm);
		if (n == 0) {
			long long left = deadline - bb_now_ms();
			if (left <= 0) return -ETIMEDOUT;
			n = snd_pcm_wait(pcm, (int)left);
			if (n == 0) return -ETIMEDOUT;
			if (n > 0) {
				// Some plugins, PulseAudio's among them, say there is room
				// when there isn't; don't spin on them.
				if (snd_pcm_avail_update(pcm) == 0) usleep(1000);
				continue;
			}
		}
		if (n > 0) {
			n = snd_pcm_writei(pcm, buf, (snd_pcm_uframes_t)n < frames ? (snd_pcm_uframes_t)n : frames);
			if (n > 0) deadline = bb_now_ms() + timeout;
		}
		if (n < 0) {
			int err = snd_pcm_recover(pcm, (int)n, 1);
			if (err < 0) return err;
			continue;
		}
		buf += n * 2;
		frames -= n;
	}
	return 0;
}

static long bb_drop(snd_pcm_t *pcm) {
	snd_pcm_sframes_t delay = 0;
	if (snd_pcm_delay(pcm, &delay) < 0 || delay < 0) delay = 0;
	int err = snd_pcm_drop(pcm);
	if (err < 0) return err;
	err = snd_pcm_prepare(pcm);
	if (err < 0) return err;
	return delay;
}

static long bb_queued(snd_pcm_t *pcm) {
	snd_pcm_sframes_t delay;
	if (snd_pcm_delay(pcm, &delay) < 0 || delay < 0) return 0;
	return delay;
}

// bb_dump returns what snd_pcm_dump reports of pcm, for the caller to free,
// or NULL.
static char *bb_dump(snd_pcm_t *pcm) {
	snd_output_t *out;
	if (snd_output_buffer_open(&out) < 0) return NULL;
	char *s = NULL;
	if (snd_pcm_dump(pcm, out) >= 0) {
		char *buf;
		size_t n = snd_output_buffer_string(out, &buf);
		s = strndup(buf, n);
	}
	snd_output_close(out);
	return s;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unsafe"

	"github.com/ujjwal313/boombrick/internal/audio"
)

// writeTimeout is how long a write waits for the device to take audio before
// it gives up: far longer than the device's 100 ms buffer takes to play, and
// time enough for Bluetooth headphones to start.
const writeTimeout = 3 * time.Second

// errStalled is reported by a write the device took no audio for.
var errStalled = errors.New("alsa: the device stopped taking audio")

// Device is the default playback device.
type Device struct {
	pcm   *C.snd_pcm_t
	rate  int
	stall time.Duration // how long a write waits for room before giving up
	log   *slog.Logger
	watch *watch
}

var _ audio.Output = (*Device)(nil)

// New returns a device that opens on first use. log hears what it opened and
// of any call into ALSA that is slow to return; nil is none.
func New(log *slog.Logger) *Device {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Device{stall: writeTimeout, log: log, watch: newWatch(log, stuckAfter)}
}

func alsaErr(op string, code C.int) error {
	return fmt.Errorf("alsa %s: %s", op, C.GoString(C.snd_strerror(code)))
}

// Open opens the device at rate. If it is already open at another rate, the
// queued audio is played out first so the previous track isn't cut short.
func (d *Device) Open(rate int) error {
	if d.pcm != nil && d.rate == rate {
		return nil
	}
	if d.pcm != nil {
		d.watch.begin("drain")
		C.snd_pcm_drain(d.pcm)
		d.watch.end()
		d.Close()
	}
	var pcm *C.snd_pcm_t
	d.watch.begin("open")
	err := C.bb_open(&pcm, C.uint(rate))
	var desc string
	if err >= 0 {
		desc = describe(pcm)
	}
	d.watch.end()
	if err < 0 {
		return alsaErr("open", err)
	}
	d.pcm, d.rate = pcm, rate
	d.log.Info("sound card", "rate", rate, "device", desc)
	return nil
}

// describe is the chain of layers pcm is made of, for the log.
func describe(pcm *C.snd_pcm_t) string {
	s := C.bb_dump(pcm)
	if s == nil {
		return ""
	}
	defer C.free(unsafe.Pointer(s))
	return chain(C.GoString(s))
}

// Write plays buf, recovering from underruns. When the device takes no audio
// for a while it gives up, and closes the device.
func (d *Device) Write(buf []int16) error {
	if d.pcm == nil {
		return errors.New("alsa: write to a closed device")
	}
	if len(buf) < 2 {
		return nil
	}
	d.watch.begin("write")
	err := C.bb_write(d.pcm, (*C.short)(unsafe.Pointer(&buf[0])), C.snd_pcm_uframes_t(len(buf)/2),
		C.int(d.stall.Milliseconds()))
	d.watch.end()
	if err < 0 {
		// The device is gone, unrecoverable or stalled; close it so the next
		// Open starts afresh.
		d.Close()
		if err == -C.ETIMEDOUT {
			return fmt.Errorf("%w for %v", errStalled, d.stall)
		}
		return alsaErr("write", err)
	}
	return nil
}

// Drop discards queued audio, leaves the device ready for more and reports how
// many frames it held.
func (d *Device) Drop() (int, error) {
	if d.pcm == nil {
		return 0, nil
	}
	d.watch.begin("drop")
	n := C.bb_drop(d.pcm)
	d.watch.end()
	if n < 0 {
		return 0, alsaErr("drop", C.int(n))
	}
	return int(n), nil
}

// pause holds the device, which then takes no audio; the tests use it to stall
// a write.
func (d *Device) pause() error {
	if err := C.snd_pcm_pause(d.pcm, 1); err < 0 {
		return alsaErr("pause", err)
	}
	return nil
}

// queued reports the frames waiting in the device; the tests use it to check Drop.
func (d *Device) queued() int {
	if d.pcm == nil {
		return 0
	}
	return int(C.bb_queued(d.pcm))
}

// Close closes the device; queued audio is discarded.
func (d *Device) Close() error {
	if d.pcm == nil {
		return nil
	}
	d.watch.begin("close")
	err := C.snd_pcm_close(d.pcm)
	d.watch.end()
	d.pcm = nil
	if err < 0 {
		return alsaErr("close", err)
	}
	return nil
}

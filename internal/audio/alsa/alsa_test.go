package alsa

import (
	"errors"
	"testing"
	"time"
)

// A device that stops taking audio, as a wedged Bluetooth one can, fails the
// write after a while instead of holding it for good, and is closed so the
// next Open starts afresh.
func TestWriteGivesUpOnAStalledDevice(t *testing.T) {
	d := New(nil)
	if err := d.Open(48000); err != nil {
		t.Skipf("no ALSA device: %v", err)
	}
	defer d.Close()
	d.stall = 200 * time.Millisecond
	buf := make([]int16, 2*4800) // 100 ms, the whole buffer
	if err := d.Write(buf); err != nil {
		t.Fatal(err)
	}
	if err := d.pause(); err != nil {
		t.Skipf("the device can't pause: %v", err)
	}
	start := time.Now()
	err := d.Write(buf)
	if !errors.Is(err, errStalled) {
		t.Fatalf("write to a paused device: %v, want %v", err, errStalled)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("the write gave up after %v", took)
	}
	if d.pcm != nil {
		t.Fatal("the stalled device was left open")
	}
}

func TestPlaysSilenceAndChangesRate(t *testing.T) {
	d := New(nil)
	if err := d.Open(48000); err != nil {
		t.Skipf("no ALSA device: %v", err)
	}
	defer d.Close()
	buf := make([]int16, 2*4800) // 100 ms
	for i := 0; i < 3; i++ {
		if err := d.Write(buf); err != nil {
			t.Fatal(err)
		}
	}
	if d.queued() <= 0 {
		t.Fatal("nothing queued after writing 300 ms")
	}
	held, err := d.Drop()
	if err != nil {
		t.Fatal(err)
	}
	if held <= 0 {
		t.Fatalf("Drop reported %d frames held after writing 300 ms", held)
	}
	if n := d.queued(); n != 0 {
		t.Fatalf("%d frames still queued after drop", n)
	}
	if err := d.Write(buf); err != nil {
		t.Fatalf("write after drop: %v", err)
	}
	old := d.pcm
	if err := d.Open(44100); err != nil {
		t.Fatal(err)
	}
	if d.rate != 44100 || d.pcm == old {
		t.Fatalf("rate change did not reopen the device: rate %d, same handle %v", d.rate, d.pcm == old)
	}
	if err := d.Write(buf); err != nil {
		t.Fatalf("write after reopening at 44100: %v", err)
	}
}

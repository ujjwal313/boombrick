package audio

import (
	"sync"
	"syscall"
	"testing"
)

// threads is an Output that notes the OS thread each call comes from.
type threads struct {
	sink
	tmu  sync.Mutex
	tids map[int]bool
}

func (o *threads) note() {
	o.tmu.Lock()
	defer o.tmu.Unlock()
	if o.tids == nil {
		o.tids = map[int]bool{}
	}
	o.tids[syscall.Gettid()] = true
}

func (o *threads) count() int {
	o.tmu.Lock()
	defer o.tmu.Unlock()
	return len(o.tids)
}

func (o *threads) Open(rate int) error     { o.note(); return o.sink.Open(rate) }
func (o *threads) Write(buf []int16) error { o.note(); return o.sink.Write(buf) }
func (o *threads) Drop() (int, error)      { o.note(); return o.sink.Drop() }
func (o *threads) Close() error            { o.note(); return o.sink.Close() }

// Every call into the output, from opening it to closing it, comes from one
// OS thread: bluealsa's ALSA plugin, behind Bluetooth headphones, has been
// seen to stop taking audio when they come from several.
func TestOutputCallsStayOnOneThread(t *testing.T) {
	out := &threads{}
	e := NewEngine(out, nil)
	e.Play(&ramp{rate: OutputRate, n: 1 << 40})
	eventually(t, "playback", func() bool { return out.written() > OutputRate/5 })
	e.Pause()
	eventually(t, "the pause to drop the buffer", func() bool { return out.dropCount() == 1 })
	e.Resume()
	written := out.written()
	eventually(t, "playback after the pause", func() bool { return out.written() > written+OutputRate/5 })
	if err := e.Release(); err != nil {
		t.Fatal(err)
	}
	e.Resume()
	eventually(t, "playback after Release", func() bool { return !out.isClosed() })
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if n := out.count(); n != 1 {
		t.Fatalf("the output was called from %d threads", n)
	}
}

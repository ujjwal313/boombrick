//go:build e2e

package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/background"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/resume"
	"github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/store"
)

// fixtureRate is the song's frames per second: noise-180s.mp3 is 44.1 kHz, and
// a snapshot's frame counts the track's own frames.
const fixtureRate = 44100

// rig is one subtest's world: its own data and tmp folders, and the Brick's
// way of starting the background player.
type rig struct {
	t          *testing.T
	song, bin  string
	data, lock string
	stay       string // CrossMix's keep-awake file
	env        []string
}

// The background player as Boombrick runs it: the real program, a real queue,
// and the Brick's way of starting and stopping it, with PATH empty, as on the
// Brick no tool is there to call, and no HOME. It needs cgo, a sound device
// and the decode fixtures (make fixtures):
//
//	go test -tags e2e -run Background -v ./cmd/boombrick
func TestBackgroundPlayer(t *testing.T) {
	song, err := filepath.Abs(filepath.Join("..", "..", "internal", "audio", "decode", "testdata", "noise-180s.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(song); err != nil {
		t.Skipf("%s is missing; run make fixtures", song)
	}
	bin := filepath.Join(t.TempDir(), "boombrick")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		t.Fatal(err)
	}

	// newRig makes a world on the desktop's layout, or on CrossMix's: the
	// program copied into an app folder of its own, holding the data and logs.
	newRig := func(t *testing.T, crossmix bool) *rig {
		dir := t.TempDir()
		tmp := filepath.Join(dir, "tmp")
		data, logs, exe := filepath.Join(dir, "data"), filepath.Join(dir, "data"), bin
		if crossmix {
			app := filepath.Join(dir, "Apps", "Boombrick")
			data, logs, exe = filepath.Join(app, "data"), filepath.Join(app, "logs"), filepath.Join(app, "boombrick")
		}
		for _, d := range []string{data, logs, tmp} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if crossmix {
			b, err := os.ReadFile(bin)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(exe, b, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// Stricter than what the Brick gives the program, which is its
		// parent's whole environment: with PATH empty and no HOME, a tool or a
		// folder it takes for granted shows up here. Only the audio variables
		// WSL needs to reach its sound server are passed. Anything else the
		// program turns out to need is a finding, not something to add here.
		env := []string{"PATH=", "BOOMBRICK_TMP=" + tmp}
		if crossmix {
			env = append(env, "BOOMBRICK_PLATFORM=crossmix")
		} else {
			env = append(env, "BOOMBRICK_DATA="+data)
		}
		for _, k := range []string{"PULSE_SERVER", "XDG_RUNTIME_DIR"} {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
		r := &rig{t: t, song: song, bin: exe, data: data, lock: filepath.Join(tmp, "boombrick-player.lock"),
			stay: filepath.Join(tmp, "stay_awake"), env: env}
		t.Cleanup(func() { background.Stop(r.lock, 5*time.Second) })
		t.Cleanup(func() {
			if t.Failed() {
				b, _ := os.ReadFile(filepath.Join(logs, "Boombrick.txt"))
				t.Logf("the log:\n%s", b)
			}
		})
		return r
	}

	t.Run("taken back", func(t *testing.T) {
		r := newRig(t, false)
		r.queue(10 * fixtureRate)
		pid := r.start()
		// The hand-over the queue was saved with is cleared, so that what is
		// read after Stop was written by the player.
		r.clearHandover()
		time.Sleep(2 * time.Second)
		t.Logf("the background player's memory: %s", memory(t, pid))
		if took, err := background.Stop(r.lock, 5*time.Second); !took || err != nil {
			t.Fatalf("Stop = %v, %v", took, err)
		}
		snap, h := r.saved()
		if snap.Frame < 11*fixtureRate || !h.Playing {
			t.Fatalf("saved at frame %d, playing %v; want past %d, playing", snap.Frame, h.Playing, 11*fixtureRate)
		}
	})

	t.Run("stopped by itself", func(t *testing.T) {
		r := newRig(t, false)
		r.queue(178 * fixtureRate)
		r.start()
		for end := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			if _, held, _ := background.Holder(r.lock); !held {
				break
			}
			if time.Now().After(end) {
				background.Stop(r.lock, time.Second)
				t.Fatal("the background player didn't stop at the end of the queue")
			}
		}
		if _, h := r.saved(); h.Playing {
			t.Fatal("stopped by itself, it saved the music as playing")
		}
	})

	t.Run("round trip", func(t *testing.T) {
		r := newRig(t, false)
		st, prefs := r.open()
		p, closeP := r.player(st, prefs)
		// A descriptor like the ones the screen's C libraries leave open: it
		// must not reach the background player.
		leak, err := syscall.Open("/dev/zero", syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(leak)
		p.PlayFrom([]player.Track{r.track()}, 0, 10*fixtureRate)
		waitFor(t, "the music to play", func() bool { s := p.State(); return s.Playing && !s.Loading && !s.Waiting })
		p.SetSleep(player.Sleep45)
		saver := resume.NewSaver(st.DB)
		err = background.HandOver(p, saver.Save, func(h resume.Handover) error { return resume.SaveHandover(st.DB, h) },
			r.bin, r.env, 10*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if p.State().Playing {
			t.Fatal("after the hand-over, the first player still plays")
		}
		pid, held, _ := background.Holder(r.lock)
		if !held {
			t.Fatal("after the hand-over, nobody holds the lock")
		}
		fds, err := os.ReadDir("/proc/" + strconv.Itoa(pid) + "/fd")
		if err != nil {
			t.Fatal(err)
		}
		var open []string
		for _, e := range fds {
			to, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/fd/" + e.Name())
			if err != nil {
				continue
			}
			open = append(open, e.Name()+" -> "+to)
			if n, _ := strconv.Atoi(e.Name()); n > 2 && to == "/dev/zero" {
				t.Errorf("the background player inherited descriptor %d, %s", n, to)
			}
		}
		t.Logf("the background player's descriptors: %s", strings.Join(open, ", "))
		snap, _ := p.Snapshot()
		handedAt, sleep := snap.Frame, p.Sleep()
		time.Sleep(2 * time.Second)
		closeP()

		p2, closeP2 := r.player(st, prefs)
		background.TakeBack(r.lock, 5*time.Second, p2, r.store(st), r.log())
		if _, held, _ := background.Holder(r.lock); held {
			t.Fatal("after the take-back, the lock is still held")
		}
		waitFor(t, "the music to play again", func() bool { s := p2.State(); return s.Playing && !s.Loading })
		snap2, _ := p2.Snapshot()
		if want := handedAt + fixtureRate*3/2; snap2.Frame < want {
			t.Fatalf("taken back at frame %d; want at least %d (handed over at %d)", snap2.Frame, want, handedAt)
		}
		got := p2.Sleep()
		if got.Mode != player.Sleep45 || got.At.Sub(sleep.At).Abs() > time.Second {
			t.Fatalf("sleep timer = %v, want %v", got, sleep)
		}
		if h, err := resume.LoadHandover(st.DB); err != nil || h != (resume.Handover{}) {
			t.Fatalf("hand-over left = %+v, %v; want it cleared", h, err)
		}
		closeP2()
	})

	// Under CrossMix the player keeps the device awake while it plays, and lets
	// it sleep again once it stops by itself, but not when Boombrick takes the
	// music back, for Boombrick plays on.
	t.Run("crossmix", func(t *testing.T) {
		r := newRig(t, true)
		r.queue(10 * fixtureRate)
		r.start()
		waitFor(t, "the keep-awake file", func() bool { _, err := os.Stat(r.stay); return err == nil })
		if err := os.Remove(r.stay); err != nil {
			t.Fatal(err)
		}
		for end := time.Now().Add(8 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			if _, err := os.Stat(r.stay); err == nil {
				break
			}
			if time.Now().After(end) {
				t.Fatal("the keep-awake file wasn't written again")
			}
		}
		if took, err := background.Stop(r.lock, 5*time.Second); !took || err != nil {
			t.Fatalf("Stop = %v, %v", took, err)
		}
		if _, err := os.Stat(r.stay); err != nil {
			t.Fatalf("taken back, the keep-awake file is gone: %v", err)
		}

		r.queue(178 * fixtureRate)
		r.start()
		for end := time.Now().Add(15 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			if _, held, _ := background.Holder(r.lock); !held {
				break
			}
			if time.Now().After(end) {
				background.Stop(r.lock, time.Second)
				t.Fatal("the background player didn't stop at the end of the queue")
			}
		}
		if _, err := os.Stat(r.stay); !os.IsNotExist(err) {
			t.Fatalf("stopped by itself, the keep-awake file is still there: %v", err)
		}
	})

	t.Run("crashed", func(t *testing.T) {
		r := newRig(t, false)
		r.queue(10 * fixtureRate)
		pid := r.start()
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the lock to be free", func() bool { _, held, _ := background.Holder(r.lock); return !held })
		st, prefs := r.open()
		p, _ := r.player(st, prefs)
		background.TakeBack(r.lock, 5*time.Second, p, r.store(st), r.log())
		if s := p.State(); !s.Queued || s.Playing {
			t.Fatalf("after a crash, queued %v, playing %v; want the queue restored paused", s.Queued, s.Playing)
		}
		time.Sleep(500 * time.Millisecond)
		if p.State().Playing {
			t.Fatal("after a crash, the music plays")
		}
		if h, err := resume.LoadHandover(st.DB); err != nil || h != (resume.Handover{}) {
			t.Fatalf("hand-over left = %+v, %v; want it cleared", h, err)
		}
		if _, held, _ := background.Holder(r.lock); held {
			t.Fatal("the lock is held")
		}
	})

	t.Run("one at a time", func(t *testing.T) {
		r := newRig(t, false)
		r.queue(10 * fixtureRate)
		pid := r.start()
		if _, err := background.Start(r.bin, []string{"--background"}, r.env, 10*time.Second); err == nil {
			t.Fatal("a second background player started")
		}
		if got, held, _ := background.Holder(r.lock); !held || got != pid {
			t.Fatalf("lock held by %d (held %v); want the first player, %d", got, held, pid)
		}
		if took, err := background.Stop(r.lock, 5*time.Second); !took || err != nil {
			t.Fatalf("Stop = %v, %v", took, err)
		}
	})
}

func (r *rig) track() player.Track {
	return player.Track{Path: r.song, Title: "Noise", Artist: "Fixture", Duration: 180 * time.Second}
}

func (r *rig) start() int {
	r.t.Helper()
	pid, err := background.Start(r.bin, []string{"--background"}, r.env, 10*time.Second)
	if err != nil {
		r.t.Fatal(err)
	}
	return pid
}

// open is the database and settings the test process uses, closed with the
// test.
func (r *rig) open() (*store.Store, *settings.Settings) {
	r.t.Helper()
	st, err := store.Open(filepath.Join(r.data, "boombrick.db"))
	if err != nil {
		r.t.Fatal(err)
	}
	prefs, err := settings.Load(st.DB, r.log())
	if err != nil {
		st.Close()
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { prefs.Close(); st.Close() })
	return st, prefs
}

func (r *rig) log() *slog.Logger { return slog.New(slog.NewTextHandler(testLog{r.t}, nil)) }

type testLog struct{ t *testing.T }

func (w testLog) Write(b []byte) (int, error) {
	w.t.Logf("%s", bytes.TrimRight(b, "\n"))
	return len(b), nil
}

// player is a real player, as Boombrick makes it, closed with the test. Local
// files never reach the Spotify helper.
func (r *rig) player(st *store.Store, prefs *settings.Settings) (*player.Player, func()) {
	sp := spotify.NewHelper(filepath.Join(r.data, "no-such-helper"), filepath.Join(r.data, "spotify"), r.log(), io.Discard)
	p := newPlayer(st.DB, prefs, sp, nil, func() bool { return false }, r.log(), func(player.Notice) {})
	var once sync.Once
	closeP := func() { once.Do(func() { p.Close(); sp.Close() }) }
	r.t.Cleanup(closeP)
	return p, closeP
}

func (r *rig) store(st *store.Store) background.Store {
	return background.Store{
		Load:         func() (player.Snapshot, bool, error) { return resume.Load(st.DB) },
		LoadHandover: func() (resume.Handover, error) { return resume.LoadHandover(st.DB) },
		SaveHandover: func(h resume.Handover) error { return resume.SaveHandover(st.DB, h) },
	}
}

// queue saves the song as the queue, at frame, handed over playing.
func (r *rig) queue(frame int64) {
	st, err := store.Open(filepath.Join(r.data, "boombrick.db"))
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	snap := player.Snapshot{Queue: []player.Track{r.track()}, Order: []int{0}, Frame: frame}
	if err := resume.NewSaver(st.DB).Save(snap); err != nil {
		r.t.Fatal(err)
	}
	if err := resume.SaveHandover(st.DB, resume.Handover{Playing: true}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) saved() (player.Snapshot, resume.Handover) {
	st, err := store.Open(filepath.Join(r.data, "boombrick.db"))
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	snap, _, err := resume.Load(st.DB)
	if err != nil {
		r.t.Fatal(err)
	}
	h, err := resume.LoadHandover(st.DB)
	if err != nil {
		r.t.Fatal(err)
	}
	return snap, h
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (r *rig) clearHandover() {
	r.t.Helper()
	st, err := store.Open(filepath.Join(r.data, "boombrick.db"))
	if err != nil {
		r.t.Fatal(err)
	}
	defer st.Close()
	if err := resume.SaveHandover(st.DB, resume.Handover{}); err != nil {
		r.t.Fatal(err)
	}
}

var memLine = regexp.MustCompile(`(?m)^(VmRSS|RssAnon|RssFile|RssShmem):\s*(\d+) kB`)

// memory is the resident memory of process pid, as /proc reports it, and how
// it divides: heap and stacks, mapped files, shared memory.
func memory(t *testing.T, pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Errorf("read the player's status: %v", err)
		return "?"
	}
	var parts []string
	for _, m := range memLine.FindAllSubmatch(b, -1) {
		parts = append(parts, string(m[1])+" "+string(m[2])+" kB")
	}
	return strings.Join(parts, ", ")
}

package background

import (
	"errors"
	"log/slog"
	"os"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/resume"
)

// seen is what a save or a hand-over found: whether the lock was held,
// whether the music played, and whether the sound card was let go.
type seen struct{ held, playing, released bool }

// source is the player Boombrick hands the music over from. Like the player,
// it opens the sound card again when it plays.
type source struct {
	fakePlayer
	playing  bool
	released bool // the sound card is let go
	releases int
	stuck    error  // what Release says when the card won't let go
	lock     string // looked at when it saves
	saved    []player.Snapshot
	left     []resume.Handover
	saw      []seen
}

func (s *source) State() player.State {
	return player.State{Queued: true, Playing: s.playing}
}

func (s *source) Toggle() {
	s.fakePlayer.Toggle()
	s.playing = !s.playing
	if s.playing {
		s.released = false
	}
}

func (s *source) Release() error {
	s.playing = false
	s.releases++
	if s.stuck != nil {
		return s.stuck
	}
	s.released = true
	return nil
}

func (s *source) look() {
	if s.lock == "" {
		return
	}
	_, held, _ := Holder(s.lock)
	s.saw = append(s.saw, seen{held, s.playing, s.released})
}

func (s *source) save(snap player.Snapshot) error {
	s.look()
	s.saved = append(s.saved, snap)
	return nil
}

func (s *source) handover(h resume.Handover) error {
	s.look()
	s.left = append(s.left, h)
	return nil
}

// fadingSource is a player whose first Toggle ends the sleep timer's fade,
// which turns the timer off and plays on, as Toggle does there.
type fadingSource struct {
	source
	fading bool
}

func (f *fadingSource) Toggle() {
	if f.fading {
		f.fading = false
		f.fakePlayer.Toggle()
		f.sleep = player.Sleep{}
		return
	}
	f.source.Toggle()
}

// Handing over pauses the music, lets go of the sound card, saves the queue
// with what goes with it, and returns once the background player plays; the
// queue is saved and the hand-over written after the card is let go and before
// the player starts. A sound card that takes one program at a time, as a USB
// DAC's, would otherwise turn the background player away.
func TestHandOver(t *testing.T) {
	path := lockPath(t)
	s := &source{playing: true, lock: path}
	s.sleep = player.Sleep{Mode: player.SleepTrack}
	if err := HandOver(s, s.save, s.handover, os.Args[0], childEnv("hold", path), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	defer Stop(path, 5*time.Second)
	if s.playing || len(s.saved) != 1 || !slices.Equal(s.left, []resume.Handover{{Playing: true, Sleep: s.sleep}}) {
		t.Fatalf("playing %v, saves %d, hand-overs %+v", s.playing, len(s.saved), s.left)
	}
	if want := []seen{{released: true}, {released: true}}; !slices.Equal(s.saw, want) {
		t.Fatalf("the save and the hand-over saw (lock held, playing, card let go) %+v, want %+v", s.saw, want)
	}
	if s.releases != 1 || !s.released {
		t.Fatalf("the sound card was let go %d times, and is let go %v; want once, and still", s.releases, s.released)
	}
	if _, held, _ := Holder(path); !held {
		t.Fatal("no background player holds the lock")
	}
}

// During the sleep timer's fade the first Toggle only ends the fade: the
// music is paused after all, and the timer as it was is handed over.
func TestHandOverDuringTheSleepFade(t *testing.T) {
	path := lockPath(t)
	f := &fadingSource{fading: true}
	f.playing = true
	f.sleep = player.Sleep{Mode: player.Sleep30, At: time.Now().Add(20 * time.Second)}
	orig := f.sleep
	if err := HandOver(f, f.save, f.handover, os.Args[0], childEnv("hold", path), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	defer Stop(path, 5*time.Second)
	if f.playing || f.fading || !slices.Equal(f.left, []resume.Handover{{Playing: true, Sleep: orig}}) {
		t.Fatalf("playing %v, fading %v, hand-overs %+v", f.playing, f.fading, f.left)
	}

	// Undone, it plays on and the timer is put back.
	f = &fadingSource{fading: true}
	f.playing = true
	f.sleep = orig
	if err := HandOver(f, f.save, f.handover, os.Args[0], childEnv("silent", path), 300*time.Millisecond); err == nil {
		t.Fatal("a player that never played took the music")
	}
	if !f.playing || f.sleep != orig || !slices.Equal(f.resumed, []player.Sleep{orig}) {
		t.Fatalf("playing %v, sleep %+v, sleep timers %v", f.playing, f.sleep, f.resumed)
	}
}

// When the background player doesn't play, the hand-over is undone and the
// music plays on here; with nothing playing there is nothing to hand over.
func TestHandOverUndone(t *testing.T) {
	path := lockPath(t)
	s := &source{playing: true}
	if err := HandOver(s, s.save, s.handover, os.Args[0], childEnv("silent", path), 300*time.Millisecond); err == nil {
		t.Fatal("a player that never played took the music")
	}
	if len(s.left) == 0 {
		t.Fatal("the hand-over was never written")
	}
	if !s.playing || s.released || s.left[len(s.left)-1] != (resume.Handover{}) {
		t.Fatalf("playing %v, card let go %v, hand-overs %+v", s.playing, s.released, s.left)
	}
	if _, held, _ := Holder(path); held {
		t.Fatal("the player that failed still holds the lock")
	}
	idle := &source{}
	if err := HandOver(idle, idle.save, idle.handover, os.Args[0], childEnv("hold", path), time.Second); err == nil || idle.toggles != 0 || idle.releases != 0 {
		t.Fatalf("with nothing playing: %v, %d toggles, %d releases", err, idle.toggles, idle.releases)
	}
}

// A hand-over that fails to save, or to be written, is undone before any
// background player starts.
func TestHandOverUndoneWhenItCantBeWritten(t *testing.T) {
	errSave, errWrite := errors.New("disk full"), errors.New("read-only")
	for _, c := range []struct {
		name string
		err  error
		save func(*source, player.Snapshot) error
		hand func(*source, resume.Handover) error
	}{
		{"save", errSave,
			func(*source, player.Snapshot) error { return errSave },
			func(s *source, h resume.Handover) error { return s.handover(h) }},
		{"hand-over", errWrite,
			func(s *source, snap player.Snapshot) error { return s.save(snap) },
			func(*source, resume.Handover) error { return errWrite }},
	} {
		path := lockPath(t)
		s := &source{playing: true}
		err := HandOver(s, func(snap player.Snapshot) error { return c.save(s, snap) },
			func(h resume.Handover) error { return c.hand(s, h) }, os.Args[0], childEnv("hold", path), time.Second)
		if !errors.Is(err, c.err) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.err)
		}
		if !s.playing {
			t.Errorf("%s: the music didn't play on", c.name)
		}
		if _, held, _ := Holder(path); held {
			t.Errorf("%s: a background player started", c.name)
		}
	}
}

// A sound card that won't let go, stuck in a call, undoes the hand-over before
// any background player starts: it couldn't open the card, and the music plays
// on here.
func TestHandOverUndoneWhenTheCardWontLetGo(t *testing.T) {
	path := lockPath(t)
	s := &source{playing: true, stuck: errors.New("stuck")}
	err := HandOver(s, s.save, s.handover, os.Args[0], childEnv("hold", path), time.Second)
	if !errors.Is(err, s.stuck) {
		t.Fatalf("err = %v, want %v", err, s.stuck)
	}
	if !s.playing || len(s.saved) != 0 {
		t.Fatalf("playing %v, queue saved %d times", s.playing, len(s.saved))
	}
	if _, held, _ := Holder(path); held {
		t.Fatal("a background player started")
	}
}

// When the hand-over can't even be cleared, that error comes with the other.
func TestHandOverUndoKeepsBothErrors(t *testing.T) {
	errSave, errWrite := errors.New("disk full"), errors.New("read-only")
	s := &source{playing: true}
	err := HandOver(s, func(player.Snapshot) error { return errSave },
		func(resume.Handover) error { return errWrite }, os.Args[0], childEnv("hold", lockPath(t)), time.Second)
	if !errors.Is(err, errSave) || !errors.Is(err, errWrite) {
		t.Fatalf("err = %v, want both", err)
	}
}

// A background player that quits before it plays is no hand-over: the music
// plays on here and the hand-over is cleared.
func TestHandOverUndoneByAPlayerThatQuits(t *testing.T) {
	path := lockPath(t)
	s := &source{playing: true}
	if err := HandOver(s, s.save, s.handover, os.Args[0], childEnv("quit", path), 5*time.Second); err == nil {
		t.Fatal("a player that quit took the music")
	}
	if len(s.left) == 0 || !s.playing || s.left[len(s.left)-1] != (resume.Handover{}) {
		t.Fatalf("playing %v, hand-overs %+v", s.playing, s.left)
	}
}

// restorer records what TakeBack does with the queue.
type restorer struct {
	fakePlayer
	restored []player.Snapshot
	refuse   bool   // it has a queue already
	lock     string // looked at when it restores
	held     bool
}

func (r *restorer) Restore(s player.Snapshot) bool {
	if r.lock != "" {
		_, r.held, _ = Holder(r.lock)
	}
	r.restored = append(r.restored, s)
	return !r.refuse
}

// store keeps a queue and a hand-over in memory.
type store struct {
	h               resume.Handover
	none            bool  // nothing is saved
	loadErr         error // the queue can't be read
	handoverErr     error // the hand-over can't be read
	saveHandoverErr error // the hand-over can't be written
}

func (s *store) funcs() Store {
	return Store{
		Load: func() (player.Snapshot, bool, error) {
			if s.loadErr != nil {
				return player.Snapshot{}, false, s.loadErr
			}
			return player.Snapshot{Queue: []player.Track{{Path: "/m/a.mp3"}}, Order: []int{0}}, !s.none, nil
		},
		LoadHandover: func() (resume.Handover, error) { return s.h, s.handoverErr },
		SaveHandover: func(h resume.Handover) error {
			if s.saveHandoverErr != nil {
				return s.saveHandoverErr
			}
			s.h = h
			return nil
		},
	}
}

var discard = slog.New(slog.DiscardHandler)

// startHolder starts a background player on path, stopped when the test ends.
func startHolder(t *testing.T, part, path string) int {
	t.Helper()
	pid, err := Start(os.Args[0], nil, childEnv(part, path), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

// Taking the music back from a background player stops it, restores the
// queue, plays on and sets the sleep timer's time left; the hand-over is
// then cleared. The queue is restored once the lock is free.
func TestTakeBack(t *testing.T) {
	path := lockPath(t)
	startHolder(t, "hold", path)
	sleep := player.Sleep{Mode: player.Sleep45, At: time.Now().Add(time.Hour)}
	st := &store{h: resume.Handover{Playing: true, Sleep: sleep}}
	r := &restorer{lock: path}
	TakeBack(path, 5*time.Second, r, st.funcs(), discard)
	if _, held, _ := Holder(path); held {
		t.Fatal("the background player still holds the lock")
	}
	if r.held {
		t.Fatal("the queue was restored while the background player held the lock")
	}
	if len(r.restored) != 1 || r.toggles != 1 || !slices.Equal(r.resumed, []player.Sleep{sleep}) {
		t.Fatalf("restored %d, toggles %d, sleep timers %v", len(r.restored), r.toggles, r.resumed)
	}
	if st.h != (resume.Handover{}) {
		t.Fatalf("the hand-over wasn't cleared: %+v", st.h)
	}
}

// A background player that won't stop when asked is killed; the music is
// still taken back and plays on from the hand-over.
func TestTakeBackFromAStubbornPlayer(t *testing.T) {
	path := lockPath(t)
	startHolder(t, "stubborn", path)
	st := &store{h: resume.Handover{Playing: true}}
	r := &restorer{}
	TakeBack(path, 300*time.Millisecond, r, st.funcs(), discard)
	if _, held, _ := Holder(path); held {
		t.Fatal("the background player still holds the lock")
	}
	if len(r.restored) != 1 || r.toggles != 1 || st.h != (resume.Handover{}) {
		t.Fatalf("restored %d, toggles %d, hand-over %+v", len(r.restored), r.toggles, st.h)
	}
}

// With no background player but "playing" saved, one died: the queue comes
// back paused and the hand-over, sleep timer and all, is cleared. With
// nothing handed over, it comes back paused as ever.
func TestTakeBackAfterACrash(t *testing.T) {
	path := lockPath(t)
	sleep := player.Sleep{Mode: player.Sleep45, At: time.Now().Add(time.Hour)}
	st := &store{h: resume.Handover{Playing: true, Sleep: sleep}}
	r := &restorer{}
	TakeBack(path, time.Second, r, st.funcs(), discard)
	if len(r.restored) != 1 || r.toggles != 0 || len(r.resumed) != 0 || st.h != (resume.Handover{}) {
		t.Fatalf("restored %d, toggles %d, sleep timers %v, hand-over %+v", len(r.restored), r.toggles, r.resumed, st.h)
	}
	r = &restorer{}
	TakeBack(path, time.Second, r, st.funcs(), discard)
	if len(r.restored) != 1 || r.toggles != 0 {
		t.Fatalf("restored %d, toggles %d", len(r.restored), r.toggles)
	}
}

// A background player that stopped by itself, as at the end of the queue,
// left nothing playing: it is taken, the queue comes back paused.
func TestTakeBackFromAPlayerThatStoppedItself(t *testing.T) {
	path := lockPath(t)
	startHolder(t, "hold", path)
	st := &store{}
	r := &restorer{}
	TakeBack(path, 5*time.Second, r, st.funcs(), discard)
	if _, held, _ := Holder(path); held {
		t.Fatal("the background player still holds the lock")
	}
	if len(r.restored) != 1 || r.toggles != 0 || len(r.resumed) != 0 {
		t.Fatalf("restored %d, toggles %d, sleep timers %v", len(r.restored), r.toggles, r.resumed)
	}
}

// A queue the player won't take, as when something is queued already, or
// that can't be read, is not played on; the hand-over is still cleared.
func TestTakeBackWithoutAQueue(t *testing.T) {
	sleep := player.Sleep{Mode: player.Sleep45, At: time.Now().Add(time.Hour)}
	for _, c := range []struct {
		name string
		st   *store
		r    *restorer
	}{
		{"refused", &store{}, &restorer{refuse: true}},
		{"load error", &store{loadErr: errors.New("corrupt")}, &restorer{}},
		{"nothing saved", &store{none: true}, &restorer{}},
	} {
		path := lockPath(t)
		startHolder(t, "hold", path)
		c.st.h = resume.Handover{Playing: true, Sleep: sleep}
		TakeBack(path, 5*time.Second, c.r, c.st.funcs(), discard)
		if c.r.toggles != 0 || len(c.r.resumed) != 0 || c.st.h != (resume.Handover{}) {
			t.Errorf("%s: toggles %d, sleep timers %v, hand-over %+v", c.name, c.r.toggles, c.r.resumed, c.st.h)
		}
	}
}

// With the hand-over unreadable the queue is restored paused; with it not
// clearable the music still plays on.
func TestTakeBackWithAFaultyHandover(t *testing.T) {
	path := lockPath(t)
	startHolder(t, "hold", path)
	st := &store{h: resume.Handover{Playing: true}, handoverErr: errors.New("corrupt")}
	r := &restorer{}
	TakeBack(path, 5*time.Second, r, st.funcs(), discard)
	if len(r.restored) != 1 || r.toggles != 0 {
		t.Fatalf("unreadable: restored %d, toggles %d", len(r.restored), r.toggles)
	}

	path = lockPath(t)
	startHolder(t, "hold", path)
	st = &store{h: resume.Handover{Playing: true}, saveHandoverErr: errors.New("read-only")}
	r = &restorer{}
	TakeBack(path, 5*time.Second, r, st.funcs(), discard)
	if len(r.restored) != 1 || r.toggles != 1 {
		t.Fatalf("unclearable: restored %d, toggles %d", len(r.restored), r.toggles)
	}
}

// A background player that can't be stopped, here one whose ID can't be had,
// leaves the queue restored paused and the hand-over for the next launch.
func TestTakeBackFromAPlayerThatCantBeStopped(t *testing.T) {
	path := lockPath(t)
	startHolder(t, "nopid", path)
	sleep := player.Sleep{Mode: player.Sleep45, At: time.Now().Add(time.Hour)}
	st := &store{h: resume.Handover{Playing: true, Sleep: sleep}}
	r := &restorer{}
	TakeBack(path, time.Second, r, st.funcs(), discard)
	if len(r.restored) != 1 || r.toggles != 0 || len(r.resumed) != 0 {
		t.Fatalf("restored %d, toggles %d, sleep timers %v", len(r.restored), r.toggles, r.resumed)
	}
	if st.h != (resume.Handover{Playing: true, Sleep: sleep}) {
		t.Fatalf("the hand-over was changed: %+v", st.h)
	}
}

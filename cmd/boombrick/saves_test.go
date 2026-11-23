package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
)

func TestNoSavesAfterAHandOver(t *testing.T) {
	var saved int
	g := &saves{saver: func(player.Snapshot) error { saved++; return nil }}
	if err := g.save(player.Snapshot{}); err != nil || saved != 1 {
		t.Fatalf("before the hand-over: saved %d, %v", saved, err)
	}
	err := g.handOver(func() error {
		if err := g.save(player.Snapshot{}); err != nil || saved != 1 {
			t.Errorf("during the hand-over: saved %d, %v", saved, err)
		}
		return g.now(player.Snapshot{})
	})
	if err != nil || saved != 2 {
		t.Fatalf("hand-over = %v, saved %d; want its own save only", err, saved)
	}
	if err := g.save(player.Snapshot{}); err != nil || saved != 2 {
		t.Fatalf("after the hand-over: saved %d, %v", saved, err)
	}
}

func TestSavesResumeAfterAFailedHandOver(t *testing.T) {
	var saved int
	g := &saves{saver: func(player.Snapshot) error { saved++; return nil }}
	failed := errors.New("no player")
	if err := g.handOver(func() error { return failed }); err != failed {
		t.Fatalf("hand-over = %v", err)
	}
	if err := g.save(player.Snapshot{}); err != nil || saved != 1 {
		t.Fatalf("after a failed hand-over: saved %d, %v", saved, err)
	}
}

// While music plays the queue is saved every interval, unless playback hasn't
// moved since, as while a stream stalls; once more when it stops, and not
// while it stays stopped. moved hears of a new track and of a stop.
func TestKeepSaving(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := player.State{Queued: true, Playing: true, Version: 1, Track: player.Track{Path: "/m/a.flac"}}
		b := a
		b.Index, b.Track = 1, player.Track{Path: "/m/b.flac"}
		at := func(st player.State, d time.Duration) player.State { st.Elapsed = d; return st }
		stopped := at(b, 3*time.Second)
		stopped.Playing = false
		script := []player.State{
			at(a, 5*time.Second),
			at(a, 10*time.Second),
			at(a, 10*time.Second+400*time.Millisecond), // stalled within the second
			at(b, 2*time.Second),
			stopped,
			stopped,
		}
		state := func() player.State {
			st := script[0]
			if len(script) > 1 {
				script = script[1:]
			}
			return st
		}
		var saved atomic.Int32
		moved := make(chan struct{}, 1)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			keepSaving(ctx, 5*time.Second, state, func() { saved.Add(1) }, moved)
		}()
		time.Sleep(time.Millisecond) // off the ticks
		for i, want := range []struct {
			saves int32
			moved bool
		}{{1, true}, {2, false}, {2, false}, {3, true}, {4, true}, {4, false}} {
			time.Sleep(5 * time.Second)
			synctest.Wait()
			heard := false
			select {
			case <-moved:
				heard = true
			default:
			}
			if n := saved.Load(); n != want.saves || heard != want.moved {
				t.Fatalf("tick %d: %d saves, moved %v; want %d, %v", i+1, n, heard, want.saves, want.moved)
			}
		}
		cancel()
		<-done
	})
}

// A Spotify song gives the background player time to sign in to Spotify
// itself before it plays, and a station longer, as opening it alone may take
// 15 s; a file plays at once.
func TestHandOverWait(t *testing.T) {
	file := player.State{Track: player.Track{Path: "/m/a.flac"}}
	stream := player.State{Track: player.Track{SpotifyID: "4Q0qVhFQa7j6jRKzo3HDmP"}}
	if got := handOverWait(file); got != 5*time.Second {
		t.Errorf("a file waits %v, want 5s", got)
	}
	if got := handOverWait(stream); got != 15*time.Second {
		t.Errorf("a Spotify song waits %v, want 15s", got)
	}
	station := player.State{Track: player.Track{StationID: "abc", Stream: "http://example.test/live", Title: "Radio X"}}
	if got := handOverWait(station); got != 20*time.Second {
		t.Errorf("a station waits %v, want 20s", got)
	}
}

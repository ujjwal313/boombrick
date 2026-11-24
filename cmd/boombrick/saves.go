package main

import (
	"context"
	"sync"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
)

// saves is the one way the queue is written while Boombrick has a screen.
// After the music is handed to the background player it writes nothing: a
// save then would put the background player's place back.
type saves struct {
	saver func(player.Snapshot) error

	mu         sync.Mutex
	handedOver bool // the background player owns the queue now
}

// save writes snap unless the music has been handed over.
func (s *saves) save(snap player.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.handedOver {
		return nil
	}
	return s.saver(snap)
}

// now writes snap whatever the state: the hand-over's own save.
func (s *saves) now(snap player.Snapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saver(snap)
}

// handOver runs f, a hand-over that saves with now. From before it starts,
// save writes nothing, once a save in flight has ended; when f fails, saves
// resume.
func (s *saves) handOver(f func() error) error {
	s.mu.Lock()
	s.handedOver = true
	s.mu.Unlock()
	err := f()
	if err != nil {
		s.mu.Lock()
		s.handedOver = false
		s.mu.Unlock()
	}
	return err
}

// handOverWait is how long a hand-over waits for the background player to
// play what st is on. A Spotify song waits longer: the player signs in to
// Spotify itself first, which on a weak Wi-Fi signal takes seconds. A station
// waits longer still: it connects first, and opening it alone may take 15 s.
func handOverWait(st player.State) time.Duration {
	switch {
	case st.Track.Live():
		return 20 * time.Second
	case st.Track.Streamed():
		return 15 * time.Second
	}
	return 5 * time.Second
}

// place is where playback is, to the second, with the rest of what a save
// records that can change without the queue's version.
type place struct {
	version uint64
	index   int
	second  time.Duration
	shuffle bool
	repeat  player.Repeat
}

func placeOf(st player.State) place {
	return place{st.Version, st.Index, st.Elapsed.Truncate(time.Second), st.Shuffle, st.Repeat}
}

// keepSaving calls save every interval while music plays, and once more when
// it stops, to keep a paused position, until ctx ends: a crash or a flat
// battery then loses at most that much. It skips a save that would record the
// same place as the last, as while a stream stalls. moved hears when playback
// has moved to another track or stopped, for the app to reload Continue
// listening.
func keepSaving(ctx context.Context, every time.Duration, state func() player.State, save func(), moved chan<- struct{}) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	wasPlaying := false
	lastPath := ""
	var at place
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		st := state()
		if st.Playing || wasPlaying {
			if p := placeOf(st); p != at {
				at = p
				save()
			}
			if st.Track.Path != lastPath || !st.Playing {
				lastPath = st.Track.Path
				select {
				case moved <- struct{}{}:
				default:
				}
			}
		}
		wasPlaying = st.Playing
	}
}

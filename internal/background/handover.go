package background

import (
	"errors"
	"log/slog"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/resume"
)

// Source is what HandOver hands the music over from. *player.Player is one.
type Source interface {
	State() player.State
	Toggle()
	Release()
	Snapshot() (player.Snapshot, bool)
	Sleep() player.Sleep
	ResumeSleep(s player.Sleep)
}

var _ Source = (*player.Player)(nil)

// HandOver hands the music to a new background player, exe run with
// --background: it pauses p on the sample reached and lets go of the sound
// card, saves the queue and what goes with it, the sleep timer as it was, and
// waits up to wait for the player to play. Paused, p would still hold the card,
// and one that takes one program at a time, as a USB DAC's may, would turn the
// player away. When that fails the hand-over is undone, p plays on with its
// sleep timer put back, and the error says why, with that of a hand-over that
// couldn't be cleared. save must be the queue's only writer while HandOver
// runs.
func HandOver(p Source, save func(player.Snapshot) error, handover func(resume.Handover) error,
	exe string, env []string, wait time.Duration) error {
	if !p.State().Playing {
		return errors.New("background: no music plays")
	}
	sleep := p.Sleep() // before the pause: a fade in its last moments has ended the timer
	p.Toggle()
	if p.State().Playing {
		p.Toggle() // the first only ended the sleep timer's fade
	}
	p.Release()
	undo := func(err error) error {
		herr := handover(resume.Handover{})
		if !p.State().Playing {
			p.Toggle()
		}
		if p.Sleep() != sleep {
			p.ResumeSleep(sleep) // the pause ended an end-of-track or end-of-album timer
		}
		return errors.Join(err, herr)
	}
	snap, ok := p.Snapshot()
	if !ok {
		return undo(errors.New("background: nothing is queued"))
	}
	if err := save(snap); err != nil {
		return undo(err)
	}
	if err := handover(resume.Handover{Playing: true, Sleep: sleep}); err != nil {
		return undo(err)
	}
	if _, err := Start(exe, []string{"--background"}, env, wait); err != nil {
		return undo(err)
	}
	return nil
}

// Restorer is what TakeBack restores the queue into. *player.Player is one.
type Restorer interface {
	Restore(s player.Snapshot) bool
	Toggle()
	ResumeSleep(s player.Sleep)
}

var _ Restorer = (*player.Player)(nil)

// Store is where the queue and what a hand-over left are kept.
type Store struct {
	Load         func() (player.Snapshot, bool, error)
	LoadHandover func() (resume.Handover, error)
	SaveHandover func(resume.Handover) error
}

// TakeBack takes the music back from a background player holding the lock at
// lock, giving it up to wait to stop, and restores the queue into p. Taken
// from a player that held the lock, and restored, the music plays on where it
// got to, with the sleep timer's time left. With none holding it but "playing"
// saved, one died, as in a crash or a power cut, and the queue stays paused.
// What the hand-over left is then cleared, so it is used once. When the
// player can't be stopped the queue is restored paused and the hand-over is
// left for the next launch.
func TakeBack(lock string, wait time.Duration, p Restorer, s Store, log *slog.Logger) {
	took, stopErr := Stop(lock, wait)
	if stopErr != nil {
		log.Warn("take the music back", "err", stopErr)
	}
	restored := false
	if snap, ok, err := s.Load(); err != nil {
		log.Warn("load saved queue", "err", err)
	} else if ok {
		restored = p.Restore(snap)
	}
	if stopErr != nil {
		return
	}
	h, err := s.LoadHandover()
	if err != nil {
		log.Warn("load hand-over", "err", err)
		return
	}
	if !h.Playing {
		return
	}
	switch {
	case !took:
		log.Info("the background player has gone; the queue is paused")
	case !restored:
		log.Info("the queue wasn't restored; the music stays as it is")
	default:
		p.Toggle()
		if h.Sleep.Mode != player.SleepOff {
			p.ResumeSleep(h.Sleep)
		}
	}
	if err := s.SaveHandover(resume.Handover{}); err != nil {
		log.Warn("clear hand-over", "err", err)
	}
}

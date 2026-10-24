package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audible"
	"github.com/ujjwal313/boombrick/internal/player"
)

type fakeAudible struct {
	pos     audible.Position
	have    bool
	err     error
	sent    []time.Duration
	acrCall int
}

func (f *fakeAudible) RemotePosition(context.Context, string) (audible.Position, bool, error) {
	return f.pos, f.have, f.err
}
func (f *fakeAudible) SetPosition(_ context.Context, _, _ string, pos time.Duration) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, pos)
	return nil
}
func (f *fakeAudible) ACR(context.Context, string) (string, error) { f.acrCall++; return "CR!X", nil }

func newTestSync(f *fakeAudible) (*bookSync, *time.Time) {
	b := newBookSync(f, func() bool { return true }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.rate = func(string) int { return 1000 } // a frame is a millisecond
	clock := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return clock }
	return b, &clock
}

const bookPath = "/m/Audiobooks/La casa [B0GV1924QD].m4b"

func TestStartTakesAFurtherPlaceFromAnotherDevice(t *testing.T) {
	long := time.Date(2026, 10, 22, 11, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name      string
		remote    audible.Position
		have      bool
		hasLocal  bool
		localAt   time.Time
		localMs   int64
		wantFrame int64
		wantUse   bool
	}{
		{"phone went on", audible.Position{At: 20 * time.Minute, Updated: long.Add(2 * time.Hour)}, true, true, long, 600000, 1200000, true},
		{"no place here yet", audible.Position{At: 5 * time.Minute, Updated: long}, true, false, time.Time{}, 0, 300000, true},
		{"our own push comes back", audible.Position{At: 10*time.Minute + 5*time.Second, Updated: long.Add(10 * time.Second)}, true, true, long, 600000, 0, false},
		{"older than ours", audible.Position{At: 20 * time.Minute, Updated: long.Add(-3 * time.Hour)}, true, true, long, 600000, 0, false},
		{"newer but the same place", audible.Position{At: 10*time.Minute + 10*time.Second, Updated: long.Add(2 * time.Hour)}, true, true, long, 600000, 0, false},
		{"audible has none", audible.Position{}, false, true, long, 600000, 0, false},
	} {
		b, _ := newTestSync(&fakeAudible{pos: c.remote, have: c.have})
		frame, use := b.start(bookPath, c.localMs, c.localAt, c.hasLocal)
		if use != c.wantUse || (use && frame != c.wantFrame) {
			t.Errorf("%s: got %d, %v; want %d, %v", c.name, frame, use, c.wantFrame, c.wantUse)
		}
	}
}

func TestStartIgnoresWhatItCannotSync(t *testing.T) {
	f := &fakeAudible{pos: audible.Position{At: time.Hour, Updated: time.Now()}, have: true}
	b, _ := newTestSync(f)
	if _, use := b.start("/m/Audiobooks/senza asin.m4b", 0, time.Time{}, false); use {
		t.Error("a book with no ASIN was synced")
	}
	b.online = func() bool { return false }
	if _, use := b.start(bookPath, 0, time.Time{}, false); use {
		t.Error("synced offline")
	}
	b.online = func() bool { return true }
	f.err = errors.New("timeout")
	if _, use := b.start(bookPath, 0, time.Time{}, false); use {
		t.Error("an error moved the book")
	}
	var nilSync *bookSync
	if _, use := nilSync.start(bookPath, 0, time.Time{}, false); use {
		t.Error("without Audible a book moved")
	}
}

func TestPushSendsEveryHalfMinuteAndOnPause(t *testing.T) {
	f := &fakeAudible{}
	b, clock := newTestSync(f)
	st := player.State{Track: player.Track{Path: bookPath}, Playing: true, Elapsed: 10 * time.Second}
	b.push(st)
	if len(f.sent) != 1 || f.acrCall != 1 {
		t.Fatalf("first push: sent %v, acr asked %d times", f.sent, f.acrCall)
	}
	*clock = clock.Add(10 * time.Second)
	st.Elapsed = 20 * time.Second
	b.push(st)
	if len(f.sent) != 1 {
		t.Fatalf("pushed again after 10 s: %v", f.sent)
	}
	*clock = clock.Add(25 * time.Second)
	st.Elapsed = 45 * time.Second
	b.push(st)
	if len(f.sent) != 2 {
		t.Fatalf("no push after 35 s: %v", f.sent)
	}
	st.Elapsed, st.Playing = 50*time.Second, false // paused
	b.push(st)
	if len(f.sent) != 3 || f.sent[2] != 50*time.Second {
		t.Fatalf("no push on pause: %v", f.sent)
	}
	b.push(st) // still paused
	if len(f.sent) != 3 {
		t.Fatalf("pushed while paused: %v", f.sent)
	}
	if f.acrCall != 1 {
		t.Errorf("the content reference was asked for %d times", f.acrCall)
	}
}

func TestPushLeavesSongsAndOfflineAlone(t *testing.T) {
	f := &fakeAudible{}
	b, _ := newTestSync(f)
	b.push(player.State{Track: player.Track{Path: "/m/a.flac"}, Playing: true, Elapsed: time.Minute})
	b.push(player.State{Track: player.Track{Path: "/m/Audiobooks/senza.m4b"}, Playing: true, Elapsed: time.Minute})
	b.online = func() bool { return false }
	b.push(player.State{Track: player.Track{Path: bookPath}, Playing: true, Elapsed: time.Minute})
	if len(f.sent) != 0 {
		t.Fatalf("sent %v", f.sent)
	}
	b.online = func() bool { return true }
	f.err = errors.New("500")
	b.push(player.State{Track: player.Track{Path: bookPath}, Playing: true, Elapsed: time.Minute})
	f.err = nil
	b.push(player.State{Track: player.Track{Path: bookPath}, Playing: true, Elapsed: time.Minute})
	if len(f.sent) != 1 {
		t.Fatalf("a failed push wasn't tried again: %v", f.sent)
	}
}

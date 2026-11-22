package player

import (
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

// switches is a gate as the app's: streams refused while Spotify is off,
// tracks still downloading while YouTube is.
type switches struct{ spotifyOff, youtubeOff atomic.Bool }

func (s *switches) gate(t Track) (string, bool) {
	switch {
	case t.Streamed() && s.spotifyOff.Load():
		return "Spotify", false
	case t.Pending() && s.youtubeOff.Load():
		return "YouTube", false
	}
	return "", true
}

func (n *notices) offs() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var off []string
	for _, x := range n.list {
		if x.Kind == SwitchedOff {
			off = append(off, x.Off)
		}
	}
	return off
}

// With Spotify off, its songs are skipped, saying so, and the files around
// them play; nothing asks Spotify.
func TestATrackTheGateRefusesIsSkipped(t *testing.T) {
	p, s, _, n := streamPlayer(t, map[string]int64{"a": short, "c": long}, long)
	var sw switches
	sw.spotifyOff.Store(true)
	p.SetGate(sw.gate)
	p.Play([]Track{{Path: "a"}, {SpotifyID: "b"}, {Path: "c"}}, 0)
	eventually(t, "c to play", func() bool { st := p.State(); return st.Index == 2 && st.Playing })
	if got := n.offs(); !slices.Equal(got, []string{"Spotify"}) {
		t.Fatalf("switched-off notices %v, want one for Spotify", got)
	}
	if got := s.log(); len(got) != 0 {
		t.Fatalf("Spotify was asked for %v", got)
	}
}

// A queue where the gate refuses everything stops rather than going round.
func TestAQueueTheGateRefusesStops(t *testing.T) {
	p, s, _, n := streamPlayer(t, nil, long)
	var sw switches
	sw.spotifyOff.Store(true)
	p.SetGate(sw.gate)
	p.Play(spotifyTracks("a", "b"), 0)
	eventually(t, "a notice", func() bool { return len(n.offs()) == 1 })
	time.Sleep(20 * time.Millisecond)
	if st := p.State(); st.Playing {
		t.Fatalf("state %+v, want stopped", st)
	}
	if got := len(n.offs()); got != 1 {
		t.Fatalf("%d notices, want one for the whole run", got)
	}
	if len(s.log()) != 0 {
		t.Fatal("Spotify was asked")
	}
}

// The song waited on is skipped when YouTube is switched off, as its
// download won't be fetched now.
func TestRegateSkipsTheTrackWaitedOn(t *testing.T) {
	p, s, _, n := streamPlayer(t, map[string]int64{"b": long}, long)
	_ = s
	var sw switches
	p.SetGate(sw.gate)
	p.Play([]Track{{VideoID: "v1"}, {Path: "b"}}, 0)
	if st := p.State(); !st.Waiting {
		t.Fatalf("state %+v, want waiting on v1", st)
	}
	sw.youtubeOff.Store(true)
	p.Regate()
	eventually(t, "b to play", func() bool { st := p.State(); return st.Index == 1 && st.Playing })
	if got := n.offs(); !slices.Equal(got, []string{"YouTube"}) {
		t.Fatalf("notices %v, want one for YouTube", got)
	}
}

// Regate leaves what plays alone, and drops a stream primed to follow it,
// which is skipped when what plays ends.
func TestRegateDropsARefusedTrackToFollow(t *testing.T) {
	// a is 10 s, inside the stream lead, so b is primed as soon as a plays;
	// the test's output plays it in about half a second.
	p, s, _, n := streamPlayer(t, map[string]int64{"a": 10 * 48000, "c": long}, long)
	var sw switches
	p.SetGate(sw.gate)
	p.Play([]Track{{Path: "a"}, {SpotifyID: "b"}, {Path: "c"}}, 0)
	eventually(t, "b to be primed", func() bool { return slices.Equal(s.log(), []string{"b@0"}) })
	eventually(t, "b to be queued", func() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.next != nil })
	sw.spotifyOff.Store(true)
	p.Regate()
	p.mu.Lock()
	primed := p.next
	p.mu.Unlock()
	if primed != nil {
		t.Fatal("b is still primed after Spotify was switched off")
	}
	if st := p.State(); st.Index != 0 || !st.Playing {
		t.Fatalf("state %+v, want a still playing", st)
	}
	eventually(t, "c to play", func() bool { st := p.State(); return st.Index == 2 && st.Playing })
	if got := n.offs(); !slices.Equal(got, []string{"Spotify"}) {
		t.Fatalf("notices %v, want one for Spotify", got)
	}
}

// With the switch back on, the same entries play: they were skipped, never
// removed.
func TestEntriesPlayAgainWhenTheGateOpens(t *testing.T) {
	p, s, _, _ := streamPlayer(t, map[string]int64{"c": long}, long)
	var sw switches
	sw.spotifyOff.Store(true)
	p.SetGate(sw.gate)
	p.Play([]Track{{SpotifyID: "b"}, {Path: "c"}}, 0)
	eventually(t, "c to play", func() bool { st := p.State(); return st.Index == 1 && st.Playing })
	if n := p.State().Count; n != 2 {
		t.Fatalf("the queue has %d entries, want both kept", n)
	}
	sw.spotifyOff.Store(false)
	p.PlayIndex(0)
	eventually(t, "b to open", func() bool { return slices.Equal(s.log(), []string{"b@0"}) })
}

func TestSwitches(t *testing.T) {
	var spotifyOn, youtubeOn, copies bool
	g := Switches(func() bool { return spotifyOn }, func() bool { return youtubeOn }, func() bool { return copies })
	file, stream, pending := Track{Path: "/m/a.flac"}, Track{SpotifyID: "s1"}, Track{VideoID: "v1"}
	for _, c := range []struct {
		t    Track
		off  string
		pass bool
	}{{file, "", true}, {stream, "Spotify", false}, {pending, "YouTube", false}} {
		if off, ok := g(c.t); off != c.off || ok != c.pass {
			t.Errorf("%+v, both off: %q, %v", c.t, off, ok)
		}
	}
	// Preferring copies, a Spotify song gets to the opener, which plays its
	// copy or refuses it.
	copies = true
	if _, ok := g(stream); !ok {
		t.Error("a Spotify song that may have a copy was refused")
	}
	spotifyOn, youtubeOn = true, true
	for _, tr := range []Track{file, stream, pending} {
		if _, ok := g(tr); !ok {
			t.Errorf("%+v refused with both on", tr)
		}
	}
}

// A stream opener that finds no copy with Spotify off refuses with
// ErrSpotifyOff: the track is skipped, saying Spotify is off.
func TestErrSpotifyOffSkipsSayingSo(t *testing.T) {
	p, s, _, n := streamPlayer(t, map[string]int64{"c": long}, long)
	s.set("b", ErrSpotifyOff)
	p.Play([]Track{{SpotifyID: "b"}, {Path: "c"}}, 0)
	eventually(t, "c to play", func() bool { st := p.State(); return st.Index == 1 && st.Playing })
	if got := n.offs(); !slices.Equal(got, []string{"Spotify"}) {
		t.Fatalf("notices %v", got)
	}
}

// A run of refused tracks is skipped with one notice, not one each.
func TestARunOfRefusedTracksSaysSoOnce(t *testing.T) {
	p, _, _, n := streamPlayer(t, map[string]int64{"z": long}, long)
	var sw switches
	sw.spotifyOff.Store(true)
	p.SetGate(sw.gate)
	p.Play(append(spotifyTracks("a", "b", "c", "d", "e"), Track{Path: "z"}), 0)
	eventually(t, "z to play", func() bool { st := p.State(); return st.Index == 5 && st.Playing })
	if got := n.offs(); len(got) != 1 {
		t.Fatalf("notices %v, want one", got)
	}
}

// Prev goes back past refused tracks to the one before them.
func TestPrevGoesBackPastRefusedTracks(t *testing.T) {
	p, _, _, _ := streamPlayer(t, map[string]int64{"a": long, "c": long}, long)
	var sw switches
	sw.spotifyOff.Store(true)
	p.SetGate(sw.gate)
	p.Play([]Track{{Path: "a"}, {SpotifyID: "b"}, {Path: "c"}}, 2)
	eventually(t, "c to play", func() bool { st := p.State(); return st.Index == 2 && st.Playing })
	p.Prev()
	eventually(t, "a to play", func() bool { st := p.State(); return st.Index == 0 && st.Playing })
}

// A queue restored on a song still downloading, with YouTube off, doesn't
// wait for it: play skips it.
func TestARestoredPendingTrackRefusedIsSkippedWhenPlayed(t *testing.T) {
	p, _, _, n := streamPlayer(t, map[string]int64{"b": long}, long)
	var sw switches
	sw.youtubeOff.Store(true)
	p.SetGate(sw.gate)
	if !p.Restore(Snapshot{Queue: []Track{{VideoID: "v1"}, {Path: "b"}}, Order: []int{0, 1}}) {
		t.Fatal("restore refused")
	}
	if st := p.State(); st.Waiting {
		t.Fatalf("state %+v: waiting for a download the held queue won't fetch", st)
	}
	p.Toggle()
	eventually(t, "b to play", func() bool { st := p.State(); return st.Index == 1 && st.Playing })
	if got := n.offs(); !slices.Equal(got, []string{"YouTube"}) {
		t.Fatalf("notices %v", got)
	}
}

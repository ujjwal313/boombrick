package screens

import (
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
)

const gib = 1 << 30

func TestSizes(t *testing.T) {
	cases := map[int64]string{512: "1 KB", 9_856_000: "9.4 MB", 595 * gib / 10: "59.5 GB"}
	for n, want := range cases {
		if got := Size(n); got != want {
			t.Errorf("Size(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStorageCardLines(t *testing.T) {
	var c StorageCard
	c.Set(595*gib/10, 386*gib/10, 142*gib/10)
	if got := c.Summary(); got != "20.9 GB used · 38.6 GB free of 59.5 GB" {
		t.Errorf("summary %q", got)
	}
	if c.legend != [3]string{"Music 14.2 GB", "Other 6.7 GB", "Free 38.6 GB"} {
		t.Errorf("legend %q", c.legend)
	}
}

func TestSleepValues(t *testing.T) {
	now := time.Unix(1000, 0)
	cases := []struct {
		s    player.Sleep
		want string
	}{
		{player.Sleep{}, "Off"},
		{player.Sleep{Mode: player.Sleep30, At: now.Add(18*time.Minute + 22*time.Second)}, "30 min · 18:22 left"},
		{player.Sleep{Mode: player.Sleep15, At: now.Add(-time.Second)}, "15 min · 0:00 left"},
		{player.Sleep{Mode: player.SleepAlbum}, "End of album"},
	}
	for _, c := range cases {
		if got := SleepValue(c.s, now); got != c.want {
			t.Errorf("SleepValue(%+v) = %q, want %q", c.s, got, c.want)
		}
	}
	timed := player.Sleep{Mode: player.Sleep30, At: now.Add(90*time.Second + 300*time.Millisecond)}
	if got := SleepValue(timed, now); got != "30 min · 1:31 left" {
		t.Errorf("part of a second left counts as a second: %q", got)
	}
	if due, ok := SleepDue(timed, now); !ok || due != now.Add(300*time.Millisecond) {
		t.Errorf("SleepDue = %v, %v; want in 300 ms", due.Sub(now), ok)
	}
	if _, ok := SleepDue(player.Sleep{Mode: player.SleepTrack}, now); ok {
		t.Error("End of track has a time to change")
	}
	if NextSleep(player.SleepAlbum, 1, false) != player.SleepOff || NextSleep(player.SleepOff, -1, false) != player.SleepAlbum ||
		NextSleep(player.Sleep15, 1, false) != player.Sleep30 {
		t.Error("NextSleep doesn't step through the modes in order, wrapping")
	}
}

func TestNextSleepPassesOverTheEndsWhileAStationPlays(t *testing.T) {
	if got := NextSleep(player.Sleep60, 1, true); got != player.SleepOff {
		t.Errorf("after 60 min live: %v, want Off", got)
	}
	if got := NextSleep(player.SleepOff, -1, true); got != player.Sleep60 {
		t.Errorf("before Off live: %v, want 60 min", got)
	}
	if got := NextSleep(player.SleepTrack, -1, true); got != player.Sleep60 {
		t.Errorf("before End of track live: %v, want 60 min", got)
	}
	if got := NextSleep(player.SleepTrack, 1, true); got != player.SleepOff {
		t.Errorf("after End of track live: %v, want Off", got)
	}
}

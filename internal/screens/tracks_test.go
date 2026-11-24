package screens

import (
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/lyrics"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/player"
)

func TestFormatting(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{Clock(3*time.Minute + 42*time.Second), "3:42"},
		{Clock(72*time.Minute + 5*time.Second), "72:05"},
		{SongsAndTime(1, 0), "1 song"},
		{SongsAndTime(12, 46*time.Minute+20*time.Second), "12 songs · 46 min"},
		{SongsAndTime(40, 125*time.Minute), "40 songs · 2 h 5 min"},
		{Count(7), "7"},
		{Count(2431), "2,431"},
		{Count(1234567), "1,234,567"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestClockText(t *testing.T) {
	var c ClockText
	if got := c.Of(0); got != "0:00" {
		t.Fatalf("the zero ClockText at 0: %q", got)
	}
	if got := c.Of(93*time.Second + 400*time.Millisecond); got != "1:33" {
		t.Fatalf("at 1:33.4: %q", got)
	}
	if n := testing.AllocsPerRun(20, func() { c.Of(93*time.Second + 900*time.Millisecond) }); n != 0 {
		t.Fatalf("the same second allocates %v times", n)
	}
	if got := c.Of(94 * time.Second); got != "1:34" {
		t.Fatalf("at 1:34: %q", got)
	}
}

func TestTrackOf(t *testing.T) {
	lt := library.Track{Path: "/m/x.flac", Title: "Rust", Artist: "Odalys", Album: "Copper Season",
		Gain: -6.5, Peak: 0.9, HasGain: true, Duration: time.Minute}
	pt := TrackOf(&lt)
	if pt.AlbumArtist != "Odalys" || pt.Path != lt.Path || pt.Gain != -6.5 || !pt.HasGain || pt.Duration != time.Minute {
		t.Fatalf("TrackOf = %+v", pt)
	}
	lt.AlbumArtist = "Various Artists"
	if TrackOf(&lt).AlbumArtist != "Various Artists" {
		t.Fatal("an album artist tag wins")
	}
}

func TestLyricsTrack(t *testing.T) {
	got := LyricsTrack(player.Track{Path: "/m/1.flac", Title: "Rust", Artist: "Odalys", AlbumArtist: "Various Artists",
		Album: "Copper Season", Duration: 222 * time.Second})
	if got != (lyrics.Track{Path: "/m/1.flac", Title: "Rust", Artist: "Odalys", Album: "Copper Season", Duration: 222 * time.Second}) {
		t.Fatalf("%+v", got)
	}
	got = LyricsTrack(player.Track{SpotifyID: "abc", Title: "Rust", Artist: "Odalys"})
	if got != (lyrics.Track{SpotifyID: "abc", Title: "Rust", Artist: "Odalys"}) || got.Key() != "spotify:abc" {
		t.Fatalf("streamed: %+v key %q", got, got.Key())
	}
	got = LyricsTrack(player.Track{Title: "Radio X", Stream: "http://example.test/live", StationID: "abc"})
	if got.Key() != "" {
		t.Fatalf("a station asks for lyrics: key %q", got.Key())
	}
}

func TestPlayingLyricsTrack(t *testing.T) {
	st := player.State{Queued: true, Duration: 223 * time.Second, Track: player.Track{Path: "/m/1.flac", Title: "Rust"}}
	if got := PlayingLyricsTrack(st); got.Duration != 223*time.Second {
		t.Fatalf("without a length from the library: %v, want the decoder's", got.Duration)
	}
	st.Track.Duration = 222 * time.Second
	if got := PlayingLyricsTrack(st); got.Duration != 222*time.Second {
		t.Fatalf("with one: %v, want the library's", got.Duration)
	}
}

func TestSourceOf(t *testing.T) {
	defer SetDownloadsDir("")
	cases := map[string]media.Source{
		"/mnt/SDCARD/Music/Boombrick/Lumen Coast/Night Drive Theory/01 Night Drive Theory.opus": media.Downloaded,
		"/mnt/SDCARD/Music/boombrick/Lumen Coast/Night Drive Theory/02 Sodium Lights.opus":      media.Downloaded,
		"/mnt/SDCARD/Music/Boombrick2/x.flac":                                                   media.Local,
		"/mnt/SDCARD/Music/Boombrick":                                                           media.Local,
		"/mnt/SDCARD/Music/Odalys/Copper Season/01 Rust.flac":                                   media.Local,
	}
	for _, dir := range []string{"/mnt/SDCARD/Music/Boombrick", "/mnt/SDCARD/Music/Boombrick/"} {
		SetDownloadsDir(dir)
		for path, want := range cases {
			if got := SourceOf(path); got != want {
				t.Errorf("downloads in %q: SourceOf(%q) = %v", dir, path, got)
			}
		}
	}
	SetDownloadsDir("")
	if got := SourceOf("/mnt/SDCARD/Music/Boombrick/x.opus"); got != media.Local {
		t.Errorf("with no downloads folder: %v", got)
	}
}

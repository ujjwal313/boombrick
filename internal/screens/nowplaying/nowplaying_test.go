package nowplaying

import (
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func TestMain(m *testing.M) { uitest.Main(m) }

type fake struct {
	st                             player.State
	toggles, nexts, prevs, repeats int
	shuffles                       []bool
	seeks                          []time.Duration
}

func (f *fake) State() player.State  { return f.st }
func (f *fake) Toggle()              { f.toggles++ }
func (f *fake) Next()                { f.nexts++ }
func (f *fake) Prev()                { f.prevs++ }
func (f *fake) Seek(d time.Duration) { f.seeks = append(f.seeks, d) }
func (f *fake) SetShuffle(on bool)   { f.shuffles = append(f.shuffles, on) }
func (f *fake) CycleRepeat()         { f.repeats++ }

var t0 = time.Unix(1000, 0)

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

func playing() *fake {
	return &fake{st: player.State{
		Queued: true, Playing: true, Count: 9, Index: 2,
		Track:   player.Track{Path: "a.flac", Title: "Low Tide Radio", Artist: "Marisol Vega", Album: "Salt & Static"},
		Elapsed: 93 * time.Second, Duration: 222 * time.Second, Rate: 44100, Format: "FLAC",
		Shuffle: true, Repeat: player.RepeatAll,
	}}
}

func TestGolden(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	u.Now = t0
	New(playing()).Draw(u)
	uitest.Golden(t, "nowplaying", off.Image())
}

func TestFocusMovesAlongTheControls(t *testing.T) {
	n := New(playing())
	for range 2 { // play/pause, next, repeat
		if !n.Handle(press(input.Right), t0) {
			t.Fatal("Right should move focus from play/pause to repeat")
		}
	}
	if n.focus != repeatCtl || n.Handle(press(input.Right), t0) {
		t.Fatalf("focus = %d; Right past repeat should do nothing", n.focus)
	}
	if !n.Animating(t0.Add(time.Millisecond)) {
		t.Fatal("focus should be animating")
	}
}

func TestAnimatingSettles(t *testing.T) {
	n := New(playing())
	if n.Animating(t0) {
		t.Fatal("a fresh screen should not be animating")
	}
	n.Handle(press(input.Right), t0)
	if n.Animating(t0.Add(anim.Focus + time.Millisecond)) {
		t.Fatal("focus animation should be done once anim.Focus has passed")
	}
}

func TestAActivatesTheFocusedControl(t *testing.T) {
	f := playing()
	n := New(f)
	n.Handle(press(input.A), t0) // play/pause
	n.Handle(press(input.Left), t0)
	n.Handle(press(input.A), t0) // previous
	n.Handle(press(input.Left), t0)
	n.Handle(press(input.A), t0) // shuffle off
	for i := 0; i < 4; i++ {
		n.Handle(press(input.Right), t0)
	}
	n.Handle(press(input.A), t0) // repeat
	if f.toggles != 1 || f.prevs != 1 || len(f.shuffles) != 1 || f.shuffles[0] || f.repeats != 1 {
		t.Fatalf("toggles %d, prevs %d, shuffles %v, repeats %d", f.toggles, f.prevs, f.shuffles, f.repeats)
	}
}

func TestShouldersChangeTrack(t *testing.T) {
	f := playing()
	n := New(f)
	n.Handle(press(input.L1), t0)
	n.Handle(press(input.R1), t0)
	n.Handle(press(input.R1), t0)
	if f.prevs != 1 || f.nexts != 2 {
		t.Fatalf("prevs %d, nexts %d", f.prevs, f.nexts)
	}
}

func TestHintFollowsFocusAndState(t *testing.T) {
	f := playing()
	n := New(f)
	if h := n.hints(f.st); h[0].Label != "Pause" {
		t.Fatalf("playing: A hint %q, want Pause", h[0].Label)
	}
	f.st.Playing = false
	if h := n.hints(f.st); h[0].Label != "Play" {
		t.Fatalf("paused: A hint %q, want Play", h[0].Label)
	}
	n.Handle(press(input.Right), t0)
	if h := n.hints(f.st); h[0].Label != "Next" {
		t.Fatalf("on next: A hint %q, want Next", h[0].Label)
	}
}

func TestGoldenWithArt(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	n := New(playing())
	for _, at := range []time.Time{t0, t0.Add(anim.ArtFade)} {
		u.BeginFrame(at)
		n.Draw(u)
	}
	if u.Animating() {
		t.Fatal("the cover is still fading in")
	}
	uitest.Golden(t, "nowplaying-art", off.Image())
}

func TestLyricsPill(t *testing.T) {
	f := playing()
	n := New(f)
	opened := 0
	n.SetLyrics(func() { opened++ })
	if !n.Handle(press(input.Down), t0) || n.focus != lyricsPill {
		t.Fatalf("Down focused %d, want the Lyrics pill", n.focus)
	}
	if n.Handle(press(input.Left), t0) || n.Handle(press(input.Down), t0) {
		t.Fatal("focus moved off the pill sideways or down")
	}
	if h := n.hints(f.st); h[0].Label != "Lyrics" {
		t.Fatalf("A hint on the pill: %q", h[0].Label)
	}
	n.Handle(press(input.A), t0)
	n.Handle(press(input.Up), t0)
	if n.focus != playCtl {
		t.Fatalf("Up from the pill focused %d, want play/pause", n.focus)
	}
	n.Handle(press(input.Y), t0)
	if opened != 2 {
		t.Fatalf("A on the pill and Y opened Lyrics %d times, want 2", opened)
	}
}

func TestNoLyricsPillWithoutLyrics(t *testing.T) {
	n := New(playing())
	if n.Handle(press(input.Down), t0) || n.Handle(press(input.Y), t0) {
		t.Fatal("Down or Y did something with no Lyrics screen to open")
	}
}

func TestHintsWithLyricsFit(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	f := playing()
	n := New(f)
	n.SetLyrics(func() {})
	if w := widgets.HintsWidth(u, n.hints(f.st)); w > widgets.MaxHintsWidth {
		t.Fatalf("hints are %v wide, more than %v", w, widgets.MaxHintsWidth)
	}
}

func TestWaitingForADownload(t *testing.T) {
	f := &fake{st: player.State{Queued: true, Waiting: true, Count: 3,
		Track: player.Track{VideoID: "v1", Title: "Night Drive Theory", Artist: "Lumen Coast", Album: "Night Drive Theory",
			Duration: 231 * time.Second}}}
	n := New(f)
	n.SetDownloads(func(id string) (media.DownloadState, float32) {
		if id != "v1" {
			t.Errorf("asked about %q", id)
		}
		return media.Downloading, 0.42
	}, nil)
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(265, 0.12)
	u.Status = fixture.Status()
	u.Now = t0
	n.Draw(u)
	uitest.Golden(t, "nowplaying-waiting", off.Image())
}

func TestWaitingTexts(t *testing.T) {
	n := New(&fake{})
	s := player.State{Queued: true, Waiting: true, Track: player.Track{VideoID: "v1", Duration: 231 * time.Second}}
	if f, left, right := n.progress(s); f != 0 || left != "Waiting to download" || right != "3:51" {
		t.Fatalf("without downloads: %v, %q, %q", f, left, right)
	}
	st, note := media.Queued, ""
	n.SetDownloads(func(string) (media.DownloadState, float32) { return st, 0.42 }, func() string { return note })
	if f, left, _ := n.progress(s); f != 0 || left != "Waiting to download" {
		t.Fatalf("queued: %v, %q", f, left)
	}
	// Queued, it says why the queue waits.
	for _, note = range []string{"Downloads paused", "Waiting for Wi-Fi", "Card full", "Getting the downloader"} {
		if _, left, _ := n.progress(s); left != note {
			t.Fatalf("queued with %q: %q", note, left)
		}
	}
	st = media.Failed
	if f, left, _ := n.progress(s); f != 0 || left != "Couldn’t download" {
		t.Fatalf("a failed download: %v, %q", f, left)
	}
	st = media.Downloading
	if f, left, _ := n.progress(s); f != 0.42 || left != "Downloading 42 %" {
		t.Fatalf("a download under way: %v, %q", f, left)
	}
	st = media.Saved // landed, and not given to the player yet
	if f, left, _ := n.progress(s); f != 1 || left != "Downloading 100 %" {
		t.Fatalf("a download just saved: %v, %q", f, left)
	}
	s.Track.Duration = 0
	if _, _, right := n.progress(s); right != "" {
		t.Fatalf("an unknown length shows %q", right)
	}
	if a := testing.AllocsPerRun(20, func() { downloadingText(0.42) }); a != 0 {
		t.Fatalf("the progress text made %v allocations", a)
	}
}

func TestStreamTexts(t *testing.T) {
	n := New(&fake{})
	s := player.State{Queued: true, Loading: true, Playing: true,
		Track: player.Track{SpotifyID: "3UmaczJpikHgJFyBTAJVoz", Title: "Stan", Duration: 404 * time.Second}}
	if f, left, right := n.progress(s); f != 0 || left != "Loading" || right != "6:44" {
		t.Fatalf("opening: %v, %q, %q", f, left, right)
	}
	s.Loading, s.Stalled = false, true
	s.Elapsed, s.Duration, s.Rate, s.Kbps = 101*time.Second, 404*time.Second, 44100, 160
	if f, left, right := n.progress(s); f != 0.25 || left != "Connection lost" || right != "6:44" {
		t.Fatalf("picking up: %v, %q, %q", f, left, right)
	}
	if got := formatLine(s); got != "160 kbps" {
		t.Fatalf("format line %q", got)
	}
	s.Kbps, s.Rate = 0, 48000 // played from the file an import downloaded
	if got := formatLine(s); got != "48 kHz" {
		t.Fatalf("from its file: format line %q", got)
	}
	if got := source(s.Track); got != media.Spotify {
		t.Fatalf("badge %v", got)
	}
}

// Frames drawn again allocate nothing: the clocks, the format line and the
// hint bar are made when they change.
func TestFramesDrawnAgainAllocateNothing(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Status = fixture.Status()
	u.Now = t0
	f := playing()
	n := New(f)
	n.SetLyrics(func() {})
	n.Draw(u)
	if a := testing.AllocsPerRun(10, func() { n.Draw(u) }); a != 0 {
		t.Fatalf("a frame allocates %v times", a)
	}
	f.st.Format, f.st.Rate = "OPUS", 48000
	n.Draw(u)
	if n.format != "OPUS · 48 kHz" {
		t.Fatalf("the format line is %q after the track changed", n.format)
	}
}

// The screen is redrawn for the download queue only while it shows a
// download: playback waits on one, or the track is still to download.
func TestShowsDownloadsOnlyForADownload(t *testing.T) {
	f := playing()
	n := New(f)
	if n.ShowsDownloads() {
		t.Fatal("a track on the card shows a download")
	}
	f.st.Track = player.Track{VideoID: "v1", Title: "Night Drive Theory"}
	if !n.ShowsDownloads() {
		t.Fatal("a track still to download doesn't")
	}
	f.st.Track.Path, f.st.Waiting = "/m/v1.opus", true
	if !n.ShowsDownloads() {
		t.Fatal("waiting on a download doesn't")
	}
}

func TestBadgeSaysWhereTheTrackIsFrom(t *testing.T) {
	cases := []struct {
		t    player.Track
		want media.Source
	}{
		{player.Track{VideoID: "v1"}, media.YouTube},
		{player.Track{VideoID: "v1", Path: "/mnt/SDCARD/Music/Boombrick/Lumen Coast/x.opus"}, media.Downloaded},
		{player.Track{Path: "/mnt/SDCARD/Music/Odalys/Copper Season/01 Rust.flac"}, media.Local},
	}
	for _, c := range cases {
		if got := source(c.t); got != c.want {
			t.Errorf("source(%+v) = %v, want %v", c.t, got, c.want)
		}
	}
}

func TestGoldenWithLyricsPill(t *testing.T) {
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	u.Now = t0
	n := New(playing())
	n.SetLyrics(func() {})
	n.Draw(u)
	uitest.Golden(t, "nowplaying-lyrics", off.Image())
}

func TestSeekWithL2AndR2(t *testing.T) {
	f := playing() // 93 s in
	n := New(f)
	if !n.Handle(press(input.R2), t0) || !n.Handle(press(input.L2), t0) {
		t.Fatal("L2 and R2 weren't handled")
	}
	if len(f.seeks) != 2 || f.seeks[0] != 103*time.Second || f.seeks[1] != 83*time.Second {
		t.Errorf("seeks = %v, want [1m43s 1m23s]", f.seeks)
	}
	f.st.Elapsed = 4 * time.Second
	n.Handle(press(input.L2), t0)
	if got := f.seeks[2]; got != 0 {
		t.Errorf("L2 near the start sought to %v, want 0", got)
	}
	f.st.Queued = false
	if n.Handle(press(input.R2), t0) || len(f.seeks) != 3 {
		t.Error("R2 with nothing queued sought")
	}
}

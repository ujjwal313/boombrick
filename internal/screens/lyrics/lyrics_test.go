package lyrics

import (
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	lrc "github.com/ujjwal313/boombrick/internal/lyrics"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(1000, 0)

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

type fakePlayer struct {
	st      player.State
	toggles int
	seeks   []time.Duration
}

func (p *fakePlayer) State() player.State  { return p.st }
func (p *fakePlayer) Toggle()              { p.toggles++ }
func (p *fakePlayer) Seek(d time.Duration) { p.seeks = append(p.seeks, d) }

type fakeSource struct {
	ly    lrc.Lyrics
	state lrc.State
	asked lrc.Track
	asks  int
}

func (f *fakeSource) Lyrics(t lrc.Track) (lrc.Lyrics, lrc.State) {
	f.asked = t
	f.asks++
	return f.ly, f.state
}

const song = `[00:10.00]Salt on the window, static on the line
[00:14.00]I kept the radio low
[00:18.00]<00:18.00>Every <00:18.40>station <00:19.00>said <00:19.40>your <00:19.80>name<00:21.00>
[00:22.00]Low tide radio
[00:26.00]Carry me home
[00:30.00]Carry me home`

func playing(at time.Duration) *fakePlayer {
	return &fakePlayer{st: player.State{Queued: true, Playing: true, Elapsed: at, Duration: 222 * time.Second,
		Track: player.Track{Path: "/m/1.flac", Title: "Low Tide Radio", Artist: "Marisol Vega", AlbumArtist: "Marisol Vega",
			Album: "Salt & Static", Duration: 222 * time.Second}}}
}

// render draws s twice: once to ask for the art, once after it has faded in.
func render(t *testing.T, s *Screen) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	for _, at := range []time.Time{t0, t0.Add(anim.ArtFade + anim.Lyric)} {
		u.BeginFrame(at)
		s.Draw(u)
	}
	return off.Image()
}

func TestSyncedGolden(t *testing.T) {
	src := &fakeSource{ly: lrc.Parse(song), state: lrc.Found}
	s := New(playing(19200*time.Millisecond), src, func() {})
	uitest.Golden(t, "lyrics-synced", render(t, s))
	if src.asked.Path != "/m/1.flac" || src.asked.Duration != 222*time.Second {
		t.Fatalf("asked for %+v", src.asked)
	}
}

// A line the footer would cut in half is left out: just above the footer the
// synced screen looks the same as one with no lyrics. A line that fits ends
// in its line box's descent, clear of the footer's top.
func TestNoLineIsCutByTheFooter(t *testing.T) {
	synced := render(t, New(playing(16*time.Second), &fakeSource{ly: lrc.Parse(song), state: lrc.Found}, func() {}))
	bare := render(t, New(playing(16*time.Second), &fakeSource{state: lrc.None}, func() {}))
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	top := int((float32(theme.ScreenH-footPad) - u.LineHeight(timeStyle)) * theme.Scale)
	for y := top - 4; y < top; y++ {
		for x := 0; x < theme.DeviceW; x++ {
			if synced.NRGBAAt(x, y) != bare.NRGBAAt(x, y) {
				t.Fatalf("lyric text at (%d, %d) just above the footer", x, y)
			}
		}
	}
}

func near(a, b float32) bool { return a-b < 0.01 && b-a < 0.01 }

func TestFillFollowsTheSungText(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	rows := lineRows(u, "Café déjà vu", []string{"Café déjà vu"}) // multi-byte runes
	for _, c := range []struct {
		sung  float64
		runes string
	}{{0.25, "Caf"}, {0.5, "Café d"}, {1, "Café déjà vu"}} {
		if got, want := fillWidth(rows, 0, c.sung), u.TextWidth(c.runes, curStyle); !near(got, want) {
			t.Errorf("fill at %v = %v, want %v", c.sung, got, want)
		}
	}
	half := fillWidth(rows, 0, 3.5/12)
	if want := (u.TextWidth("Caf", curStyle) + u.TextWidth("Café", curStyle)) / 2; !near(half, want) {
		t.Errorf("part way across a character: %v, want %v", half, want)
	}
}

// The space a wrap drops is no part of either row, but the sung share counts it.
func TestFillReachesTheEndOfAWrappedLine(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	rows := lineRows(u, "Every station said your name", []string{"Every station", "said your name"})
	if got := fillWidth(rows, 1, 1); !near(got, rows[1].width()) {
		t.Errorf("a finished line fills %v of %v", got, rows[1].width())
	}
	// Half way through the dropped space: the first row is done, the second not begun.
	at := 13.5 / 28
	if got := fillWidth(rows, 0, at); !near(got, rows[0].width()) {
		t.Errorf("first row filled %v of %v", got, rows[0].width())
	}
	if got := fillWidth(rows, 1, at); got != 0 {
		t.Errorf("second row filled %v too early", got)
	}
}

// Chinese and Japanese wrap between characters, dropping nothing: the fill
// counts only the characters there are.
func TestFillOfALineWrappedBetweenCharacters(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	rows := lineRows(u, "あいうえお", []string{"あいう", "えお"})
	if got := fillWidth(rows, 0, 0.6); !near(got, rows[0].width()) {
		t.Errorf("three of five sung: the first row filled %v of %v", got, rows[0].width())
	}
	if got, want := fillWidth(rows, 1, 0.8), rows[1].x[1]; !near(got, want) {
		t.Errorf("four of five sung: the second row filled %v, want its first character's %v", got, want)
	}
}

// A right-to-left row fills from its right end, where its first word is.
func TestRightToLeftRowsFillFromTheRight(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	rtl := lineRows(u, "مرحبا بالعالم", []string{"مرحبا بالعالم"})[0]
	if x0, x1 := fillSpan(rtl, 50); !near(x0, side+rtl.width()-50) || x1 != theme.ScreenW {
		t.Errorf("right to left: fill spans %v to %v", x0, x1)
	}
	ltr := lineRows(u, "Low tide radio", []string{"Low tide radio"})[0]
	if x0, x1 := fillSpan(ltr, 50); x0 != 0 || !near(x1, side+50) {
		t.Errorf("left to right: fill spans %v to %v", x0, x1)
	}
}

// Arabic lyrics fill from the right, as they are read.
func TestSyncedArabicGolden(t *testing.T) {
	src := &fakeSource{ly: lrc.Parse("[00:10.00]مرحبا بالعالم\n[00:14.00]مرحبا"), state: lrc.Found}
	s := New(playing(11500*time.Millisecond), src, func() {}) // part way through the first line
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.UseFixtureFonts(t, u)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	for _, at := range []time.Time{t0, t0.Add(anim.Lyric)} {
		u.BeginFrame(at)
		s.Draw(u)
	}
	uitest.Golden(t, "lyrics-synced-arabic", off.Image())
}

func TestNewLyricsReplaceTheOldWhateverTheirShape(t *testing.T) {
	src := &fakeSource{ly: lrc.Parse(song), state: lrc.Found}
	s := New(playing(19*time.Second), src, func() {})
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.BeginFrame(t0)
	s.Draw(u)
	if got := lineText(s, 2); got != "Every station said your name" {
		t.Fatalf("first lyrics: %q", got)
	}
	src.ly = lrc.Parse(strings.Replace(song, "Every ", "Never ", 1))
	u.BeginFrame(t0.Add(time.Second))
	s.Draw(u)
	if got := lineText(s, 2); got != "Never station said your name" {
		t.Fatalf("lyrics handed back with the same count and state: %q", got)
	}
	if s.Animating(t0.Add(time.Second)) {
		t.Fatal("new lyrics started with a rise")
	}
}

// lineText is line i as the screen wrapped it, its rows joined back up.
func lineText(s *Screen, i int) string {
	var parts []string
	for _, r := range s.rows[i] {
		parts = append(parts, r.text)
	}
	return strings.Join(parts, " ")
}

// Japanese lines, which have no spaces, wrap between characters, in the font
// downloaded for them.
func TestSyncedJapaneseGolden(t *testing.T) {
	src := &fakeSource{ly: lrc.Parse("[00:10.00]東京事変の新しいアルバム\n" +
		"[00:14.00]東京事変の新しいアルバム、東京事変の新しいアルバム、大地の音楽\n[00:18.00]新しい音楽")}
	src.state = lrc.Found
	s := New(playing(15*time.Second), src, func() {})
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.UseFixtureFonts(t, u)
	uitest.WithCovers(u)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	for _, at := range []time.Time{t0, t0.Add(anim.ArtFade + anim.Lyric)} {
		u.BeginFrame(at)
		s.Draw(u)
	}
	if rows := s.rows[1]; len(rows) < 2 {
		t.Fatalf("the long line wrapped to %d rows", len(rows))
	}
	uitest.Golden(t, "lyrics-synced-japanese", off.Image())
}

// Lines wrapped before fonts arrived are wrapped again.
func TestRowsWrappedAgainWhenFontsArrive(t *testing.T) {
	src := &fakeSource{ly: lrc.Parse(song), state: lrc.Found}
	s := New(playing(19200*time.Millisecond), src, func() {})
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.BeginFrame(t0)
	s.Draw(u)
	before := s.rows[s.line]
	if len(before) == 0 {
		t.Fatal("the current line wasn't wrapped")
	}
	u.Fonts.Refresh()
	s.Draw(u)
	if after := s.rows[s.line]; &after[0] == &before[0] {
		t.Fatal("the current line wasn't wrapped again")
	}
}

func TestPlainGolden(t *testing.T) {
	src := &fakeSource{ly: lrc.Parse("Salt on the window, static on the line\nI kept the radio low\n\nLow tide radio\nCarry me home"), state: lrc.Found}
	uitest.Golden(t, "lyrics-plain", render(t, New(playing(time.Minute), src, func() {})))
}

func TestNoLyricsGolden(t *testing.T) {
	uitest.Golden(t, "lyrics-none", render(t, New(playing(time.Minute), &fakeSource{state: lrc.None}, func() {})))
}

func TestMessages(t *testing.T) {
	st := playing(0).st
	for _, c := range []struct {
		state lrc.State
		want  string
	}{
		{lrc.Looking, "Looking for lyrics"},
		{lrc.None, "No lyrics for this song"},
		{lrc.Instrumental, "Instrumental"},
		{lrc.Offline, "Lyrics need a connection the first time"},
		{lrc.Failed, "Couldn't get the lyrics just now"},
	} {
		if got := message(st, c.state); got != c.want {
			t.Errorf("message(%v) = %q, want %q", c.state, got, c.want)
		}
	}
	if got := message(player.State{}, lrc.None); got != "Nothing is playing" {
		t.Errorf("with nothing queued: %q", got)
	}
}

func TestButtons(t *testing.T) {
	p := playing(5 * time.Second)
	backs := 0
	s := New(p, &fakeSource{state: lrc.None}, func() { backs++ })
	s.Handle(press(input.Left), t0)
	s.Handle(press(input.Right), t0)
	s.Handle(press(input.A), t0)
	s.Handle(press(input.Y), t0)
	if !s.Back() || backs != 2 {
		t.Fatalf("Y and B went back %d times", backs)
	}
	if !slices.Equal(p.seeks, []time.Duration{0, 15 * time.Second}) || p.toggles != 1 {
		t.Fatalf("seeks %v, toggles %d", p.seeks, p.toggles)
	}
}

func TestTick(t *testing.T) {
	s := New(playing(19*time.Second), &fakeSource{ly: lrc.Parse(song), state: lrc.Found}, func() {})
	render(t, s)
	if s.Tick() != time.Second/30 {
		t.Fatalf("synced lyrics tick every %v", s.Tick())
	}
	s2 := New(playing(time.Minute), &fakeSource{state: lrc.None}, func() {})
	render(t, s2)
	if s2.Tick() != 250*time.Millisecond {
		t.Fatalf("without synced lyrics the screen ticks every %v", s2.Tick())
	}
}

// Synced lyrics need 30 frames a second only while a line fills in or the
// next is about to rise; the progress bar's 4 do otherwise.
func TestTickWhenNothingFills(t *testing.T) {
	p := playing(0)
	s := New(p, &fakeSource{ly: lrc.Parse("[00:10.00]Salt on the window\n[00:40.00]Static on the line"), state: lrc.Found}, func() {})
	render(t, s)
	for _, c := range []struct {
		heard time.Duration // what has been heard: the player's count less the output's delay
		want  time.Duration
	}{
		{2 * time.Second, 250 * time.Millisecond},    // before the first line
		{9800 * time.Millisecond, time.Second / 30},  // the first line is about to start
		{15 * time.Second, time.Second / 30},         // filling, over 10 s
		{25 * time.Second, 250 * time.Millisecond},   // sung, and the next is 15 s away
		{39800 * time.Millisecond, time.Second / 30}, // the next is about to rise
		{44 * time.Second, time.Second / 30},         // the last fills over 5 s
		{3 * time.Minute, 250 * time.Millisecond},    // the outro
	} {
		p.st.Elapsed = c.heard + outputDelay
		if got := s.Tick(); got != c.want {
			t.Errorf("at %v heard: tick %v, want %v", c.heard, got, c.want)
		}
	}
}

// The player counts what it has written to the output, which plays it 100 ms
// later: the lines follow what is heard.
func TestLinesFollowWhatIsHeard(t *testing.T) {
	s := New(playing(18050*time.Millisecond), &fakeSource{ly: lrc.Parse(song), state: lrc.Found}, func() {})
	render(t, s)
	if s.line != 1 {
		t.Fatalf("line %d is current 50 ms after it was written, before it is heard", s.line)
	}
	s = New(playing(30*time.Millisecond), &fakeSource{ly: lrc.Parse("[00:00.00]From the start\n[00:05.00]Next"), state: lrc.Found}, func() {})
	render(t, s)
	if s.line != 0 {
		t.Fatalf("line %d at the very start", s.line)
	}
}

// The text and glow of the next line, and of the line that comes into view
// after it, are made a piece a frame while nothing moves, so the frames of
// the rise make none.
func TestTheNextLineIsMadeAhead(t *testing.T) {
	p := playing(17 * time.Second) // a second before line 2 starts
	s := New(p, &fakeSource{ly: lrc.Parse(song), state: lrc.Found}, func() {})
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	at := t0
	frame := func() {
		u.BeginFrame(at)
		s.Draw(u)
		at = at.Add(time.Second / 30)
		p.st.Elapsed += time.Second / 30
	}
	for s.line != 2 {
		made := u.Made()
		frame()
		if s.line == 2 {
			for s.Animating(at) {
				frame()
			}
			if n := u.Made() - made; n != 0 {
				t.Fatalf("the next line's rise made %d textures", n)
			}
		}
		if p.st.Elapsed > 25*time.Second {
			t.Fatal("line 2 never became current")
		}
	}
}

// Lyric lines are large; they keep to the screen's own text cache, which is
// emptied when the track changes.
func TestLyricLinesKeepToTheirOwnCache(t *testing.T) {
	p := playing(19 * time.Second)
	src := &fakeSource{ly: lrc.Parse(song), state: lrc.Found}
	s := New(p, src, func() {})
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.BeginFrame(t0)
	s.Draw(u)
	if s.texts.Len() == 0 {
		t.Fatal("no lyric lines in the screen's cache")
	}
	made := u.Made()
	u.Text("I kept the radio low", lineStyle, 0, 0, theme.Paper)
	if u.Made() == made {
		t.Fatal("a lyric line went into the shared text cache")
	}
	p.st.Track.Path = "/m/2.flac"
	src.state = lrc.Looking
	u.BeginFrame(t0.Add(time.Second))
	s.Draw(u)
	if n := s.texts.Len(); n != 0 {
		t.Fatalf("%d lines of the last track's lyrics kept", n)
	}
}

// Without a length from the library, the lookup gets the one the decoder found.
func TestAsksWithTheDecodersLength(t *testing.T) {
	p := playing(time.Minute)
	p.st.Track.Duration = 0
	src := &fakeSource{state: lrc.None}
	render(t, New(p, src, func() {}))
	if src.asked.Duration != 222*time.Second {
		t.Fatalf("asked for %+v", src.asked)
	}
}

func TestTheNextLineRises(t *testing.T) {
	p := playing(15 * time.Second)
	s := New(p, &fakeSource{ly: lrc.Parse(song), state: lrc.Found}, func() {})
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.BeginFrame(t0)
	s.Draw(u)
	if s.Animating(t0) {
		t.Fatal("the first line shown slid in")
	}
	p.st.Elapsed = 18500 * time.Millisecond
	at := t0.Add(time.Second)
	u.BeginFrame(at)
	s.Draw(u)
	if !s.Animating(at.Add(anim.Lyric/2)) || s.Animating(at.Add(anim.Lyric)) {
		t.Fatal("the next line doesn't rise over 250 ms")
	}
}

func TestPlainLyricsScroll(t *testing.T) {
	var lines []string
	for range 30 {
		lines = append(lines, "Carry me home")
	}
	s := New(playing(time.Minute), &fakeSource{ly: lrc.Parse(strings.Join(lines, "\n")), state: lrc.Found}, func() {})
	render(t, s)
	if s.Handle(press(input.Up), t0) {
		t.Fatal("scrolled up past the top")
	}
	if !s.Handle(press(input.Down), t0) || s.scroll.Target() != 1 {
		t.Fatalf("Down scrolled to %v", s.scroll.Target())
	}
}

func TestNextSpotifySongResetsTheScreen(t *testing.T) {
	src := &fakeSource{ly: lrc.Parse(song), state: lrc.Found}
	p := playing(19 * time.Second)
	p.st.Track = player.Track{SpotifyID: "one", Title: "Low Tide Radio", Artist: "Marisol Vega", Duration: 222 * time.Second}
	s := New(p, src, func() {})
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.BeginFrame(t0)
	s.Draw(u)
	if len(s.rows) == 0 || s.line < 0 {
		t.Fatalf("first song drew nothing: rows %d line %d", len(s.rows), s.line)
	}
	p.st.Track.SpotifyID = "two"
	src.ly, src.state = lrc.Lyrics{}, lrc.Looking
	s.refresh(p.st, t0)
	if len(s.rows) != 0 || s.line != -1 {
		t.Fatalf("the next song kept the last one's screen: rows %d line %d", len(s.rows), s.line)
	}
	if s.key != "spotify:two" {
		t.Fatalf("key %q", s.key)
	}
}

// The source is asked about a new track at once, and then every askEvery,
// not on every frame of a line filling in.
func TestTheSourceIsAskedSparingly(t *testing.T) {
	p := playing(19 * time.Second)
	src := &fakeSource{ly: lrc.Parse(song), state: lrc.Found}
	s := New(p, src, func() {})
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	at := t0
	for range 30 {
		u.BeginFrame(at)
		s.Draw(u)
		at = at.Add(time.Second / 30)
	}
	if want := int(time.Second / askEvery); src.asks != want {
		t.Fatalf("asked %d times in a second of frames, want %d", src.asks, want)
	}
	p.st.Track.Path = "/m/2.flac"
	asks := src.asks
	u.BeginFrame(at)
	s.Draw(u)
	if src.asks != asks+1 || src.asked.Path != "/m/2.flac" {
		t.Fatal("the next track wasn't asked about at once")
	}
}

// A frame of synced lyrics drawn again allocates nothing.
func TestFramesDrawnAgainAllocateNothing(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Status = fixture.Status()
	s := New(playing(19200*time.Millisecond), &fakeSource{ly: lrc.Parse(song), state: lrc.Found}, func() {})
	for i := range 10 { // the art fades in and what comes next is made ahead
		u.BeginFrame(t0.Add(time.Duration(i) * time.Second))
		s.Draw(u)
	}
	if n := testing.AllocsPerRun(10, func() { s.Draw(u) }); n != 0 {
		t.Fatalf("a frame allocates %v times", n)
	}
}

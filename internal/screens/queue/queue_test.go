package queue

import (
	"fmt"
	"image"
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(1000, 0)

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

// fakePlayer keeps a queue in play order and applies edits like the player.
type fakePlayer struct {
	tracks  []player.Track
	pos     int
	version uint64
	played  []int
	moves   int
	song    string // what the station playing says it plays
}

func (p *fakePlayer) State() player.State {
	s := player.State{Queued: len(p.tracks) > 0, Index: p.pos, Count: len(p.tracks), Version: p.version, Playing: true,
		Elapsed: 93 * time.Second, Duration: 222 * time.Second, LiveTitle: p.song}
	if s.Queued {
		s.Track = p.tracks[p.pos]
	}
	return s
}
func (p *fakePlayer) Queue(dst []player.Track) ([]player.Track, int) {
	return append(dst, p.tracks...), p.pos
}
func (p *fakePlayer) PlayIndex(i int) { p.pos = i; p.played = append(p.played, i) }
func (p *fakePlayer) Remove(i int) {
	p.tracks = slices.Delete(p.tracks, i, i+1)
	p.version++
}
func (p *fakePlayer) Move(from, to int) {
	p.moves++
	t := p.tracks[from]
	p.tracks = slices.Insert(slices.Delete(p.tracks, from, from+1), to, t)
	p.version++
}

func sample() *fakePlayer {
	names := []string{"Low Tide Radio", "Rust", "Ember", "Tin Roof", "Salt", "Northbound"}
	p := &fakePlayer{}
	for i, n := range names {
		p.tracks = append(p.tracks, player.Track{Path: n, Title: n, Artist: "Odalys", Album: "Copper Season",
			Duration: time.Duration(180+i*20) * time.Second})
	}
	return p
}

func titles(p *fakePlayer) (s []string) {
	for _, t := range p.tracks {
		s = append(s, t.Title)
	}
	return s
}

func render(t *testing.T, q *Queue, now time.Time) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	u.Now = now
	q.Draw(u)
	return off.Image()
}

func TestQueueScreen(t *testing.T) {
	p := sample()
	q := New(p)
	img := render(t, q, t0)
	uitest.Golden(t, "queue", img)
	// The first row's glow fades out over the heading, right of its text.
	top := int(q.listTop * theme.Scale)
	if p, found := uitest.HardEdge(img, image.Pt(800, top-40), image.Pt(800, top+8)); found {
		t.Fatalf("the first row's glow ends in a hard edge at %v", p)
	}
	q.Handle(press(input.Down), t0)
	q.Handle(press(input.A), t0) // the second track up next: play order 2
	if !slices.Equal(p.played, []int{2}) {
		t.Fatalf("A played %v, want [2]", p.played)
	}
	q.Handle(press(input.Y), t0) // playing now puts focus on the first row up next: Tin Roof
	if got := titles(p); slices.Contains(got, "Tin Roof") || !slices.Contains(got, "Salt") {
		t.Fatalf("Y should remove the focused track: %v", got)
	}
}

func TestMoveMode(t *testing.T) {
	p := sample()
	q := New(p)
	render(t, q, t0)
	q.Handle(press(input.X), t0) // lift "Rust"
	q.Handle(press(input.Down), t0)
	q.Handle(press(input.Down), t0)
	uitest.Golden(t, "queue-moving", render(t, q, t0.Add(anim.Focus)))
	if !slices.Equal(titles(p)[:4], []string{"Low Tide Radio", "Rust", "Ember", "Tin Roof"}) {
		t.Fatal("the player's queue changes only on drop")
	}
	q.Handle(press(input.A), t0)
	if got := titles(p)[:4]; !slices.Equal(got, []string{"Low Tide Radio", "Ember", "Tin Roof", "Rust"}) {
		t.Fatalf("after dropping: %v", got)
	}

	q.Handle(press(input.X), t0)
	q.Handle(press(input.Up), t0)
	if !q.Back() || q.moving {
		t.Fatal("B should cancel a move")
	}
	if got := titles(p)[:4]; !slices.Equal(got, []string{"Low Tide Radio", "Ember", "Tin Roof", "Rust"}) {
		t.Fatalf("a cancelled move changed the queue: %v", got)
	}
	if q.Back() {
		t.Fatal("B with no move in progress closes the screen")
	}
}

func TestEmptyQueue(t *testing.T) {
	q := New(&fakePlayer{})
	uitest.Golden(t, "queue-empty", render(t, q, t0))
	if q.Handle(press(input.A), t0) || q.Handle(press(input.X), t0) {
		t.Fatal("an empty queue has nothing to act on")
	}
}

func TestQueueHintsFit(t *testing.T) {
	u, _ := uitest.NewContext(t, 64, 64)
	q := New(sample())
	q.refresh(t0)
	for _, moving := range []bool{false, true} {
		q.moving = moving
		if w := widgets.HintsWidth(u, q.hints()); w > widgets.MaxHintsWidth {
			t.Errorf("hints (moving %v) are %.1f wide, more than %.1f", moving, w, widgets.MaxHintsWidth)
		}
	}
}

func TestOpenedCancelsMove(t *testing.T) {
	p := sample()
	q := New(p)
	render(t, q, t0)
	q.Handle(press(input.X), t0)
	q.Handle(press(input.Down), t0)
	q.Opened() // the overlay was closed with Select and opened again
	if q.moving {
		t.Fatal("reopening should leave no move in progress")
	}
	render(t, q, t0)
	if got := q.upNext()[0].Title; got != "Rust" {
		t.Fatalf("the screen kept its lifted order: first up next is %q", got)
	}
	if q.list.Focus() != 0 {
		t.Fatalf("focus %d, want the lifted row's old place", q.list.Focus())
	}
	if p.moves != 0 {
		t.Fatal("a cancelled move reached the player")
	}
}

func TestTrackChangesUnderMove(t *testing.T) {
	p := sample()
	q := New(p)
	render(t, q, t0)
	q.Handle(press(input.X), t0) // lift "Rust", which is about to start playing
	q.Handle(press(input.Down), t0)
	p.pos++ // the current track ended
	p.version++
	q.Handle(press(input.A), t0)
	if p.moves != 0 {
		t.Fatal("a drop after the track changed must not move anything")
	}
	if q.moving {
		t.Fatal("the move should end")
	}
	render(t, q, t0)
	if got := q.upNext()[0].Title; got != "Ember" {
		t.Fatalf("up next starts with %q, want the player's order (Ember)", got)
	}
}

func TestHeader(t *testing.T) {
	q := New(sample())
	q.refresh(t0)
	if q.header != "6 tracks · 23 min" {
		t.Fatalf("header %q", q.header)
	}
	if got := trackCount(1, 0); got != "1 track" {
		t.Fatalf("got %q", got)
	}
}

// The shell fades the overlay in and out with u.Alpha; the screen dims parts
// of itself within that, and leaves it as it found it.
func TestQueueDrawsAtTheShellsAlpha(t *testing.T) {
	for _, moving := range []bool{false, true} {
		q := New(sample())
		render(t, q, t0)
		if moving {
			q.Handle(press(input.X), t0)
		}
		u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
		u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
		u.Status = fixture.Status()
		u.Now = t0.Add(anim.Focus)
		u.Alpha = 0.3
		q.Draw(u)
		if u.Alpha != 0.3 {
			t.Fatalf("moving %v: alpha %v after drawing, want 0.3", moving, u.Alpha)
		}
		// Paper text at 30 % over the ground comes to about 90 in red, or a
		// little more over the glow at the top left; drawn whole it is 243.
		img := off.Image()
		heading := int(q.listTop*theme.Scale) - 50
		hints := theme.DeviceH - int(widgets.HintsHeight*theme.Scale)
		for _, part := range []struct {
			name string
			r    image.Rectangle
		}{
			{"title and panel", image.Rect(0, int(widgets.StatusHeight*theme.Scale), theme.DeviceW, heading)},
			{"heading and rows", image.Rect(0, heading, theme.DeviceW, hints)},
			{"hint bar", image.Rect(0, hints, theme.DeviceW, theme.DeviceH)},
		} {
			if b := brightest(img, part.r); b > 120 {
				t.Errorf("moving %v: the %s reach %d, drawn over the fade", moving, part.name, b)
			}
		}
	}
}

// brightest is the highest red value in r.
func brightest(img *image.NRGBA, r image.Rectangle) uint8 {
	var b uint8
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			b = max(b, img.NRGBAAt(x, y).R)
		}
	}
	return b
}

// When the current track moves on, up next loses its first row; focus stays
// on the track it was on.
func TestFocusFollowsTheTrackAsTheQueueAdvances(t *testing.T) {
	p := sample()
	q := New(p)
	render(t, q, t0)
	q.Handle(press(input.Down), t0)
	q.Handle(press(input.Down), t0) // Tin Roof
	focused := func() string { return q.upNext()[q.list.Focus()].Title }
	if got := focused(); got != "Tin Roof" {
		t.Fatalf("focus on %q", got)
	}
	p.pos++ // Rust started
	render(t, q, t0.Add(anim.Focus))
	if got := focused(); got != "Tin Roof" {
		t.Fatalf("after the track moved on, focus is on %q, want Tin Roof", got)
	}
	p.pos -= 1 // back to Low Tide Radio
	render(t, q, t0.Add(2*anim.Focus))
	if got := focused(); got != "Tin Roof" {
		t.Fatalf("after going back a track, focus is on %q, want Tin Roof", got)
	}
	p.pos += 3 // Tin Roof itself started
	render(t, q, t0.Add(3*anim.Focus))
	if q.list.Focus() != 0 || focused() != "Salt" {
		t.Fatalf("with the focused track playing, focus is on row %d (%q), want the first, Salt", q.list.Focus(), focused())
	}
}

func TestQueueScrolled(t *testing.T) {
	p := &fakePlayer{}
	for i := range 16 {
		n := fmt.Sprintf("Track %d", i)
		p.tracks = append(p.tracks, player.Track{Path: n, Title: n, Artist: "Odalys", Album: "Copper Season",
			Duration: 200 * time.Second})
	}
	q := New(p)
	now := t0
	render(t, q, now)
	for range 9 {
		q.Handle(press(input.Down), now)
		now = now.Add(anim.Focus)
	}
	img := render(t, q, now.Add(anim.Focus))
	uitest.Golden(t, "queue-scrolled", img)
	// Above the list there is only the ground: no part of the row scrolled
	// under the heading, here its art.
	top := int(q.listTop * theme.Scale)
	if p, found := uitest.HardEdge(img, image.Pt(175, top-12), image.Pt(175, top-2)); found {
		t.Fatalf("a scrolled row shows above the list at %v", p)
	}
}

// Frames drawn again allocate nothing, with a row lifted or not: the rows,
// the panel's lines and the hint bars are made once, an empty line too.
func TestFramesDrawnAgainAllocateNothing(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Status = fixture.Status()
	u.Now = t0
	nameless := stations()
	nameless.song, nameless.tracks[0].Title = "", "" // the panel's line is empty
	for name, p := range map[string]*fakePlayer{"songs": sample(), "stations": stations(), "a nameless station": nameless} {
		q := New(p)
		for _, lifted := range []bool{false, true} {
			if lifted {
				q.Handle(press(input.X), t0)
			}
			q.Draw(u)
			if n := testing.AllocsPerRun(10, func() { q.Draw(u) }); n != 0 {
				t.Errorf("%s, lifted %v: a frame allocates %v times", name, lifted, n)
			}
		}
	}
}

// stations is a queue of stations from the Radio tab, the first playing and
// sending the song it plays.
func stations() *fakePlayer {
	p := &fakePlayer{song: "Monsoon Letters"}
	for _, s := range [][3]string{{"Radio City", "India", "https://logos.radio.test/city.png"},
		{"Deep Space One", "The United States Of America", "https://logos.radio.test/space.png"},
		{"World News Live", "United Kingdom", ""}, {"Jazz Lounge", "France", "https://logos.radio.test/jazz.png"},
		{"Kantipur FM", "Nepal", ""}} {
		p.tracks = append(p.tracks, player.Track{Title: s[0], Artist: s[1], Cover: s[2], StationID: s[0],
			Stream: "http://stream.radio.test/" + s[0]})
	}
	return p
}

// Stations show their logo, their name over their country and "Live" where
// a song's length goes; the panel shows the song the station sends and its
// name, with "Live" in place of the times.
func TestQueueOfStations(t *testing.T) {
	p := stations()
	q := New(p)
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Fallback() // gold, as for every station
	u.Status = fixture.Status()
	u.Status.NowPlaying = p.song
	u.Now = t0
	q.Draw(u)
	img := off.Image()
	if r := q.row(0); r.duration != "Live" || r.art.Glyph != "D" {
		t.Fatalf("Deep Space One's row: %q, art %+v", r.duration, r.art)
	}
	if q.line != "Monsoon Letters · Radio City" {
		t.Fatalf("the panel shows %q", q.line)
	}
	p.song = "" // the station says nothing of what it plays
	render(t, q, t0)
	if q.line != "Radio City" {
		t.Fatalf("with no song, the panel shows %q", q.line)
	}
	uitest.Golden(t, "queue-stations", img)
}

// The header's time counts songs alone: with only stations, it is the count.
func TestHeaderOfStations(t *testing.T) {
	q := New(stations())
	q.refresh(t0)
	if q.header != "5 tracks" {
		t.Fatalf("header %q", q.header)
	}
}

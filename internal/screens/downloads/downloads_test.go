package downloads

import (
	"image"
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/download"
	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(1000, 0)

// fiveTabs are the tabs there were before Radio, which the goldens were made
// with.
var fiveTabs = []int{widgets.TabHome, widgets.TabLibrary, widgets.TabSpotify, widgets.TabYouTube, widgets.TabDownloads}

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

type retry struct {
	id     string
	update bool
}

// fakeSource holds a status and records what the tab asks of it.
type fakeSource struct {
	reads   int
	status  download.Status
	retries []retry
	removed []string
	paused  []bool
}

func (f *fakeSource) Status() download.Status      { f.reads++; return f.status }
func (f *fakeSource) Retry(id string, update bool) { f.retries = append(f.retries, retry{id, update}) }
func (f *fakeSource) Remove(id string)             { f.removed = append(f.removed, id) }
func (f *fakeSource) SetPaused(on bool)            { f.paused = append(f.paused, on) }

type fakeNav struct{ toasts []string }

func (n *fakeNav) Push(screens.Screen) {}
func (n *fakeNav) Pop()                {}
func (n *fakeNav) Toast(msg string)    { n.toasts = append(n.toasts, msg) }

func render(t *testing.T, s screens.Screen, now time.Time) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Accent = theme.Derive(265, 0.12)
	u.Status = fixture.Status()
	u.Tabs.Shown = fiveTabs
	u.BeginFrame(now)
	s.Draw(u)
	u.BeginFrame(now.Add(anim.ArtFade))
	s.Draw(u)
	return off.Image()
}

func song(id, title, artist string) ytmusic.Song {
	return ytmusic.Song{VideoID: id, Title: title, Artist: artist, Thumb: "https://yt3.googleusercontent.com/" + id + "=w544-h544-l90-rj"}
}

const gib = 1 << 30

func sample() download.Status {
	return download.Status{Version: 1,
		Storage: download.Storage{Total: 595 * gib / 10, Free: 386 * gib / 10, Music: 142 * gib / 10},
		Entries: []download.Entry{
			{Song: song("v1", "Night Drive Theory", "Lumen Coast"), State: media.Downloading, Progress: 0.78, Speed: 4.1 * (1 << 20)},
			{Song: song("v2", "Sodium Lights", "Lumen Coast"), State: media.Queued},
			{Song: song("v3", "Overpass", "Lumen Coast & Vela"), State: media.Queued},
			{Song: song("v4", "Afterhours", "Kenji Arata"), State: media.Failed, Failure: download.NeedsUpdate},
			{Song: song("v5", "Copper Season", "Odalys"), State: media.Saved, Bytes: 9_856_000},
			{Song: song("v6", "Northbound", "The Hollow Pines"), State: media.Saved, Bytes: 7_340_032},
		}}
}

func TestTheTab(t *testing.T) {
	src := &fakeSource{status: sample()}
	d := New(&fakeNav{}, src)
	uitest.Golden(t, "downloads", render(t, d, t0))
	want := []string{"ACTIVE · 3", "", "", "", "FAILED · 1", "", "COMPLETED TODAY · 2", "", ""}
	var heads []string
	for _, l := range d.lines {
		heads = append(heads, l.head)
	}
	if !slices.Equal(heads, want) {
		t.Fatalf("lines %q", heads)
	}
	if l := d.lines[1]; l.status != "78 % · 4.1 MB/s" || l.title != "Night Drive Theory — Lumen Coast" {
		t.Fatalf("the downloading row: %+v", l)
	}
	if d.card.Summary() != "20.9 GB used · 38.6 GB free of 59.5 GB" {
		t.Fatalf("summary %q", d.card.Summary())
	}
}

func TestFocusPassesOverHeadings(t *testing.T) {
	d := New(&fakeNav{}, &fakeSource{status: sample()})
	render(t, d, t0)
	if d.list.Focus() != 1 {
		t.Fatalf("focus starts on line %d", d.list.Focus())
	}
	if d.Handle(press(input.Up), t0) {
		t.Fatal("Up from the first row went to its heading")
	}
	for range 3 {
		d.Handle(press(input.Down), t0)
	}
	if d.list.Focus() != 5 {
		t.Fatalf("three down from the top lands on line %d, not the failed row", d.list.Focus())
	}
}

func TestRemoveAndPause(t *testing.T) {
	nav, src := &fakeNav{}, &fakeSource{status: sample()}
	d := New(nav, src)
	render(t, d, t0)
	d.Handle(press(input.X), t0)
	d.Handle(press(input.Y), t0)
	if !slices.Equal(src.removed, []string{"v1"}) || !slices.Equal(src.paused, []bool{true}) || len(nav.toasts) != 1 {
		t.Fatalf("removed %q, paused %v, toasts %q", src.removed, src.paused, nav.toasts)
	}
	if d.Handle(press(input.A), t0) {
		t.Fatal("A on a downloading row did something")
	}
}

func TestTheFailureDialog(t *testing.T) {
	src := &fakeSource{status: sample()}
	d := New(&fakeNav{}, src)
	render(t, d, t0)
	for range 3 {
		d.Handle(press(input.Down), t0)
	}
	d.Handle(press(input.A), t0)
	if d.dlg == nil || !d.Claims(input.Start) || d.Claims(input.L1) {
		t.Fatal("A on the failed row didn't open the dialog")
	}
	uitest.Golden(t, "downloads-failed", render(t, d, t0.Add(anim.Dialog)))

	d.Handle(press(input.A), t0.Add(anim.Dialog)) // Update and retry
	if !slices.Equal(src.retries, []retry{{"v4", true}}) {
		t.Fatalf("retries %+v", src.retries)
	}
	render(t, d, t0.Add(3*anim.Dialog))
	if d.dlg != nil || d.Claims(input.Start) {
		t.Fatal("the dialog stayed")
	}

	// A network failure offers no update; X retries, B closes.
	src.status.Entries[3].Failure = download.Network
	src.status.Version++
	render(t, d, t0)
	d.Handle(press(input.A), t0)
	if n := len(d.dlg.actions); n != 2 {
		t.Fatalf("%d pills for a network failure", n)
	}
	d.Handle(press(input.X), t0)
	if !slices.Equal(src.retries, []retry{{"v4", true}, {"v4", false}}) {
		t.Fatalf("retries %+v", src.retries)
	}
}

func TestAnEmptyQueue(t *testing.T) {
	src := &fakeSource{status: download.Status{Version: 1, Storage: sample().Storage}}
	uitest.Golden(t, "downloads-empty", render(t, New(&fakeNav{}, src), t0))
}

// bump changes the status and its version, as the service does.
func bump(src *fakeSource, change func(*download.Status)) {
	change(&src.status)
	src.status.Version++
}

// focusedID is the download focus is on.
func focusedID(t *testing.T, d *Downloads) string {
	t.Helper()
	e, ok := d.focused()
	if !ok {
		t.Fatalf("focus is on line %d, not a download", d.list.Focus())
	}
	return e.Song.VideoID
}

func TestRemovingTheFocusedRowKeepsFocusNear(t *testing.T) {
	src := &fakeSource{status: sample()}
	d := New(&fakeNav{}, src)
	render(t, d, t0)
	d.Handle(press(input.Down), t0) // v2
	bump(src, func(s *download.Status) { s.Entries = slices.Delete(s.Entries, 1, 2) })
	render(t, d, t0)
	if id := focusedID(t, d); id != "v3" {
		t.Fatalf("focus went to %s after v2 was removed from the middle", id)
	}

	// The last row of a section: focus moves on to the next download.
	bump(src, func(s *download.Status) { s.Entries = slices.Delete(s.Entries, 1, 2) })
	render(t, d, t0)
	if id := focusedID(t, d); id != "v4" {
		t.Fatalf("focus went to %s after the last active row was removed", id)
	}

	// The last row of the list: focus moves back.
	for range 2 {
		d.Handle(press(input.Down), t0)
	}
	bump(src, func(s *download.Status) { s.Entries = s.Entries[:len(s.Entries)-1] })
	render(t, d, t0)
	if id := focusedID(t, d); id != "v5" {
		t.Fatalf("focus went to %s after the last row was removed", id)
	}
}

func TestTheDownloaderRowTakesNoFocus(t *testing.T) {
	src := &fakeSource{status: download.Status{Version: 1, Installing: true, Install: 0.4, Storage: sample().Storage}}
	d := New(&fakeNav{}, src)
	render(t, d, t0)
	if _, ok := d.focused(); ok {
		t.Fatal("focus is on the downloader row")
	}
	if d.lines[1].title != "Getting the downloader" || d.lines[1].status != "40 %" {
		t.Fatalf("the downloader row: %+v", d.lines[1])
	}
	for _, b := range []input.Button{input.Down, input.Up, input.X, input.A} {
		if d.Handle(press(b), t0) {
			t.Fatalf("%v did something with only the downloader row", b)
		}
	}
	render(t, d, t0)

	// With downloads, focus rests on the first of them and stays across updates.
	src.status = sample()
	src.status.Installing, src.status.Install, src.status.Version = true, 0.4, 2
	render(t, d, t0)
	if id := focusedID(t, d); id != "v1" {
		t.Fatalf("focus on %s", id)
	}
	d.Handle(press(input.Down), t0)
	bump(src, func(s *download.Status) { s.Install = 0.6 })
	render(t, d, t0)
	if id := focusedID(t, d); id != "v2" {
		t.Fatalf("focus moved to %s when the install progressed", id)
	}
	d.Handle(press(input.Up), t0)
	if d.Handle(press(input.Up), t0) {
		t.Fatal("Up from the first download went onto the downloader row")
	}
}

func TestStatusIsReadOncePerFrame(t *testing.T) {
	src := &fakeSource{status: sample()}
	d := New(&fakeNav{}, src)
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Status = fixture.Status()
	u.BeginFrame(t0)
	d.Draw(u)
	if src.reads != 1 {
		t.Fatalf("a frame read the status %d times", src.reads)
	}
	d.Handle(press(input.Down), t0)
	d.Handle(press(input.Up), t0)
	if src.reads != 1 {
		t.Fatalf("input read the status: %d reads", src.reads)
	}
}

func TestRowsAreMadeOnlyWhenTheVersionChanges(t *testing.T) {
	src := &fakeSource{status: sample()}
	d := New(&fakeNav{}, src)
	render(t, d, t0)
	first := &d.lines[0]
	src.status.Entries[1].State = media.Failed // no Version bump
	src.status.Paused = true
	render(t, d, t0)
	if &d.lines[0] != first || d.lines[4].head != "FAILED · 1" || d.status.Paused {
		t.Fatal("the rows were made again without a new version")
	}
	src.status.Version++
	render(t, d, t0)
	if d.lines[3].head != "FAILED · 2" || !d.status.Paused {
		t.Fatal("the rows weren't made again for a new version")
	}
}

func TestPauseThenResume(t *testing.T) {
	src := &fakeSource{status: sample()}
	d := New(&fakeNav{}, src)
	render(t, d, t0)
	bump(src, func(s *download.Status) { s.Paused = true })
	render(t, d, t0)
	d.Handle(press(input.Y), t0)
	if !slices.Equal(src.paused, []bool{false}) {
		t.Fatalf("Y while paused asked for %v", src.paused)
	}
	if d.lines[0].note != "Paused" {
		t.Fatalf("the Active heading says %q", d.lines[0].note)
	}
}

func TestTheQueueStateShowsWithoutActiveRows(t *testing.T) {
	st := sample()
	st.Entries = st.Entries[3:]
	st.Waiting = "Waiting for Wi-Fi"
	lines := sections(st)
	if lines[0].head != "FAILED · 1" || lines[0].note != "Waiting for Wi-Fi" || lines[2].note != "" {
		t.Fatalf("the note isn't on the first heading: %+v", lines)
	}
	if got := queueNote(download.Status{Paused: true, Waiting: "x"}); got != "Paused" {
		t.Fatalf("note %q", got)
	}
}

// failed opens the dialog on the failed row, whose failure is why.
func failed(t *testing.T, why download.Failure) (*Downloads, *fakeSource, *fakeNav) {
	t.Helper()
	src, nav := &fakeSource{status: sample()}, &fakeNav{}
	src.status.Entries[3].Failure = why
	d := New(nav, src)
	render(t, d, t0)
	for range 3 {
		d.Handle(press(input.Down), t0)
	}
	d.Handle(press(input.A), t0)
	if d.dlg == nil {
		t.Fatal("the dialog didn't open")
	}
	return d, src, nav
}

func TestDialogPillSets(t *testing.T) {
	cases := map[download.Failure][]int{
		download.NeedsUpdate: {updateRetry, retryIt, removeIt},
		download.Other:       {updateRetry, retryIt, removeIt},
		download.Network:     {retryIt, removeIt},
		download.Unavailable: {retryIt, removeIt},
	}
	for why, want := range cases {
		d, _, _ := failed(t, why)
		if !slices.Equal(d.dlg.actions, want) {
			t.Errorf("failure %v: pills %v, want %v", why, d.dlg.actions, want)
		}
	}
}

func TestDialogPillsAndClosing(t *testing.T) {
	d, src, _ := failed(t, download.NeedsUpdate)
	if d.Handle(press(input.Left), t0) {
		t.Fatal("Left from the first pill moved")
	}
	if !d.Handle(press(input.Right), t0) || d.dlg.pill != 1 || d.dlg.hints()[0].Label != "Retry" {
		t.Fatalf("Right: pill %d", d.dlg.pill)
	}
	d.Handle(press(input.Right), t0)
	if d.Handle(press(input.Right), t0) || d.dlg.pill != 2 {
		t.Fatalf("Right from the last pill: pill %d", d.dlg.pill)
	}
	if !d.Handle(press(input.Left), t0) || d.dlg.pill != 1 {
		t.Fatalf("Left: pill %d", d.dlg.pill)
	}

	d.Handle(press(input.B), t0)
	if !d.Claims(input.Start) { // it is still fading out
		t.Fatal("Start is free while the dialog closes")
	}
	if d.Handle(press(input.A), t0) {
		t.Fatal("a closing dialog took input")
	}
	render(t, d, t0.Add(3*anim.Dialog))
	if d.dlg != nil || d.Claims(input.Select) || len(src.retries) != 0 || len(src.removed) != 0 {
		t.Fatal("B did more than close the dialog")
	}
}

func TestRemovingFromTheDialog(t *testing.T) {
	d, src, nav := failed(t, download.Network)
	d.Handle(press(input.Right), t0) // Remove
	d.Handle(press(input.A), t0)
	if !slices.Equal(src.removed, []string{"v4"}) || len(nav.toasts) != 1 || nav.toasts[0] != "Removed “Afterhours”" {
		t.Fatalf("removed %q, toasts %q", src.removed, nav.toasts)
	}
}

// The tab is redrawn whenever the download queue changes.
func TestTheTabShowsDownloads(t *testing.T) {
	if !New(&fakeNav{}, &fakeSource{status: sample()}).ShowsDownloads() {
		t.Fatal("the Downloads tab isn't redrawn for the queue")
	}
}

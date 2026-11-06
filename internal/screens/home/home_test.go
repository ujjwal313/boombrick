package home

import (
	"fmt"
	"image"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/resume"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func TestMain(m *testing.M) { uitest.Main(m) }

// Each of Home's hint sets fits the bar.
func TestHintsFit(t *testing.T) {
	u, _ := uitest.NewContext(t, 64, 64)
	check := func(name string, h *Home) {
		t.Helper()
		if w := widgets.HintsWidth(u, h.hints()); w > widgets.MaxHintsWidth {
			t.Errorf("%s hints are %.1f wide, more than %.1f", name, w, widgets.MaxHintsWidth)
		}
	}
	h := New(fixture.Home())
	for _, row := range []int{0, 2} {
		h.row = row
		check(fmt.Sprintf("row %d", row), h)
	}
	rows := []fixture.Row{{Title: "Recently added", Items: []fixture.Item{{Title: "A", Play: func() {}, Queue: func() {}}}}}
	check("playable", func() *Home { h := New(nil); h.SetRows(rows); return h }())
	check("unset", New(nil))
	empty := New(nil)
	empty.SetRows(nil)
	check("empty", empty)
}

func TestHintsFollowTheFocusedItem(t *testing.T) {
	keys := func(h *Home) string {
		var s []string
		for _, hint := range h.hints() {
			s = append(s, hint.Key)
		}
		return strings.Join(s, " ")
	}
	h := New(nil)
	if got := keys(h); got != "L1/R1" {
		t.Fatalf("unset hints %q", got)
	}
	h.SetRows(nil)
	if got := keys(h); got != "Y L1/R1" {
		t.Fatalf("empty hints %q", got)
	}
	h.SetRows([]fixture.Row{{Title: "R", Items: []fixture.Item{{Title: "A", Play: func() {}}}}})
	if got := keys(h); got != "A L1/R1" {
		t.Fatalf("no Queue func: hints %q", got)
	}
	h.SetRows([]fixture.Row{{Title: "R", Items: []fixture.Item{{Title: "A", Play: func() {}, Queue: func() {}}}}})
	if got := keys(h); got != "A Y L1/R1" {
		t.Fatalf("playable hints %q", got)
	}
}

// A rescan that empties the library and then refills it doesn't leave the old
// scroll behind.
func TestSetRowsResetsScroll(t *testing.T) {
	h := New(fixture.Home())
	render(t, h, t0)
	h.Handle(press(input.Down), t0)
	h.Handle(press(input.Down), t0)
	render(t, h, t0.Add(anim.Focus))
	if h.scroll.Target() == 0 {
		t.Fatal("scroll should have moved down")
	}
	h.SetRows(nil)
	if got := h.scroll.Target(); got != 0 {
		t.Fatalf("scroll target %v after emptying, want 0", got)
	}
	h.SetRows(fixture.Home()[:2])
	h.Handle(press(input.Down), t0)
	now := t0.Add(2 * anim.Focus)
	render(t, h, now)
	if got := h.scroll.Target(); got != h.tops[1] || h.tops[1] == 0 {
		t.Fatalf("scroll target %v after refilling, want the second row's heading %v", got, h.tops[1])
	}
}

// A reload that adds or reorders rows leaves focus on the same album, in view.
func TestSetRowsKeepsFocusOnTheSameItem(t *testing.T) {
	item := func(title string) fixture.Item { return fixture.Item{Title: title, Subtitle: "Artist"} }
	items := func(titles ...string) []fixture.Item {
		var its []fixture.Item
		for _, s := range titles {
			its = append(its, item(s))
		}
		return its
	}
	recent := fixture.Row{Title: "Recently added", Items: items("One", "Two", "Three")}
	h := New(nil)
	h.SetRows([]fixture.Row{recent})
	h.Handle(press(input.Right), t0)
	h.Handle(press(input.Right), t0) // Three
	cont := fixture.Row{Title: "Continue listening", Items: items("Two")}
	recent.Items = items("New", "One", "Two", "Three")
	h.SetRows([]fixture.Row{cont, recent})
	if h.row != 1 || h.col != 3 {
		t.Fatalf("focus moved to row %d col %d, want row 1 col 3 (Recently added, Three)", h.row, h.col)
	}
	inView(t, h)
	h.SetRows([]fixture.Row{{Title: "Recently added", Items: items("X", "Y")}})
	if h.row != 0 || h.col != 1 {
		t.Fatalf("focus at row %d col %d, want it clamped to 0, 1", h.row, h.col)
	}
	inView(t, h)

	// Scrolled to the end of a long row, then the album focused moves to the
	// front of it: the row scrolls back.
	h.SetRows([]fixture.Row{{Title: "Recently added", Items: items("A", "B", "C", "D", "E", "F", "G", "H")}})
	for range 7 {
		h.Handle(press(input.Right), t0)
	}
	if h.offsets[0].Target() <= 0 {
		t.Fatal("the row should have scrolled to its last item")
	}
	h.SetRows([]fixture.Row{
		{Title: "Recently added", Items: items("H", "Z")},
		{Title: "Continue listening", Items: items("A", "B", "C", "D", "E", "F", "G")},
	})
	if h.row != 0 || h.col != 0 {
		t.Fatalf("focus at row %d col %d, want 0, 0 (H)", h.row, h.col)
	}
	inView(t, h)
	if got := h.offsets[0].Target(); got != 0 {
		t.Fatalf("the row fits, but its offset is %v", got)
	}

	// The album focused moves along the row, past its right-hand end.
	h = New(nil)
	h.SetRows([]fixture.Row{{Title: "Recently added", Items: items("A", "B", "C", "D", "E", "F", "G", "H")}})
	h.Handle(press(input.Right), t0)
	h.Handle(press(input.Right), t0) // C
	h.SetRows([]fixture.Row{{Title: "Recently added", Items: items("X", "Y", "Z", "W", "V", "A", "B", "C")}})
	if h.row != 0 || h.col != 7 {
		t.Fatalf("focus at row %d col %d, want 0, 7 (C)", h.row, h.col)
	}
	inView(t, h)
}

// A row kept by title is clamped to how far it can now scroll, though focus
// is elsewhere.
func TestSetRowsClampsOtherRowsOffsets(t *testing.T) {
	item := func(title string) fixture.Item { return fixture.Item{Title: title, Subtitle: "Artist"} }
	long := fixture.Row{Title: "Recently added"}
	for _, s := range []string{"A", "B", "C", "D", "E", "F", "G", "H"} {
		long.Items = append(long.Items, item(s))
	}
	cont := fixture.Row{Title: "Continue listening", Items: []fixture.Item{item("X")}}
	h := New(nil)
	h.SetRows([]fixture.Row{cont, long})
	h.Handle(press(input.Down), t0)
	for range 7 {
		h.Handle(press(input.Right), t0)
	}
	h.Handle(press(input.Up), t0)
	long.Items = long.Items[:2]
	h.SetRows([]fixture.Row{cont, long})
	if got := h.offsets[1].Target(); got != 0 {
		t.Fatalf("Recently added now fits but stays scrolled to %v", got)
	}
}

// inView fails unless the focused item lies between the side paddings.
func inView(t *testing.T, h *Home) {
	t.Helper()
	size, gap := itemSize(h.rows[h.row])
	x := side + float64(h.col)*(size+gap) - h.offsets[h.row].Target()
	if x < side-1e-6 || x+size > theme.ScreenW-side+1e-6 {
		t.Fatalf("the focused item (row %d col %d) is at x %.1f to %.1f, out of view", h.row, h.col, x, x+size)
	}
}

var t0 = time.Unix(1000, 0)

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

func render(t *testing.T, h *Home, now time.Time) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	u.Now = now
	h.Draw(u)
	return off.Image()
}

// Frame 01a: focus on the first Continue listening card.
func TestFirstCardFocused(t *testing.T) {
	uitest.Golden(t, "home-01a", render(t, New(fixture.Home()), t0))
}

// Frame 01b: D-pad right moves the ring and scale to the next card in 120 ms.
func TestFocusMovesRight(t *testing.T) {
	h := New(fixture.Home())
	render(t, h, t0)
	if !h.Handle(press(input.Right), t0) {
		t.Fatal("Right should move focus")
	}
	if !h.Animating(t0.Add(anim.Focus / 2)) {
		t.Fatal("focus should be animating halfway through")
	}
	end := t0.Add(anim.Focus)
	if h.Animating(end) {
		t.Fatal("focus animation should end after anim.Focus")
	}
	uitest.Golden(t, "home-01b", render(t, h, end))
}

// Frame 01c: after moving down twice the focused row sits under the tabs.
func TestFocusedRowScrollsUnderTabs(t *testing.T) {
	h := New(fixture.Home())
	render(t, h, t0)
	for _, b := range []input.Button{input.Right, input.Down, input.Down} {
		h.Handle(press(b), t0)
	}
	if h.row != 2 || h.col != 1 {
		t.Fatalf("focus = row %d col %d, want 2, 1", h.row, h.col)
	}
	if got := h.scroll.Target(); got <= 0 || got > h.tops[2] {
		t.Fatalf("scroll target = %v, want (0, %v]", got, h.tops[2])
	}
	uitest.Golden(t, "home-01c", render(t, h, t0.Add(anim.Focus)))
}

// The last row snaps under the tabs like the others, so its focused tile,
// scaled and ringed, is never clipped by the hint bar.
func TestLastRowScrollsUnderTabs(t *testing.T) {
	h := New(fixture.Home())
	render(t, h, t0)
	last := len(h.rows) - 1
	for i := 0; i < last; i++ {
		h.Handle(press(input.Down), t0)
	}
	if got, want := h.scroll.Target(), h.tops[last]; got != want {
		t.Fatalf("scroll target = %v, want %v (heading under the tabs)", got, want)
	}
}

func TestFocusStaysInBounds(t *testing.T) {
	h := New(fixture.Home())
	if h.Handle(press(input.Left), t0) || h.Handle(press(input.Up), t0) {
		t.Fatal("moving off the first card should do nothing")
	}
	for i := 0; i < 5; i++ {
		h.Handle(press(input.Right), t0)
	}
	if h.col != 3 {
		t.Fatalf("col = %d, want 3 (four cards)", h.col)
	}
	h.Handle(press(input.Down), t0)  // Downloads has five tiles
	h.Handle(press(input.Right), t0) // to the fifth
	h.Handle(press(input.Down), t0)  // Spotify playlists has four cards
	if h.row != 2 || h.col != 3 {
		t.Fatalf("focus = %d,%d, want 2,3 (column clamped)", h.row, h.col)
	}
}

// A frame drawn again allocates nothing: no closure per item, no focus
// strength for items that never had focus, no hint bar.
func TestDrawingAgainAllocatesNothing(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	u.Now = t0
	h := New(fixture.Home())
	h.Draw(u)
	if n := testing.AllocsPerRun(10, func() { h.Draw(u) }); n != 0 {
		t.Fatalf("a frame allocates %v times", n)
	}
}

// Only the focused item and the one focus left, still fading out, keep a
// focus strength.
func TestFadedFocusIsForgotten(t *testing.T) {
	h := New(fixture.Home())
	at := t0
	for range 3 {
		h.Handle(press(input.Right), at)
		at = at.Add(anim.Focus)
	}
	if len(h.focus) != 2 {
		t.Fatalf("%d items keep a focus strength", len(h.focus))
	}
}

// covers records the albums whose covers are asked for, and has none.
type covers map[string]bool

func (c covers) Cover(cv media.Cover, _ int) (*image.NRGBA, bool) {
	c[cv.Album] = true
	return nil, false
}

func (c covers) Backdrop(media.Cover) (*image.NRGBA, bool) { return nil, true }

// Items out of view, off the side of their row or in a row scrolled away,
// aren't drawn.
func TestItemsOutOfViewArentDrawn(t *testing.T) {
	var rows []fixture.Row
	for r := range 6 {
		var items []fixture.Item
		for i := range maxRecent {
			name := fmt.Sprintf("%d-%d", r, i)
			items = append(items, fixture.Item{Title: name, Art: widgets.ArtSpec{Cover: media.Cover{Album: name}}})
		}
		rows = append(rows, fixture.Row{Title: fmt.Sprint(r), Cards: true, Items: items})
	}
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	asked := covers{}
	u.Covers = asked
	u.Now = t0
	New(rows).Draw(u)
	for _, album := range []string{"0-0", "0-3", "1-0"} {
		if !asked[album] {
			t.Errorf("%s, in view, wasn't drawn", album)
		}
	}
	for _, album := range []string{"0-5", "0-11", "5-0"} {
		if asked[album] {
			t.Errorf("%s, out of view, was drawn", album)
		}
	}
}

// A row scrolls sideways just enough to bring the focused card between the
// side paddings; the first cards don't move it.
func TestRowScrollsSideways(t *testing.T) {
	h := New(fixture.Home())
	h.Handle(press(input.Right), t0)
	h.Handle(press(input.Right), t0)
	if got := h.offsets[0].Target(); got != 0 {
		t.Fatalf("offset on the third card = %v, want 0", got)
	}
	h.Handle(press(input.Right), t0)
	want := float64(3*(cardSize+cardGap)+cardSize) - (theme.ScreenW - 2*side)
	if got := h.offsets[0].Target(); math.Abs(got-want) > 1e-6 {
		t.Fatalf("offset on the fourth card = %v, want %v", got, want)
	}
	if !h.Animating(t0.Add(anim.Focus / 2)) {
		t.Fatal("the row should be scrolling halfway through")
	}
	if h.Animating(t0.Add(anim.Focus)) {
		t.Fatal("the row should stop scrolling after anim.Focus")
	}
	for i := 0; i < 3; i++ {
		h.Handle(press(input.Left), t0)
	}
	if got := h.offsets[0].Target(); got != 0 {
		t.Fatalf("offset back on the first card = %v, want 0", got)
	}
}

// Rows keep their sideways position when focus leaves and comes back.
func TestRowsRememberTheirOffset(t *testing.T) {
	h := New(fixture.Home())
	for i := 0; i < 3; i++ {
		h.Handle(press(input.Right), t0)
	}
	want := h.offsets[0].Target()
	if want <= 0 {
		t.Fatalf("offset on the fourth card = %v, want > 0", want)
	}
	for _, b := range []input.Button{input.Left, input.Left, input.Down, input.Up} {
		h.Handle(press(b), t0)
	}
	if h.row != 0 || h.col != 1 {
		t.Fatalf("focus = %d,%d, want 0,1", h.row, h.col)
	}
	if got := h.offsets[0].Target(); got != want {
		t.Fatalf("offset after leaving and returning = %v, want %v", got, want)
	}
}

// The fourth Continue listening card, scrolled into view.
func TestScrolledRowGolden(t *testing.T) {
	h := New(fixture.Home())
	render(t, h, t0)
	for i := 0; i < 3; i++ {
		h.Handle(press(input.Right), t0)
	}
	uitest.Golden(t, "home-scrolled", render(t, h, t0.Add(anim.Focus)))
}

func TestAPlaysTheFocusedItem(t *testing.T) {
	played := 0
	rows := fixture.Home()
	rows[0].Items[0].Play = func() { played++ }
	rows[0].Items[0].Queue = func() {}
	h := New(rows)
	if !h.Handle(press(input.A), t0) || played != 1 {
		t.Fatalf("A on a playable item: played %d", played)
	}
	h.Handle(press(input.Right), t0)
	if h.Handle(press(input.A), t0) {
		t.Fatal("A on sample content should do nothing")
	}
	if got := h.hints(); len(got) != 5 {
		t.Fatalf("sample item hints = %v", got)
	}
	h.Handle(press(input.Left), t0)
	if got := h.hints(); len(got) != 3 || got[0].Label != "Play" || got[1].Label != "Add to queue" {
		t.Fatalf("playable item hints = %v, want A Play, Y Add to queue and L1/R1 Tabs", got)
	}
}

func TestAlbumRow(t *testing.T) {
	if _, ok := AlbumRow(nil, nil, nil); ok {
		t.Fatal("no albums should give no row")
	}
	albums := []*library.Album{{Artist: "Odalys", Title: "Copper Season"}, {Artist: "Kenji Arata", Title: "Afterhours"}}
	var got, queued []string
	row, ok := AlbumRow(albums, func(a *library.Album) { got = append(got, a.Title) },
		func(a *library.Album) { queued = append(queued, a.Title) })
	if !ok || row.Title != "Recently added" || row.Meta != "2 albums" || len(row.Items) != 2 {
		t.Fatalf("row = %+v", row)
	}
	row.Items[1].Play()
	if len(got) != 1 || got[0] != "Afterhours" {
		t.Fatalf("Play started %v", got)
	}
	row.Items[0].Queue()
	if len(queued) != 1 || queued[0] != "Copper Season" {
		t.Fatalf("Queue added %v", queued)
	}
	one, _ := AlbumRow(albums[:1], nil, nil)
	if one.Meta != "1 album" {
		t.Fatalf("meta for one album = %q", one.Meta)
	}
}

func TestSetRowsKeepsFocusInBounds(t *testing.T) {
	h := New(fixture.Home())
	render(t, h, t0)
	for i := 0; i < 3; i++ {
		h.Handle(press(input.Down), t0)
	}
	h.SetRows(fixture.Home()[:2])
	if h.row != 1 {
		t.Fatalf("row = %d after dropping two rows, want 1", h.row)
	}
	render(t, h, t0.Add(anim.Focus))
	if got := h.scroll.Target(); got != h.tops[1] {
		t.Fatalf("scroll target %v, want the focused row's heading %v", got, h.tops[1])
	}
}

func TestReleaseDoesNothing(t *testing.T) {
	h := New(fixture.Home())
	if h.Handle(input.Event{Button: input.Right, Kind: input.Release}, t0) {
		t.Fatal("a release should not move focus")
	}
}

func TestContinueRow(t *testing.T) {
	ix := library.NewIndex([]library.Album{
		{Artist: "Odalys", Title: "Copper Season", Tracks: []library.Track{{Path: "/a/1"}, {Path: "/a/2"}}},
	})
	progress := []resume.Progress{
		{Artist: "Gone", Album: "Deleted", Path: "/g/1", Fraction: 0.5}, // no longer in the library
		{Artist: "Odalys", Album: "Copper Season", Path: "/a/2", Frame: 4800, Fraction: 0.7},
	}
	var gotAlbum *library.Album
	var gotTrack int
	var gotFrame int64
	row, ok := ContinueRow(progress, ix, func(a *library.Album, track int, frame int64) {
		gotAlbum, gotTrack, gotFrame = a, track, frame
	}, nil)
	if !ok || row.Title != "Continue listening" || row.Meta != "1 in progress" || len(row.Items) != 1 || !row.Continue {
		t.Fatalf("row = %+v", row)
	}
	if it := row.Items[0]; it.Progress != 0.7 || it.Title != "Copper Season" {
		t.Fatalf("item = %+v", it)
	}
	row.Items[0].Play()
	if gotAlbum != ix.Recent[0] || gotTrack != 1 || gotFrame != 4800 {
		t.Fatalf("Play resumed %v track %d at %d", gotAlbum, gotTrack, gotFrame)
	}
	if _, ok := ContinueRow(nil, ix, nil, nil); ok {
		t.Fatal("nothing in progress gives no row")
	}
}

// An album renamed since it was played, as when a rescan takes "(Disc 1)" off
// its name, is found by its file, and shows once however many names it was
// played under: as most recently played.
func TestContinueRowFindsARenamedAlbum(t *testing.T) {
	ix := library.NewIndex([]library.Album{
		{Artist: "Odalys", Title: "Copper Season", Tracks: []library.Track{{Path: "/a/1"}, {Path: "/a/2"}}},
	})
	progress := []resume.Progress{
		{Artist: "Odalys", Album: "Copper Season (Disc 1)", Path: "/a/2", Frame: 4800, Fraction: 0.7},
		{Artist: "Odalys", Album: "Copper Season", Path: "/a/1", Fraction: 0.2},
	}
	var gotTrack int
	row, ok := ContinueRow(progress, ix, func(a *library.Album, track int, frame int64) { gotTrack = track }, nil)
	if !ok || len(row.Items) != 1 || row.Items[0].Progress != 0.7 || row.Items[0].Title != "Copper Season" {
		t.Fatalf("row = %+v", row)
	}
	if row.Items[0].Play(); gotTrack != 1 {
		t.Fatalf("Play resumed track %d, want 1", gotTrack)
	}
}

func TestYQueuesTheFocusedItem(t *testing.T) {
	queued := 0
	rows := fixture.Home()
	rows[0].Items[0].Queue = func() { queued++ }
	h := New(rows)
	if !h.Handle(press(input.Y), t0) || queued != 1 {
		t.Fatal("Y should queue the focused item")
	}
}

// While the first scan runs with nothing indexed, Home says the music is
// being read, with only the tab hint, until rows arrive.
func TestHomeWhileReading(t *testing.T) {
	h := New(nil)
	scans := 0
	h.SetEmpty("/mnt/SDCARD/Music", func() { scans++ })
	h.SetReading()
	uitest.Golden(t, "home-reading", render(t, h, t0))
	if got := h.hints(); len(got) != 1 || got[0].Key != "L1/R1" {
		t.Fatalf("hints while reading = %v, want only L1/R1 Tabs", got)
	}
	if h.Handle(press(input.Y), t0) || scans != 0 {
		t.Fatal("Y while reading should not rescan")
	}
	h.SetRows(nil)
	if got := h.hints(); len(got) != 2 || got[0].Key != "Y" {
		t.Fatalf("hints once the library is known to be empty = %v", got)
	}
}

func TestHomeBeforeLoadAndEmpty(t *testing.T) {
	h := New(nil)
	if h.Handle(press(input.Down), t0) || h.Handle(press(input.Y), t0) {
		t.Fatal("before rows are set, Home ignores input")
	}
	render(t, h, t0) // chrome only; must not panic
	scans := 0
	h.SetEmpty("/mnt/SDCARD/Music", func() { scans++ })
	h.SetRows([]fixture.Row{{Title: "Recently added"}}) // a row with no items is dropped
	uitest.Golden(t, "home-empty", render(t, h, t0))
	if !h.Handle(press(input.Y), t0) || scans != 1 {
		t.Fatal("Y on the empty Home rescans")
	}
}

// Spotify playlists starts with Liked Songs; each card opens its page.
func TestSpotifyRow(t *testing.T) {
	if _, ok := SpotifyRow(nil, nil, nil); ok {
		t.Fatal("a row with no playlists kept")
	}
	pls := []spotify.Playlist{
		{URI: "spotify:playlist:37i9dQZF1DX76Wlfdnj7AP", Name: "This Is Eminem", Owner: "Spotify", Count: 50},
		{URI: "spotify:playlist:5ABHKGoOzxkaa28ttQV9sE", Name: "Road trip", Owner: "Ujjwal", Count: 1},
	}
	var opened, imported []string
	row, ok := SpotifyRow(pls, func(p spotify.Playlist) { opened = append(opened, p.Name) },
		func(p spotify.Playlist) { imported = append(imported, p.URI) })
	if !ok || row.Meta != "2 playlists" || !row.Spotify || len(row.Items) != 3 {
		t.Fatalf("row %+v", row)
	}
	if it := row.Items[0]; it.Title != "Liked Songs" || it.Art.Glyph != "♥" {
		t.Fatalf("first card %+v", it)
	}
	if row.Items[1].Subtitle != "50 songs" || row.Items[2].Subtitle != "1 song" {
		t.Fatalf("subtitles %q, %q", row.Items[1].Subtitle, row.Items[2].Subtitle)
	}
	for _, it := range row.Items {
		it.Play()
	}
	if strings.Join(opened, ",") != "Liked Songs,This Is Eminem,Road trip" {
		t.Fatalf("opened %v", opened)
	}
	h := New([]fixture.Row{row})
	if got := h.hints(); got[0].Label != "Open" || got[1].Key != "X" {
		t.Fatalf("hints %v", got)
	}
	h.Handle(input.Event{Button: input.X, Kind: input.Press}, time.Time{})
	if strings.Join(imported, ",") != spotify.LikedURI {
		t.Fatalf("X imported %v", imported)
	}
}

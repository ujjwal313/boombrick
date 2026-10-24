package browse

import (
	"fmt"
	"image"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audible"
	"github.com/ujjwal313/boombrick/internal/audiobooks"
	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/resume"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(1000, 0)

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

// fakePlayer records what the screens ask of the player.
type fakePlayer struct {
	played   []string // first path of each Play
	lists    [][]string
	start    []int
	frames   []int64 // where each PlayFrom began
	queued   [][]string
	shuffled []bool
	playing  string
}

func (p *fakePlayer) State() player.State {
	return player.State{Queued: p.playing != "", Track: player.Track{Path: p.playing}}
}
func (p *fakePlayer) Play(ts []player.Track, start int) {
	p.played, p.start = append(p.played, ts[0].Path), append(p.start, start)
	p.lists = append(p.lists, paths(ts))
}
func (p *fakePlayer) PlayFrom(ts []player.Track, start int, frame int64) {
	p.played, p.start, p.frames = append(p.played, ts[0].Path), append(p.start, start), append(p.frames, frame)
	p.lists = append(p.lists, paths(ts))
}
func (p *fakePlayer) SetShuffle(on bool)        { p.shuffled = append(p.shuffled, on) }
func (p *fakePlayer) Enqueue(ts []player.Track) { p.queued = append(p.queued, paths(ts)) }

func paths(ts []player.Track) []string {
	var paths []string
	for _, t := range ts {
		paths = append(paths, t.Path)
	}
	return paths
}

// fakeNav records pushes and toasts.
type fakeNav struct {
	pushed []screens.Screen
	toasts []string
}

func (n *fakeNav) Push(s screens.Screen) { n.pushed = append(n.pushed, s) }
func (n *fakeNav) Pop()                  {}
func (n *fakeNav) Toast(msg string)      { n.toasts = append(n.toasts, msg) }

// sampleIndex is a small library with names from the design.
func sampleIndex() *library.Index { return library.NewIndex(sampleAlbums()) }

// genreIndex is sampleIndex with genres tagged: Folk, Jazz, Pop and Rock.
func genreIndex() *library.Index {
	albums := sampleAlbums()
	tag := func(album, track int, genres ...string) { albums[album].Tracks[track].Genres = genres }
	tag(0, 0, "Rock")         // Rust
	tag(0, 1, "Rock", "Pop")  // Ember
	tag(0, 2, "Folk")         // Tin Roof
	tag(1, 0, "Jazz", "Rock") // Neon
	tag(2, 0, "Folk")         // Salt
	return library.NewIndex(albums)
}

// rockSongs are genreIndex's Rock songs, by artist.
var rockSongs = []string{"/m/a/1.flac", "/m/Copper Season/Rust.flac", "/m/Copper Season/Ember.flac"}

func sampleAlbums() []library.Album {
	track := func(album, title string, n int, d time.Duration) library.Track {
		return library.Track{Path: "/m/" + album + "/" + title + ".flac", Title: title, Artist: "Odalys", Album: album,
			Number: n, Duration: d}
	}
	return []library.Album{
		{Artist: "Odalys", Title: "Copper Season", Year: 2019, Duration: 15 * time.Minute, Tracks: []library.Track{
			track("Copper Season", "Rust", 1, 222*time.Second), track("Copper Season", "Ember", 2, 245*time.Second),
			track("Copper Season", "Tin Roof", 3, 178*time.Second), track("Copper Season", "Low Tide", 4, 210*time.Second),
		}},
		{Artist: "Kenji Arata", Title: "Afterhours", Year: 2021, Duration: 4 * time.Minute, Tracks: []library.Track{
			{Path: "/m/a/1.flac", Title: "Neon", Artist: "Kenji Arata", Album: "Afterhours", Duration: 4 * time.Minute},
		}},
		{Artist: "Odalys", Title: "Northbound", Year: 2016, Tracks: []library.Track{
			{Path: "/m/n/1.flac", Title: "Salt", Artist: "Odalys", Album: "Northbound"},
		}},
	}
}

func render(t *testing.T, s screens.Screen, now time.Time) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	u.Now = now
	s.Draw(u)
	return off.Image()
}

// fadesSoftly fails if the frame steps sharply anywhere on the line from one
// device pixel to another, as a focus glow cut off by a clip does.
func fadesSoftly(t *testing.T, img *image.NRGBA, from, to image.Point) {
	t.Helper()
	if p, found := uitest.HardEdge(img, from, to); found {
		t.Fatalf("a hard edge at %v, between %v and %v", p, from, to)
	}
}

// The first track sits just under the tabs; its glow fades out under them.
func TestAlbumPageFirstTrackGlow(t *testing.T) {
	pg := NewAlbumPage(&fakeNav{}, &fakePlayer{}, sampleIndex().Album("Odalys", "Copper Season"))
	pg.Handle(press(input.Right), t0)
	pg.Handle(press(input.Right), t0) // the first track
	img := render(t, pg, t0.Add(anim.Focus))
	uitest.Golden(t, "album-first-track", img)
	// Between the YouTube and Downloads tabs, down to the track.
	fadesSoftly(t, img, image.Pt(700, 120), image.Pt(700, 154))
}

func TestAlbumPage(t *testing.T) {
	ix := sampleIndex()
	p, nav := &fakePlayer{playing: "/m/Copper Season/Ember.flac"}, &fakeNav{}
	pg := NewAlbumPage(nav, p, ix.Album("Odalys", "Copper Season"))
	uitest.Golden(t, "album", render(t, pg, t0))

	pg.Handle(press(input.A), t0) // Play: in order, from the top
	if !slices.Equal(p.shuffled, []bool{false}) || p.start[0] != 0 {
		t.Fatalf("Play pill: shuffle %v, start %v", p.shuffled, p.start)
	}
	pg.Handle(press(input.Right), t0) // Shuffle
	if got := pg.hints()[0]; got.Key != "A" || got.Label != "Shuffle" {
		t.Fatalf("A's hint on the Shuffle pill = %+v", got)
	}
	pg.Handle(press(input.A), t0)
	if p.shuffled[1] != true {
		t.Fatal("the Shuffle pill should turn shuffle on")
	}
	pg.Handle(press(input.Right), t0) // into the tracks
	if got := pg.hints()[0].Label; got != "Play" {
		t.Fatalf("A's hint on a track = %q", got)
	}
	pg.Handle(press(input.Down), t0)
	pg.Handle(press(input.A), t0)
	if p.start[2] != 1 {
		t.Fatalf("A on the second track started at %d", p.start[2])
	}
	if !slices.Equal(p.shuffled, []bool{false, true, false}) {
		t.Fatalf("shuffle set %v: A on a track plays the album in order from it", p.shuffled)
	}
	pg.Handle(press(input.Y), t0)
	if len(p.queued) != 1 || !slices.Equal(p.queued[0], []string{"/m/Copper Season/Ember.flac"}) || nav.toasts[0] != "Added to queue" {
		t.Fatalf("Y on a track queued %v, toasts %v", p.queued, nav.toasts)
	}
	img := render(t, pg, t0.Add(anim.Focus))
	uitest.Golden(t, "album-track-focused", img)
	// From the art across to the second track.
	fadesSoftly(t, img, image.Pt(295, 312), image.Pt(322, 312))
	pg.Handle(press(input.Left), t0) // back to the pills
	pg.Handle(press(input.Y), t0)
	if len(p.queued[1]) != 4 {
		t.Fatalf("Y on the pills should queue the album, queued %v", p.queued[1])
	}
}

func TestArtistPage(t *testing.T) {
	ix := sampleIndex()
	p, nav := &fakePlayer{}, &fakeNav{}
	ar := ix.Artists[1] // Odalys: Northbound (2016), Copper Season (2019)
	pg := NewArtistPage(nav, p, ar)
	img := render(t, pg, t0)
	uitest.Golden(t, "artist", img)
	// Right of the artist's name, down to the first card.
	fadesSoftly(t, img, image.Pt(300, 150), image.Pt(300, 220))
	pg.Handle(press(input.Right), t0)
	pg.Handle(press(input.A), t0)
	if len(nav.pushed) != 1 {
		t.Fatal("A should open the album")
	}
	if ap, ok := nav.pushed[0].(*AlbumPage); !ok || ap.title != "Copper Season" {
		t.Fatalf("opened %+v", nav.pushed[0])
	}
}

// odalysSongs are Odalys's songs in sampleIndex, albums oldest first.
var odalysSongs = []string{"/m/n/1.flac", "/m/Copper Season/Rust.flac", "/m/Copper Season/Ember.flac",
	"/m/Copper Season/Tin Roof.flac", "/m/Copper Season/Low Tide.flac"}

// shuffledAll fails unless p's one Play was songs, from any of them, with
// shuffle turned on.
func shuffledAll(t *testing.T, p *fakePlayer, songs []string) {
	t.Helper()
	if !slices.Equal(p.shuffled, []bool{true}) || len(p.lists) != 1 || !slices.Equal(p.lists[0], songs) {
		t.Fatalf("shuffle %v, played %q; want shuffle on and %q", p.shuffled, p.lists, songs)
	}
	if s := p.start[0]; s < 0 || s >= len(songs) {
		t.Fatalf("started at %d of %d", s, len(songs))
	}
}

// X on an artist's page shuffles all their songs, whichever album is focused.
func TestArtistPageShufflesAll(t *testing.T) {
	p := &fakePlayer{}
	pg := NewArtistPage(&fakeNav{}, p, sampleIndex().Artists[1])
	pg.Handle(press(input.Right), t0)
	if !pg.Handle(press(input.X), t0) {
		t.Fatal("X should be taken")
	}
	shuffledAll(t, p, odalysSongs)
	if got := pg.hints()[1]; got.Key != "X" || got.Label != "Shuffle all" {
		t.Fatalf("the second hint is %+v", got)
	}
}

// X on an artist in the Library shuffles all their songs; it does nothing on
// albums or songs.
func TestLibraryShufflesAnArtist(t *testing.T) {
	p := &fakePlayer{}
	l := New(&fakeNav{}, p, "/m", nil)
	l.SetIndex(sampleIndex())
	if l.Handle(press(input.X), t0) || len(p.lists) != 0 {
		t.Fatal("X on an album should do nothing")
	}
	l.Handle(press(input.R2), t0)
	l.Handle(press(input.Down), t0) // Odalys
	if !l.Handle(press(input.X), t0) {
		t.Fatal("X on an artist should be taken")
	}
	shuffledAll(t, p, odalysSongs)
	if got := l.hints()[1]; got.Key != "X" || got.Label != "Shuffle" {
		t.Fatalf("the second hint is %+v", got)
	}
	l.Handle(press(input.R2), t0)
	if l.Handle(press(input.X), t0) || len(p.lists) != 1 {
		t.Fatal("X on a song should do nothing")
	}
}

// With songs tagged, Genres comes after Songs and before Books: A opens a
// genre, X shuffles it and Y queues it.
func TestLibraryGenres(t *testing.T) {
	p, nav := &fakePlayer{}, &fakeNav{}
	l := New(nav, p, "/m", nil)
	l.SetIndex(genreIndex())
	for range 3 {
		l.Handle(press(input.R2), t0)
	}
	if l.filter != genresFilter {
		t.Fatal("R2 from Songs should show the genres")
	}
	for range 3 {
		l.Handle(press(input.Down), t0) // Folk, Jazz, Pop, Rock
	}
	if got := l.hints()[1]; got.Key != "X" || got.Label != "Shuffle" {
		t.Fatalf("the second hint is %+v", got)
	}
	l.Handle(press(input.Y), t0)
	if len(p.queued) != 1 || !slices.Equal(p.queued[0], rockSongs) || len(nav.toasts) != 1 {
		t.Fatalf("Y on Rock queued %q, toasts %v", p.queued, nav.toasts)
	}
	l.Handle(press(input.X), t0)
	shuffledAll(t, p, rockSongs)
	l.Handle(press(input.A), t0)
	if pg, ok := nav.pushed[0].(*AlbumPage); !ok || pg.title != "Rock" {
		t.Fatalf("A on Rock opened %+v", nav.pushed)
	}
	uitest.Golden(t, "library-genres", render(t, l, t0.Add(anim.Focus)))
}

// A library without genre tags has no Genres filter, so Books follows Songs;
// one whose genres go while it shows shows the songs instead.
func TestLibraryHidesGenresWithoutTags(t *testing.T) {
	l := New(&fakeNav{}, &fakePlayer{}, "/m", nil)
	l.SetIndex(sampleIndex())
	l.Handle(press(input.R2), t0)
	l.Handle(press(input.R2), t0)
	if !l.Handle(press(input.R2), t0) || l.filter != booksFilter || l.Handle(press(input.R2), t0) {
		t.Fatal("R2 on Songs should skip to Books, the last filter, without genres")
	}
	if !l.Handle(press(input.L2), t0) || l.filter != songsFilter {
		t.Fatal("L2 on Books should go back to Songs without genres")
	}
	l.SetIndex(genreIndex())
	if !l.Handle(press(input.R2), t0) || l.filter != genresFilter {
		t.Fatal("R2 on Songs should show the genres once there are some")
	}
	l.SetIndex(sampleIndex())
	if l.filter != songsFilter {
		t.Fatalf("filter %d once the genres went, want the songs", l.filter)
	}
	render(t, l, t0)
}

// When the filters' counts don't all fit, only the chosen filter shows its
// count.
func TestLibraryCountsThatDontFit(t *testing.T) {
	var albums []library.Album
	for i := range 1000 {
		a := library.Album{Artist: fmt.Sprintf("Artist %d", i%100), Title: fmt.Sprintf("Album %d", i)}
		for j := range 10 {
			a.Tracks = append(a.Tracks, library.Track{Path: fmt.Sprintf("/m/%d/%d.flac", i, j),
				Title: fmt.Sprintf("Song %d", j), Genres: []string{fmt.Sprintf("Genre %d", (i+j)%30)}})
		}
		albums = append(albums, a)
	}
	l := New(&fakeNav{}, &fakePlayer{}, "/m", nil)
	l.SetIndex(library.NewIndex(albums))
	l.Handle(press(input.R2), t0)
	img := render(t, l, t0.Add(anim.Focus))
	if l.allCounts {
		t.Fatal("1,000 albums, 100 artists, 10,000 songs and 30 genres can't all show their counts")
	}
	l.SetIndex(sampleIndex())
	render(t, l, t0)
	if !l.allCounts {
		t.Fatal("a small library's counts all fit")
	}
	uitest.Golden(t, "library-one-count", img)
}

// A genre's page is laid out as an album's, its songs each with their artist.
func TestGenrePage(t *testing.T) {
	p := &fakePlayer{playing: "/m/Copper Season/Ember.flac"}
	pg := NewGenrePage(&fakeNav{}, p, genreIndex().Genres[3])
	if pg.title != "Rock" || pg.who != "2 artists" || pg.length != "3 songs · 11 min" || pg.rows[0].Subtitle != "Kenji Arata" {
		t.Fatalf("page %q, %q, %q; first row %+v", pg.title, pg.who, pg.length, pg.rows[0])
	}
	pg.Handle(press(input.A), t0) // Play
	pg.Handle(press(input.Right), t0)
	pg.Handle(press(input.A), t0) // Shuffle
	pg.Handle(press(input.Right), t0)
	pg.Handle(press(input.Down), t0)
	pg.Handle(press(input.A), t0) // the second song
	if !slices.Equal(p.shuffled, []bool{false, true, false}) || p.start[0] != 0 || p.start[2] != 1 {
		t.Fatalf("shuffle %v, starts %v", p.shuffled, p.start)
	}
	for _, l := range p.lists {
		if !slices.Equal(l, rockSongs) {
			t.Fatalf("played %q, want %q", l, rockSongs)
		}
	}
	pg.Handle(press(input.Y), t0)
	if len(p.queued) != 1 || !slices.Equal(p.queued[0], rockSongs[1:2]) {
		t.Fatalf("Y on the second song queued %q", p.queued)
	}
	if pg := NewGenrePage(&fakeNav{}, p, genreIndex().Genres[1]); pg.who != "1 artist" {
		t.Fatalf("Jazz is by %q", pg.who)
	}
	uitest.Golden(t, "genre", render(t, NewGenrePage(&fakeNav{}, p, genreIndex().Genres[3]), t0))
}

func TestPageHintsFit(t *testing.T) {
	u, _ := uitest.NewContext(t, 64, 64)
	ix := sampleIndex()
	shuffle := NewAlbumPage(&fakeNav{}, &fakePlayer{}, ix.Recent[0])
	shuffle.Handle(press(input.Right), t0)
	for _, hints := range [][]widgets.Hint{
		NewAlbumPage(&fakeNav{}, &fakePlayer{}, ix.Recent[0]).hints(),
		shuffle.hints(),
		NewArtistPage(&fakeNav{}, &fakePlayer{}, ix.Artists[0]).hints(),
	} {
		if w := widgets.HintsWidth(u, hints); w > widgets.MaxHintsWidth {
			t.Errorf("hints %v are %.1f wide, more than %.1f", hints, w, widgets.MaxHintsWidth)
		}
	}
}

func TestLibraryFilters(t *testing.T) {
	ix := sampleIndex()
	p, nav := &fakePlayer{}, &fakeNav{}
	l := New(nav, p, "/mnt/SDCARD/Music", nil)
	l.SetIndex(ix)
	img := render(t, l, t0)
	uitest.Golden(t, "library-albums", img)
	// Between the Albums and Artists filters, down to the first card.
	fadesSoftly(t, img, image.Pt(300, 150), image.Pt(300, 216))
	l.Handle(press(input.A), t0)
	if ap, ok := nav.pushed[0].(*AlbumPage); !ok || ap.title != ix.Albums[0].Title {
		t.Fatalf("A on the first album opened %+v", nav.pushed)
	}
	if !l.Handle(press(input.R2), t0) || l.filter != artistsFilter {
		t.Fatal("R2 should show artists")
	}
	img = render(t, l, t0.Add(anim.Focus))
	uitest.Golden(t, "library-artists", img)
	// Right of the filters, down to the first row.
	fadesSoftly(t, img, image.Pt(900, 150), image.Pt(900, 225))
	l.Handle(press(input.Y), t0) // Kenji Arata's one album
	if len(p.queued) != 1 || len(p.queued[0]) != 1 {
		t.Fatalf("Y on an artist queued %v", p.queued)
	}
	l.Handle(press(input.R2), t0)
	l.Handle(press(input.Down), t0)
	l.Handle(press(input.A), t0)
	if len(p.played) != 1 || p.start[0] != 1 {
		t.Fatalf("A on the second song: played %v from %v", p.played, p.start)
	}
	if !slices.Equal(p.shuffled, []bool{false}) {
		t.Fatalf("shuffle set %v: A on a song plays the list in order from it", p.shuffled)
	}
	uitest.Golden(t, "library-songs", render(t, l, t0.Add(anim.Focus)))
	if l.Handle(press(input.R2), t0) {
		t.Fatal("R2 on the last filter does nothing")
	}
	l.Handle(press(input.L2), t0)
	l.Handle(press(input.L2), t0)
	if l.filter != albumsFilter {
		t.Fatal("L2 goes back through the filters")
	}
}

// Frames drawn again allocate nothing: rows' lines and art, counts and hint
// bars are made once.
func TestFramesDrawnAgainAllocateNothing(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Status = fixture.Status()
	u.Now = t0
	ix := genreIndex()
	draw := func(name string, s screens.Screen) {
		t.Helper()
		s.Draw(u)
		if n := testing.AllocsPerRun(10, func() { s.Draw(u) }); n != 0 {
			t.Errorf("%s: a frame allocates %v times", name, n)
		}
	}
	l := New(&fakeNav{}, &fakePlayer{playing: ix.Songs[1].Path}, "/m", nil)
	l.SetIndex(ix)
	for f := range filterCount {
		l.filter = f
		draw(filterNames[f], l)
	}
	draw("an artist's page", NewArtistPage(&fakeNav{}, &fakePlayer{}, ix.Artists[0]))
	draw("an album's page", NewAlbumPage(&fakeNav{}, &fakePlayer{}, ix.Albums[0]))
	draw("a genre's page", NewGenrePage(&fakeNav{}, &fakePlayer{}, ix.Genres[0]))
}

// A new index's rows are made afresh.
func TestNewIndexRowsAreMadeAfresh(t *testing.T) {
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	l := New(&fakeNav{}, &fakePlayer{}, "/m", nil)
	l.SetIndex(sampleIndex())
	l.filter = songsFilter
	l.Draw(u)
	l.SetIndex(library.NewIndex([]library.Album{{Artist: "Marisol Vega", Title: "Salt & Static", Tracks: []library.Track{
		{Path: "/m/s/1.flac", Title: "Low Tide Radio", Artist: "Marisol Vega", Album: "Salt & Static", Duration: 222 * time.Second},
	}}}))
	l.Draw(u)
	if r := l.rows[songsFilter][0]; r.subtitle != "Marisol Vega · Salt & Static" || r.duration != "3:42" {
		t.Fatalf("the first song's row: %q, %q", r.subtitle, r.duration)
	}
	if l.counts != [filterCount]string{"1", "1", "1", "0", "0"} {
		t.Fatalf("counts %q", l.counts)
	}
}

func TestEmptyLibrary(t *testing.T) {
	scans := 0
	nav := &fakeNav{}
	l := New(nav, &fakePlayer{}, "/mnt/SDCARD/Music", func() { scans++ })
	if l.Handle(press(input.Y), t0) || scans != 0 {
		t.Fatal("before the index loads, Y does nothing")
	}
	l.SetIndex(library.NewIndex(nil))
	uitest.Golden(t, "library-empty", render(t, l, t0))
	if !l.Handle(press(input.Y), t0) || scans != 1 || len(nav.toasts) != 1 {
		t.Fatalf("Y on the empty library: %d scans, toasts %v", scans, nav.toasts)
	}
}

// While the first scan runs with nothing indexed, the tab says the music is
// being read, with only the tab hint, until the index arrives.
func TestLibraryWhileReading(t *testing.T) {
	scans := 0
	l := New(&fakeNav{}, &fakePlayer{}, "/mnt/SDCARD/Music", func() { scans++ })
	if got := l.hints(); len(got) != 1 || got[0].Key != "L1/R1" {
		t.Fatalf("hints before the index loads = %v, want only L1/R1 Tabs", got)
	}
	l.SetReading()
	render(t, l, t0) // the reading state; Home's golden covers how it looks
	if got := l.hints(); len(got) != 1 || got[0].Key != "L1/R1" {
		t.Fatalf("hints while reading = %v, want only L1/R1 Tabs", got)
	}
	if l.Handle(press(input.Y), t0) || scans != 0 {
		t.Fatal("Y while reading should not rescan")
	}
	l.SetIndex(sampleIndex())
	if got := l.hints(); len(got) != 4 {
		t.Fatalf("hints once the index loads = %v", got)
	}
}

func TestLibraryHintsFit(t *testing.T) {
	u, _ := uitest.NewContext(t, 64, 64)
	l := New(&fakeNav{}, &fakePlayer{}, "", nil)
	l.SetIndex(genreIndex())
	for f := range filterCount {
		l.filter = f
		if w := widgets.HintsWidth(u, l.hints()); w > widgets.MaxHintsWidth {
			t.Errorf("filter %d hints are %.1f wide, more than %.1f", f, w, widgets.MaxHintsWidth)
		}
	}
}

func TestEmptyAlbum(t *testing.T) {
	p, nav := &fakePlayer{}, &fakeNav{}
	pg := NewAlbumPage(nav, p, &library.Album{Title: "Nothing", Artist: "Nobody"})
	pg.Handle(press(input.A), t0)
	pg.Handle(press(input.Right), t0)
	pg.Handle(press(input.A), t0) // Shuffle
	pg.Handle(press(input.Y), t0)
	if len(p.played) != 0 || len(p.queued) != 0 || len(nav.toasts) != 0 {
		t.Fatalf("an empty album played %v, queued %v, toasted %v", p.played, p.queued, nav.toasts)
	}
}

func TestSongCounts(t *testing.T) {
	if got := artistLine(1, 1); got != "1 album · 1 song" {
		t.Errorf("one album, one song: %q", got)
	}
	if got := artistLine(2, 1234); got != "2 albums · 1,234 songs" {
		t.Errorf("two albums: %q", got)
	}
}

// A reload that inserts albums, artists, songs and genres ahead of the
// focused ones keeps focus on the same items.
func TestLibraryKeepsFocusAcrossReloads(t *testing.T) {
	l := New(&fakeNav{}, &fakePlayer{}, "/m", nil)
	ix := genreIndex()
	l.SetIndex(ix)
	render(t, l, t0) // lays out the album grid
	for f := range filterCount {
		l.filter = f
		l.views[f].SetFocus(l.views[f].Count()-1, t0)
	}
	album, artist, song := ix.Albums[len(ix.Albums)-1], ix.Artists[len(ix.Artists)-1], ix.Songs[len(ix.Songs)-1]
	genre := ix.Genres[len(ix.Genres)-1]

	more := []library.Album{{Artist: "Aaron", Title: "Aardvark", Tracks: []library.Track{
		{Path: "/m/aa/1.flac", Title: "Aa", Artist: "Aaron", Album: "Aardvark", Genres: []string{"Ambient"}},
	}}}
	for _, a := range ix.Recent {
		more = append(more, *a)
	}
	ix2 := library.NewIndex(more)
	l.SetIndex(ix2)
	if a := ix2.Albums[l.views[albumsFilter].Focus()]; a.Title != album.Title {
		t.Errorf("album focus moved to %q, want %q", a.Title, album.Title)
	}
	if a := ix2.Artists[l.views[artistsFilter].Focus()]; a.Name != artist.Name {
		t.Errorf("artist focus moved to %q, want %q", a.Name, artist.Name)
	}
	if s := ix2.Songs[l.views[songsFilter].Focus()]; s.Path != song.Path {
		t.Errorf("song focus moved to %q, want %q", s.Path, song.Path)
	}
	if g := ix2.Genres[l.views[genresFilter].Focus()]; g.Name != genre.Name {
		t.Errorf("genre focus moved to %q, want %q", g.Name, genre.Name)
	}
}

func TestLibraryAlbumsWithArt(t *testing.T) {
	l := New(&fakeNav{}, &fakePlayer{}, "/m", nil)
	l.SetIndex(sampleIndex())
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Accent = theme.Derive(fixture.PlayingHue, fixture.PlayingChroma)
	u.Status = fixture.Status()
	for _, at := range []time.Time{t0, t0.Add(anim.ArtFade)} {
		u.BeginFrame(at)
		l.Draw(u)
	}
	uitest.Golden(t, "library-albums-art", off.Image())
}

func bookIndex() *library.Index {
	return library.NewIndex([]library.Album{
		{Artist: "Georges Simenon", Title: "La casa del giudice", Tracks: []library.Track{{Path: "/m/Audiobooks/La casa [B0GV1924QD].m4b", Title: "La casa del giudice"}}},
		{Artist: "Odalys", Title: "Northbound", Tracks: []library.Track{{Path: "/m/n/1.flac", Title: "Salt"}}},
	})
}

func TestLibraryBooksFilterPlaysFromWhereTheBookWasLeft(t *testing.T) {
	p := &fakePlayer{}
	l := New(&fakeNav{}, p, "/m", nil)
	shelf := NewShelf(p)
	l.SetShelf(shelf)
	l.SetIndex(bookIndex())
	if l.counts != [filterCount]string{"1", "1", "1", "0", "1"} {
		t.Fatalf("counts %q", l.counts)
	}
	l.filter = booksFilter
	l.Handle(press(input.A), t0)
	if len(p.played) != 1 || p.played[0] != "/m/Audiobooks/La casa [B0GV1924QD].m4b" || p.frames[0] != 0 {
		t.Fatalf("played %q from %v", p.played, p.frames)
	}
	shelf.SetProgress([]resume.Progress{{Artist: "Georges Simenon", Album: "La casa del giudice", Path: "/m/Audiobooks/La casa [B0GV1924QD].m4b", Frame: 123456}})
	l.Handle(press(input.A), t0)
	if len(p.played) != 2 || p.frames[1] != 123456 {
		t.Fatalf("played %q from %v, want it to carry on from frame 123456", p.played, p.frames)
	}
}

// fakeCatalogue is an Audible library with what each book is doing.
type fakeCatalogue struct {
	signedIn  bool
	status    audiobooks.Status
	msg       string
	entries   []audiobooks.Entry
	downloads []string
	refreshes int
}

func (c *fakeCatalogue) SignedIn() bool                      { return c.signedIn }
func (c *fakeCatalogue) Status() (audiobooks.Status, string) { return c.status, c.msg }
func (c *fakeCatalogue) Entries() []audiobooks.Entry         { return c.entries }
func (c *fakeCatalogue) Download(asin string)                { c.downloads = append(c.downloads, asin) }
func (c *fakeCatalogue) Refresh()                            { c.refreshes++ }

func entry(asin, title string, path string) audiobooks.Entry {
	return audiobooks.Entry{Book: audible.Book{ASIN: asin, Title: title, Authors: "Georges Simenon", Length: 4*time.Hour + 9*time.Minute}, Path: path}
}

func TestBooksTabDownloadsWhatIsNotOnTheCardAndPlaysWhatIs(t *testing.T) {
	p := &fakePlayer{}
	nav := &fakeNav{}
	cat := &fakeCatalogue{signedIn: true, status: audiobooks.OK, entries: []audiobooks.Entry{
		entry("B0H764PGVH", "Firmato Picpus", ""),
		entry("B0GV1924QD", "La casa del giudice", "/m/Audiobooks/La casa [B0GV1924QD].m4b"),
	}}
	b := NewBooks(nav, p, NewShelf(p), cat, "/data/audible/auth.json")
	b.SetIndex(bookIndex())
	b.Handle(press(input.A), t0) // the first book isn't on the card
	if len(cat.downloads) != 1 || cat.downloads[0] != "B0H764PGVH" || len(p.played) != 0 {
		t.Fatalf("downloads %v, played %v", cat.downloads, p.played)
	}
	b.Handle(press(input.Down), t0)
	b.Handle(press(input.A), t0) // the second is
	if len(p.played) != 1 || p.played[0] != "/m/Audiobooks/La casa [B0GV1924QD].m4b" || len(cat.downloads) != 1 {
		t.Fatalf("downloads %v, played %v", cat.downloads, p.played)
	}
	b.Handle(press(input.Y), t0) // queue the book on the card
	if len(p.queued) != 1 {
		t.Fatalf("queued %v", p.queued)
	}
	b.Handle(press(input.X), t0)
	if cat.refreshes != 1 {
		t.Fatalf("refreshes %d", cat.refreshes)
	}
	// A book already on its way isn't asked for twice.
	cat.entries[0].State = audiobooks.Downloading
	b.Handle(press(input.Up), t0)
	b.Handle(press(input.A), t0)
	if len(cat.downloads) != 1 {
		t.Fatalf("downloads %v", cat.downloads)
	}
}

func TestBooksTabSaysWhyItIsEmpty(t *testing.T) {
	for _, c := range []struct {
		name string
		cat  *fakeCatalogue
		want string
	}{
		{"no account", &fakeCatalogue{}, "Audible isn't set up"},
		{"reading", &fakeCatalogue{signedIn: true, status: audiobooks.Loading}, "Reading your Audible library"},
		{"offline", &fakeCatalogue{signedIn: true, status: audiobooks.Offline}, "No network"},
		{"broken", &fakeCatalogue{signedIn: true, status: audiobooks.Broken, msg: "401"}, "Couldn't read your library"},
		{"none", &fakeCatalogue{signedIn: true, status: audiobooks.OK}, "Your Audible library is empty"},
	} {
		b := NewBooks(&fakeNav{}, &fakePlayer{}, NewShelf(&fakePlayer{}), c.cat, "/data/audible/auth.json")
		b.sync()
		if title, _, _, empty := b.emptyState(); !empty || title != c.want {
			t.Errorf("%s: %q (empty %v), want %q", c.name, title, empty, c.want)
		}
		render(t, b, t0) // and it draws
	}
}

func TestBooksTabDrawsItsList(t *testing.T) {
	cat := &fakeCatalogue{signedIn: true, status: audiobooks.OK, entries: []audiobooks.Entry{
		entry("B0H764PGVH", "Firmato Picpus", ""),
		{Book: audible.Book{ASIN: "B1", Title: "In arrivo", Authors: "X", Length: time.Hour}, State: audiobooks.Downloading, Progress: 0.42},
		{Book: audible.Book{ASIN: "B2", Title: "Fallito", Authors: "X"}, State: audiobooks.Failed, Err: "Audible says wait a while, then try again"},
	}}
	b := NewBooks(&fakeNav{}, &fakePlayer{}, NewShelf(&fakePlayer{}), cat, "")
	render(t, b, t0)
	if r := rowOf(cat.entries[1], ""); r.Duration != "42%" {
		t.Fatalf("progress shows %q", r.Duration)
	}
	if r := rowOf(cat.entries[0], ""); r.Subtitle != "Georges Simenon · 4 h 9 min" {
		t.Fatalf("subtitle %q", r.Subtitle)
	}
	if r := rowOf(cat.entries[2], ""); r.Subtitle != "Audible says wait a while, then try again" {
		t.Fatalf("failed row %q", r.Subtitle)
	}
}

// signalPlayer reports each PlayFrom on a channel, for plays made off the
// UI thread.
type signalPlayer struct {
	fakePlayer
	from chan int64
}

func (p *signalPlayer) PlayFrom(ts []player.Track, start int, frame int64) { p.from <- frame }

func TestBookAsksWhereElseItWasLeft(t *testing.T) {
	p := &signalPlayer{from: make(chan int64, 1)}
	shelf := NewShelf(p)
	book := bookIndex().Books[0]
	when := time.Unix(5000, 0)
	shelf.SetProgress([]resume.Progress{{Artist: "Georges Simenon", Album: "La casa del giudice",
		Path: "/m/Audiobooks/La casa [B0GV1924QD].m4b", Frame: 100, Updated: when}})
	var asked struct {
		frame int64
		when  time.Time
		has   bool
	}
	shelf.SetRemote(func(path string, frame int64, w time.Time, has bool) (int64, bool) {
		asked.frame, asked.when, asked.has = frame, w, has
		return 777, true
	})
	shelf.Play(book)
	select {
	case f := <-p.from:
		if f != 777 {
			t.Fatalf("started at %d, want the other device's 777", f)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the book never started")
	}
	if asked.frame != 100 || !asked.when.Equal(when) || !asked.has {
		t.Fatalf("asked with %+v", asked)
	}
	// When nothing better is found the card's own place stands.
	shelf.SetRemote(func(string, int64, time.Time, bool) (int64, bool) { return 0, false })
	shelf.Play(book)
	if f := <-p.from; f != 100 {
		t.Fatalf("started at %d, want the card's 100", f)
	}
}

func TestBooksTabOffersToSignIn(t *testing.T) {
	nav := &fakeNav{}
	b := NewBooks(nav, &fakePlayer{}, NewShelf(&fakePlayer{}), &fakeCatalogue{}, "/data/audible/auth.json")
	if title, body, detail, _ := b.emptyState(); title != "Audible isn't set up" || !strings.Contains(body, "Copy your Audible") || detail == "" {
		t.Fatalf("without a sign-in page: %q %q %q", title, body, detail)
	}
	page := &fakeScreen{}
	b.SetSignIn(func() screens.Screen { return page })
	if _, body, _, _ := b.emptyState(); !strings.Contains(body, "Press A to sign in") {
		t.Fatalf("body %q", body)
	}
	if hs := b.hints(); hs[0].Key != "A" || hs[0].Label != "Sign in" {
		t.Fatalf("hints %v", hs)
	}
	render(t, b, t0)
	if !b.Handle(press(input.A), t0) || len(nav.pushed) != 1 || nav.pushed[0] != screens.Screen(page) {
		t.Fatalf("A pushed %v", nav.pushed)
	}
	// Once signed in, the tab is the library's, and A downloads.
	b.cat = &fakeCatalogue{signedIn: true, status: audiobooks.OK, entries: []audiobooks.Entry{entry("B1", "Un libro", "")}}
	b.Handle(press(input.A), t0)
	if len(nav.pushed) != 1 {
		t.Fatal("A opened the sign-in page while signed in")
	}
}

type fakeScreen struct{}

func (*fakeScreen) Handle(input.Event, time.Time) bool { return false }
func (*fakeScreen) Draw(*ui.Context)                   {}
func (*fakeScreen) Animating(time.Time) bool           { return false }

// Right on a book opens its page, which does what A and Y do in the tab and
// scrolls a long description.
func TestBookPage(t *testing.T) {
	p, nav := &fakePlayer{}, &fakeNav{}
	long := strings.Repeat("Una storia lunga, con un caso difficile per l'ispettore. ", 60)
	e := entry("B0H764PGVH", "Firmato Picpus", "")
	e.Narrators, e.Series, e.Sequence, e.Percent, e.Summary = "Moro Silo", "Maigret", "7", 12, long
	cat := &fakeCatalogue{signedIn: true, status: audiobooks.OK, entries: []audiobooks.Entry{e}}
	b := NewBooks(nav, p, NewShelf(p), cat, "/data/audible/auth.json")
	b.SetIndex(bookIndex())
	if !b.Handle(press(input.Right), t0) || len(nav.pushed) != 1 {
		t.Fatalf("Right pushed %v", nav.pushed)
	}
	pg, ok := nav.pushed[0].(*BookPage)
	if !ok {
		t.Fatalf("Right opened %T", nav.pushed[0])
	}
	render(t, pg, t0)
	if pg.limit == 0 || !pg.Handle(press(input.Down), t0) || pg.scroll != 1 {
		t.Fatalf("a long description scrolls: limit %d, scroll %d", pg.limit, pg.scroll)
	}
	if !pg.Handle(press(input.Up), t0) || pg.Handle(press(input.Up), t0) {
		t.Fatal("Up scrolls back to the top and stops there")
	}
	pg.Handle(press(input.A), t0)
	if len(cat.downloads) != 1 || cat.downloads[0] != "B0H764PGVH" {
		t.Fatalf("A downloaded %v", cat.downloads)
	}
	if got := facts(e); !slices.Equal(got, []string{"Maigret, book 7", "Read by Moro Silo", "4 h 9 min", "12% listened"}) {
		t.Fatalf("facts %q", got)
	}
	if r := rowOf(e, ""); r.Subtitle != "Georges Simenon · Maigret, book 7 · 4 h 9 min · 12% listened" {
		t.Fatalf("row %q", r.Subtitle)
	}
	cat.entries[0].Path = "/m/Audiobooks/x [B0H764PGVH].m4b"
	render(t, pg, t0)
	cat.entries = nil // gone from the catalogue: the page still draws
	render(t, pg, t0)
	empty := NewBooks(nav, p, NewShelf(p), &fakeCatalogue{}, "")
	if empty.Handle(press(input.Right), t0) {
		t.Fatal("Right with no account should do nothing")
	}
}

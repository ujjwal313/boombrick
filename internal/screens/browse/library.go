package browse

import (
	"slices"
	"strings"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

const (
	albumsFilter = iota
	artistsFilter
	songsFilter
	genresFilter
	booksFilter
	filterCount
)

var filterNames = [filterCount]string{"Albums", "Artists", "Songs", "Genres", "Books"}

const listTop = contentTop + widgets.FiltersHeight

const emptyText = "Copy music to the Music folder on the SD card, or find something on YouTube Music."

// Library is the Library tab: albums, artists, songs or genres, chosen with
// L2 and R2. Genres shows only when some song is tagged with one.
type Library struct {
	nav       screens.Nav
	p         Player
	musicDir  string
	rescan    func()
	shelf     *Shelf // plays books; nil plays them from the start
	ix        *library.Index
	filter    int
	views     [filterCount]views.Grid
	counts    [filterCount]string    // the filters' counts
	rows      [filterCount][]listRow // the artists', songs' and genres' rows, made as they first show
	laidOut   bool
	measured  bool            // allCounts is known for this index
	allCounts bool            // every filter's count fits in the row
	reading   bool            // the first scan is running with nothing indexed yet
	state     widgets.Wrapped // the empty or reading state's description, wrapped
}

// listRow is what an artist's, a song's or a genre's row shows that takes
// making.
type listRow struct {
	art      widgets.ArtSpec
	subtitle string
	duration string // a song's length, or ""
}

// New creates the tab. It shows nothing but its chrome until SetIndex.
func New(nav screens.Nav, p Player, musicDir string, rescan func()) *Library {
	l := &Library{nav: nav, p: p, musicDir: musicDir, rescan: rescan}
	l.views[albumsFilter] = screens.AlbumGrid()
	l.views[booksFilter] = screens.AlbumGrid()
	for _, f := range []int{artistsFilter, songsFilter, genresFilter} {
		l.views[f] = views.Grid{Cols: 1, CellW: theme.ScreenW - 2*side, CellH: widgets.RowHeight, Pad: pad,
			ViewH: contentBot - listTop}
	}
	return l
}

// SetIndex shows ix, keeping each filter's focus on the same album, artist,
// song or genre when it is still there. Without genres, the genres filter
// gives way to the songs.
func (l *Library) SetIndex(ix *library.Index) {
	old := l.ix
	l.ix = ix
	l.counts = [filterCount]string{screens.Count(len(ix.Albums)), screens.Count(len(ix.Artists)),
		screens.Count(len(ix.Songs)), screens.Count(len(ix.Genres)), screens.Count(len(ix.Books))}
	l.measured = false
	l.rows[artistsFilter] = make([]listRow, len(ix.Artists))
	l.rows[songsFilter] = make([]listRow, len(ix.Songs))
	l.rows[genresFilter] = make([]listRow, len(ix.Genres))
	if !l.visible(l.filter) {
		l.filter = songsFilter
	}
	if old == nil {
		l.views[albumsFilter].SetCount(len(ix.Albums))
		l.views[artistsFilter].SetCount(len(ix.Artists))
		l.views[songsFilter].SetCount(len(ix.Songs))
		l.views[genresFilter].SetCount(len(ix.Genres))
		l.views[booksFilter].SetCount(len(ix.Books))
		return
	}
	g := &l.views[albumsFilter]
	g.SetCountFocus(len(ix.Albums), refocus(old.Albums, ix.Albums, g.Focus(),
		func(a *library.Album) [2]string { return [2]string{a.Artist, a.Title} }))
	g = &l.views[artistsFilter]
	g.SetCountFocus(len(ix.Artists), refocus(old.Artists, ix.Artists, g.Focus(),
		func(a *library.Artist) string { return a.Name }))
	g = &l.views[songsFilter]
	g.SetCountFocus(len(ix.Songs), refocus(old.Songs, ix.Songs, g.Focus(),
		func(s *library.Track) string { return s.Path }))
	g = &l.views[genresFilter]
	g.SetCountFocus(len(ix.Genres), refocus(old.Genres, ix.Genres, g.Focus(),
		func(g *library.Genre) string { return strings.ToLower(g.Name) }))
	g = &l.views[booksFilter]
	g.SetCountFocus(len(ix.Books), refocus(old.Books, ix.Books, g.Focus(),
		func(a *library.Album) [2]string { return [2]string{a.Artist, a.Title} }))
}

// visible reports whether filter f shows: the genres only when there are
// some.
func (l *Library) visible(f int) bool {
	return f != genresFilter || len(l.ix.Genres) > 0
}

// step is the filter after f in direction d (-1 or 1) among those that show,
// or -1 at either end.
func (l *Library) step(f, d int) int {
	for f += d; f >= 0 && f < filterCount; f += d {
		if l.visible(f) {
			return f
		}
	}
	return -1
}

// refocus finds the item that was at f in old within now, by key; without it
// focus keeps its position.
func refocus[T any, K comparable](old, now []T, f int, key func(T) K) int {
	if f < 0 || f >= len(old) {
		return f
	}
	k := key(old[f])
	if i := slices.IndexFunc(now, func(x T) bool { return key(x) == k }); i >= 0 {
		return i
	}
	return f
}

// SetShelf has books played from where they were left, through s.
func (l *Library) SetShelf(s *Shelf) { l.shelf = s }

// SetReading says the first scan is running and nothing is indexed yet;
// until SetIndex the tab says the music is being read.
func (l *Library) SetReading() { l.reading = true }

func (l *Library) empty() bool { return l.ix != nil && len(l.ix.Albums) == 0 && len(l.ix.Books) == 0 }

// Handle switches filters with L2/R2, moves with the D-pad, opens or plays
// with A and queues with Y. X shuffles an artist or a genre. The empty
// library rescans with Y.
func (l *Library) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release || l.ix == nil {
		return false
	}
	if l.empty() {
		if ev.Kind == input.Press && ev.Button == input.Y && l.rescan != nil {
			l.rescan()
			l.nav.Toast(screens.Scanning)
			return true
		}
		return false
	}
	g := &l.views[l.filter]
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.L2, input.R2:
			d := -1
			if ev.Button == input.R2 {
				d = 1
			}
			f := l.step(l.filter, d)
			if f < 0 {
				return false
			}
			l.filter = f
			return true
		case input.A:
			if g.Count() > 0 {
				l.open(g.Focus())
			}
			return true
		case input.Y:
			if g.Count() > 0 {
				l.p.Enqueue(l.tracksAt(g.Focus()))
				l.nav.Toast(screens.AddedToQueue)
			}
			return true
		case input.X:
			if l.filter != artistsFilter && l.filter != genresFilter || g.Count() == 0 {
				return false
			}
			shuffle(l.p, l.tracksAt(g.Focus()))
			return true
		}
	}
	return g.Move(ev.Button, now)
}

func (l *Library) open(i int) {
	switch l.filter {
	case albumsFilter:
		l.nav.Push(NewAlbumPage(l.nav, l.p, l.ix.Albums[i]))
	case artistsFilter:
		l.nav.Push(NewArtistPage(l.nav, l.p, l.ix.Artists[i]))
	case booksFilter:
		if l.shelf != nil {
			l.shelf.Play(l.ix.Books[i])
			break
		}
		l.p.SetShuffle(false)
		l.p.Play(screens.Tracks(l.ix.Books[i].Tracks), 0)
	case songsFilter:
		l.p.SetShuffle(false)
		l.p.Play(songTracks(l.ix.Songs), i)
	case genresFilter:
		l.nav.Push(NewGenrePage(l.nav, l.p, l.ix.Genres[i]))
	}
}

// tracksAt is what Y queues for item i of the current filter.
func (l *Library) tracksAt(i int) []player.Track {
	switch l.filter {
	case albumsFilter:
		return screens.Tracks(l.ix.Albums[i].Tracks)
	case artistsFilter:
		return artistTracks(l.ix.Artists[i])
	case genresFilter:
		return songTracks(l.ix.Genres[i].Songs)
	}
	if l.filter == booksFilter {
		return screens.Tracks(l.ix.Books[i].Tracks)
	}
	return []player.Track{screens.TrackOf(l.ix.Songs[i])}
}

// Animating reports whether the current view is moving.
func (l *Library) Animating(now time.Time) bool { return l.views[l.filter].Animating(now) }

// Draw renders the frame.
func (l *Library) Draw(u *ui.Context) {
	if !l.laidOut {
		l.laidOut = true
		screens.LayoutGrid(u, &l.views[albumsFilter], contentBot-listTop)
	}
	switch {
	case l.empty():
		widgets.DrawBackground(u, widgets.StateGround)
		widgets.EmptyState(u, &l.state, "Your library is empty", emptyText, l.musicDir)
	case l.ix == nil && l.reading:
		screens.DrawReading(u, &l.state, l.musicDir)
	default:
		widgets.DrawBackground(u, widgets.TabGround)
		if l.ix != nil {
			l.drawFilters(u)
			l.drawView(u)
		}
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, screens.LibraryTab)
	widgets.HintBar(u, l.hints())
}

// drawFilters draws L2, the filters with their counts, and R2. When the
// counts don't all fit, only the chosen filter shows its count.
func (l *Library) drawFilters(u *ui.Context) {
	var names, counts, one [filterCount]string
	n, active := 0, 0
	for f := range filterCount {
		if !l.visible(f) {
			continue
		}
		if f == l.filter {
			active = n
		}
		names[n], counts[n] = filterNames[f], l.counts[f]
		if f == l.filter {
			one[n] = l.counts[f]
		}
		n++
	}
	if !l.measured {
		l.measured = true
		l.allCounts = widgets.FiltersWidth(u, names[:n], counts[:n]) <= theme.ScreenW-2*side
	}
	shown := counts[:n]
	if !l.allCounts {
		shown = one[:n]
	}
	widgets.Filters(u, names[:n], shown, active, side, contentTop)
}

// row is item i's row in the artists, songs or genres, made the first time
// it shows rather than every frame.
func (l *Library) row(i int) *listRow {
	r := &l.rows[l.filter][i]
	if r.subtitle != "" {
		return r
	}
	switch l.filter {
	case artistsFilter:
		ar := l.ix.Artists[i]
		r.art, r.subtitle = screens.ArtistArt(ar), artistLine(len(ar.Albums), ar.Songs)
		return r
	case genresFilter:
		g := l.ix.Genres[i]
		r.art, r.subtitle = screens.GenreArt(g), screens.SongsAndTime(len(g.Songs), g.Duration)
		return r
	}
	s := l.ix.Songs[i]
	r.art, r.subtitle = screens.SongArt(s), s.Artist+" · "+s.Album
	if s.Duration > 0 {
		r.duration = screens.Clock(s.Duration)
	}
	return r
}

func (l *Library) drawView(u *ui.Context) {
	g := &l.views[l.filter]
	if l.filter == albumsFilter {
		drawAlbums(u, g, l.ix.Albums, listTop, func(i int) string { return l.ix.Albums[i].Artist })
		return
	}
	if l.filter == booksFilter {
		drawAlbums(u, g, l.ix.Books, listTop, func(i int) string { return l.ix.Books[i].Artist })
		return
	}
	playing := l.p.State().Track.Path
	u.PushClip(screens.GlowArea)
	g.Draw(u.Now, side, listTop, func(i int, x, y, k float32) {
		lr := l.row(i)
		r := widgets.Row{Art: &lr.art, Subtitle: lr.subtitle, Duration: lr.duration}
		switch l.filter {
		case artistsFilter:
			r.Title, r.Round = l.ix.Artists[i].Name, true
		case genresFilter:
			r.Title = l.ix.Genres[i].Name
		default:
			s := l.ix.Songs[i]
			r.Title, r.Playing = s.Title, s.Path == playing
		}
		box := gfx.Rect{X: x, Y: y, W: g.CellW, H: widgets.RowHeight}
		widgets.RowGlow(u, box, widgets.RowRadius, k)
		u.PushClip(screens.Below(listTop))
		widgets.ListRow(u, r, box, k)
		u.PopClip()
	})
	u.PopClip()
}

// The tab's hint bars: before the first index, for an empty library, and
// with albums, artists or genres, or songs shown.
var (
	tabHints    = []widgets.Hint{{Key: "L1/R1", Label: "Tabs"}}
	rescanHints = []widgets.Hint{{Key: "Y", Label: "Rescan SD card"}, {Key: "L1/R1", Label: "Tabs"}}
	openHints   = []widgets.Hint{{Key: "A", Label: "Open"}, {Key: "Y", Label: "Add to queue"},
		{Key: "L2/R2", Label: "Filter"}, {Key: "L1/R1", Label: "Tabs"}}
	shuffleHints = []widgets.Hint{{Key: "A", Label: "Open"}, {Key: "X", Label: "Shuffle"}, {Key: "Y", Label: "Add to queue"},
		{Key: "L2/R2", Label: "Filter"}, {Key: "L1/R1", Label: "Tabs"}}
	songHints = []widgets.Hint{{Key: "A", Label: "Play"}, {Key: "Y", Label: "Add to queue"},
		{Key: "L2/R2", Label: "Filter"}, {Key: "L1/R1", Label: "Tabs"}}
)

func (l *Library) hints() []widgets.Hint {
	switch {
	case l.ix == nil:
		return tabHints
	case l.empty():
		return rescanHints
	case l.filter == songsFilter, l.filter == booksFilter:
		return songHints
	case l.filter == artistsFilter, l.filter == genresFilter:
		return shuffleHints
	}
	return openHints
}

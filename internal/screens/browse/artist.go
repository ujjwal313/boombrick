package browse

import (
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// ArtistPage shows an album artist's albums, oldest first, in the grid.
type ArtistPage struct {
	nav    screens.Nav
	p      Player
	artist *library.Artist
	meta   string   // the heading's album and song counts
	subs   []string // the cards' second lines
	grid   views.Grid
	top    float32 // the grid's top, once the heading is measured
}

// NewArtistPage opens artist ar.
func NewArtistPage(nav screens.Nav, p Player, ar *library.Artist) *ArtistPage {
	pg := &ArtistPage{nav: nav, p: p, artist: ar, grid: screens.AlbumGrid(),
		meta: artistLine(len(ar.Albums), ar.Songs), subs: make([]string, len(ar.Albums))}
	for i, a := range ar.Albums {
		if a.Year > 0 {
			pg.subs[i] = strconv.Itoa(a.Year)
		} else {
			pg.subs[i] = screens.SongsAndTime(len(a.Tracks), 0)
		}
	}
	pg.grid.SetCount(len(ar.Albums))
	return pg
}

// Handle moves through the albums, opens one with A and queues one with Y.
// X shuffles all the artist's songs.
func (pg *ArtistPage) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	if ev.Button == input.X {
		if ev.Kind == input.Press {
			shuffle(pg.p, artistTracks(pg.artist))
		}
		return ev.Kind == input.Press
	}
	if ev.Kind == input.Press && pg.grid.Count() > 0 {
		a := pg.artist.Albums[pg.grid.Focus()]
		switch ev.Button {
		case input.A:
			pg.nav.Push(NewAlbumPage(pg.nav, pg.p, a))
			return true
		case input.Y:
			pg.p.Enqueue(screens.Tracks(a.Tracks))
			pg.nav.Toast(screens.AddedToQueue)
			return true
		}
	}
	return pg.grid.Move(ev.Button, now)
}

// Animating reports whether focus or the grid is moving.
func (pg *ArtistPage) Animating(now time.Time) bool { return pg.grid.Animating(now) }

// Draw renders the frame.
func (pg *ArtistPage) Draw(u *ui.Context) {
	if pg.top == 0 {
		pg.top = albumTop + u.LineHeight(text.Heading)
		screens.LayoutGrid(u, &pg.grid, contentBot-pg.top)
	}
	widgets.DrawBackground(u, widgets.TabGround)
	screens.Heading(u, pg.artist.Name, pg.meta, albumTop)
	drawAlbums(u, &pg.grid, pg.artist.Albums, pg.top, func(i int) string { return pg.subs[i] })
	widgets.StatusBar(u)
	widgets.Tabs(u, screens.LibraryTab)
	widgets.HintBar(u, pg.hints())
}

// drawAlbums draws the album grid from top down to the hint bar, with sub
// giving each card's second line. Cards are clipped at top, so scrolled ones
// don't show over what is above; the focused card's glow fades out over it.
func drawAlbums(u *ui.Context, g *views.Grid, albums []*library.Album, top float32, sub func(i int) string) {
	u.PushClip(screens.GlowArea)
	g.Draw(u.Now, gridX, top, func(i int, x, y, k float32) {
		a := albums[i]
		widgets.CardGlow(u, x, y, gridArt, k)
		u.PushClip(screens.Below(top))
		widgets.Card(u, widgets.CardItem{Title: a.Title, Subtitle: sub(i), Art: screens.AlbumArt(a)}, x, y, gridArt, k, true)
		u.PopClip()
	})
	u.PopClip()
}

var artistHints = []widgets.Hint{{Key: "A", Label: "Open"}, {Key: "X", Label: "Shuffle all"}, {Key: "Y", Label: "Add to queue"},
	{Key: "B", Label: "Back"}, {Key: "L1/R1", Label: "Tabs"}}

func (pg *ArtistPage) hints() []widgets.Hint { return artistHints }

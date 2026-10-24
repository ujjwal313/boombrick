package browse

import (
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// The album page's layout (screens/layout.go).
const (
	albumArt = screens.PageArt
	albumTop = screens.PageTop
	listX    = screens.PageListX
	pillGap  = screens.PillGap
)

const (
	playPill = iota
	shufflePill
	pillCount
)

// AlbumPage shows an album's tracks, or a genre's songs laid out the same
// way. Focus starts on the Play pill.
type AlbumPage struct {
	nav    screens.Nav
	p      Player
	art    widgets.ArtSpec
	title  string
	who    string // the artist and the year when known, or a genre's artist count
	length string // the songs and their time
	tracks []player.Track
	rows   []widgets.Row
	list   views.Grid
	onList bool
	pill   int
	pills  [pillCount]anim.Value // focus strength
}

// NewAlbumPage opens album a.
func NewAlbumPage(nav screens.Nav, p Player, a *library.Album) *AlbumPage {
	who := a.Artist
	if a.Year > 0 {
		who += " · " + strconv.Itoa(a.Year)
	}
	return newPage(nav, p, screens.AlbumArt(a), a.Title, who, screens.SongsAndTime(len(a.Tracks), a.Duration),
		screens.Tracks(a.Tracks))
}

// NewGenrePage opens genre g: its songs, each with its artist.
func NewGenrePage(nav screens.Nav, p Player, g *library.Genre) *AlbumPage {
	who := screens.Count(g.Artists) + " artists"
	if g.Artists == 1 {
		who = "1 artist"
	}
	pg := newPage(nav, p, screens.GenreArt(g), g.Name, who, screens.SongsAndTime(len(g.Songs), g.Duration),
		songTracks(g.Songs))
	for i, s := range g.Songs {
		pg.rows[i].Subtitle = s.Artist
	}
	return pg
}

// newPage makes a page of tracks: art, title and two lines of details in
// the left column, the tracks numbered on the right.
func newPage(nav screens.Nav, p Player, art widgets.ArtSpec, title, who, length string, tracks []player.Track) *AlbumPage {
	pg := &AlbumPage{nav: nav, p: p, art: art, title: title, who: who, length: length, tracks: tracks,
		rows: make([]widgets.Row, len(tracks))}
	pg.list = views.Grid{Cols: 1, CellW: theme.ScreenW - side - listX, CellH: widgets.RowHeight, Pad: pad,
		ViewH: contentBot - contentTop}
	pg.list.SetCount(len(tracks))
	pg.list.SetActive(false, time.Time{})
	pg.pills[playPill].Set(1)
	for i, t := range tracks {
		pg.rows[i] = widgets.Row{Index: strconv.Itoa(i + 1), Title: t.Title}
		if t.Duration > 0 {
			pg.rows[i].Duration = screens.Clock(t.Duration)
		}
	}
	return pg
}

// Handle moves between the pills and the tracks, plays with A and queues with Y.
func (pg *AlbumPage) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	switch ev.Button {
	case input.A:
		if ev.Kind == input.Press {
			pg.play()
		}
		return ev.Kind == input.Press
	case input.Y:
		if ev.Kind == input.Press {
			pg.enqueue()
		}
		return ev.Kind == input.Press
	}
	if pg.onList {
		if pg.list.Move(ev.Button, now) {
			return true
		}
		if ev.Button != input.Left {
			return false
		}
		pg.onList = false
		pg.list.SetActive(false, now)
		pg.pills[pg.pill].To(1, now, anim.Focus, anim.EaseOutCubic)
		return true
	}
	next := pg.pill
	switch ev.Button {
	case input.Left:
		next--
	case input.Right:
		next++
	default:
		return false
	}
	switch {
	case next == pillCount && pg.list.Count() > 0:
		pg.pills[pg.pill].To(0, now, anim.Focus, anim.EaseOutCubic)
		pg.onList = true
		pg.list.SetActive(true, now)
	case next >= 0 && next < pillCount:
		pg.pills[pg.pill].To(0, now, anim.Focus, anim.EaseOutCubic)
		pg.pill = next
		pg.pills[next].To(1, now, anim.Focus, anim.EaseOutCubic)
	default:
		return false
	}
	return true
}

func (pg *AlbumPage) play() {
	if len(pg.tracks) == 0 {
		return
	}
	switch {
	case pg.onList:
		pg.p.SetShuffle(false)
		pg.p.Play(pg.tracks, pg.list.Focus())
	case pg.pill == playPill:
		pg.p.SetShuffle(false)
		pg.p.Play(pg.tracks, 0)
	default:
		shuffle(pg.p, pg.tracks)
	}
}

func (pg *AlbumPage) enqueue() {
	if len(pg.tracks) == 0 {
		return
	}
	ts := pg.tracks
	if pg.onList {
		f := pg.list.Focus()
		ts = ts[f : f+1]
	}
	pg.p.Enqueue(ts)
	pg.nav.Toast(screens.AddedToQueue)
}

// Animating reports whether focus or the list is moving.
func (pg *AlbumPage) Animating(now time.Time) bool {
	return pg.list.Animating(now) || !pg.pills[playPill].Done(now) || !pg.pills[shufflePill].Done(now)
}

// Draw renders the frame.
func (pg *AlbumPage) Draw(u *ui.Context) {
	now := u.Now
	widgets.DrawBackground(u, widgets.TabGround)

	widgets.Art(u, pg.art, albumArt, widgets.CardRadius, gfx.Rect{X: side, Y: albumTop, W: albumArt, H: albumArt})
	y := float32(albumTop + albumArt + pad)
	u.Text(u.Fit(pg.title, text.Heading, albumArt), text.Heading, side, y, theme.Paper)
	y += u.LineHeight(text.Heading)
	u.Text(u.Fit(pg.who, text.Meta, albumArt), text.Meta, side, y, theme.Ink2)
	y += u.LineHeight(text.Meta)
	u.Text(u.Fit(pg.length, text.Meta, albumArt), text.Meta, side, y, theme.Ink3)
	py := float32(contentBot - pad - widgets.PillHeight)
	x := float32(side)
	play, shuffle := float32(pg.pills[playPill].At(now)), float32(pg.pills[shufflePill].At(now))
	widgets.PillGlow(u, "play", "Play", x, py, play)
	x += widgets.Pill(u, "play", "Play", x, py, play) + pillGap
	widgets.PillGlow(u, "shuffle", "", x, py, shuffle)
	widgets.Pill(u, "shuffle", "", x, py, shuffle)

	playing := pg.p.State().Track.Path
	u.PushClip(screens.GlowArea)
	pg.list.Draw(now, listX, contentTop, func(i int, x, y, k float32) {
		r := pg.rows[i]
		r.Playing = pg.tracks[i].Path == playing
		box := gfx.Rect{X: x, Y: y, W: pg.list.CellW, H: widgets.RowHeight}
		widgets.RowGlow(u, box, widgets.RowRadius, k)
		u.PushClip(screens.Below(contentTop))
		widgets.ListRow(u, r, box, k)
		u.PopClip()
	})
	u.PopClip()

	widgets.StatusBar(u)
	widgets.Tabs(u, screens.LibraryTab)
	widgets.HintBar(u, pg.hints())
}

// albumHints are the page's hint bars, by the pill A presses; on the tracks,
// A plays.
var albumHints = [pillCount][]widgets.Hint{
	playPill: {{Key: "A", Label: "Play"}, {Key: "Y", Label: "Add to queue"}, {Key: "B", Label: "Back"},
		{Key: "L1/R1", Label: "Tabs"}},
	shufflePill: {{Key: "A", Label: "Shuffle"}, {Key: "Y", Label: "Add to queue"}, {Key: "B", Label: "Back"},
		{Key: "L1/R1", Label: "Tabs"}},
}

func (pg *AlbumPage) hints() []widgets.Hint {
	if !pg.onList && pg.pill == shufflePill {
		return albumHints[shufflePill]
	}
	return albumHints[playPill]
}

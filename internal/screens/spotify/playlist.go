package spotify

import (
	"math/rand/v2"
	"strings"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// The page's pills: Play, and Shuffle as an icon alone, as on the Library's
// album page.
const (
	playPill = iota
	shufflePill
	pillCount
)

// copyCircle is the size of the mark on a song with a file.
const copyCircle = 40

// The page's hints, kept so that no frame builds them; with importing, X
// Import comes before Back.
var (
	trackHints = []widgets.Hint{{Key: "A", Label: "Play"}, {Key: "Y", Label: "Add to queue"}, backHint, tabsHint}
	pillHints  = [pillCount][]widgets.Hint{
		playPill:    {{Key: "A", Label: "Play"}, {Key: "Y", Label: "Add to queue"}, backHint, tabsHint},
		shufflePill: {{Key: "A", Label: "Shuffle"}, {Key: "Y", Label: "Add to queue"}, backHint, tabsHint},
	}
	importHint     = widgets.Hint{Key: "X", Label: "Import"}
	songImportHint = widgets.Hint{Key: "X", Label: "Import song"}
	waitHints      = []widgets.Hint{backHint, tabsHint}
	retryPageHints = []widgets.Hint{{Key: "X", Label: "Retry"}, backHint, tabsHint}
)

// PlaylistPage shows a playlist, or Liked Songs, as the Library shows an
// album: its picture, details and pills on the left, its songs on the right,
// marked when they have a file. Focus starts on the Play pill.
type PlaylistPage struct {
	e      *Env
	pl     spotify.Playlist
	who    string // the owner
	art    widgets.ArtSpec
	have   bool
	err    error // why there are no songs yet
	tracks []spotify.Track
	queue  []player.Track // tracks, to play
	rows   []widgets.Row
	arts   []widgets.ArtSpec
	count  string // "42 songs · 2 h 31 min", once the songs have come
	list   views.Grid
	onList bool
	pill   int
	pills  [pillCount]anim.Value
	msg    widgets.Wrapped
	hints  map[[2]int][]widgets.Hint // by pill (or -1 on the list) and whether importing
}

// NewPlaylistPage opens playlist pl.
func NewPlaylistPage(e *Env, pl spotify.Playlist) *PlaylistPage {
	return newPage(e, pl, owner(pl), art(pl.Name, pl.Cover))
}

// NewLikedPage opens Liked Songs.
func NewLikedPage(e *Env) *PlaylistPage {
	return newPage(e, liked(), "", screens.LikedArt)
}

func newPage(e *Env, pl spotify.Playlist, who string, a widgets.ArtSpec) *PlaylistPage {
	pg := &PlaylistPage{e: e, pl: pl, who: who, art: a, hints: map[[2]int][]widgets.Hint{},
		list: views.Grid{Cols: 1, CellW: theme.ScreenW - screens.Side - screens.PageListX, CellH: widgets.RowHeight,
			Pad: screens.Pad, ViewH: screens.ContentBot - screens.ContentTop}}
	pg.list.SetActive(false, time.Time{})
	pg.pills[playPill].Set(1)
	return pg
}

// load takes the songs once they have come.
func (pg *PlaylistPage) load() {
	if pg.have {
		return
	}
	ts, have, err := pg.e.Src.Tracks(pg.pl.URI)
	pg.err = err
	if !have {
		return
	}
	pg.have, pg.tracks, pg.queue = true, ts, Tracks(ts)
	pg.arts = make([]widgets.ArtSpec, len(ts))
	pg.rows = make([]widgets.Row, len(ts))
	var length time.Duration
	for i, t := range ts {
		pg.arts[i] = art(t.Album, t.Cover)
		sub := strings.Join(t.Artists, ", ")
		if t.Album != "" {
			sub += " · " + t.Album
		}
		pg.rows[i] = widgets.Row{Art: &pg.arts[i], Title: t.Title, Subtitle: sub}
		if pg.e.Import != nil {
			pg.rows[i].Trail = copyCircle
		}
		if d := pg.queue[i].Duration; d > 0 {
			pg.rows[i].Duration = screens.Clock(d)
			length += d
		}
	}
	pg.count = screens.SongsAndTime(len(ts), length)
	pg.list.SetCount(len(ts))
	pg.list.SetActive(pg.onList, time.Time{})
}

// Handle moves between the pills and the songs, plays with A and queues
// with Y. X imports the playlist from the pills and the song focused from
// the songs, or asks again for songs that couldn't be had.
func (pg *PlaylistPage) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	pg.load()
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.A:
			return pg.play()
		case input.Y:
			return pg.enqueue()
		case input.X:
			switch {
			case !pg.have && pg.err != nil:
				pg.e.Src.Retry()
			case pg.have && pg.e.CanImport() && pg.onList:
				pg.e.Nav.Push(NewSongReview(pg.e, pg.tracks[pg.list.Focus()]))
			case pg.have && pg.e.CanImport():
				pg.e.Nav.Push(NewReview(pg.e, []spotify.Playlist{pg.pl}, true))
			default:
				return false
			}
			return true
		}
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
	case next == pillCount && len(pg.queue) > 0:
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

func (pg *PlaylistPage) play() bool {
	if len(pg.queue) == 0 {
		return false
	}
	p := pg.e.Player
	switch {
	case pg.onList:
		p.SetShuffle(false)
		p.Play(pg.queue, pg.list.Focus())
	case pg.pill == playPill:
		p.SetShuffle(false)
		p.Play(pg.queue, 0)
	default:
		p.SetShuffle(true)
		p.Play(pg.queue, rand.IntN(len(pg.queue)))
	}
	return true
}

func (pg *PlaylistPage) enqueue() bool {
	if len(pg.queue) == 0 {
		return false
	}
	ts := pg.queue
	if pg.onList {
		f := pg.list.Focus()
		ts = ts[f : f+1 : f+1]
	}
	pg.e.Player.Enqueue(ts)
	pg.e.Nav.Toast(screens.AddedToQueue)
	return true
}

// Animating reports whether focus or the list is moving.
func (pg *PlaylistPage) Animating(now time.Time) bool {
	return pg.list.Animating(now) || !pg.pills[playPill].Done(now) || !pg.pills[shufflePill].Done(now)
}

// Draw renders the frame.
func (pg *PlaylistPage) Draw(u *ui.Context) {
	pg.load()
	widgets.DrawBackground(u, widgets.TabGround)
	widgets.Art(u, pg.art, screens.PageArt, widgets.CardRadius,
		gfx.Rect{X: screens.Side, Y: screens.PageTop, W: screens.PageArt, H: screens.PageArt})
	y := float32(screens.PageTop + screens.PageArt + screens.Pad)
	u.Text(u.Fit(pg.pl.Name, text.Heading, screens.PageArt), text.Heading, screens.Side, y, theme.Paper)
	y += u.LineHeight(text.Heading)
	if pg.who != "" {
		u.Text(u.Fit(pg.who, text.Meta, screens.PageArt), text.Meta, screens.Side, y, theme.Ink2)
		y += u.LineHeight(text.Meta)
	}
	if pg.count != "" {
		u.Text(u.Fit(pg.count, text.Meta, screens.PageArt), text.Meta, screens.Side, y, theme.Ink3)
	}
	var hints []widgets.Hint
	switch {
	case len(pg.queue) > 0:
		pg.drawPills(u)
		pg.drawSongs(u)
		hints = pg.songHints()
	default:
		s := "Loading…"
		hints = waitHints
		switch {
		case pg.have:
			s = "This playlist has no songs."
		case pg.err != nil:
			s, hints = failText(pg.err), retryPageHints
		}
		screens.Message(u, &pg.msg, s, gfx.Rect{X: screens.PageListX, Y: screens.ContentTop,
			W: theme.ScreenW - screens.Side - screens.PageListX, H: screens.ContentBot - screens.ContentTop})
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, tab)
	widgets.HintBar(u, hints)
}

// songHints are the hints with songs to show: a song's or a pill's, with X
// Import when an import may start.
func (pg *PlaylistPage) songHints() []widgets.Hint {
	k := [2]int{pg.pill, 0}
	if pg.onList {
		k[0] = -1
	}
	imp := pg.e.CanImport()
	if imp {
		k[1] = 1
	}
	if h, ok := pg.hints[k]; ok {
		return h
	}
	h, x := trackHints, songImportHint
	if !pg.onList {
		h, x = pillHints[pg.pill], importHint
	}
	if imp {
		h = append(append(h[:2:2], x), h[2:]...)
	}
	pg.hints[k] = h
	return h
}

// drawPills draws Play and the round Shuffle pill along the column's foot.
func (pg *PlaylistPage) drawPills(u *ui.Context) {
	y := float32(screens.ContentBot - screens.Pad - widgets.PillHeight)
	x := float32(screens.Side)
	k0, k1 := float32(pg.pills[playPill].At(u.Now)), float32(pg.pills[shufflePill].At(u.Now))
	widgets.PillGlow(u, "play", "Play", x, y, k0)
	x += widgets.Pill(u, "play", "Play", x, y, k0) + screens.PillGap
	widgets.PillGlow(u, "shuffle", "", x, y, k1)
	widgets.Pill(u, "shuffle", "", x, y, k1)
}

// drawSongs draws the songs: a song with a file is marked, and offline one
// without a file, which won't play, is faded.
func (pg *PlaylistPage) drawSongs(u *ui.Context) {
	playing := pg.e.Player.State().Track
	online := pg.e.online()
	u.PushClip(screens.GlowArea)
	pg.list.Draw(u.Now, screens.PageListX, screens.ContentTop, func(i int, x, y, k float32) {
		r := pg.rows[i]
		id := pg.queue[i].SpotifyID
		r.Playing = playing.Streamed() && playing.SpotifyID == id
		box := gfx.Rect{X: x, Y: y, W: pg.list.CellW, H: widgets.RowHeight}
		have := pg.e.has(id)
		alpha := u.Alpha
		if !online && !have {
			u.Alpha *= 0.45
		}
		widgets.RowGlow(u, box, widgets.RowRadius, k)
		u.PushClip(screens.Below(screens.ContentTop))
		widgets.ListRow(u, r, box, k)
		if have {
			tb := widgets.TrailBox(box, copyCircle)
			widgets.DownloadCircle(u, media.Saved, 1, copyCircle, tb.X, box.Y+(box.H-copyCircle)/2)
		}
		u.PopClip()
		u.Alpha = alpha
	})
	u.PopClip()
}

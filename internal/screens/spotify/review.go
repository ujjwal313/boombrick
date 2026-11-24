package spotify

import (
	"errors"
	"image/color"
	"strconv"
	"strings"
	"time"

	"github.com/ujjwal313/boombrick/internal/importer"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// The review's rows: a status circle, the Spotify song, and its match.
const (
	statusCircle = 40
	spotifyW     = 300
)

var (
	tallyText  = text.Style{Face: text.Figtree, Size: 24, Tabular: true}
	matchTitle = text.Style{Face: text.Figtree, Size: 24}
	detailText = text.Style{Face: text.Figtree, Size: 20, Tabular: true}
)

// Review shows an import's songs as they are matched: the sure, the unsure
// to decide on, and those not found. With several playlists chosen it shows
// one at a time.
type Review struct {
	e       *Env
	pls     []spotify.Playlist
	first   bool // the first of the import's pages, which ends its leaving
	cur     int
	started map[string]bool            // the playlists imported from this page
	songs   map[string][]spotify.Track // an import of songs rather than a playlist, by its URI
	job     importer.Job               // the shown import, its songs changed as they change
	have    bool
	version uint64 // the importer's, when job was last brought up to date
	list    views.Grid
	rows    []reviewRow
	tally   [importer.Skipped + 1]int // the songs by state
	counts  string
	msg     widgets.Wrapped
	other   *others // the Other matches list, while open
}

// reviewRow is a song's row, written when its import changes.
type reviewRow struct {
	title, artist, match, detail string
	icon                         string
	col                          color.NRGBA
}

// NewReview imports pls and shows how their songs are matched. first says it
// is the import's first page, as when started from a playlist's page.
func NewReview(e *Env, pls []spotify.Playlist, first bool) *Review {
	return &Review{e: e, pls: pls, first: first, started: map[string]bool{},
		list: views.Grid{Cols: 1, CellW: theme.ScreenW - 2*screens.Side, CellH: widgets.RowHeight, Pad: screens.Pad}}
}

// NewSongReview imports song t alone, started from a playlist's page, as an
// import of its own under the song's URI: the playlist's import is left as it
// is.
func NewSongReview(e *Env, t spotify.Track) *Review {
	pl := spotify.Playlist{URI: "spotify:track:" + t.ID, Name: t.Title, Cover: t.Cover}
	r := NewReview(e, []spotify.Playlist{pl}, true)
	r.songs = map[string][]spotify.Track{pl.URI: {t}}
	return r
}

// tracks is import uri's songs: its own, or the playlist's.
func (r *Review) tracks(uri string) ([]spotify.Track, bool, error) {
	if ts, ok := r.songs[uri]; ok {
		return ts, true, nil
	}
	return r.e.Src.Tracks(uri)
}

// Claims keeps Start, which goes on to the downloads.
func (r *Review) Claims(b input.Button) bool { return b == input.Start }

// refresh imports the playlists whose songs have come, and brings the shown
// one's import up to date when it has changed: only the rows of the songs
// that changed are made again.
func (r *Review) refresh() {
	for _, p := range r.pls {
		if r.started[p.URI] {
			continue
		}
		if ts, ok, _ := r.tracks(p.URI); ok {
			r.e.Import.Import(p.URI, p.Name, ts)
			r.started[p.URI] = true
		}
	}
	v := r.e.Import.Version()
	if r.have && v == r.version {
		return
	}
	uri := r.pls[r.cur].URI
	since := r.version
	if !r.have {
		since = 0
	}
	c, ok := r.e.Import.Changes(uri, since)
	if !ok || !r.started[uri] {
		r.have, r.version = false, v
		return
	}
	r.version = c.Version
	switch {
	case c.All:
		r.job = importer.Job{URI: uri, Items: c.Items}
		r.rows = make([]reviewRow, len(c.Items))
		r.tally = [len(r.tally)]int{}
		for i, it := range c.Items {
			r.rows[i] = rowOf(it)
			r.count(it.State, 1)
		}
		r.list.SetCountFocus(len(r.rows), min(r.list.Focus(), max(len(r.rows)-1, 0)))
	case len(c.At) > 0:
		for k, i := range c.At {
			r.count(r.job.Items[i].State, -1)
			r.job.Items[i] = c.Items[k]
			r.rows[i] = rowOf(c.Items[k])
			r.count(c.Items[k].State, 1)
		}
	default:
		return // nothing of this import changed
	}
	r.have = true
	n := r.tally
	sure := n[importer.Matched] + n[importer.Accepted] + n[importer.Have]
	r.counts = strconv.Itoa(sure) + " matched · " + strconv.Itoa(n[importer.Unsure]) + " unsure · " +
		strconv.Itoa(n[importer.NotFound]) + " not found"
	if w := n[importer.Waiting]; w > 0 {
		all := len(r.job.Items)
		r.counts += " · Matching " + strconv.Itoa(all-w) + " of " + strconv.Itoa(all)
	}
}

// count adds d to the songs counted in state st.
func (r *Review) count(st importer.State, d int) {
	if int(st) < len(r.tally) {
		r.tally[st] += d
	}
}

// rowOf is the review's row for song it.
func rowOf(it importer.Item) reviewRow {
	r := reviewRow{title: it.Track.Title, artist: strings.Join(it.Track.Artists, ", ")}
	var best *importer.Candidate
	if len(it.Candidates) > 0 {
		best = &it.Candidates[min(it.Chosen, len(it.Candidates)-1)]
		r.match, r.detail = best.Song.Title, matchDetail(it.Track, best.Song)
	}
	switch it.State {
	case importer.Waiting:
		r.icon, r.col, r.match, r.detail = "clock", theme.Ink3, "Matching…", ""
	case importer.Have:
		r.icon, r.col, r.match, r.detail = "check", theme.Success, "Already downloaded", ""
	case importer.Matched, importer.Accepted:
		r.icon, r.col = "check", theme.Success
	case importer.Unsure:
		r.icon, r.col = "help", theme.GoldSoft
	case importer.NotFound:
		r.icon, r.col, r.match, r.detail = "x", theme.Danger, "Not found on YouTube Music", ""
	case importer.Skipped:
		r.icon, r.col, r.detail = "x", theme.Ink3, "Skipped"
	}
	return r
}

// matchDetail says a match's length, and how it differs from the Spotify
// song's: "4:08 · exact length", "6:12 · 1:42 longer than Spotify".
func matchDetail(t spotify.Track, s ytmusic.Song) string {
	d := screens.Clock(s.Duration)
	diff := s.Duration - time.Duration(t.Duration)*time.Millisecond
	switch {
	case t.Duration <= 0 || s.Duration <= 0:
		return d
	case diff.Abs() <= 2*time.Second:
		return d + " · exact length"
	case diff > 0:
		return d + " · " + screens.Clock(diff) + " longer than Spotify"
	}
	return d + " · " + screens.Clock(-diff) + " shorter than Spotify"
}

// Handle decides on the focused song: A uses its match, X shows the others,
// Y skips it. L2 and R2 move between the playlists, and Start goes on.
func (r *Review) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release || r.e.leaving {
		return false
	}
	if r.other != nil {
		return r.other.handle(r, ev, now)
	}
	r.refresh()
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.L2, input.R2:
			next := r.cur - 1
			if ev.Button == input.R2 {
				next = r.cur + 1
			}
			if next < 0 || next >= len(r.pls) {
				return false
			}
			r.cur, r.have = next, false
			r.list.SetCountFocus(0, 0)
			return true
		case input.Start:
			r.e.Nav.Push(NewProgress(r.e, r.pls))
			return true
		}
		if !r.have || len(r.job.Items) == 0 {
			return false
		}
		it := r.job.Items[r.list.Focus()]
		switch ev.Button {
		case input.A:
			if it.State != importer.Unsure {
				return false
			}
			r.e.Import.Use(r.job.URI, it.Track.ID, 0)
			return true
		case input.X:
			if len(it.Candidates) == 0 {
				return false
			}
			r.other = newOthers(it, now)
			return true
		case input.Y:
			if it.State == importer.Skipped || it.State == importer.Have || it.State == importer.Waiting {
				return false
			}
			r.e.Import.Skip(r.job.URI, it.Track.ID)
			return true
		}
	}
	return r.list.Move(ev.Button, now)
}

// Back closes the Other matches list first.
func (r *Review) Back() bool {
	if r.other != nil {
		r.other = nil
		return true
	}
	return false
}

// Animating reports whether the list or the Other matches list is moving.
func (r *Review) Animating(now time.Time) bool {
	return r.list.Animating(now) || r.other != nil && r.other.animating(now)
}

// Draw renders the frame; once the import's pages are closing, it closes.
func (r *Review) Draw(u *ui.Context) {
	if r.e.leaving {
		if r.first {
			r.e.leaving = false
		}
		r.e.Nav.Pop()
	}
	r.refresh()
	widgets.DrawBackground(u, widgets.FlatGround)
	widgets.StatusBar(u)
	p := r.pls[r.cur]
	top := importHeader(u, p.Name, 1)
	switch err := r.e.Import.Err(); {
	case err != nil:
		u.Text(u.Fit(changedText(err), tallyText, theme.ScreenW-2*screens.Side), tallyText, screens.Side, top, theme.Danger)
	case r.have:
		u.Text(r.counts, tallyText, screens.Side, top, theme.Ink2)
	}
	top += u.LineHeight(tallyText) + 8
	area := gfx.Rect{X: screens.Side, Y: top, W: theme.ScreenW - 2*screens.Side, H: screens.ContentBot - top}
	switch {
	case !r.have:
		s := "Getting the songs…"
		if _, ok, err := r.tracks(p.URI); !ok && err != nil {
			s = failText(err)
		}
		screens.Message(u, &r.msg, s, area)
	case len(r.rows) == 0:
		screens.Message(u, &r.msg, "This playlist has no songs.", area)
	default:
		r.drawRows(u, top)
	}
	if r.other != nil {
		r.other.draw(u)
		widgets.HintBar(u, othersHints)
		return
	}
	widgets.HintBar(u, r.hints())
}

// changedText says matching has stopped.
func changedText(err error) string {
	if errors.Is(err, ytmusic.ErrChanged) {
		return "YouTube Music isn’t answering as Boombrick expects. An update should fix it."
	}
	return "Matching has stopped."
}

func (r *Review) drawRows(u *ui.Context, top float32) {
	r.list.ViewH = screens.ContentBot - top
	u.PushClip(gfx.Rect{X: 0, Y: top - screens.Pad, W: theme.ScreenW, H: screens.ContentBot - top + screens.Pad})
	r.list.Draw(u.Now, screens.Side, top, func(i int, x, y, k float32) {
		box := gfx.Rect{X: x, Y: y, W: r.list.CellW, H: widgets.RowHeight}
		widgets.RowGlow(u, box, widgets.RowRadius, k)
		widgets.RowFocus(u, box, widgets.RowRadius, k)
		row := r.rows[i]
		cy := y + (widgets.RowHeight-statusCircle)/2
		u.Shape(raster.Shape{W: statusCircle, H: statusCircle, Radius: statusCircle / 2, Kind: raster.Ring, RingOffset: -2, RingWidth: 2},
			gfx.Rect{X: x + 12, Y: cy, W: statusCircle, H: statusCircle}, row.col)
		u.Icon(row.icon, 22, 2.5, false, x+12+(statusCircle-22)/2, cy+(statusCircle-22)/2, row.col)
		tx := x + 12 + statusCircle + 16
		u.Text(u.Fit(row.title, matchTitle, spotifyW), matchTitle, tx, y+10, theme.Paper)
		u.Text(u.Fit(row.artist, detailText, spotifyW), detailText, tx, y+10+u.LineHeight(matchTitle), theme.Ink2)
		mx := tx + spotifyW + 20
		u.Icon("right", 20, 2, false, mx-20, y+(widgets.RowHeight-20)/2, theme.Ink4)
		mw := x + box.W - 16 - mx
		u.Text(u.Fit(row.match, matchTitle, mw), matchTitle, mx, y+10, row.matchCol())
		u.Text(u.Fit(row.detail, detailText, mw), detailText, mx, y+10+u.LineHeight(matchTitle), theme.Ink3)
	})
	u.PopClip()
}

// matchCol is the match title's colour: the state's, for a song decided on
// or not found.
func (row reviewRow) matchCol() color.NRGBA {
	if row.col == theme.Success {
		return theme.Paper
	}
	return row.col
}

var (
	unsureHints  = []widgets.Hint{{Key: "A", Label: "Use this"}, {Key: "X", Label: "Other matches"}, {Key: "Y", Label: "Skip"}, {Key: "Start", Label: "Download"}}
	matchedHints = []widgets.Hint{{Key: "X", Label: "Other matches"}, {Key: "Y", Label: "Skip"}, {Key: "Start", Label: "Download"}}
	plainHints   = []widgets.Hint{{Key: "Start", Label: "Download"}, {Key: "B", Label: "Back"}}
	othersHints  = []widgets.Hint{{Key: "A", Label: "Use this"}, {Key: "B", Label: "Back"}}
)

func (r *Review) hints() []widgets.Hint {
	if !r.have || len(r.job.Items) == 0 {
		return plainHints
	}
	it := r.job.Items[r.list.Focus()]
	switch {
	case it.State == importer.Unsure:
		return unsureHints
	case len(it.Candidates) > 0 && it.State != importer.Skipped:
		return matchedHints
	}
	return plainHints
}

// others is the list of a song's candidates, to choose another.
type others struct {
	it    importer.Item
	focus int
	shown anim.Value
}

func newOthers(it importer.Item, now time.Time) *others {
	o := &others{it: it}
	o.shown.To(1, now, anim.Dialog, anim.EaseOutCubic)
	return o
}

func (o *others) handle(r *Review, ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	switch ev.Button {
	case input.Up:
		if o.focus == 0 {
			return false
		}
		o.focus--
	case input.Down:
		if o.focus+1 >= len(o.it.Candidates) {
			return false
		}
		o.focus++
	case input.A:
		if ev.Kind == input.Press {
			r.e.Import.Use(r.job.URI, o.it.Track.ID, o.focus)
			r.other = nil
		}
	default:
		return false
	}
	return true
}

func (o *others) animating(now time.Time) bool { return !o.shown.Done(now) }

func (o *others) draw(u *ui.Context) {
	const rowH, pad = 72, 24
	h := pad + u.LineHeight(text.Heading) + 8 + float32(len(o.it.Candidates))*rowH + pad
	p := widgets.BeginPanel(u, h, float32(o.shown.At(u.Now)))
	defer p.End(u)
	x, y := p.Box.X+pad, p.Box.Y+pad
	u.Text("Other matches", text.Heading, x, y, theme.Paper)
	y += u.LineHeight(text.Heading) + 8
	w := p.Box.W - 2*pad
	for i, c := range o.it.Candidates {
		box := gfx.Rect{X: x - 8, Y: y, W: w + 16, H: rowH - 6}
		if i == o.focus {
			widgets.RowFocus(u, box, widgets.RowRadius, 1)
		}
		u.Text(u.Fit(c.Song.Artist+" – "+c.Song.Title, matchTitle, w), matchTitle, x, y+6, theme.Paper)
		d := matchDetail(o.it.Track, c.Song)
		if c.Song.Album != "" {
			d = c.Song.Album + " · " + d
		}
		u.Text(u.Fit(d, detailText, w), detailText, x, y+6+u.LineHeight(matchTitle), theme.Ink3)
		y += rowH
	}
}

package spotify

import (
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/importer"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// The downloading page's summary: the playlist's art, a big counter and a
// bar; then its songs' rows.
const (
	summaryArt  = 120
	barH        = 10
	stateSize   = 40
	progressRow = 72
)

var (
	counterText = text.Display.Tnum()
	ofText      = text.Style{Face: text.Figtree, Size: 26}
	lineText    = text.Style{Face: text.Figtree, Size: 22, Tabular: true}
	statusText  = text.Style{Face: text.Figtree, Size: 20, Tabular: true}
)

var (
	progressHints = []widgets.Hint{{Key: "B", Label: "Run in background"}, {Key: "Y", Label: "Pause"}, {Key: "X", Label: "Cancel import"}}
	pausedHints   = []widgets.Hint{{Key: "B", Label: "Run in background"}, {Key: "Y", Label: "Resume"}, {Key: "X", Label: "Cancel import"}}
)

// Progress follows the downloads of an import's songs, those matched and
// accepted. Leaving it leaves the import's pages, which run on in the
// background.
type Progress struct {
	e       *Env
	pls     []spotify.Playlist
	version uint64     // the importer's, when the songs were last brought up to date
	jobs    []followed // by playlist
	rows    []place    // the songs downloading, in the playlists' order
	list    views.Grid
	art     widgets.ArtSpec
	msg     widgets.Wrapped
	// The songs saved, and how many songs' worth are downloaded, counted
	// again when the queue or the songs change.
	queue   uint64 // the queue's version when they were counted
	recount bool
	saved   int
	done    float32
	counted [2]int // saved and all, as last drawn
	counter string
	of      string
}

// followed is an import as the page follows it: for each of its songs, the
// download chosen, with no VideoID unless the song is matched or accepted,
// and its row's "Title — Artist".
type followed struct {
	version uint64 // the importer's, when they were last brought up to date
	songs   []ytmusic.Song
	labels  []string
}

// place is a row's song: the playlist's, and where it is in its import.
type place struct{ job, song int }

// NewProgress follows the downloads of pls' imports.
func NewProgress(e *Env, pls []spotify.Playlist) *Progress {
	p := &Progress{e: e, pls: pls, jobs: make([]followed, len(pls)), recount: true,
		list: views.Grid{Cols: 1, CellW: theme.ScreenW - 2*screens.Side, CellH: progressRow, Pad: screens.Pad}}
	if len(pls) > 0 {
		p.art = art(pls[0].Name, pls[0].Cover)
		if pls[0].URI == spotify.LikedURI {
			p.art = screens.LikedArt
		}
	}
	p.version = ^uint64(0)
	return p
}

// ShowsDownloads says the page follows the download queue.
func (p *Progress) ShowsDownloads() bool { return true }

// refresh brings the songs followed up to date when an import has changed,
// making again only what changed.
func (p *Progress) refresh() {
	v := p.e.Import.Version()
	if v == p.version {
		return
	}
	p.version = v
	changed := false
	for k, pl := range p.pls {
		f := &p.jobs[k]
		c, ok := p.e.Import.Changes(pl.URI, f.version)
		if !ok {
			continue
		}
		f.version = c.Version
		if c.All {
			f.songs, f.labels = make([]ytmusic.Song, len(c.Items)), make([]string, len(c.Items))
			for i, it := range c.Items {
				f.set(i, it)
			}
			changed = true
		}
		for n, i := range c.At {
			changed = f.set(i, c.Items[n]) || changed
		}
	}
	if !changed {
		return
	}
	p.rows = p.rows[:0]
	for k, f := range p.jobs {
		for i, s := range f.songs {
			if s.VideoID != "" {
				p.rows = append(p.rows, place{k, i})
			}
		}
	}
	p.list.SetCountFocus(len(p.rows), min(p.list.Focus(), max(len(p.rows)-1, 0)))
	p.recount = true
}

// set takes song i of the import as it now stands, and reports whether its
// download changed.
func (f *followed) set(i int, it importer.Item) bool {
	var s ytmusic.Song
	if it.State == importer.Matched || it.State == importer.Accepted {
		s = it.Candidates[it.Chosen].Song
	}
	if s == f.songs[i] {
		return false
	}
	f.songs[i], f.labels[i] = s, ""
	if s.VideoID != "" {
		f.labels[i] = it.Track.Title + " — " + s.Artist
	}
	return true
}

// count counts the songs saved, and how many songs' worth are downloaded,
// when the queue or the songs have changed since they were last counted.
func (p *Progress) count() {
	v := p.e.Queue.Version()
	if v == p.queue && !p.recount {
		return
	}
	p.queue, p.recount = v, false
	p.saved, p.done = 0, 0
	for _, r := range p.rows {
		st, f := p.e.Queue.State(p.jobs[r.job].songs[r.song].VideoID)
		switch st {
		case media.Saved:
			p.saved++
			p.done++
		case media.Downloading:
			p.done += f
		}
	}
}

// Handle pauses or resumes the download queue with Y, and cancels the
// import with X.
func (p *Progress) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind != input.Press {
		return p.list.Move(ev.Button, now)
	}
	switch ev.Button {
	case input.Y:
		p.e.Queue.SetPaused(!p.e.Queue.Paused())
		return true
	case input.X:
		for _, pl := range p.pls {
			p.e.Import.Cancel(pl.URI)
		}
		p.e.Nav.Toast("Import cancelled")
		p.e.leaving = true
		p.e.Nav.Pop()
		return true
	}
	return p.list.Move(ev.Button, now)
}

// Back leaves the import's pages: B pops this one, and the others pop
// themselves.
func (p *Progress) Back() bool {
	p.e.leaving = true
	return false
}

// Animating reports whether the list is moving.
func (p *Progress) Animating(now time.Time) bool { return p.list.Animating(now) }

// Draw renders the frame.
func (p *Progress) Draw(u *ui.Context) {
	p.refresh()
	p.count()
	widgets.DrawBackground(u, widgets.FlatGround)
	widgets.StatusBar(u)
	title := "Downloading"
	if len(p.pls) == 1 {
		title = p.pls[0].Name
	}
	top := importHeader(u, title, 2)
	saved, done, all := p.saved, p.done, len(p.rows)
	if c := [2]int{saved, all}; c != p.counted || p.counter == "" {
		p.counted, p.counter, p.of = c, strconv.Itoa(saved), "of "+songs(all)
	}
	x := float32(screens.Side)
	widgets.Art(u, p.art, summaryArt, widgets.CardRadius, gfx.Rect{X: x, Y: top, W: summaryArt, H: summaryArt})
	sx := x + summaryArt + 24
	u.Text(p.counter, counterText, sx, top-8, theme.Paper)
	u.Text(p.of, ofText, sx+u.TextWidth(p.counter, counterText)+12, top+u.Ascent(counterText)-u.Ascent(ofText)-8, theme.Ink2)
	by := top + u.LineHeight(counterText) - 4
	bw := theme.ScreenW - screens.Side - sx
	bar := raster.Shape{W: int(bw), H: barH, Radius: barH / 2, Kind: raster.Fill}
	u.Shape(bar, gfx.Rect{X: sx, Y: by, W: bw, H: barH}, theme.PaperA(0.18))
	if all > 0 && done > 0 {
		u.PushClip(gfx.Rect{X: sx, Y: by, W: bw * done / float32(all), H: barH})
		u.Shape(bar, gfx.Rect{X: sx, Y: by, W: bw, H: barH}, u.Accent.Color)
		u.PopClip()
	}
	line := "Downloading"
	switch {
	case p.e.Queue.Paused():
		line = "Paused"
	case saved == all && all > 0:
		line = "Done"
	case p.e.Queue.Note() != "":
		line = p.e.Queue.Note()
	}
	u.Text(line, lineText, sx, by+barH+12, theme.Ink2)
	top += summaryArt + 20
	if all == 0 {
		screens.Message(u, &p.msg, "No songs to download yet.", gfx.Rect{X: screens.Side, Y: top, W: theme.ScreenW - 2*screens.Side,
			H: screens.ContentBot - top})
	} else {
		p.drawRows(u, top)
	}
	hints := progressHints
	if p.e.Queue.Paused() {
		hints = pausedHints
	}
	widgets.HintBar(u, hints)
}

func (p *Progress) drawRows(u *ui.Context, top float32) {
	p.list.ViewH = screens.ContentBot - top
	u.PushClip(gfx.Rect{X: 0, Y: top, W: theme.ScreenW, H: screens.ContentBot - top})
	p.list.Draw(u.Now, screens.Side, top, func(i int, x, y, k float32) {
		r := p.rows[i]
		f := &p.jobs[r.job]
		st, done := p.e.Queue.State(f.songs[r.song].VideoID)
		widgets.DownloadCircle(u, st, done, stateSize, x, y+(progressRow-stateSize)/2)
		status := downloadText(st, done)
		sw := u.TextWidth(status, statusText)
		u.Text(status, statusText, x+p.list.CellW-sw, u.CenterY(statusText, y, progressRow), theme.Ink2)
		tx := x + stateSize + 16
		u.Text(u.Fit(f.labels[r.song], matchTitle, p.list.CellW-stateSize-16-sw-16), matchTitle, tx, u.CenterY(matchTitle, y, progressRow), theme.Paper)
		u.Fill(gfx.Rect{X: x, Y: y + progressRow - 1, W: p.list.CellW, H: 1}, theme.PaperA(0.08))
	})
	u.PopClip()
}

// percents are "0 %" to "100 %", made on first use so that drawing a
// download's progress doesn't build a string each frame.
var percents [101]string

func percent(f float32) string {
	if percents[0] == "" {
		for i := range percents {
			percents[i] = strconv.Itoa(i) + " %"
		}
	}
	return percents[min(max(int(f*100+0.5), 0), 100)]
}

// downloadText is a song's download as its row says it.
func downloadText(st media.DownloadState, f float32) string {
	switch st {
	case media.Downloading:
		return percent(f)
	case media.Saved:
		return "Saved"
	case media.Failed:
		return "Failed"
	}
	return "Queued"
}

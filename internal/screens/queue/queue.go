// Package queue is the Queue screen: what's playing, what follows, and
// moving or removing what follows.
package queue

import (
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/views"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Player is what the Queue needs from the player.
type Player interface {
	State() player.State
	Queue(dst []player.Track) ([]player.Track, int)
	PlayIndex(i int)
	Remove(i int)
	Move(from, to int)
}

// Layout: a full screen, padding 40.
const (
	side    = 40
	panelH  = 88 // 64 px art and 12 above and below
	panelAr = 64
)

var (
	nowLabel = text.Style{Face: text.FigtreeSemiBold, Size: 18}
	timeText = text.Style{Face: text.Figtree, Size: 22, Tabular: true}
	upNext   = text.Style{Face: text.OutfitSemiBold, Size: 30}
)

// Queue shows the play queue. Up next lists the tracks after the current one;
// X lifts one to move it with the D-pad, A drops it and B puts it back.
type Queue struct {
	p       Player
	tracks  []player.Track // the queue in play order, as last read
	pos     int            // the current track in tracks
	version uint64
	index   int
	stale   bool         // tracks must be read again
	st      player.State // the player's state at the start of this frame
	header  string
	list    views.Grid // up next: tracks[pos+1:]
	rows    []upRow    // up next's rows, made as they first show
	moving  bool
	from    int          // where the lifted row was, in up next
	moveAt  player.State // the player's state when the row was lifted
	moveMsg string
	listTop float32
	// The panel's lines, made again only when what they show changes.
	clock   string
	clockAt [2]time.Duration // the seconds clock shows
	line    string
	lineOf  [2]string // the title and artist line shows, or a station's song and name
}

// upRow is what a row of up next shows that takes making.
type upRow struct {
	index    string
	art      widgets.ArtSpec
	duration string
}

// New creates the screen.
func New(p Player) *Queue {
	return &Queue{p: p, stale: true, list: views.Grid{Cols: 1, CellW: theme.ScreenW - 2*side, CellH: widgets.RowHeight, Pad: 14}}
}

// refresh reads the player's state and, when the queue or the current track
// changed, the queue again. While a row is lifted the screen keeps its own
// order.
func (q *Queue) refresh(now time.Time) {
	q.st = q.p.State()
	if q.moving {
		return
	}
	if !q.stale && q.st.Version == q.version && q.st.Index == q.index {
		return
	}
	// When only the current track moved on or back, up next shifts by as
	// much; focus stays on the same track.
	shift := 0
	if q.st.Version == q.version {
		shift = q.st.Index - q.index
	}
	q.tracks, q.pos = q.p.Queue(q.tracks[:0])
	q.version, q.index, q.stale = q.st.Version, q.st.Index, false
	q.list.SetCount(max(len(q.tracks)-q.pos-1, 0))
	q.rows = slices.Grow(q.rows[:0], q.list.Count())[:q.list.Count()]
	clear(q.rows)
	if shift != 0 {
		q.list.SetFocus(min(max(q.list.Focus()-shift, 0), q.list.Count()-1), now)
	}
	q.header = ""
	if len(q.tracks) > 0 {
		var total time.Duration
		for _, t := range q.tracks {
			total += t.Duration
		}
		q.header = trackCount(len(q.tracks), total)
	}
}

// trackCount describes n tracks lasting d: "12 tracks · 46 min".
func trackCount(n int, d time.Duration) string {
	s := screens.Count(n) + " tracks"
	if n == 1 {
		s = "1 track"
	}
	m := int(d / time.Minute)
	switch {
	case d <= 0:
		return s
	case m < 60:
		return fmt.Sprintf("%s · %d min", s, max(m, 1))
	}
	return fmt.Sprintf("%s · %d h %d min", s, m/60, m%60)
}

// upNext is the tracks after the current one.
func (q *Queue) upNext() []player.Track {
	if q.pos+1 > len(q.tracks) {
		return nil
	}
	return q.tracks[q.pos+1:]
}

// Handle applies a button.
func (q *Queue) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	q.refresh(now)
	n, f := q.list.Count(), q.list.Focus()
	if q.moving {
		switch ev.Button {
		case input.Up, input.Down:
			to := f - 1
			if ev.Button == input.Down {
				to = f + 1
			}
			if to < 0 || to >= n {
				return false
			}
			up := q.upNext()
			up[f], up[to] = up[to], up[f]
			q.rows[f], q.rows[to] = upRow{}, upRow{}
			q.list.SetFocus(to, now)
			return true
		case input.A:
			if ev.Kind != input.Press {
				return false
			}
			q.moving, q.stale = false, true
			if q.st.Index == q.moveAt.Index && q.st.Version == q.moveAt.Version {
				q.p.Move(q.pos+1+q.from, q.pos+1+f)
			} else {
				q.refresh(now)
			}
			return true
		}
		return false
	}
	switch ev.Button {
	case input.Up, input.Down:
		return q.list.Move(ev.Button, now)
	case input.A, input.X, input.Y:
		if ev.Kind != input.Press || n == 0 {
			return false
		}
		switch ev.Button {
		case input.A:
			q.p.PlayIndex(q.pos + 1 + f)
			q.stale = true
			q.list.SetFocus(0, now)
		case input.X:
			q.moving, q.from, q.moveAt = true, f, q.st
			q.moveMsg = "Moving “" + q.upNext()[f].Title + "”"
		case input.Y:
			q.p.Remove(q.pos + 1 + f)
			q.stale = true
		}
		return true
	}
	return false
}

// Back cancels a move, putting the row back where it was. With no move in
// progress B closes the screen.
func (q *Queue) Back() bool {
	if !q.moving {
		return false
	}
	q.cancelMove()
	return true
}

// Opened drops a move left over from when the screen was last open.
func (q *Queue) Opened() {
	if q.moving {
		q.cancelMove()
		return
	}
	q.stale = true
}

func (q *Queue) cancelMove() {
	q.moving, q.stale = false, true
	q.refresh(time.Now())
	q.list.SetFocus(q.from, time.Now())
}

// Animating reports whether the list is moving.
func (q *Queue) Animating(now time.Time) bool { return q.list.Animating(now) }

// Draw renders the frame.
func (q *Queue) Draw(u *ui.Context) {
	now := u.Now
	q.refresh(now)
	widgets.DrawBackground(u, widgets.TabGround)
	widgets.StatusBar(u)

	y := float32(widgets.StatusHeight + 12)
	u.Text("Queue", text.Title, side, y, theme.Paper)
	baseline := y + u.Ascent(text.Title)
	right := float32(theme.ScreenW - side)
	if q.moving {
		w := u.TextWidth(q.moveMsg, text.Meta)
		u.Text(q.moveMsg, text.Meta, right-w, u.Baseline(text.Meta, baseline), u.Accent.Color)
		u.Icon("updown", 22, 2, false, right-w-30, baseline-20, u.Accent.Color)
	} else if q.header != "" {
		u.Text(q.header, text.Meta, right-u.TextWidth(q.header, text.Meta), u.Baseline(text.Meta, baseline), theme.Ink2)
	}
	y += u.LineHeight(text.Title) + 12

	if len(q.tracks) == 0 {
		msg := "Nothing is queued"
		u.Text(msg, text.Body, (theme.ScreenW-u.TextWidth(msg, text.Body))/2, y+60, theme.Ink3)
		widgets.HintBar(u, q.hints())
		return
	}
	panelY := y
	y += panelH + 16
	headY := y
	y += u.LineHeight(upNext) + 4
	if q.listTop != y {
		q.listTop = y
		q.list.ViewH = theme.ScreenH - widgets.HintsHeight - y
	}
	// Dimming multiplies the alpha the screen is drawn at, which fades it in
	// and out as the overlay opens and closes.
	a := u.Alpha
	if q.moving {
		u.Alpha = a * 0.6
	}
	q.drawPanel(u, panelY)
	u.Alpha = a
	u.Text("Up next", upNext, side, headY, theme.Paper)
	q.drawList(u, now)
	if q.list.Count() == 0 {
		u.Text("Nothing after this track", text.Meta, side+16, y+14, theme.Ink3)
	}
	widgets.HintBar(u, q.hints())
}

// drawPanel draws the now-playing panel: accent border at 50 %, art, the
// NOW PLAYING label, the title and artist, and the time. It shows what the
// player is playing, not the screen's snapshot of the queue. A station shows
// the song it sends and its name, or its name alone, and "Live" for the time.
func (q *Queue) drawPanel(u *ui.Context, y float32) {
	st := q.st
	w := float32(theme.ScreenW - 2*side)
	box := gfx.Rect{X: side, Y: y, W: w, H: panelH}
	u.Shape(raster.Shape{W: int(w), H: panelH, Radius: 14, Kind: raster.Ring, RingOffset: -1.5, RingWidth: 1.5},
		box, fadeTo(u.Accent.Color, 0.5))
	widgets.Art(u, screens.TrackArt(st.Track), panelAr, 8, gfx.Rect{X: side + 12, Y: y + 12, W: panelAr, H: panelAr})
	x := float32(side + 12 + panelAr + 16)
	clock, of := "Live", [2]string{st.LiveTitle, st.Track.Title} // a station's
	if !st.Track.Live() {
		if at := [2]time.Duration{st.Elapsed / time.Second, st.Duration / time.Second}; q.clock == "" || at != q.clockAt {
			q.clock, q.clockAt = screens.Clock(st.Elapsed)+" / "+screens.Clock(st.Duration), at
		}
		clock, of = q.clock, [2]string{st.Track.Title, st.Track.Artist}
	}
	// The line is made again only when what it shows changes. With nothing to
	// show it is "", as it is in a new Queue.
	if of != q.lineOf {
		q.line, q.lineOf = of[0]+" · "+of[1], of
		if of[0] == "" { // a station sending no song: its name alone
			q.line = of[1]
		}
	}
	cw := u.TextWidth(clock, timeText)
	u.Text(clock, timeText, side+w-16-cw, u.CenterY(timeText, y, panelH), theme.Ink2)
	labelH, titleH := u.LineHeight(nowLabel), u.LineHeight(text.Body)
	ty := y + (panelH-labelH-titleH)/2
	u.Text("NOW PLAYING", nowLabel, x, ty, u.Accent.Color)
	u.Text(u.Fit(q.line, text.Body, side+w-16-cw-24-x), text.Body, x, ty+labelH, theme.Paper)
}

// row is up next's row i, made the first time it shows rather than every
// frame.
func (q *Queue) row(i int) *upRow {
	r := &q.rows[i]
	if r.index == "" {
		t := &q.upNext()[i]
		r.index, r.art = strconv.Itoa(i+1), screens.TrackArt(*t)
		switch {
		case t.Live():
			r.duration = "Live"
		case t.Duration > 0:
			r.duration = screens.Clock(t.Duration)
		}
	}
	return r
}

// drawList draws up next. In move mode the rows show grips, the lifted row
// rises on the lifted-row colour with a ring, a shadow and chevrons, and the
// others dim to 55 %. Rows are clipped at the top of the list, so scrolled
// ones don't show under the heading; glows and the shadow fade out over it.
func (q *Queue) drawList(u *ui.Context, now time.Time) {
	a := u.Alpha
	up := q.upNext()
	bottom := float32(theme.ScreenH - widgets.HintsHeight)
	list := gfx.Rect{X: 0, Y: q.listTop, W: theme.ScreenW, H: bottom - q.listTop}
	u.PushClip(gfx.Rect{X: 0, Y: widgets.StatusHeight, W: theme.ScreenW, H: bottom - widgets.StatusHeight})
	q.list.Draw(now, side, q.listTop, func(i int, x, y, k float32) {
		ur := q.row(i)
		r := widgets.Row{Index: ur.index, Grip: q.moving, Art: &ur.art, Title: up[i].Title, Subtitle: up[i].Artist,
			Duration: ur.duration}
		box := gfx.Rect{X: x, Y: y, W: q.list.CellW, H: widgets.RowHeight}
		if !q.moving {
			widgets.RowGlow(u, box, widgets.RowRadius, k)
			u.PushClip(list)
			widgets.ListRow(u, r, box, k)
			u.PopClip()
			return
		}
		if i != q.list.Focus() {
			u.Alpha = a * 0.55
			u.PushClip(list)
			widgets.ListRow(u, r, box, 0)
			u.PopClip()
			u.Alpha = a
			return
		}
		// The clip goes on before the lift's scale, which would scale it too.
		shadow := box
		shadow.Y += 18
		u.PushScale(box.X+box.W/2, box.Y, 1.025)
		u.Shape(raster.Shape{W: int(box.W), H: int(box.H), Radius: widgets.RowRadius, Kind: raster.Shadow, GlowBlur: 40},
			shadow, color.NRGBA{A: 140})
		u.Pop()
		u.PushClip(list)
		u.PushScale(box.X+box.W/2, box.Y, 1.025)
		mask := raster.Shape{W: int(box.W), H: int(box.H), Radius: widgets.RowRadius, Kind: raster.Fill}
		u.Shape(mask, box, theme.LiftedRow)
		mask.Kind, mask.RingWidth = raster.Ring, 4
		u.Shape(mask, box, u.Accent.Color)
		r.Duration = ""
		widgets.ListRow(u, r, box, 0)
		cx := box.X + box.W - 16 - 24
		u.Icon("up", 24, 2, false, cx, box.Y+box.H/2-26, u.Accent.Color)
		u.Icon("down", 24, 2, false, cx, box.Y+box.H/2+2, u.Accent.Color)
		u.Pop()
		u.PopClip()
	})
	u.PopClip()
}

func fadeTo(c color.NRGBA, a float64) color.NRGBA {
	c.A = uint8(float64(c.A) * a)
	return c
}

// The screen's hint bars: while a row is lifted, with nothing up next, and
// otherwise.
var (
	moveHints  = []widgets.Hint{{Key: "A", Label: "Drop here"}, {Key: "▲ ▼", Label: "Move"}, {Key: "B", Label: "Cancel"}}
	emptyHints = []widgets.Hint{{Key: "B", Label: "Back"}}
	listHints  = []widgets.Hint{{Key: "A", Label: "Play now"}, {Key: "X", Label: "Move"}, {Key: "Y", Label: "Remove"},
		{Key: "B", Label: "Back"}}
)

func (q *Queue) hints() []widgets.Hint {
	switch {
	case q.moving:
		return moveHints
	case q.list.Count() == 0:
		return emptyHints
	}
	return listHints
}

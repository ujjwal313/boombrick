// Package lyrics is the Lyrics screen: the playing track's lyrics over its
// blurred art, the current line filling in as it is sung.
package lyrics

import (
	"image"
	"image/color"
	"strings"
	"time"
	"unicode/utf8"

	lrc "github.com/ujjwal313/boombrick/internal/lyrics"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Player is what the screen needs from the player.
type Player interface {
	State() player.State
	Toggle()
	Seek(d time.Duration)
}

// Source is what the screen needs from the lyrics service.
type Source interface {
	Lyrics(t lrc.Track) (lrc.Lyrics, lrc.State)
}

// Layout in design pixels.
const (
	side      = 56
	headTop   = widgets.StatusHeight + 12
	headArt   = 60
	pillH     = 34
	lineGap   = 22 // between lines
	curMargin = 10 // more above and below the current line
	curRow    = 1.08 * 60
	footPad   = 22
	barH      = 4
	lineW     = theme.ScreenW - 2*side
	glowBlur  = 30 // the current line's glow
)

// outputDelay is how long after the player counts music played it is heard:
// the ALSA device keeps 100 ms queued (internal/audio/alsa). Lines are timed
// by what is heard; Bluetooth headphones add a delay of their own, which this
// leaves out.
const outputDelay = 100 * time.Millisecond

// heard is what has been heard when the player has played elapsed.
func heard(elapsed time.Duration) time.Duration { return max(elapsed-outputDelay, 0) }

// lineCache is the budget of the screen's own text cache: the lines on screen
// and the next, made ahead, fit with room to spare. Lyric lines are large, and
// in the shared cache they would push every other screen's text out.
const lineCache = 6 << 20

var (
	titleStyle  = text.Style{Face: text.Figtree, Size: 26}
	artistStyle = text.Style{Face: text.Figtree, Size: 22}
	pillStyle   = text.Style{Face: text.Figtree, Size: 20}
	lineStyle   = text.Style{Face: text.OutfitSemiBold, Size: 36}
	curStyle    = text.Style{Face: text.OutfitSemiBold, Size: 60}
	timeStyle   = text.Style{Face: text.Figtree, Size: 22, Tabular: true}
)

// The lines around the current one, as paper alphas: the two before it,
// furthest first, and the three after it.
var (
	before = [2]float64{0.32, 0.38}
	after  = [3]float64{0.60, 0.38, 0.20}
)

// Screen shows the playing track's lyrics.
type Screen struct {
	p    Player
	src  Source
	back func() // back to Now Playing

	track  lrc.Track // the track asked about, as asked
	asked  time.Time // when
	key    string    // the track whose lyrics are shown, by its lyrics key
	ly     lrc.Lyrics
	state  lrc.State
	line   int           // the current line when last drawn, -1 before the first
	slide  anim.Value    // the lines' rise as a new one becomes current
	head   *lrc.Line     // the first line of the lyrics shown, to tell new lyrics from old
	rows   map[int][]row // current lines wrapped, by index
	plain  []string      // plain lyrics wrapped into rows
	gen    uint64        // the font engine's generation rows and plain were wrapped in
	scroll anim.Value    // plain lyrics' scroll, in rows
	texts  *ui.TextCache // the lyric lines' text

	scrub          *screens.Scrub    // L2 and R2, shown in the footer
	elapsed, total screens.ClockText // the footer's
}

// askEvery is how often the screen asks again about the track it shows: an
// answer still being looked for may come in, or better lyrics replace plain
// ones. Asking every frame of a line filling in would take the lyrics
// service's lock 30 times a second for nothing.
const askEvery = 250 * time.Millisecond

// New creates the screen; back returns to Now Playing.
func New(p Player, src Source, back func()) *Screen {
	return &Screen{p: p, src: src, back: back, line: -1, rows: map[int][]row{}, texts: ui.NewTextCache(lineCache),
		scrub: screens.NewScrub(p)}
}

// SetHeld tells the screen whether a button is down, so that a scrub whose
// trigger was let go elsewhere is dropped.
func (s *Screen) SetHeld(held func(input.Button) bool) { s.scrub.SetHeld(held) }

// row is one wrapped row of the current line, with how far along it each
// character starts so the fill needs no shaping while the song plays.
type row struct {
	text  string
	x     []float32 // x[i] is the width of the first i characters
	start int       // where the row starts in its line, in characters
	of    int       // the line's length, in characters
	rtl   bool      // drawn right to left
}

// lineRows makes the rows of line, wrapped into texts, each knowing where in
// the line it starts.
func lineRows(u *ui.Context, line string, texts []string) []row {
	rows := make([]row, len(texts))
	of, at := utf8.RuneCountInString(line), 0 // at: where the next row is looked for, in bytes
	for i, t := range texts {
		r := newRow(u, t)
		r.rtl, r.of = text.RightToLeft(t), of
		if j := strings.Index(line[at:], t); j >= 0 {
			at += j
		}
		r.start = utf8.RuneCountInString(line[:at])
		at = min(at+len(t), len(line))
		rows[i] = r
	}
	return rows
}

// newRow measures s at every character boundary, once.
func newRow(u *ui.Context, s string) row {
	r := row{text: s, x: make([]float32, 1, utf8.RuneCountInString(s)+1)}
	for i := range s {
		if i > 0 {
			r.x = append(r.x, u.TextWidth(s[:i], curStyle))
		}
	}
	if s != "" {
		r.x = append(r.x, u.TextWidth(s, curStyle))
	}
	return r
}

func (r row) runes() int     { return len(r.x) - 1 }
func (r row) width() float32 { return r.x[len(r.x)-1] }

// fillWidth is how far along rows[k] the fill reaches when the share sung of
// the whole line has been sung, stopping part of the way across a character.
// A space a wrap drops between rows counts as a character, as it does in the
// lyrics' own timing; a line wrapped between characters drops none.
func fillWidth(rows []row, k int, sung float64) float32 {
	r := rows[k]
	left := sung*float64(r.of) - float64(r.start)
	switch {
	case left <= 0:
		return 0
	case left >= float64(r.runes()):
		return r.width()
	}
	whole := int(left)
	return r.x[whole] + float32(left-float64(whole))*(r.x[whole+1]-r.x[whole])
}

// fillSpan is the stretch of the screen, from x0 to x1, the fill covers on r
// when it reaches fill along it: from the left for a row drawn left to right,
// from its right end for one drawn right to left, where its first word is,
// taking in whatever overhangs the row's end.
func fillSpan(r row, fill float32) (x0, x1 float32) {
	if r.rtl {
		return side + r.width() - fill, theme.ScreenW
	}
	return 0, side + fill
}

// refresh picks up the playing track's lyrics, starting over for a new track.
// It asks the source at once about a new track, and otherwise every askEvery.
func (s *Screen) refresh(st player.State, now time.Time) {
	if t := screens.PlayingLyricsTrack(st); t != s.track {
		if key := t.Key(); key != s.key {
			s.key, s.line, s.plain = key, -1, nil
			clear(s.rows)
			s.texts.Clear()
			s.slide.Set(0)
			s.scroll.Set(0)
		}
		s.track, s.asked = t, time.Time{}
	}
	if !st.Queued {
		s.ly, s.state, s.head = lrc.Lyrics{}, lrc.None, nil
		s.asked = time.Time{}
		return
	}
	if !s.asked.IsZero() && now.Sub(s.asked) < askEvery {
		return
	}
	s.asked = now
	ly, state := s.src.Lyrics(s.track)
	var head *lrc.Line
	if len(ly.Lines) > 0 {
		head = &ly.Lines[0]
	}
	if state != s.state || ly.Synced != s.ly.Synced || len(ly.Lines) != len(s.ly.Lines) || head != s.head {
		// Different lyrics, even if the same shape: nothing worked out for
		// the old ones holds, and the new ones don't rise into place.
		s.plain, s.line, s.head = nil, -1, head
		clear(s.rows)
		s.texts.Clear()
		s.slide.Set(0)
	}
	s.ly, s.state = ly, state
}

// Handle plays and pauses with A, seeks 10 s with ◀ ▶, rewinds and
// fast-forwards with L2 and R2, scrolls plain lyrics with ▲ ▼ and goes back
// with Y.
func (s *Screen) Handle(ev input.Event, now time.Time) bool {
	if s.scrub.Handle(ev, now) {
		return true
	}
	if ev.Kind == input.Release {
		return false
	}
	switch ev.Button {
	case input.A, input.Y:
		if ev.Kind != input.Press {
			return false
		}
		if ev.Button == input.A {
			s.p.Toggle()
		} else {
			s.back()
		}
		return true
	case input.Left, input.Right:
		st := s.p.State()
		if !st.Queued {
			return false
		}
		d := -screens.SeekStep
		if ev.Button == input.Right {
			d = screens.SeekStep
		}
		s.p.Seek(max(st.Elapsed+d, 0))
		return true
	case input.Up, input.Down:
		if s.plain == nil {
			return false
		}
		to := s.scroll.Target() - 1
		if ev.Button == input.Down {
			to += 2
		}
		to = min(max(to, 0), float64(max(len(s.plain)-1, 0)))
		if to == s.scroll.Target() {
			return false
		}
		s.scroll.To(to, now, anim.Focus, anim.EaseOutCubic)
		return true
	}
	return false
}

// Back returns to Now Playing, as Y does.
func (s *Screen) Back() bool {
	s.back()
	return true
}

// Tick asks for 30 frames a second while a line of synced lyrics fills in or
// the next is about to rise into place, and 4 for the progress bar otherwise:
// before the first line, in a break once a line is sung and through the end.
func (s *Screen) Tick() time.Duration {
	if s.state != lrc.Found || !s.ly.Synced {
		return 250 * time.Millisecond
	}
	t := heard(s.p.State().Elapsed)
	cur, sung := s.ly.At(t)
	if cur >= 0 && sung < 1 || cur+1 < len(s.ly.Lines) && s.ly.Lines[cur+1].Start-t <= 250*time.Millisecond {
		return time.Second / 30
	}
	return 250 * time.Millisecond
}

// Animating reports whether the lines are rising or scrolling, or a trigger
// is down for a scrub.
func (s *Screen) Animating(now time.Time) bool {
	return s.scrub.Active() || !s.slide.Done(now) || !s.scroll.Done(now)
}

// Draw renders the frame. One that makes no text while the lines are still
// makes a piece of the next line's ahead of time.
func (s *Screen) Draw(u *ui.Context) {
	made := u.Made()
	st := s.p.State()
	s.refresh(st, u.Now)
	// Lines wrapped before fonts arrived are wrapped again with them.
	if g := u.Fonts.Generation(); g != s.gen {
		s.gen, s.plain = g, nil
		clear(s.rows)
	}
	art := screens.TrackArt(st.Track)
	drawBackdrop(u, art)
	status := u.Status
	u.Status.NowPlaying = "" // the header shows what's playing
	widgets.StatusBar(u)
	u.Status = status
	top := s.drawHeader(u, st, art)
	bottom := s.drawFooter(u, s.scrub.Shown(st, u.Now)) // the lines keep to what is heard
	area := gfx.Rect{X: 0, Y: top, W: theme.ScreenW, H: bottom - top}
	switch {
	case s.state == lrc.Found && s.ly.Synced:
		s.drawSynced(u, st, area)
		if s.slide.Done(u.Now) && u.Made() == made {
			s.prepare(u)
		}
	case s.state == lrc.Found:
		s.drawPlain(u, area)
	default:
		msg := message(st, s.state)
		u.Text(msg, text.Body, (theme.ScreenW-u.TextWidth(msg, text.Body))/2, u.CenterY(text.Body, area.Y, area.H), theme.Ink3)
	}
}

// drawBackdrop draws the blurred art 1120 px square at (-160, -208), 75 %
// over the ground, under a veil of ground from 55 % at the top to 82 % at the
// bottom. Without art the Now Playing glow stands in.
func drawBackdrop(u *ui.Context, art widgets.ArtSpec) {
	t, a, none := u.Backdrop(art.Cover)
	if none {
		widgets.DrawBackground(u, widgets.NowPlayingGround)
		return
	}
	u.Fill(gfx.Rect{X: 0, Y: 0, W: theme.ScreenW, H: theme.ScreenH}, theme.Ground)
	if t != nil {
		u.DrawCover(t, gfx.Rect{X: -160, Y: -208, W: 1120, H: 1120}, 0.75*a)
	}
	u.Image("lyrics:veil", veil, gfx.Rect{X: 0, Y: 0, W: theme.ScreenW, H: theme.ScreenH}, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
}

// veil is the ground colour from 55 % to 82 % opaque down the screen, one
// pixel wide, drawn stretched across.
func veil() *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 1, theme.DeviceH))
	g := theme.Ground
	for y := range theme.DeviceH {
		a := 0.55 + 0.27*float64(y)/float64(theme.DeviceH-1)
		img.SetNRGBA(0, y, color.NRGBA{R: g.R, G: g.G, B: g.B, A: uint8(255*a + 0.5)})
	}
	return img
}

// drawHeader draws the art, the title over the artist and the Synced or
// Plain pill, and returns where the lines may start.
func (s *Screen) drawHeader(u *ui.Context, st player.State, art widgets.ArtSpec) float32 {
	y := float32(headTop)
	if !st.Queued {
		return y + headArt + 16
	}
	widgets.Art(u, art, headArt, 8, gfx.Rect{X: side, Y: y, W: headArt, H: headArt})
	x := float32(side + headArt + 16)
	right := float32(theme.ScreenW - side)
	if label := pillLabel(s.state, s.ly); label != "" {
		w := u.TextWidth(label, pillStyle) + 28
		right -= w
		box := gfx.Rect{X: right, Y: y + (headArt-pillH)/2, W: w, H: pillH}
		u.Shape(raster.Shape{W: int(w), H: pillH, Radius: pillH / 2, Kind: raster.Ring, RingOffset: -1.5, RingWidth: 1.5},
			box, theme.PaperA(0.30))
		u.Text(label, pillStyle, box.X+14, u.CenterY(pillStyle, box.Y, pillH), theme.Ink2)
		right -= 16
	}
	th, ah := u.LineHeight(titleStyle), u.LineHeight(artistStyle)
	ty := y + (headArt-th-ah)/2
	u.Text(u.Fit(st.Track.Title, titleStyle, right-x), titleStyle, x, ty, theme.Paper)
	u.Text(u.Fit(st.Track.Artist, artistStyle, right-x), artistStyle, x, ty+th, theme.Ink2)
	return y + headArt + 16
}

func pillLabel(state lrc.State, ly lrc.Lyrics) string {
	switch {
	case state != lrc.Found:
		return ""
	case ly.Synced:
		return "Synced"
	}
	return "Plain"
}

// drawFooter draws the elapsed time, a 4 px progress bar and the length
// along the bottom, and returns its top.
func (s *Screen) drawFooter(u *ui.Context, st player.State) float32 {
	h := u.LineHeight(timeStyle)
	y := float32(theme.ScreenH-footPad) - h
	if !st.Queued {
		return y
	}
	elapsed, total := s.elapsed.Of(st.Elapsed), s.total.Of(st.Duration)
	u.Text(elapsed, timeStyle, side, y, theme.Ink2)
	tw := u.TextWidth(total, timeStyle)
	u.Text(total, timeStyle, theme.ScreenW-side-tw, y, theme.Ink2)
	x0 := side + u.TextWidth(elapsed, timeStyle) + 16
	bar := gfx.Rect{X: x0, Y: y + (h-barH)/2, W: theme.ScreenW - side - tw - 16 - x0, H: barH}
	shape := raster.Shape{W: int(bar.W), H: barH, Radius: barH / 2, Kind: raster.Fill}
	u.Shape(shape, bar, theme.PaperA(0.18))
	if st.Duration > 0 {
		f := min(max(float32(st.Elapsed)/float32(st.Duration), 0), 1)
		u.PushClip(gfx.Rect{X: bar.X, Y: bar.Y, W: bar.W * f, H: barH})
		u.Shape(shape, bar, u.Accent.Color)
		u.PopClip()
	}
	return y
}

// drawSynced draws the current line, large and filling in, the two lines
// before it and the three after, centred in area. The lines rise into place
// as the next one becomes current.
func (s *Screen) drawSynced(u *ui.Context, st player.State, area gfx.Rect) {
	now := u.Now
	cur, sung := s.ly.At(heard(st.Elapsed))
	lh := u.LineHeight(lineStyle)
	if cur != s.line {
		if cur == s.line+1 && s.line >= 0 {
			s.slide.Set(float64(lh + lineGap))
			s.slide.To(0, now, anim.Lyric, anim.EaseOutCubic)
		} else {
			s.slide.Set(0) // a seek: no rise
		}
		s.line = cur
	}
	w := float32(lineW)
	rows := s.wrapped(u, cur, w)
	curH := float32(len(rows)) * curRow
	curTop := area.Y + area.H/2 + float32(s.slide.At(now)) - curH/2

	u.PushClip(area)
	defer u.PopClip()
	y := curTop - curMargin
	for k := 1; k <= 2 && cur-k >= 0; k++ {
		y -= lineGap + lh
		if fits(area, y, lh) {
			u.TextIn(s.texts, u.Fit(s.ly.Lines[cur-k].Text, lineStyle, w), lineStyle, side, y, theme.PaperA(before[2-k]))
		}
	}
	if cur >= 0 {
		s.drawCurrent(u, rows, sung, curTop)
	}
	y = curTop + curH + curMargin
	for k := 0; k < 3 && cur+1+k < len(s.ly.Lines); k++ {
		y += lineGap
		if fits(area, y, lh) {
			u.TextIn(s.texts, u.Fit(s.ly.Lines[cur+1+k].Text, lineStyle, w), lineStyle, side, y, theme.PaperA(after[k]))
		}
		y += lh
	}
}

// prepare makes ahead of time, a piece a call, what the next line would
// otherwise need all in the frame it becomes current: its rows, each row's
// text and glow, and the text of the lines around it, including the one that
// comes into view.
func (s *Screen) prepare(u *ui.Context) {
	next := s.line + 1
	if next >= len(s.ly.Lines) {
		return
	}
	rows, ok := s.rows[next]
	if !ok {
		s.wrapped(u, next, lineW)
		return
	}
	for _, r := range rows {
		if u.PrepareText(s.texts, r.text, curStyle) || u.PrepareGlow(r.text, curStyle, glowBlur) {
			return
		}
	}
	for i := max(next-2, 0); i <= min(next+3, len(s.ly.Lines)-1); i++ {
		if i != next && u.PrepareText(s.texts, u.Fit(s.ly.Lines[i].Text, lineStyle, lineW), lineStyle) {
			return
		}
	}
}

// fits reports whether a line h tall at y lies wholly inside area; a line the
// header or footer would cut in half is left out.
func fits(area gfx.Rect, y, h float32) bool { return y >= area.Y && y+h <= area.Y+area.H }

// wrapped is line i wrapped to w at the current line's size, worked out once.
func (s *Screen) wrapped(u *ui.Context, i int, w float32) []row {
	if i < 0 {
		return nil
	}
	if r, ok := s.rows[i]; ok {
		return r
	}
	line := s.ly.Lines[i].Text
	rows := lineRows(u, line, widgets.Wrap(u, line, curStyle, w))
	if len(rows) == 0 {
		rows = []row{newRow(u, "")} // an instrumental break keeps its place
	}
	s.rows[i] = rows
	return rows
}

// drawCurrent draws the current line's rows over an accent glow, in paper at
// 45 % and, over that, in paper as far as sung of its characters reach.
func (s *Screen) drawCurrent(u *ui.Context, rows []row, sung float64, top float32) {
	glow := u.Accent.Color
	glow.A = uint8(float64(glow.A)*0.35 + 0.5)
	lh := u.LineHeight(curStyle)
	for k, r := range rows {
		y := top + float32(k)*curRow + (curRow-lh)/2
		u.TextGlow(r.text, curStyle, side, y, glowBlur, glow)
		u.TextIn(s.texts, r.text, curStyle, side, y, theme.PaperA(0.45))
		switch fill := fillWidth(rows, k, sung); {
		case fill <= 0:
		case fill >= r.width():
			u.TextIn(s.texts, r.text, curStyle, side, y, theme.Paper)
		default:
			x0, x1 := fillSpan(r, fill)
			u.PushClip(gfx.Rect{X: x0, Y: y - 10, W: x1 - x0, H: lh + 20})
			u.TextIn(s.texts, r.text, curStyle, side, y, theme.Paper)
			u.PopClip()
		}
	}
}

// drawPlain draws plain lyrics as a column the D-pad scrolls a row at a time.
func (s *Screen) drawPlain(u *ui.Context, area gfx.Rect) {
	w := float32(lineW)
	if s.plain == nil {
		for _, l := range s.ly.Lines {
			r := widgets.Wrap(u, l.Text, lineStyle, w)
			if len(r) == 0 {
				r = []string{""} // a blank line between verses
			}
			s.plain = append(s.plain, r...)
		}
	}
	lh := u.LineHeight(lineStyle)
	step := lh + 8
	y := area.Y + 12 - float32(s.scroll.At(u.Now))*step
	u.PushClip(area)
	for _, r := range s.plain {
		if y > area.Y+area.H {
			break
		}
		if y+lh >= area.Y {
			u.TextIn(s.texts, r, lineStyle, side, y, theme.PaperA(0.60))
		}
		y += step
	}
	u.PopClip()
}

// message is what shows in place of lyrics.
func message(st player.State, state lrc.State) string {
	switch {
	case !st.Queued:
		return "Nothing is playing"
	case state == lrc.Instrumental:
		return "Instrumental"
	case state == lrc.None:
		return "No lyrics for this song"
	case state == lrc.Offline:
		return "Lyrics need a connection the first time"
	case state == lrc.Failed:
		return "Couldn't get the lyrics just now"
	}
	return "Looking for lyrics"
}

// Package nowplaying is the Now Playing screen.
package nowplaying

import (
	"cmp"
	"fmt"
	"image/color"
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
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
	Next()
	Prev()
	SetShuffle(on bool)
	CycleRepeat()
	Seek(d time.Duration)
}

// Controls, left to right.
const (
	shuffleCtl = iota
	prevCtl
	playCtl
	nextCtl
	repeatCtl
	controlCount
)

// lyricsPill is the focus index of the Lyrics pill, under the controls.
const lyricsPill = controlCount

// The Lyrics pill: 48 high with a 1.5 px border, holding a Y glyph, the
// lyrics icon and "Lyrics".
const lyricsH = 48

var lyricsText = text.Style{Face: text.Figtree, Size: 22}

// The Live pill, in a station's progress bar's place: 30 high with a 1.5 px
// border, holding an 8 px dot and "Live", 8 apart, with padding 12 each side.
const (
	liveH   = 30
	liveDot = 8
	livePad = 12
)

// controlOffset is each control's centre relative to the column's centre, in
// design pixels. Fixed offsets keep prev and next clear of the 108 px
// play/pause ring and its focus ring.
var controlOffset = [controlCount]float32{-170, -100, 0, 100, 170}

var controlIcons = [controlCount]string{"shuffle", "prev", "play", "next", "repeat"}

// Layout in design pixels, fitted to the canvas.
const (
	side     = 44
	artSize  = 300
	colGap   = 44
	groupGap = 26
	ctlRow   = 108 // the play/pause circle
	ctlFocus = 64  // focus circle around the other controls
	knob     = 18
)

var (
	titleStyle  = text.Style{Face: text.OutfitSemiBold, Size: 52}
	artistStyle = text.Style{Face: text.Figtree, Size: 30}
	albumStyle  = text.Style{Face: text.FigtreeItalic, Size: 24}
	timeStyle   = text.Style{Face: text.Figtree, Size: 22, Tabular: true}
	formatStyle = text.Style{Face: text.Figtree, Size: 20}
	oneStyle    = text.Style{Face: text.FigtreeSemiBold, Size: 15}
)

// NowPlaying shows the current track and its transport controls.
type NowPlaying struct {
	p         Player
	focus     int
	values    [controlCount + 1]anim.Value                        // focus strength per control
	lyrics    func()                                              // opens the Lyrics screen; nil: no pill
	downloads func(videoID string) (media.DownloadState, float32) // how far a song waited on has got; nil: none
	note      func() string                                       // why the queue waits, or ""; nil: none
	scrub     *screens.Scrub                                      // L2 and R2

	// What the frames show, made again only when it changes.
	elapsed, length screens.ClockText
	format          string
	formatOf        formatKey
	station         string    // "Radio City · India", under the song a station sends
	stationOf       [2]string // the station's name and country in station
	hintBar         []widgets.Hint
	hintLabel       string // A's label in hintBar
	hintLive        bool   // hintBar is a station's, with no lyrics or seeking
}

// formatKey is what formatLine depends on.
type formatKey struct {
	kbps, rate int
	format     string
	live       bool
}

// New creates the screen with focus on play/pause.
func New(p Player) *NowPlaying {
	n := &NowPlaying{p: p, focus: playCtl, scrub: screens.NewScrub(p)}
	n.values[playCtl].Set(1)
	return n
}

// SetHeld tells the screen whether a button is down, so that a scrub whose
// trigger was let go elsewhere is dropped.
func (n *NowPlaying) SetHeld(held func(input.Button) bool) { n.scrub.SetHeld(held) }

// SetLyrics gives the screen its Lyrics pill and Y button, which call open.
func (n *NowPlaying) SetLyrics(open func()) { n.lyrics, n.hintBar = open, nil }

// SetDownloads lets the screen show how far a song it waits on has got, and
// with note, which may be nil, why one queued isn't downloading.
func (n *NowPlaying) SetDownloads(state func(videoID string) (media.DownloadState, float32), note func() string) {
	n.downloads, n.note = state, note
}

// ShowsDownloads reports whether the screen shows a download: playback waits
// on one, or the track is still to download.
func (n *NowPlaying) ShowsDownloads() bool {
	s := n.p.State()
	return s.Waiting || s.Track.Pending()
}

// hasLyrics reports whether the screen offers lyrics for s's track. A
// station has none.
func (n *NowPlaying) hasLyrics(s player.State) bool { return n.lyrics != nil && !s.Track.Live() }

// keepFocus moves focus off the Lyrics pill, back to play/pause, when the
// track has no lyrics: a station started while focus rested there.
func (n *NowPlaying) keepFocus(s player.State) {
	if n.focus == lyricsPill && !n.hasLyrics(s) {
		n.values[lyricsPill].Set(0)
		n.focus = playCtl
		n.values[playCtl].Set(1)
	}
}

// source is where the track comes from, for its badge.
func source(t player.Track) media.Source {
	switch {
	case t.Live():
		return media.Radio
	case t.Streamed():
		return media.Spotify
	case t.Pending():
		return media.YouTube
	case t.VideoID != "":
		return media.Downloaded
	}
	return screens.SourceOf(t.Path)
}

// downloading are "Downloading 0 %" to "Downloading 100 %", made on first use
// so that a waited-on download's progress doesn't build a string each frame.
var downloading [101]string

func downloadingText(p float32) string {
	if downloading[0] == "" {
		for i := range downloading {
			downloading[i] = "Downloading " + strconv.Itoa(i) + " %"
		}
	}
	return downloading[min(max(int(p*100+0.5), 0), 100)]
}

// progress is the bar's fill and the words under it: the elapsed and total
// times, or while the track downloads, how far it has got and its length.
// A stream says when it is opening, or picking up after a break.
func (n *NowPlaying) progress(s player.State) (float32, string, string) {
	if s.Loading {
		var length string // unknown: none
		if s.Track.Duration > 0 {
			length = n.length.Of(s.Track.Duration)
		}
		return 0, "Loading", length
	}
	if !s.Waiting {
		var f float32
		if s.Duration > 0 {
			f = min(max(float32(s.Elapsed)/float32(s.Duration), 0), 1)
		}
		left := "Connection lost"
		if !s.Stalled {
			left = n.elapsed.Of(s.Elapsed)
		}
		return f, left, n.length.Of(s.Duration)
	}
	st, p := media.Queued, float32(0)
	if n.downloads != nil {
		st, p = n.downloads(s.Track.VideoID)
	}
	f, left := float32(0), "Waiting to download"
	switch st {
	case media.Queued:
		if n.note != nil {
			left = cmp.Or(n.note(), left) // paused, offline, the card full or the downloader coming
		}
	case media.Downloading:
		f = min(max(p, 0), 1)
		left = downloadingText(f)
	case media.Saved: // landed, and about to play
		f, left = 1, downloadingText(1)
	case media.Failed:
		left = "Couldn’t download"
	}
	var length string // unknown: none
	if s.Track.Duration > 0 {
		length = n.length.Of(s.Track.Duration)
	}
	return f, left, length
}

// moveTo moves focus to f.
func (n *NowPlaying) moveTo(f int, now time.Time) {
	n.values[n.focus].To(0, now, anim.Focus, anim.EaseOutCubic)
	n.focus = f
	n.values[f].To(1, now, anim.Focus, anim.EaseOutCubic)
}

// Handle moves focus along the controls, activates them with A, changes
// track with L1 and R1 and seeks with L2 and R2. B is handled by the app,
// which closes the screen. A station has no lyrics, and can't seek.
func (n *NowPlaying) Handle(ev input.Event, now time.Time) bool {
	if n.scrub.Handle(ev, now) {
		return true
	}
	if ev.Kind == input.Release {
		return false
	}
	s := n.p.State()
	n.keepFocus(s)
	switch ev.Button {
	case input.Left, input.Right:
		if n.focus == lyricsPill {
			return false
		}
		f := n.focus - 1
		if ev.Button == input.Right {
			f = n.focus + 1
		}
		f = min(max(f, 0), controlCount-1)
		if f == n.focus {
			return false
		}
		n.moveTo(f, now)
		return true
	case input.Down:
		if !n.hasLyrics(s) || n.focus == lyricsPill {
			return false
		}
		n.moveTo(lyricsPill, now)
		return true
	case input.Up:
		if n.focus != lyricsPill {
			return false
		}
		n.moveTo(playCtl, now)
		return true
	case input.A:
		if ev.Kind != input.Press {
			return false
		}
		n.activate()
		return true
	case input.Y:
		if ev.Kind != input.Press || !n.hasLyrics(s) {
			return false
		}
		n.lyrics()
		return true
	case input.L1:
		n.p.Prev()
		return true
	case input.R1:
		n.p.Next()
		return true
	}
	return false
}

func (n *NowPlaying) activate() {
	switch n.focus {
	case shuffleCtl:
		n.p.SetShuffle(!n.p.State().Shuffle)
	case prevCtl:
		n.p.Prev()
	case playCtl:
		n.p.Toggle()
	case nextCtl:
		n.p.Next()
	case repeatCtl:
		n.p.CycleRepeat()
	case lyricsPill:
		n.lyrics()
	}
}

// Tick asks for frames 4 times a second while music plays, for the progress
// bar. A station has none, so it asks once a second, as the tabs do: the
// player wakes no one when the station's song changes or it connects, so a
// tick is how the screen sees that.
func (n *NowPlaying) Tick() time.Duration {
	if n.p.State().Track.Live() {
		return time.Second
	}
	return 250 * time.Millisecond
}

// Animating reports whether focus is moving or a trigger is down for a
// scrub. The progress bar is redrawn on Tick while music plays.
func (n *NowPlaying) Animating(now time.Time) bool {
	if n.scrub.Active() {
		return true
	}
	for i := range n.values {
		if !n.values[i].Done(now) {
			return true
		}
	}
	return false
}

// Draw renders the frame.
func (n *NowPlaying) Draw(u *ui.Context) {
	s := n.scrub.Shown(n.p.State(), u.Now)
	n.keepFocus(s)
	live := s.Track.Live()
	widgets.DrawBackground(u, widgets.NowPlayingGround)
	status := u.Status
	u.Status.NowPlaying = "" // the screen itself shows what's playing
	widgets.StatusBar(u)
	u.Status = status

	top, bottom := float32(widgets.StatusHeight), float32(theme.ScreenH-widgets.HintsHeight)
	art := gfx.Rect{X: side, Y: top + (bottom-top-artSize)/2, W: artSize, H: artSize}
	shadow := art
	shadow.Y += 24
	u.Shape(raster.Shape{W: artSize, H: artSize, Radius: 16, Kind: raster.Shadow, GlowBlur: 60}, shadow, color.NRGBA{A: 128})
	widgets.Art(u, screens.TrackArt(s.Track), artSize, 16, art)

	x := art.X + artSize + colGap
	w := float32(theme.ScreenW) - side - x
	titleH, artistH, albumH, timeH := u.LineHeight(titleStyle), u.LineHeight(artistStyle), u.LineHeight(albumStyle), u.LineHeight(timeStyle)
	barH := float32(knob)
	if live { // no album line, and the Live pill in the progress bar's place
		albumH, barH = 0, liveH
	}
	h := 32 + groupGap + titleH + artistH + albumH + groupGap + barH + 8 + timeH + groupGap + ctlRow
	if n.hasLyrics(s) {
		h += groupGap + lyricsH
	}
	y := top + (bottom-top-h)/2

	// Spotify's and Radio's badges fade while offline.
	src := source(s.Track)
	bw := widgets.Badge(u, src, x, y, (src == media.Spotify || src == media.Radio) && !u.Status.Online)
	if k := (formatKey{s.Kbps, s.Rate, s.Format, live}); k != n.formatOf {
		n.format, n.formatOf = formatLine(s), k
	}
	u.Text(n.format, formatStyle, x+bw+12, u.CenterY(formatStyle, y, 32), theme.Ink3)
	y += 32 + groupGap
	// A station's song goes over its name and country; with none, its name
	// over its country.
	title, artist := s.Track.Title, s.Track.Artist
	if live && s.LiveTitle != "" {
		title, artist = s.LiveTitle, n.stationLine(s.Track)
	}
	u.Text(u.Fit(title, titleStyle, w), titleStyle, x, y, theme.Paper)
	y += titleH
	u.Text(u.Fit(artist, artistStyle, w), artistStyle, x, y, theme.Paper)
	y += artistH
	if !live {
		u.Text(u.Fit(s.Track.Album, albumStyle, w), albumStyle, x, y, theme.Ink2)
	}
	y += albumH + groupGap
	if live {
		drawLive(u, s, x, y)
	} else {
		f, left, right := n.progress(s)
		drawProgress(u, f, left, right, x, y, w)
	}
	y += barH + 8 + timeH + groupGap
	n.drawControls(u, s, x, y, w)
	if n.hasLyrics(s) {
		n.drawLyricsPill(u, x, y+ctlRow+groupGap)
	}

	widgets.HintBar(u, n.hints(s))
}

// stationLine is "Radio City · India", the station's name and country,
// made again only when the station changes.
func (n *NowPlaying) stationLine(t player.Track) string {
	if of := [2]string{t.Title, t.Artist}; n.station == "" || of != n.stationOf {
		n.station, n.stationOf = t.Title, of
		if t.Artist != "" {
			n.station += " · " + t.Artist
		}
	}
	return n.station
}

func formatLine(s player.State) string {
	if s.Track.Live() { // the codec, and the bitrate when the station gives one
		if s.Kbps > 0 && s.Format != "" {
			return s.Format + " · " + strconv.Itoa(s.Kbps) + " kbps"
		}
		return s.Format
	}
	if s.Kbps > 0 {
		return strconv.Itoa(s.Kbps) + " kbps"
	}
	if s.Rate == 0 {
		return s.Format
	}
	if s.Format == "" { // a Spotify song playing from its file
		return fmt.Sprintf("%g kHz", float64(s.Rate)/1000)
	}
	return fmt.Sprintf("%s · %g kHz", s.Format, float64(s.Rate)/1000)
}

// drawProgress draws the 6 px track, its accent fill to f, the 18 px knob,
// and left and right below it.
func drawProgress(u *ui.Context, f float32, left, right string, x, y, w float32) {
	bar := gfx.Rect{X: x, Y: y + (knob-6)/2, W: w, H: 6}
	shape := raster.Shape{W: int(w), H: 6, Radius: 3, Kind: raster.Fill}
	u.Shape(shape, bar, theme.PaperA(0.18))
	if f > 0 {
		u.PushClip(gfx.Rect{X: x, Y: bar.Y, W: w * f, H: 6})
		u.Shape(shape, bar, u.Accent.Color)
		u.PopClip()
	}
	u.Shape(raster.Shape{W: knob, H: knob, Radius: knob / 2, Kind: raster.Fill},
		gfx.Rect{X: x + w*f - knob/2, Y: y, W: knob, H: knob}, theme.Paper)
	ty := y + knob + 8
	u.Text(left, timeStyle, x, ty, theme.Ink2)
	u.Text(right, timeStyle, x+w-u.TextWidth(right, timeStyle), ty, theme.Ink2)
}

// drawLive draws a station's Live pill at (x, y), in the accent, or ink3
// while paused. Below it, where a song's times go, it says when the stream is
// opening or being picked up again.
func drawLive(u *ui.Context, s player.State, x, y float32) {
	col := u.Accent.Color
	if !s.Playing {
		col = theme.Ink3
	}
	w := livePad + liveDot + 8 + u.TextWidth("Live", text.Chrome) + livePad
	u.Shape(raster.Shape{W: int(w), H: liveH, Radius: liveH / 2, Kind: raster.Ring, RingOffset: -1.5, RingWidth: 1.5},
		gfx.Rect{X: x, Y: y, W: w, H: liveH}, col)
	u.Shape(raster.Shape{W: liveDot, H: liveDot, Radius: liveDot / 2, Kind: raster.Fill},
		gfx.Rect{X: x + livePad, Y: y + (liveH-liveDot)/2, W: liveDot, H: liveDot}, col)
	u.Text("Live", text.Chrome, x+livePad+liveDot+8, u.CenterY(text.Chrome, y, liveH), col)
	var state string
	switch {
	case s.Loading:
		state = "Connecting…"
	case s.Stalled:
		state = "Reconnecting…"
	}
	u.Text(state, timeStyle, x, y+liveH+8, theme.Ink2)
}

// drawControls spreads the five controls across the column, each focusable
// like a button.
func (n *NowPlaying) drawControls(u *ui.Context, s player.State, x, y, w float32) {
	cy := y + ctlRow/2
	for i := range controlIcons {
		cx := x + w/2 + controlOffset[i]
		d := float32(ctlFocus)
		if i == playCtl {
			d = ctlRow
		}
		k := float32(n.values[i].At(u.Now))
		box := gfx.Rect{X: cx - d/2, Y: cy - d/2, W: d, H: d}
		u.PushScale(cx, box.Y, 1+0.06*k)
		widgets.FocusGlow(u, box, float64(d)/2, k)
		widgets.FocusRing(u, box, float64(d)/2, k, theme.Ground)
		drawControl(u, s, i, cx, cy, box)
		u.Pop()
	}
}

func drawControl(u *ui.Context, s player.State, i int, cx, cy float32, box gfx.Rect) {
	switch i {
	case playCtl:
		u.Shape(raster.Shape{W: ctlRow, H: ctlRow, Radius: ctlRow / 2, Kind: raster.Ring, RingOffset: -2, RingWidth: 2}, box, u.Accent.Color)
		name := "play"
		if s.Playing {
			name = "pause"
		}
		u.Icon(name, 44, 2, true, cx-22, cy-22, theme.Paper)
	case prevCtl, nextCtl:
		u.Icon(controlIcons[i], 44, 2, true, cx-22, cy-22, theme.Paper)
	default:
		on := s.Shuffle
		if i == repeatCtl {
			on = s.Repeat != player.RepeatOff
		}
		col := theme.Ink3
		if on {
			col = u.Accent.Color
		}
		u.Icon(controlIcons[i], 34, 2, false, cx-17, cy-17, col)
		if on {
			u.Shape(raster.Shape{W: 6, H: 6, Radius: 3, Kind: raster.Fill}, gfx.Rect{X: cx - 3, Y: cy + 17 + 6, W: 6, H: 6}, col)
		}
		if i == repeatCtl && s.Repeat == player.RepeatOne {
			u.Text("1", oneStyle, cx+12, cy-26, col)
		}
	}
}

// drawLyricsPill draws the Lyrics pill at (x, y), focusable like a button.
func (n *NowPlaying) drawLyricsPill(u *ui.Context, x, y float32) {
	g := widgets.HintRound.Height
	w := 12 + g + 10 + 24 + 8 + u.TextWidth("Lyrics", lyricsText) + 18
	box := gfx.Rect{X: x, Y: y, W: w, H: lyricsH}
	k := float32(n.values[lyricsPill].At(u.Now))
	u.PushScale(x+w/2, y, 1+0.06*k)
	defer u.Pop()
	widgets.FocusGlow(u, box, lyricsH/2, k)
	widgets.FocusRing(u, box, lyricsH/2, k, theme.Ground)
	u.Shape(raster.Shape{W: int(w), H: lyricsH, Radius: lyricsH / 2, Kind: raster.Ring, RingOffset: -1.5, RingWidth: 1.5},
		box, theme.PaperA(0.30))
	cx := x + 12
	cx += widgets.KeyGlyph(u, "Y", cx, y+(lyricsH-g)/2, widgets.HintRound, theme.PaperA(0.55)) + 10
	u.Icon("lyrics", 24, 2, false, cx, y+(lyricsH-24)/2, theme.Paper)
	u.Text("Lyrics", lyricsText, cx+24+8, u.CenterY(lyricsText, y, lyricsH), theme.Paper)
}

func (n *NowPlaying) hints(s player.State) []widgets.Hint {
	var label string
	if n.focus == lyricsPill {
		label = "Lyrics"
	} else {
		label = [controlCount]string{"Shuffle", "Previous", "Pause", "Next", "Repeat"}[n.focus]
		if n.focus == playCtl && !s.Playing {
			label = "Play"
		}
	}
	live := s.Track.Live()
	if n.hintBar != nil && label == n.hintLabel && live == n.hintLive {
		return n.hintBar
	}
	hints := []widgets.Hint{{Key: "A", Label: label}, {Key: "B", Label: "Back"}}
	if n.hasLyrics(s) {
		hints = append(hints, widgets.Hint{Key: "Y", Label: "Lyrics"})
	}
	hints = append(hints, widgets.Hint{Key: "L1/R1", Label: "Track"})
	if !live {
		hints = append(hints, widgets.Hint{Key: "L2/R2", Label: "Seek"})
	}
	n.hintBar, n.hintLabel, n.hintLive = hints, label, live
	return n.hintBar
}

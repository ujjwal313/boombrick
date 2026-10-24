// Package home is the Home tab: rows of recent and saved music.
package home

import (
	"fmt"
	"slices"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/resume"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/focus"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Layout in design pixels.
const (
	contentTop = widgets.StatusHeight + widgets.TabsHeight
	contentBot = theme.ScreenH - widgets.HintsHeight
	padTop     = 14
	side       = 32
	cardSize   = 200
	tileSize   = 166
	cardGap    = 32
	tileGap    = 28
	headingGap = 20 // heading to its items
	rowGap     = 30 // items to the next heading: a 20 px gap plus a 10 px margin
)

// Home shows rows of cards and tiles and moves focus between them.
type Home struct {
	rows     []fixture.Row
	row, col int
	scroll   anim.Value
	offsets  []anim.Value // each row's sideways scroll
	focus    []focusValue // the focused item's and those still fading out
	tops     []float64    // each row's heading, from the top of the content
	set      bool         // rows have been given, even if there are none
	reading  bool         // the first scan is running with nothing indexed yet
	musicDir string       // shown on the empty state
	rescan   func()
	state    widgets.Wrapped              // the empty or reading state's description, wrapped
	hintBars map[itemHints][]widgets.Hint // made as each is first shown
}

// focusValue is an item's focus strength, 0 to 1.
type focusValue struct {
	row, col int
	k        anim.Value
}

// New creates the screen with focus on the first item. With no rows it shows
// only the chrome until SetRows.
func New(rows []fixture.Row) *Home {
	h := &Home{rows: rows, set: len(rows) > 0, offsets: make([]anim.Value, len(rows)),
		hintBars: map[itemHints][]widgets.Hint{}}
	h.value(0, 0).Set(1)
	return h
}

// SetEmpty sets what the empty state shows and what Y does there.
func (h *Home) SetEmpty(musicDir string, rescan func()) { h.musicDir, h.rescan = musicDir, rescan }

// SetReading says the first scan is running and nothing is indexed yet;
// until SetRows the screen says the music is being read.
func (h *Home) SetReading() { h.reading = true }

// SetRows replaces the rows, keeping focus on the same row and item where
// they still exist (found by title, and by title and subtitle). Otherwise
// focus stays at its position, clamped. Rows with no items are dropped.
func (h *Home) SetRows(rows []fixture.Row) {
	var rowTitle, title, subtitle string
	offsets := map[string]anim.Value{}
	for i, r := range h.rows {
		if i < len(h.offsets) {
			offsets[r.Title] = h.offsets[i]
		}
	}
	if h.row < len(h.rows) {
		r := h.rows[h.row]
		rowTitle = r.Title
		if h.col < len(r.Items) {
			title, subtitle = r.Items[h.col].Title, r.Items[h.col].Subtitle
		}
	}
	h.rows = slices.DeleteFunc(slices.Clone(rows), func(r fixture.Row) bool { return len(r.Items) == 0 })
	h.set = true
	h.tops = nil
	h.focus = h.focus[:0]
	if len(h.rows) == 0 {
		h.row, h.col = 0, 0
		h.offsets = nil
		h.scroll.Set(0)
		return
	}
	h.offsets = make([]anim.Value, len(h.rows))
	for i, r := range h.rows {
		h.offsets[i] = offsets[r.Title]
	}
	if i := slices.IndexFunc(h.rows, func(r fixture.Row) bool { return r.Title == rowTitle }); i >= 0 {
		h.row = i
		if j := slices.IndexFunc(h.rows[i].Items, func(it fixture.Item) bool {
			return it.Title == title && it.Subtitle == subtitle
		}); j >= 0 {
			h.col = j
		}
	}
	h.row = min(h.row, len(h.rows)-1)
	h.col = min(h.col, len(h.rows[h.row].Items)-1)
	h.value(h.row, h.col).Set(1)
	// Rows that lost items may be scrolled past their end, and the focused
	// item may have moved; bring it into view.
	for i := range h.offsets {
		if m := h.maxOffset(i); h.offsets[i].Target() > m {
			h.offsets[i].Set(m)
		}
	}
	h.offsets[h.row].Set(h.offsetTarget(h.row, h.col))
}

// maxRecent is how many albums the Recently added row shows.
const maxRecent = 12

// maxContinue is how many albums Continue listening shows.
const maxContinue = 4

// ContinueRow is Continue listening: albums part-way through, most recent
// first, skipping any no longer in the library. play resumes an album at a
// track and frame; queue adds it to the queue.
func ContinueRow(progress []resume.Progress, ix *library.Index, play func(a *library.Album, track int, frame int64),
	queue func(*library.Album)) (fixture.Row, bool) {
	var items []fixture.Item
	for _, pr := range progress {
		a := ix.Album(pr.Artist, pr.Album)
		if a == nil {
			continue
		}
		track := slices.IndexFunc(a.Tracks, func(t library.Track) bool { return t.Path == pr.Path })
		if track < 0 {
			continue
		}
		frame := pr.Frame
		// The badge is the continued track's: an album may mix downloads with files of its own.
		it := fixture.Item{Title: a.Title, Subtitle: a.Artist, Art: screens.AlbumArt(a),
			Source: screens.SourceOf(a.Tracks[track].Path), Progress: pr.Fraction, Play: func() { play(a, track, frame) }}
		if queue != nil {
			it.Queue = func() { queue(a) }
		}
		items = append(items, it)
		if len(items) == maxContinue {
			break
		}
	}
	if len(items) == 0 {
		return fixture.Row{}, false
	}
	return fixture.Row{Title: "Continue listening", Meta: fmt.Sprintf("%d in progress", len(items)), Cards: true,
		Continue: true, Items: items}, true
}

// AlbumRow is the Recently added row for albums found on the SD card, newest
// first; play starts an album and queue adds it to the queue. It reports false
// when there are no albums.
func AlbumRow(albums []*library.Album, play, queue func(*library.Album)) (fixture.Row, bool) {
	if len(albums) == 0 {
		return fixture.Row{}, false
	}
	items := make([]fixture.Item, min(len(albums), maxRecent))
	for i := range items {
		a := albums[i]
		items[i] = fixture.Item{Title: a.Title, Subtitle: a.Artist, Art: screens.AlbumArt(a), Source: source(a),
			Play: func() { play(a) }}
		if queue != nil {
			items[i].Queue = func() { queue(a) }
		}
	}
	meta := fmt.Sprintf("%d albums", len(albums))
	if len(albums) == 1 {
		meta = "1 album"
	}
	return fixture.Row{Title: "Recently added", Meta: meta, Items: items}, true
}

// SpotifyRow is Spotify playlists: Liked Songs, then the user's playlists,
// as kept from the last time the Spotify tab asked. A card opens its page
// with open, and imports it with imp, which may be nil. It reports false when
// no playlists are kept, as when signed out.
func SpotifyRow(pls []spotify.Playlist, open, imp func(spotify.Playlist)) (fixture.Row, bool) {
	if len(pls) == 0 {
		return fixture.Row{}, false
	}
	item := func(p spotify.Playlist, it fixture.Item) fixture.Item {
		it.Source, it.Play = media.Spotify, func() { open(p) }
		if imp != nil {
			it.Import = func() { imp(p) }
		}
		return it
	}
	liked := spotify.Playlist{URI: spotify.LikedURI, Name: "Liked Songs"}
	items := []fixture.Item{item(liked, fixture.Item{Title: "Liked Songs", Subtitle: "Your songs", Art: screens.LikedArt})}
	for _, p := range pls {
		art := widgets.SpecFor(p.Name)
		if p.Cover != "" {
			art.Cover = media.Cover{URL: p.Cover}
		}
		sub := screens.Count(p.Count) + " songs"
		if p.Count == 1 {
			sub = "1 song"
		}
		items = append(items, item(p, fixture.Item{Title: p.Name, Subtitle: sub, Art: art}))
	}
	meta := fmt.Sprintf("%d playlists", len(pls))
	if len(pls) == 1 {
		meta = "1 playlist"
	}
	return fixture.Row{Title: "Spotify playlists", Meta: meta, Cards: true, Spotify: true, Items: items}, true
}

// source is where album a's files came from, for its badge: Downloaded for
// an album filed under the downloads folder.
func source(a *library.Album) media.Source {
	if len(a.Tracks) == 0 {
		return media.Local
	}
	return screens.SourceOf(a.Tracks[0].Path)
}

// value is the focus strength of the item at row, col, at 0 the first time.
// The pointer is good until value is next called.
func (h *Home) value(row, col int) *anim.Value {
	for i := range h.focus {
		if f := &h.focus[i]; f.row == row && f.col == col {
			return &f.k
		}
	}
	h.focus = append(h.focus, focusValue{row: row, col: col})
	return &h.focus[len(h.focus)-1].k
}

// strength is the focus strength of the item at row, col at now.
func (h *Home) strength(row, col int, now time.Time) float32 {
	for i := range h.focus {
		if f := &h.focus[i]; f.row == row && f.col == col {
			return float32(f.k.At(now))
		}
	}
	return 0
}

// layout measures the rows once text metrics are available.
func (h *Home) layout(u *ui.Context) {
	if h.tops != nil || len(h.rows) == 0 {
		return
	}
	headH := float64(u.LineHeight(text.Heading))
	cardH := float64(widgets.CardHeight(u, cardSize))
	h.tops = make([]float64, len(h.rows))
	y := 0.0
	for i, r := range h.rows {
		h.tops[i] = y
		itemH := float64(tileSize)
		if r.Cards {
			itemH = cardH
		}
		y += headH + headingGap + itemH + rowGap
	}
	h.scroll.Set(h.scrollTarget())
}

// scrollTarget puts the focused row's heading directly under the tabs. The
// last row snaps there too, even though that leaves space below it, so its
// focused item is never clipped by the hint bar.
func (h *Home) scrollTarget() float64 {
	return h.tops[h.row]
}

// itemSize is a row's item size and the gap between its items.
func itemSize(r fixture.Row) (size, gap float64) {
	if r.Cards {
		return cardSize, cardGap
	}
	return tileSize, tileGap
}

// offsetTarget scrolls a row sideways as little as possible to keep item col
// between the side paddings. Its focus zoom and ring spill into them, as on
// the first item.
func (h *Home) offsetTarget(row, col int) float64 {
	size, gap := itemSize(h.rows[row])
	left := float64(col) * (size + gap)
	return focus.Scroll(h.offsets[row].Target(), left, left+size, theme.ScreenW-2*side)
}

// maxOffset is the furthest a row scrolls sideways: its last item at the
// right-hand padding, or 0 when the row fits.
func (h *Home) maxOffset(row int) float64 {
	size, gap := itemSize(h.rows[row])
	n := float64(len(h.rows[row].Items))
	return max(0, n*size+(n-1)*gap-(theme.ScreenW-2*side))
}

// Handle moves focus with the D-pad.
func (h *Home) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	if len(h.rows) == 0 {
		if ev.Button == input.Y && ev.Kind == input.Press && h.set && h.rescan != nil {
			h.rescan()
			return true
		}
		return false
	}
	if ev.Button == input.A || ev.Button == input.Y || ev.Button == input.X {
		it := h.rows[h.row].Items[h.col]
		act := it.Play
		switch ev.Button {
		case input.Y:
			act = it.Queue
		case input.X:
			act = it.Import
		}
		if act != nil && ev.Kind == input.Press {
			act()
			return true
		}
		return false
	}
	row, col := h.row, h.col
	switch ev.Button {
	case input.Left:
		col--
	case input.Right:
		col++
	case input.Up:
		row--
	case input.Down:
		row++
	default:
		return false
	}
	row = min(max(row, 0), len(h.rows)-1)
	col = min(max(col, 0), len(h.rows[row].Items)-1)
	if row == h.row && col == h.col {
		return false
	}
	// Items that have faded out are forgotten.
	h.focus = slices.DeleteFunc(h.focus, func(f focusValue) bool { return f.k.Target() == 0 && f.k.Done(now) })
	h.value(h.row, h.col).To(0, now, anim.Focus, anim.EaseOutCubic)
	h.row, h.col = row, col
	h.value(row, col).To(1, now, anim.Focus, anim.EaseOutCubic)
	h.offsets[row].To(h.offsetTarget(row, col), now, anim.Focus, anim.EaseOutCubic)
	if h.tops != nil {
		h.scroll.To(h.scrollTarget(), now, anim.Focus, anim.EaseOutCubic)
	}
	return true
}

// Animating reports whether focus or scrolling is still moving.
func (h *Home) Animating(now time.Time) bool {
	if !h.scroll.Done(now) {
		return true
	}
	for i := range h.offsets {
		if !h.offsets[i].Done(now) {
			return true
		}
	}
	for i := range h.focus {
		if !h.focus[i].k.Done(now) {
			return true
		}
	}
	return false
}

// Draw renders the frame.
func (h *Home) Draw(u *ui.Context) {
	if len(h.rows) == 0 {
		h.drawEmpty(u)
		return
	}
	h.layout(u)
	now := u.Now
	widgets.DrawBackground(u, widgets.TabGround)

	u.PushClip(gfx.Rect{X: 0, Y: contentTop, W: theme.ScreenW, H: contentBot - contentTop})
	base := float32(contentTop+padTop) - float32(h.scroll.At(now))
	headH, cardH := u.LineHeight(text.Heading), widgets.CardHeight(u, cardSize)
	var fx, fy, fsize float32 // where the focused item goes
	for ri := range h.rows {
		r := &h.rows[ri]
		top := base + float32(h.tops[ri])
		itemsTop := top + headH + headingGap
		itemH := float32(tileSize)
		if r.Cards {
			itemH = cardH
		}
		// A row out of view draws only its items with focus, whose glows
		// reach into view.
		shown := top < contentBot && itemsTop+itemH > contentTop
		if shown {
			drawHeading(u, r, top)
		}
		s, g := itemSize(*r)
		size, gap := float32(s), float32(g)
		shift := float32(h.offsets[ri].At(now))
		for ci := range r.Items {
			x := side + float32(ci)*(size+gap) - shift
			if ri == h.row && ci == h.col {
				fx, fy, fsize = x, itemsTop, size // drawn last so its ring and glow sit on top
				continue
			}
			k := h.strength(ri, ci, now)
			if k == 0 && (!shown || x+size < 0 || x > theme.ScreenW) {
				continue
			}
			drawItem(u, r, ci, x, itemsTop, size, k)
		}
	}
	drawItem(u, &h.rows[h.row], h.col, fx, fy, fsize, h.strength(h.row, h.col, now))
	u.PopClip()

	widgets.StatusBar(u)
	widgets.Tabs(u, screens.HomeTab)
	widgets.HintBar(u, h.hints())
}

// drawEmpty draws the chrome alone before the first rows arrive, or that the
// first scan is reading the music, then the empty-library state.
func (h *Home) drawEmpty(u *ui.Context) {
	switch {
	case h.set:
		widgets.DrawBackground(u, widgets.StateGround)
		widgets.EmptyState(u, &h.state, "Your library is empty",
			"Copy music to the Music folder on the SD card, or find something on YouTube Music.", h.musicDir)
	case h.reading:
		screens.DrawReading(u, &h.state, h.musicDir)
	default:
		widgets.DrawBackground(u, widgets.TabGround)
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, screens.HomeTab)
	widgets.HintBar(u, h.hints())
}

func drawHeading(u *ui.Context, r *fixture.Row, top float32) {
	u.Text(r.Title, text.Heading, side, top, theme.Paper)
	baseline := top + u.Ascent(text.Heading)
	w := u.TextWidth(r.Meta, text.Meta)
	u.Text(r.Meta, text.Meta, theme.ScreenW-side-w, u.Baseline(text.Meta, baseline), theme.Ink3)
}

// drawItem draws row r's item ci with its art's top-left at (x, y), size
// wide, at focus strength k.
func drawItem(u *ui.Context, r *fixture.Row, ci int, x, y, size, k float32) {
	it := &r.Items[ci]
	widgets.CardGlow(u, x, y, size, k)
	widgets.Card(u, widgets.CardItem{Title: it.Title, Subtitle: it.Subtitle, Art: it.Art, Source: it.Source,
		Badge: r.Continue, Progress: it.Progress}, x, y, size, k, r.Cards)
}

// The hint bars that don't depend on the focused item, and those for sample
// content, which has nothing to play.
var (
	tabHints      = []widgets.Hint{{Key: "L1/R1", Label: "Tabs"}}
	rescanHints   = []widgets.Hint{{Key: "Y", Label: "Rescan SD card"}, {Key: "L1/R1", Label: "Tabs"}}
	playlistHints = []widgets.Hint{
		{Key: "A", Label: "Open"}, {Key: "X", Label: "Import"},
		{Key: "Y", Label: "Add to queue"}, {Key: "L1/R1", Label: "Tabs"},
	}
	sampleHints = []widgets.Hint{
		{Key: "A", Label: "Play"}, {Key: "B", Label: "Back"}, {Key: "X", Label: "Download"},
		{Key: "Y", Label: "Add to queue"}, {Key: "L1/R1", Label: "Tabs"},
	}
)

// itemHints is what a playable item's hint bar depends on: whether A opens it,
// as a Spotify playlist's page, and whether X imports it and Y queues it.
type itemHints struct{ open, imp, queue bool }

func (h *Home) hints() []widgets.Hint {
	if len(h.rows) == 0 {
		if h.set {
			return rescanHints
		}
		return tabHints
	}
	r := &h.rows[h.row]
	it := &r.Items[h.col]
	if it.Play == nil {
		if r.Spotify {
			return playlistHints
		}
		return sampleHints
	}
	k := itemHints{open: r.Spotify, imp: it.Import != nil, queue: it.Queue != nil}
	if hints, ok := h.hintBars[k]; ok {
		return hints
	}
	hints := []widgets.Hint{{Key: "A", Label: "Play"}}
	if k.open {
		hints[0].Label = "Open"
	}
	if k.imp {
		hints = append(hints, widgets.Hint{Key: "X", Label: "Import"})
	}
	if k.queue {
		hints = append(hints, widgets.Hint{Key: "Y", Label: "Add to queue"})
	}
	hints = append(hints, widgets.Hint{Key: "L1/R1", Label: "Tabs"})
	h.hintBars[k] = hints
	return hints
}

package browse

import (
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/audiobooks"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// BookPage is a book's detail page, opened from the Books tab: the cover, the
// title and author on the left; on the right who reads it, the series, the
// length, where the listener is in it and the publisher's description, which
// Up and Down scroll. A downloads or plays the book, as in the tab, and Y
// adds it to the queue once it is on the card. It reads the book from the
// catalogue as it draws, so a download's progress shows.
type BookPage struct {
	books   *Books
	asin    string
	scroll  int             // lines of the description scrolled away
	title   widgets.Wrapped // the title, wrapped
	summary widgets.Wrapped // the description, wrapped
	limit   int             // the most the description can scroll, as of the last frame
}

// NewBookPage opens the page of the book with that ASIN.
func NewBookPage(b *Books, asin string) *BookPage { return &BookPage{books: b, asin: asin} }

// entry is the book as the catalogue has it now.
func (pg *BookPage) entry() (audiobooks.Entry, bool) {
	for _, e := range pg.books.cat.Entries() {
		if e.ASIN == pg.asin {
			return e, true
		}
	}
	return audiobooks.Entry{}, false
}

// Handle scrolls the description with Up and Down, and does with A and Y
// what they do in the tab.
func (pg *BookPage) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	switch ev.Button {
	case input.Up, input.Down:
		d := 1
		if ev.Button == input.Up {
			d = -1
		}
		n := min(max(pg.scroll+d, 0), pg.limit)
		if n == pg.scroll {
			return false
		}
		pg.scroll = n
		return true
	case input.A, input.Y:
		if ev.Kind != input.Press {
			return false
		}
		if e, ok := pg.entry(); ok {
			pg.books.act(e, ev.Button == input.Y)
		}
		return true
	}
	return false
}

// Animating reports whether a download's progress is showing.
func (pg *BookPage) Animating(now time.Time) bool {
	e, ok := pg.entry()
	return ok && (e.State == audiobooks.Downloading || e.State == audiobooks.Decrypting || e.State == audiobooks.Queued)
}

// facts are the lines of what the page says of a book, apart from its
// description.
func facts(e audiobooks.Entry) []string {
	var fs []string
	if e.Series != "" {
		fs = append(fs, seriesOf(e))
	}
	if e.Narrators != "" {
		fs = append(fs, "Read by "+e.Narrators)
	}
	if l := length(e.Length); l != "" {
		fs = append(fs, l)
	}
	switch {
	case e.State == audiobooks.Failed:
		fs = append(fs, e.Err)
	case e.State == audiobooks.Queued:
		fs = append(fs, "Waiting to download")
	case e.State == audiobooks.Downloading:
		fs = append(fs, "Downloading, "+strconv.Itoa(int(e.Progress*100))+"%")
	case e.State == audiobooks.Decrypting:
		fs = append(fs, "Unlocking")
	case e.Finished:
		fs = append(fs, "Finished")
	case e.Percent >= 1:
		fs = append(fs, strconv.Itoa(int(e.Percent))+"% listened")
	}
	if e.Path != "" {
		fs = append(fs, "On this card")
	}
	return fs
}

// Draw renders the frame.
func (pg *BookPage) Draw(u *ui.Context) {
	widgets.DrawBackground(u, widgets.TabGround)
	e, ok := pg.entry()
	if !ok { // the catalogue dropped it; B goes back
		widgets.StatusBar(u)
		widgets.Tabs(u, screens.BooksTab)
		widgets.HintBar(u, []widgets.Hint{{Key: "B", Label: "Back"}, {Key: "L1/R1", Label: "Tabs"}})
		return
	}
	spec := widgets.SpecFor(e.Title)
	if e.Cover != "" {
		spec.Cover = media.Cover{URL: e.Cover}
	}
	widgets.Art(u, spec, albumArt, widgets.CardRadius, gfx.Rect{X: side, Y: albumTop, W: albumArt, H: albumArt})
	y := float32(albumTop + albumArt + pad)
	tl := pg.title.Lines(u, e.Title, text.Heading, albumArt)
	for i, l := range tl[:min(len(tl), 2)] { // a book's title is long: two lines, the second cut short
		if i == 1 && len(tl) > 2 {
			l = tl[1] + " " + tl[2]
		}
		u.Text(u.Fit(l, text.Heading, albumArt), text.Heading, side, y, theme.Paper)
		y += u.LineHeight(text.Heading)
	}
	u.Text(u.Fit(e.Authors, text.Meta, albumArt), text.Meta, side, y, theme.Ink2)
	py := float32(contentBot - pad - widgets.PillHeight)
	icon, label := "play", "Play"
	switch {
	case e.Path != "":
	case e.State == audiobooks.Queued || e.State == audiobooks.Downloading || e.State == audiobooks.Decrypting:
		icon, label = "", "On its way"
	case e.State == audiobooks.Failed:
		icon, label = "download", "Try again"
	default:
		icon, label = "download", "Download"
	}
	widgets.PillGlow(u, icon, label, side, py, 1)
	widgets.Pill(u, icon, label, side, py, 1)

	// The right column: the facts, then the description under them.
	w := float32(theme.ScreenW - side - listX)
	lh := u.LineHeight(text.Body)
	y = contentTop
	for _, f := range facts(e) {
		u.Text(u.Fit(f, text.Body, w), text.Body, listX, y, theme.Paper)
		y += lh
	}
	y += pad
	lines := pg.summary.Lines(u, e.Summary, text.Meta, w)
	mh := u.LineHeight(text.Meta)
	room := max(int((contentBot-y)/mh), 1)
	pg.limit = max(len(lines)-room, 0)
	pg.scroll = min(pg.scroll, pg.limit)
	for i := pg.scroll; i < len(lines) && i < pg.scroll+room; i++ {
		u.Text(lines[i], text.Meta, listX, y, theme.Ink2)
		y += mh
	}
	if len(lines) == 0 && e.Summary == "" {
		u.Text("No description yet.", text.Meta, listX, y, theme.Ink3)
	}

	widgets.StatusBar(u)
	widgets.Tabs(u, screens.BooksTab)
	widgets.HintBar(u, pg.hints(e))
}

func (pg *BookPage) hints(e audiobooks.Entry) []widgets.Hint {
	label := "Download"
	switch {
	case e.Path != "":
		label = "Play"
	case e.State == audiobooks.Queued || e.State == audiobooks.Downloading || e.State == audiobooks.Decrypting:
		label = "On its way"
	case e.State == audiobooks.Failed:
		label = "Try again"
	}
	hs := []widgets.Hint{{Key: "A", Label: label}}
	if e.Path != "" {
		hs = append(hs, widgets.Hint{Key: "Y", Label: "Add to queue"})
	}
	if pg.limit > 0 {
		hs = append(hs, widgets.Hint{Key: "▲ ▼", Label: "Scroll"})
	}
	return append(hs, widgets.Hint{Key: "B", Label: "Back"}, widgets.Hint{Key: "L1/R1", Label: "Tabs"})
}

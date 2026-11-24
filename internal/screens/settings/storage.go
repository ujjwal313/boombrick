package settings

import (
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/space"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// What the Storage page lists, under the card.
var cacheNames = [...]string{"Art cache", "Lyrics", "Fonts", "Partial downloads", "Downloader"}

var valueText = text.Style{Face: text.Figtree, Size: 22, Tabular: true}

// cacheRowH is the height of the page's rows: lower than Settings' rows, which
// take focus, so that the card, the five rows and the pill fit above the hints.
const cacheRowH = 48

// storagePage is the Storage page: the card, what the
// caches take, and Clear caches, with a dialog to confirm it.
type storagePage struct {
	space  Space
	card   screens.StorageCard
	sizes  space.Sizes
	shown  [len(cacheNames)]string // the sizes as written, "Measuring…" until known
	dialog *confirm
	hints  []widgets.Hint
}

func newStoragePage(sp Space, now time.Time) *storagePage {
	sp.Cleared() // a clear that ended after the page was left isn't news now
	sp.Measure()
	p := &storagePage{space: sp, hints: []widgets.Hint{{Key: "A", Label: "Clear caches"}, {Key: "B", Label: "Back"}}}
	for i := range p.shown {
		p.shown[i] = "Measuring…"
	}
	return p
}

// handle opens the dialog with A, and passes input to it while it is open.
func (p *storagePage) handle(s *Screen, ev input.Event, now time.Time) bool {
	if p.dialog != nil {
		if p.dialog.handle(ev, now) && p.dialog.confirmed {
			p.space.Clear()
			for i := range p.shown {
				p.shown[i] = "Measuring…"
			}
		}
		return true
	}
	if ev.Kind == input.Press && ev.Button == input.A {
		p.dialog = newConfirm(widgets.Dialog{Icon: "trash", IconColor: theme.GoldSoft, Title: "Clear caches?",
			Body:  "Covers, lyrics and fonts are fetched again when needed. Your music, downloads and the downloader stay.",
			Pills: []widgets.DialogPill{{Label: "Clear"}, {Label: "Cancel"}}}, now)
		return true
	}
	return false
}

// back closes the dialog, or the page.
func (p *storagePage) back(s *Screen, now time.Time) bool {
	if p.dialog != nil {
		p.dialog.close(now)
		return true
	}
	s.storage = nil
	return true
}

func (p *storagePage) animating(now time.Time) bool {
	return p.dialog != nil && p.dialog.animating(now)
}

// refresh takes new sizes, and a finished clear's result for its toast.
func (p *storagePage) refresh(s *Screen) {
	if s.opts.Card != nil {
		p.card.Set(s.opts.Card())
	}
	if sz, ok := p.space.Sizes(); ok && (sz != p.sizes || p.shown[0] == "Measuring…") {
		p.sizes = sz
		for i, n := range [...]int64{sz.Art, sz.Lyrics, sz.Fonts, sz.Parts, sz.Downloader} {
			p.shown[i] = screens.Size(n)
		}
	}
	if freed, ok := p.space.Cleared(); ok && s.opts.Toast != nil {
		s.opts.Toast("Freed " + screens.Size(freed))
	}
}

func (p *storagePage) draw(s *Screen, u *ui.Context) {
	p.refresh(s)
	widgets.StatusBar(u)
	header(u, "Storage")
	y := listTop(u)
	p.card.Draw(u, gfx.Rect{X: side, Y: y, W: theme.ScreenW - 2*side, H: screens.StorageCardHeight})
	y += screens.StorageCardHeight + 12
	for i, name := range cacheNames {
		cy := u.CenterY(labelText, y, cacheRowH)
		u.Text(name, labelText, side, cy, theme.Paper)
		w := u.TextWidth(p.shown[i], valueText)
		u.Text(p.shown[i], valueText, theme.ScreenW-side-w, u.CenterY(valueText, y, cacheRowH), theme.Ink2)
		if i < len(cacheNames)-1 {
			u.Fill(gfx.Rect{X: side, Y: y + cacheRowH - 1, W: theme.ScreenW - 2*side, H: 1}, theme.PaperA(0.14))
		}
		y += cacheRowH
	}
	y += 16
	widgets.PillGlow(u, "trash", "Clear caches", side, y, 1)
	widgets.Pill(u, "trash", "Clear caches", side, y, 1)
	if p.dialog != nil {
		if p.dialog.gone(u.Now) {
			p.dialog = nil
		} else {
			p.dialog.draw(u)
			widgets.HintBar(u, p.dialog.hints())
			return
		}
	}
	widgets.HintBar(u, p.hints)
}

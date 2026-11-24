package spotify

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

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
)

// The tab's layout: the heading and the account under the tabs and the list
// below them, the full width; signed out, a card in the middle.
const (
	headTop = screens.ContentTop + 8
	listTop = headTop + 52
	chip    = 40 // the account's initial, in an accent circle
	cardW   = 460
	cardPad = 24
)

var (
	nameText    = text.Style{Face: text.Figtree, Size: 22}
	premiumText = text.Style{Face: text.Figtree, Size: 20}
)

// The tab's hints, kept so that no frame builds them.
var (
	libraryHints   = []widgets.Hint{{Key: "A", Label: "Open"}, tabsHint}
	retryListHints = []widgets.Hint{{Key: "A", Label: "Open"}, {Key: "X", Label: "Retry"}, tabsHint}
	importHints    = []widgets.Hint{{Key: "A", Label: "Open"}, {Key: "Y", Label: "Import"}, tabsHint}
	signInHints    = []widgets.Hint{{Key: "A", Label: "Sign in"}, tabsHint}
	failedHints    = []widgets.Hint{retryHint, tabsHint}
	offlineHints   = []widgets.Hint{{Key: "A", Label: "Open Downloads"}, tabsHint}
	tabsHints      = []widgets.Hint{tabsHint}
)

// Tab is the Spotify tab: signed out, a card that opens the sign-in; signed
// in, the user's library, Liked Songs first and then their playlists.
type Tab struct {
	e     *Env
	list  views.Grid
	rows  []widgets.Row
	arts  []widgets.ArtSpec
	pls   []spotify.Playlist // the playlists the rows were made from
	liked int                // Liked Songs' song count the first row says, -1 while unknown
	msg   widgets.Wrapped
	body  widgets.Wrapped // the sign-in card's line
	off   widgets.Wrapped // the offline state's line

	// The library as last asked: again once it has changed, and a second on,
	// as asking is what has it ask the helper again for the status and for
	// lists that are old or failed.
	version      uint64
	asked        time.Time
	st           spotify.Status
	err, listErr error
	initial      string // the account's, in its circle

	// What the frame drawn last showed, for Handle: signed out, the status
	// not to be had, offline with no playlists kept, the playlists not to be
	// had, or known and none.
	signedOut, failed, offline, listFailed, empty bool
}

// NewTab makes the tab.
func NewTab(e *Env) *Tab {
	t := &Tab{e: e, liked: -1,
		list: views.Grid{Cols: 1, CellW: theme.ScreenW - 2*screens.Side, CellH: widgets.RowHeight, Pad: screens.Pad, ViewH: screens.ContentBot - listTop}}
	t.setRows(nil, -1)
	return t
}

// setRows makes the rows again for pls, keeping focus on the same row.
func (t *Tab) setRows(pls []spotify.Playlist, liked int) {
	focus := t.list.Focus()
	t.pls, t.liked = pls, liked
	t.arts = make([]widgets.ArtSpec, 1+len(pls))
	t.rows = make([]widgets.Row, 1+len(pls))
	t.arts[0] = screens.LikedArt
	t.rows[0] = widgets.Row{Art: &t.arts[0], Title: likedTitle}
	if liked >= 0 {
		t.rows[0].Subtitle = songs(liked)
	}
	for i, p := range pls {
		t.arts[i+1] = art(p.Name, p.Cover)
		sub := songs(p.Count)
		if o := owner(p); o != "" {
			sub += " · " + o
		}
		t.rows[i+1] = widgets.Row{Art: &t.arts[i+1], Title: p.Name, Subtitle: sub}
	}
	t.list.SetCountFocus(len(t.rows), min(focus, len(t.rows)-1))
}

// refresh takes the library as it now stands, asking it again when it has
// changed or a second on.
func (t *Tab) refresh(now time.Time) {
	if v := t.e.Src.Version(); v != t.version || now.Sub(t.asked) >= time.Second {
		t.version, t.asked = v, now
		t.ask()
	}
	// Offline with nothing kept to browse, the tab says it is offline. With
	// playlists kept, they are still shown.
	t.offline = !t.signedOut && !t.failed && t.st.State != "" && len(t.pls) == 0 &&
		(t.st.State == spotify.Offline || !t.e.online())
}

// ask asks the library for the status, the playlists and Liked Songs' count,
// making the rows again when they have changed.
func (t *Tab) ask() {
	st, err := t.e.Src.Status()
	t.st, t.err, t.listErr = st, err, nil
	t.signedOut = err == nil && (st.State == spotify.SignedOut || st.State == spotify.SigningIn)
	t.failed = err != nil && st.State == ""
	name := st.Name
	if name == "" {
		name = st.User
	}
	t.initial = ""
	if r, _ := utf8.DecodeRuneInString(name); r != utf8.RuneError {
		t.initial = strings.ToUpper(string(r))
	}
	if t.signedOut || t.failed || st.State == "" {
		return
	}
	pls, have, perr := t.e.Src.Playlists()
	liked := -1
	if ts, ok, _ := t.e.Src.Tracks(spotify.LikedURI); ok {
		liked = len(ts)
	}
	if !slices.Equal(pls, t.pls) || liked != t.liked {
		t.setRows(pls, liked)
	}
	t.listFailed, t.empty = !have && perr != nil, have && len(pls) == 0
	if !have {
		t.listErr = perr
	}
}

// Handle opens the focused row's page with A, or the sign-in when signed
// out; X asks again for a library that failed, and Y starts an import.
func (t *Tab) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	switch {
	case t.failed:
		if ev.Kind == input.Press && ev.Button == input.A {
			t.retry()
			return true
		}
		return false
	case t.signedOut:
		if ev.Kind == input.Press && ev.Button == input.A {
			t.e.Nav.Push(NewSignIn(t.e))
			return true
		}
		return false
	case t.offline:
		return ev.Kind == input.Press && ev.Button == input.A && screens.OpenDownloads(t.e.Nav)
	}
	if ev.Kind == input.Press {
		switch ev.Button {
		case input.A:
			if f := t.list.Focus(); f == 0 {
				t.e.Nav.Push(NewLikedPage(t.e))
			} else {
				t.e.Nav.Push(NewPlaylistPage(t.e, t.pls[f-1]))
			}
			return true
		case input.X:
			if t.listFailed {
				t.retry()
				return true
			}
			return false
		case input.Y:
			if !t.e.CanImport() {
				return false
			}
			t.e.Nav.Push(NewChoose(t.e))
			return true
		}
	}
	return t.list.Move(ev.Button, now)
}

// retry has the library ask again for what failed, from the next frame.
func (t *Tab) retry() {
	t.e.Src.Retry()
	t.asked = time.Time{}
}

// Animating reports whether the list is moving.
func (t *Tab) Animating(now time.Time) bool { return t.list.Animating(now) }

// Draw renders the frame.
func (t *Tab) Draw(u *ui.Context) {
	t.refresh(u.Now)
	st, err, listErr := t.st, t.err, t.listErr
	if t.offline {
		widgets.DrawBackground(u, widgets.StateGround)
		// With YouTube off there is no Downloads tab to open.
		dl := screens.DownloadsOn()
		widgets.OfflineState(u, &t.off, "Spotify needs Wi-Fi or a hotspot — your downloads still play.", dl)
		widgets.StatusBar(u)
		widgets.Tabs(u, tab)
		if dl {
			widgets.HintBar(u, offlineHints)
		} else {
			widgets.HintBar(u, tabsHints)
		}
		return
	}
	widgets.DrawBackground(u, widgets.TabGround)
	content := gfx.Rect{X: screens.Side, Y: screens.ContentTop, W: theme.ScreenW - 2*screens.Side,
		H: screens.ContentBot - screens.ContentTop}
	var hints []widgets.Hint
	switch {
	case t.failed:
		screens.Message(u, &t.msg, failText(err), content)
		hints = failedHints
	case t.signedOut:
		t.drawSignedOut(u)
		hints = signInHints
	case st.State == "":
		screens.Message(u, &t.msg, "Loading…", content)
		hints = tabsHints
	default:
		t.drawLibrary(u, st, listErr)
		hints = libraryHints
		switch {
		case t.listFailed:
			hints = retryListHints
		case t.e.CanImport():
			hints = importHints
		}
	}
	widgets.StatusBar(u)
	widgets.Tabs(u, tab)
	widgets.HintBar(u, hints)
}

// drawSignedOut draws the card that leads to signing in.
func (t *Tab) drawSignedOut(u *ui.Context) {
	lines := t.body.Lines(u, "Stream your playlists and Liked Songs. Needs Spotify Premium.", text.Meta, cardW-2*cardPad)
	h := cardPad + 32 + 16 + u.LineHeight(text.Heading) + float32(len(lines))*u.LineHeight(text.Meta) + 20 +
		widgets.PillHeight + cardPad
	box := gfx.Rect{X: (theme.ScreenW - cardW) / 2, Y: screens.ContentTop + (screens.ContentBot-screens.ContentTop-h)/2,
		W: cardW, H: h}
	u.Shape(raster.Shape{W: int(box.W), H: int(box.H), Radius: 16, Kind: raster.Ring, RingOffset: -1.5, RingWidth: 1.5},
		box, theme.PaperA(0.18))
	x, y := box.X+cardPad, box.Y+cardPad
	widgets.Badge(u, media.Spotify, x, y, false)
	y += 32 + 16
	u.Text("Sign in to Spotify", text.Heading, x, y, theme.Paper)
	y += u.LineHeight(text.Heading)
	for _, l := range lines {
		u.Text(l, text.Meta, x, y, theme.Ink2)
		y += u.LineHeight(text.Meta)
	}
	y += 20
	widgets.PillGlow(u, "user", "Sign in", x, y, 1)
	widgets.Pill(u, "user", "Sign in", x, y, 1)
}

// drawLibrary draws the heading, the account and the list, with what the
// playlists' asking says below Liked Songs while there are none.
func (t *Tab) drawLibrary(u *ui.Context, st spotify.Status, listErr error) {
	u.Text("Your library", text.Heading, screens.Side, headTop, theme.Paper)
	t.drawAccount(u, st)
	u.PushClip(screens.GlowArea)
	t.list.Draw(u.Now, screens.Side, listTop, func(i int, x, y, k float32) {
		box := gfx.Rect{X: x, Y: y, W: t.list.CellW, H: widgets.RowHeight}
		widgets.RowGlow(u, box, widgets.RowRadius, k)
		u.PushClip(screens.Below(listTop))
		widgets.ListRow(u, t.rows[i], box, k)
		u.PopClip()
	})
	u.PopClip()
	if len(t.pls) == 0 {
		s := "Loading your playlists…"
		switch {
		case t.empty:
			s = "No playlists yet."
		case listErr != nil:
			s = failText(listErr)
		}
		top := float32(listTop + screens.Pad + widgets.RowHeight)
		screens.Message(u, &t.msg, s, gfx.Rect{X: screens.Side, Y: top, W: t.list.CellW, H: screens.ContentBot - top})
	}
}

// drawAccount draws the account's initial in an accent circle, then its name
// over "Premium", at the right of the heading's line.
func (t *Tab) drawAccount(u *ui.Context, st spotify.Status) {
	name := st.Name
	if name == "" {
		name = st.User
	}
	premium := ""
	if st.Premium {
		premium = "Premium"
	}
	w := max(u.TextWidth(name, nameText), u.TextWidth(premium, premiumText))
	x := theme.ScreenW - screens.Side - w
	cx := x - 12 - chip
	u.Shape(raster.Shape{W: chip, H: chip, Radius: chip / 2, Kind: raster.Fill}, gfx.Rect{X: cx, Y: headTop, W: chip, H: chip},
		u.Accent.Color)
	if t.initial != "" {
		u.Text(t.initial, nameText, cx+(chip-u.TextWidth(t.initial, nameText))/2, u.CenterY(nameText, headTop, chip), theme.Ground)
	}
	y := float32(headTop)
	if premium == "" {
		y = u.CenterY(nameText, headTop, chip)
	}
	u.Text(name, nameText, x, y, theme.Paper)
	if premium != "" {
		u.Text(premium, premiumText, x, y+u.LineHeight(nameText), theme.Ink3)
	}
}

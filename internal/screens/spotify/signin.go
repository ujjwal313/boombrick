package spotify

import (
	"fmt"
	"image"
	"image/color"
	"strconv"
	"time"

	"rsc.io/qr"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// The sign-in's layout: the steps and the code on the left, the QR code on
// its paper tile on the right.
const (
	signTop  = widgets.StatusHeight + 24
	stepDot  = 36
	qrSize   = 220
	qrPad    = 22
	rightW   = qrSize + 2*qrPad
	rightX   = theme.ScreenW - screens.Side - rightW
	leftW    = rightX - 2*screens.Side
	codeTile = 18
)

var (
	signTitle = text.Style{Face: text.OutfitSemiBold, Size: 44}
	stepText  = text.Style{Face: text.Figtree, Size: 24}
	urlText   = text.Style{Face: text.Figtree, Size: 22}
	codeText  = text.Style{Face: text.OutfitSemiBold, Size: 44, Tabular: true}
	waitText  = text.Style{Face: text.Figtree, Size: 24}
	countText = text.Style{Face: text.Figtree, Size: 20, Tabular: true}
)

// steps say what to do on the phone.
var steps = [...]string{
	"Scan the code, or open the link on your phone.",
	"Enter the code below if asked.",
	"Approve Boombrick in Spotify.",
}

var (
	codeHints     = []widgets.Hint{{Key: "X", Label: "New code"}, {Key: "B", Label: "Cancel"}}
	cancelHints   = []widgets.Hint{{Key: "B", Label: "Cancel"}}
	signInRetries = []widgets.Hint{{Key: "X", Label: "Try again"}, {Key: "B", Label: "Cancel"}}
)

// SignIn signs in to Spotify with a device code: the link and the code, and
// the link as a QR code for a phone's camera. It closes by itself once the
// code is approved on the phone.
type SignIn struct {
	nav   screens.Nav
	src   Source
	code  *spotify.Login // the code last shown
	steps [len(steps)]widgets.Wrapped
	msg   widgets.Wrapped
	done  bool
}

// NewSignIn starts a sign-in.
func NewSignIn(e *Env) *SignIn {
	e.Src.Login()
	return &SignIn{nav: e.Nav, src: e.Src}
}

// Handle asks for a new code with X; B cancels, as on any page.
func (s *SignIn) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Press && ev.Button == input.X && !s.done {
		s.code = nil
		s.src.Login()
		return true
	}
	return false
}

// Animating is false: the page is drawn again each second (Due) and when an
// answer comes.
func (s *SignIn) Animating(time.Time) bool { return false }

// Due is the next whole second after t, for the code's countdown, and so the
// status is asked each second.
func (s *SignIn) Due(t time.Time) (time.Time, bool) {
	return t.Truncate(time.Second).Add(time.Second), !s.done
}

// Draw renders the frame, and closes the page once the code is approved.
func (s *SignIn) Draw(u *ui.Context) {
	st, err := s.src.Status()
	if err == nil {
		err = s.src.LoginErr()
	}
	if st.State == spotify.Ready && !s.done {
		s.done = true
		name := st.Name
		if name == "" {
			name = st.User
		}
		s.nav.Pop()
		s.nav.Toast("Signed in as " + name)
	}
	if st.State == spotify.SigningIn && st.Login != nil {
		s.code = st.Login
	}
	widgets.DrawBackground(u, widgets.StateGround)
	widgets.StatusBar(u)
	s.drawSteps(u)

	right := gfx.Rect{X: rightX, Y: signTop, W: rightW, H: screens.ContentBot - signTop}
	expired := s.code != nil && !u.Now.Before(s.code.Expires) || s.code != nil && st.State == spotify.SignedOut
	hints := codeHints
	switch {
	case err != nil && s.code == nil:
		screens.Message(u, &s.msg, failText(err), right)
		hints = signInRetries
	case s.code == nil:
		screens.Message(u, &s.msg, "Getting a code…", right)
		hints = cancelHints
	default:
		s.drawCode(u, right, expired)
	}
	widgets.HintBar(u, hints)
}

// drawSteps draws the title, the three steps and the code's panel on the
// left.
func (s *SignIn) drawSteps(u *ui.Context) {
	x, y := float32(screens.Side), float32(signTop)
	widgets.LogoMark(u, 30, x, y, theme.Gold)
	y += 52
	u.Text("Sign in to Spotify", signTitle, x, y, theme.Paper)
	y += u.LineHeight(signTitle) + 12
	for i, step := range steps {
		u.Shape(raster.Shape{W: stepDot, H: stepDot, Radius: stepDot / 2, Kind: raster.Fill},
			gfx.Rect{X: x, Y: y, W: stepDot, H: stepDot}, theme.Gold)
		n := strconv.Itoa(i + 1)
		u.Text(n, stepText, x+(stepDot-u.TextWidth(n, stepText))/2, u.CenterY(stepText, y, stepDot), theme.Ground)
		lines := s.steps[i].Lines(u, step, stepText, leftW-stepDot-14)
		ty := y + (stepDot-u.LineHeight(stepText))/2
		for _, l := range lines {
			u.Text(l, stepText, x+stepDot+14, ty, theme.Paper)
			ty += u.LineHeight(stepText)
		}
		y = max(y+stepDot, ty) + 12
	}
	if s.code == nil {
		return
	}
	y += 8
	box := gfx.Rect{X: x, Y: y, W: leftW, H: 20 + u.LineHeight(urlText) + u.LineHeight(codeText) + 16}
	u.Shape(raster.Shape{W: int(box.W), H: int(box.H), Radius: 14, Kind: raster.Fill}, box, theme.PaperA(0.06))
	u.Text(u.Fit(s.code.URL, urlText, box.W-40), urlText, box.X+20, box.Y+14, theme.Ink2)
	u.Text(s.code.Code, codeText, box.X+20, box.Y+14+u.LineHeight(urlText), theme.Paper)
}

// drawCode draws the QR code on its tile, and below it whether the code waits
// or has expired.
func (s *SignIn) drawCode(u *ui.Context, box gfx.Rect, expired bool) {
	tile := gfx.Rect{X: box.X, Y: box.Y, W: rightW, H: rightW}
	u.Shape(raster.Shape{W: rightW, H: rightW, Radius: codeTile, Kind: raster.Fill}, tile, theme.Paper)
	url := s.code.URL
	u.Image("qr:"+url, func() *image.NRGBA { return qrImage(url, u.Px(qrSize)) },
		gfx.Rect{X: tile.X + qrPad, Y: tile.Y + qrPad, W: qrSize, H: qrSize}, theme.Ground)
	y := tile.Y + rightW + 18
	if expired {
		u.Text("Code expired", waitText, box.X, y, theme.Ink2)
		u.Text("Press X for a new one", countText, box.X, y+u.LineHeight(waitText), theme.Ink3)
		return
	}
	u.Text("Waiting for approval…", waitText, box.X, y, theme.GoldSoft)
	left := max(s.code.Expires.Sub(u.Now), 0)
	u.Text(fmt.Sprintf("Code expires in %d:%02d", int(left/time.Minute), int(left%time.Minute/time.Second)), countText,
		box.X, y+u.LineHeight(waitText), theme.Ink3)
}

// qrImage draws url's QR code px square: white modules on clear, to be
// tinted, with a quiet margin of two modules.
func qrImage(url string, px int) *image.NRGBA {
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		return nil
	}
	n := code.Size + 4
	m := max(px/n, 1)
	off := (px - m*code.Size) / 2
	img := image.NewNRGBA(image.Rect(0, 0, px, px))
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	for y := range code.Size {
		for x := range code.Size {
			if !code.Black(x, y) {
				continue
			}
			for dy := range m {
				for dx := range m {
					img.SetNRGBA(off+x*m+dx, off+y*m+dy, white)
				}
			}
		}
	}
	return img
}

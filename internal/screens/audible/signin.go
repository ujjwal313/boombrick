// Package audible is the page that signs the device in to Audible.
package audible

import (
	"image"
	"image/color"
	"strconv"
	"time"

	"rsc.io/qr"

	"github.com/ujjwal313/boombrick/internal/audible"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/raster"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// The sign-in's layout: the steps on the left, the QR code on its paper tile
// on the right.
const (
	signTop = widgets.StatusHeight + 24
	stepDot = 36
	qrSize  = 220
	qrPad   = 22
	rightW  = qrSize + 2*qrPad
	rightX  = theme.ScreenW - screens.Side - rightW
	leftW   = rightX - 2*screens.Side
	tile    = 18
)

var (
	signTitle = text.Style{Face: text.OutfitSemiBold, Size: 44}
	stepText  = text.Style{Face: text.Figtree, Size: 24}
	urlText   = text.Style{Face: text.Figtree, Size: 22}
	waitText  = text.Style{Face: text.Figtree, Size: 24}
)

// steps say what to do on the phone.
var steps = [...]string{
	"Scan the code, or open the link on your phone, on this Wi-Fi.",
	"Open Amazon's sign-in and sign in.",
	"Paste the address you end on, and tap Sign in.",
}

var (
	cancelHints = []widgets.Hint{{Key: "B", Label: "Cancel"}}
	retryHints  = []widgets.Hint{{Key: "X", Label: "Try again"}, {Key: "B", Label: "Cancel"}}
)

// Source is the sign-in the page shows. *audible.SignIn is one.
type Source interface {
	Start() (string, error)
	Stop()
	State() (audible.SignInState, error)
}

// SignIn shows the address of the page the phone opens, and closes by itself
// once the phone has signed in.
type SignIn struct {
	nav   screens.Nav
	src   Source
	url   string
	err   error // why the page couldn't start
	steps [len(steps)]widgets.Wrapped
	msg   widgets.Wrapped
	done  bool
}

// NewSignIn starts the page.
func NewSignIn(nav screens.Nav, src Source) *SignIn {
	s := &SignIn{nav: nav, src: src}
	s.start()
	return s
}

func (s *SignIn) start() { s.url, s.err = s.src.Start() }

// Close stops serving the page when the screen closes.
func (s *SignIn) Close() { s.src.Stop() }

// Handle starts the page again with X when it couldn't start.
func (s *SignIn) Handle(ev input.Event, now time.Time) bool {
	if ev.Kind == input.Press && ev.Button == input.X && s.err != nil {
		s.start()
		return true
	}
	return false
}

// Animating is false: the page is drawn again each second (Due).
func (s *SignIn) Animating(time.Time) bool { return false }

// Due is the next whole second after t, to see whether the phone signed in.
func (s *SignIn) Due(t time.Time) (time.Time, bool) {
	return t.Truncate(time.Second).Add(time.Second), !s.done
}

// Draw renders the frame, and closes the page once the phone has signed in.
func (s *SignIn) Draw(u *ui.Context) {
	if st, _ := s.src.State(); st == audible.SignedIn && !s.done {
		s.done = true
		s.nav.Pop()
		s.nav.Toast("Signed in to Audible")
	}
	widgets.DrawBackground(u, widgets.StateGround)
	widgets.StatusBar(u)
	s.drawSteps(u)
	right := gfx.Rect{X: rightX, Y: signTop, W: rightW, H: screens.ContentBot - signTop}
	hints := cancelHints
	switch {
	case s.err != nil:
		message(u, &s.msg, noNetwork, right)
		hints = retryHints
	default:
		s.drawCode(u, right)
	}
	widgets.HintBar(u, hints)
}

// noNetwork is what the page says when it has no address to serve on.
const noNetwork = "Connect to Wi-Fi first: the sign-in page needs the local network."

func (s *SignIn) drawSteps(u *ui.Context) {
	x, y := float32(screens.Side), float32(signTop)
	widgets.LogoMark(u, 30, x, y, theme.Gold)
	y += 52
	u.Text("Sign in to Audible", signTitle, x, y, theme.Paper)
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
	if s.url == "" {
		return
	}
	y += 8
	box := gfx.Rect{X: x, Y: y, W: leftW, H: 20 + u.LineHeight(urlText)}
	u.Shape(raster.Shape{W: int(box.W), H: int(box.H), Radius: 14, Kind: raster.Fill}, box, theme.PaperA(0.06))
	u.Text(u.Fit(s.url, urlText, box.W-40), urlText, box.X+20, box.Y+10, theme.Paper)
}

func (s *SignIn) drawCode(u *ui.Context, box gfx.Rect) {
	t := gfx.Rect{X: box.X, Y: box.Y, W: rightW, H: rightW}
	u.Shape(raster.Shape{W: rightW, H: rightW, Radius: tile, Kind: raster.Fill}, t, theme.Paper)
	url := s.url
	u.Image("qr:"+url, func() *image.NRGBA { return qrImage(url, u.Px(qrSize)) },
		gfx.Rect{X: t.X + qrPad, Y: t.Y + qrPad, W: qrSize, H: qrSize}, theme.Ground)
	u.Text("Waiting for the phone…", waitText, box.X, t.Y+rightW+18, theme.GoldSoft)
}

// message draws s wrapped and centred in box, in ink2.
func message(u *ui.Context, c *widgets.Wrapped, s string, box gfx.Rect) {
	lines := c.Lines(u, s, text.Body, box.W)
	lh := u.LineHeight(text.Body)
	y := box.Y + (box.H-float32(len(lines))*lh)/2
	for _, l := range lines {
		u.Text(l, text.Body, box.X+(box.W-u.TextWidth(l, text.Body))/2, y, theme.Ink2)
		y += lh
	}
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

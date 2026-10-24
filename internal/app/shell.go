package app

import (
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Toast timing: shown for 2.5 s, fading in and out. The volume pill, a kind of
// toast, stays 1.5 s after the last change.
const (
	toastFor  = 2500 * time.Millisecond
	toastFade = 150 * time.Millisecond
	volumeFor = 1500 * time.Millisecond
)

// modal is a dialog over everything else, the quick menu, which takes all
// input while it is open.
type modal interface {
	screens.Screen
	Open(now time.Time)
	IsOpen() bool
	Visible(now time.Time) bool
}

// overlay is a full-screen screen opened over the tabs with a button.
type overlay struct {
	button input.Button
	screen screens.Screen
	can    func() bool // whether it may open now; nil means always
}

// shell routes input and drawing between each tab's stack of pages and the
// full-screen overlays, and runs their transitions and toasts. It knows
// nothing of SDL, so it is tested on its own.
type shell struct {
	stacks   [][]screens.Screen // per tab, root first
	tab      int
	trans    anim.Value     // the top page sliding in: 0 to 1 on a push, 1 to 0 on a pop
	leaving  screens.Screen // the page being popped, until its transition ends
	overlays []overlay
	open     int        // the open overlay, or -1
	showing  int        // the overlay drawn: the open one, or one fading out
	shown    anim.Value // the overlay's slide and fade, 0 to 1
	menu     modal      // opened by Menu; nil for none
	alert    modal      // a dialog opened by news, over everything, the menu too; nil for none
	menuDown bool       // Menu is down, and opens the menu when let go unless another button comes first
	toast    string
	volume   int       // the level the volume pill shows
	volumeOn bool      // the toast is the volume pill, and toast is empty
	toastAt  time.Time // when the toast or the pill began
}

func newShell(roots ...screens.Screen) *shell {
	s := &shell{open: -1, showing: -1}
	for _, r := range roots {
		s.stacks = append(s.stacks, []screens.Screen{r})
	}
	s.trans.Set(1)
	return s
}

// add puts o over the tabs and returns its index, to open it by.
func (s *shell) add(o overlay) int {
	s.overlays = append(s.overlays, o)
	return len(s.overlays) - 1
}

// top is the current tab's top page.
func (s *shell) top() screens.Screen {
	st := s.stacks[s.tab]
	return st[len(st)-1]
}

// overlay is the open overlay's screen, or nil.
func (s *shell) overlay() screens.Screen {
	if s.open < 0 {
		return nil
	}
	return s.overlays[s.open].screen
}

// showsDownloads reports whether what is shown draws the download queue: the
// open overlay, or with none open the tab's top page. An overlay covers the
// tab, which isn't drawn under it; while it slides in or out, frames are drawn
// for that anyway.
func (s *shell) showsDownloads() bool {
	if s.open >= 0 {
		return showsDownloads(s.overlay())
	}
	return showsDownloads(s.top())
}

func showsDownloads(scr screens.Screen) bool {
	v, ok := scr.(screens.DownloadView)
	return ok && v.ShowsDownloads()
}

// active is the screen input goes to.
func (s *shell) active() screens.Screen {
	if s.open >= 0 {
		return s.overlays[s.open].screen
	}
	return s.top()
}

func (s *shell) push(p screens.Screen, now time.Time) {
	s.stacks[s.tab] = append(s.stacks[s.tab], p)
	s.leaving = nil
	s.trans.Set(0)
	s.trans.To(1, now, anim.Push, anim.EaseOutCubic)
}

func (s *shell) pop(now time.Time) {
	st := s.stacks[s.tab]
	if len(st) < 2 {
		return
	}
	s.leaving = st[len(st)-1]
	if c, ok := s.leaving.(screens.Closer); ok {
		c.Close()
	}
	st[len(st)-1] = nil
	s.stacks[s.tab] = st[:len(st)-1]
	s.trans.To(0, now, anim.Push, anim.EaseOutCubic)
}

// showTab shows tab t, closing any overlay over it.
func (s *shell) showTab(t int, now time.Time) {
	if s.open >= 0 {
		s.closeOverlay(now)
	}
	s.tab, s.leaving = t, nil
	s.trans.Set(1)
}

// showToast replaces any toast; one already fully shown stays at full alpha.
func (s *shell) showToast(msg string, now time.Time) {
	if s.toastAlpha(now) >= 1 {
		now = now.Add(-toastFade)
	}
	s.toast, s.volumeOn, s.toastAt = msg, false, now
}

// showVolume replaces any toast with the volume pill at level, which stays for
// volumeFor after the last change; one already fully shown stays at full alpha.
func (s *shell) showVolume(level int, now time.Time) {
	if s.toastAlpha(now) >= 1 {
		now = now.Add(-toastFade)
	}
	s.toast, s.volume, s.volumeOn, s.toastAt = "", level, true, now
}

// openOverlay opens overlay i. Over the tab it slides in; in place of another
// overlay it takes over as far as that one was shown, at once when fully, so
// the tab doesn't flash between them.
func (s *shell) openOverlay(i int, now time.Time) {
	if s.showing < 0 {
		s.shown.Set(0)
	}
	s.open, s.showing = i, i
	if o, ok := s.overlays[i].screen.(screens.Opener); ok {
		o.Opened()
	}
	s.shown.To(1, now, anim.Push, anim.EaseOutCubic)
}

func (s *shell) closeOverlay(now time.Time) {
	s.open = -1
	s.shown.To(0, now, anim.Push, anim.EaseOutCubic)
}

// handle routes a button event and reports whether the frame changed. The
// quick menu takes everything while it is open, and Menu opens it when let go
// alone: NextUI sets the brightness with Menu and the volume buttons. An
// overlay's button opens it from anywhere, or closes it when it is open; B
// closes an open overlay, unless the overlay uses it, or pops a page; L1 and
// R1 switch tabs when no overlay is open. The screen input goes to may claim
// one of those buttons for itself (screens.Claimer).
func (s *shell) handle(e input.Event, now time.Time) bool {
	if s.alert != nil && s.alert.IsOpen() {
		return s.alert.Handle(e, now)
	}
	if s.menu != nil && s.menu.IsOpen() {
		return s.menu.Handle(e, now)
	}
	if e.Kind == input.Release && e.Button == input.Menu && s.menuDown {
		s.menuDown = false
		// A dialog may have opened meanwhile; it keeps Menu.
		if c, ok := s.active().(screens.Claimer); !ok || !c.Claims(input.Menu) {
			s.menu.Open(now)
			return true
		}
	}
	if e.Kind == input.Press {
		s.menuDown = false // another button makes a chord of Menu
		if c, ok := s.active().(screens.Claimer); ok && c.Claims(e.Button) {
			return s.active().Handle(e, now)
		}
		if e.Button == input.Menu && s.menu != nil {
			s.menuDown = true
			return false
		}
		for i, o := range s.overlays {
			if e.Button != o.button {
				continue
			}
			switch {
			case s.open == i:
				s.closeOverlay(now)
			case o.can == nil || o.can():
				s.openOverlay(i, now)
			}
			return true
		}
		switch {
		case e.Button == input.B && s.open >= 0:
			if b, ok := s.overlays[s.open].screen.(screens.Backer); ok && b.Back() {
				return true
			}
			s.closeOverlay(now)
			return true
		case e.Button == input.B && len(s.stacks[s.tab]) > 1:
			s.pop(now)
			return true
		case (e.Button == input.L1 || e.Button == input.R1) && s.open < 0:
			t := s.tab - 1
			if e.Button == input.R1 {
				t = s.tab + 1
			}
			if t < 0 || t >= len(s.stacks) {
				return false
			}
			s.tab, s.leaving = t, nil
			s.trans.Set(1)
			return true
		}
	}
	return s.active().Handle(e, now)
}

// chord tells the shell a button went down, before it is mapped: Menu then
// opens nothing when let go. Unmapped buttons count, so that NextUI's own
// chords with Menu pass by.
func (s *shell) chord() { s.menuDown = false }

// draw renders the tab's pages, the overlay sliding 40 px and fading over
// them, the quick menu, then any toast.
func (s *shell) draw(u *ui.Context) {
	now := u.Now
	t := float32(s.shown.At(now))
	if s.showing < 0 || t < 1 {
		s.drawTab(u)
	}
	if s.showing >= 0 && t > 0 {
		slide(u, s.overlays[s.showing].screen, t)
	}
	if s.showing >= 0 && s.open < 0 && s.shown.Done(now) {
		s.showing = -1
	}
	if s.menu != nil && s.menu.Visible(now) {
		s.menu.Draw(u)
	}
	if s.alert != nil && s.alert.Visible(now) {
		s.alert.Draw(u)
	}
	if a := s.toastAlpha(now); a > 0 {
		if s.volumeOn {
			widgets.VolumePill(u, s.volume, a)
		} else {
			widgets.Toast(u, s.toast, a)
		}
	}
}

func (s *shell) drawTab(u *ui.Context) {
	st := s.stacks[s.tab]
	top := st[len(st)-1]
	if s.leaving != nil && s.trans.Done(u.Now) {
		s.leaving = nil
		s.trans.Set(1)
	}
	t := float32(s.trans.At(u.Now))
	switch {
	case s.leaving != nil: // popping: the page fades out over the one below
		top.Draw(u)
		slide(u, s.leaving, t)
	case t < 1 && len(st) > 1: // pushing: the new page fades in over the one below
		st[len(st)-2].Draw(u)
		slide(u, top, t)
	default:
		top.Draw(u)
	}
}

// slide draws scr faded to t and shifted 40*(1-t) px right.
func slide(u *ui.Context, scr screens.Screen, t float32) {
	u.PushTranslate(40*(1-t), 0)
	u.Alpha = t
	scr.Draw(u)
	u.Alpha = 1
	u.Pop()
}

// toastLen is how long the toast or the pill is shown; 0 for neither.
func (s *shell) toastLen() time.Duration {
	switch {
	case s.volumeOn:
		return volumeFor
	case s.toast != "":
		return toastFor
	}
	return 0
}

func (s *shell) toastAlpha(now time.Time) float32 {
	d, n := now.Sub(s.toastAt), s.toastLen()
	if n == 0 || d < 0 || d >= n {
		return 0
	}
	return float32(min(d, n-d, toastFade)) / float32(toastFade)
}

// animating reports whether the next frame differs without input.
func (s *shell) animating(now time.Time) bool {
	if !s.trans.Done(now) || !s.shown.Done(now) {
		return true
	}
	if d, n := now.Sub(s.toastAt), s.toastLen(); n > 0 && d >= 0 && (d < toastFade || d >= n-toastFade && d < n) {
		return true
	}
	if s.menu != nil && s.menu.Visible(now) && s.menu.Animating(now) {
		return true
	}
	if s.alert != nil && s.alert.Visible(now) && s.alert.Animating(now) {
		return true
	}
	return s.active().Animating(now)
}

// due is when what is shown next changes by itself after t, as a sleep
// timer's time left does in the quick menu or on the screen shown.
func (s *shell) due(t time.Time) (time.Time, bool) {
	at, ok := dueOf(s.active(), t)
	if s.menu != nil && s.menu.IsOpen() {
		if m, mok := dueOf(s.menu, t); mok && (!ok || m.Before(at)) {
			at, ok = m, true
		}
	}
	return at, ok
}

func dueOf(scr screens.Screen, t time.Time) (time.Time, bool) {
	if tk, ok := scr.(screens.Ticking); ok {
		return tk.Due(t)
	}
	return time.Time{}, false
}

// wake is when the loop must next draw though nothing animates now: a toast
// starting to fade out.
func (s *shell) wake(now time.Time) (time.Time, bool) {
	n := s.toastLen()
	out := s.toastAt.Add(n - toastFade)
	if n > 0 && now.Before(out) {
		return out, true
	}
	return time.Time{}, false
}

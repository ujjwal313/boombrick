package app

import (
	"slices"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(1000, 0)

// page is a screen that records what reaches it.
type page struct {
	name  string
	got   []input.Button
	drawn int
	backs int // B presses it keeps for itself
}

func (p *page) Handle(e input.Event, now time.Time) bool {
	p.got = append(p.got, e.Button)
	return true
}
func (p *page) Draw(u *ui.Context)       { p.drawn++ }
func (p *page) Animating(time.Time) bool { return false }

// backer keeps B while it has backs left.
type backer struct{ page }

func (b *backer) Back() bool {
	if b.backs > 0 {
		b.backs--
		return true
	}
	return false
}

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

func testShell() (*shell, []*page, *page) {
	tabs := []*page{{name: "home"}, {name: "library"}}
	np := &page{name: "now playing"}
	s := newShell(tabs[0], tabs[1])
	s.overlays = []overlay{{button: input.Start, screen: np}}
	return s, tabs, np
}

func draw(t *testing.T, s *shell, now time.Time) {
	t.Helper()
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Now = now
	s.draw(u)
}

func TestPushAndPop(t *testing.T) {
	s, tabs, _ := testShell()
	album := &page{name: "album"}
	s.push(album, t0)
	if !s.animating(t0) {
		t.Fatal("a push should animate")
	}
	draw(t, s, t0.Add(anim.Push/2))
	if tabs[0].drawn != 1 || album.drawn != 1 {
		t.Fatal("mid-push, the page below and the new page should both draw")
	}
	s.handle(press(input.A), t0)
	if !slices.Equal(album.got, []input.Button{input.A}) {
		t.Fatalf("A went to %v, want the top page", album.got)
	}
	s.handle(press(input.B), t0.Add(anim.Push))
	draw(t, s, t0.Add(anim.Push+anim.Push/2))
	if album.drawn != 2 || len(s.stacks[0]) != 1 {
		t.Fatal("B should pop the page, which fades out over the one below")
	}
	draw(t, s, t0.Add(3*anim.Push))
	if album.drawn != 2 {
		t.Fatal("a popped page stops drawing once its transition ends")
	}
	s.handle(press(input.B), t0)
	if !slices.Equal(tabs[0].got, []input.Button{input.B}) {
		t.Fatal("B on a tab's root page goes to the page")
	}
}

func TestTabsKeepTheirStacks(t *testing.T) {
	s, tabs, _ := testShell()
	s.push(&page{name: "album"}, t0)
	if !s.handle(press(input.R1), t0) || s.tab != 1 {
		t.Fatal("R1 should switch to the next tab")
	}
	s.handle(press(input.A), t0)
	if !slices.Equal(tabs[1].got, []input.Button{input.A}) {
		t.Fatal("input should go to the new tab's page")
	}
	if s.handle(press(input.R1), t0) {
		t.Fatal("R1 on the last tab does nothing")
	}
	s.handle(press(input.L1), t0)
	if len(s.stacks[0]) != 2 {
		t.Fatal("switching tabs keeps each tab's stack")
	}
}

func TestOverlays(t *testing.T) {
	s, tabs, np := testShell()
	allowed := false
	s.overlays[0].can = func() bool { return allowed }
	s.handle(press(input.Start), t0)
	if s.open >= 0 {
		t.Fatal("an overlay that can't open stays shut")
	}
	allowed = true
	s.handle(press(input.Start), t0)
	s.handle(press(input.A), t0)
	if s.open != 0 || !slices.Equal(np.got, []input.Button{input.A}) {
		t.Fatal("Start should open Now Playing and send it input")
	}
	s.handle(press(input.L1), t0)
	if s.tab != 0 || !slices.Contains(np.got, input.L1) {
		t.Fatal("L1 goes to the overlay, not the tabs")
	}
	opened := t0.Add(anim.Push) // fully shown
	s.handle(press(input.B), opened)
	if s.open >= 0 || len(tabs[0].got) != 0 {
		t.Fatal("B closes the overlay without reaching the tab")
	}
	draw(t, s, opened.Add(anim.Push/2))
	if np.drawn != 1 || tabs[0].drawn != 1 {
		t.Fatal("a closing overlay fades out over the tab")
	}
	draw(t, s, opened.Add(2*anim.Push))
	if np.drawn != 1 {
		t.Fatal("a closed overlay stops drawing")
	}
	s.handle(press(input.Start), t0)
	s.handle(press(input.Start), t0)
	if s.open >= 0 {
		t.Fatal("an overlay's own button closes it")
	}
}

func TestBackerKeepsB(t *testing.T) {
	s, _, _ := testShell()
	q := &backer{page{name: "queue"}}
	q.backs = 1
	s.overlays = append(s.overlays, overlay{button: input.Select, screen: q})
	s.handle(press(input.Select), t0)
	s.handle(press(input.B), t0)
	if s.open != 1 {
		t.Fatal("the first B is the overlay's (cancelling a move)")
	}
	s.handle(press(input.B), t0)
	if s.open >= 0 {
		t.Fatal("the second B closes it")
	}
}

func TestToast(t *testing.T) {
	s, _, _ := testShell()
	s.showToast("Added to queue", t0)
	if !s.animating(t0) {
		t.Fatal("a toast fades in")
	}
	if s.animating(t0.Add(time.Second)) {
		t.Fatal("a toast at rest needs no frames")
	}
	if w, ok := s.wake(t0.Add(time.Second)); !ok || !w.Equal(t0.Add(toastFor-toastFade)) {
		t.Fatalf("wake = %v, %v: the loop must wake to fade the toast out", w, ok)
	}
	if _, ok := s.wake(t0.Add(toastFor)); ok {
		t.Fatal("a finished toast needs no wake-up")
	}
}

func TestPopToAMiddlePage(t *testing.T) {
	s, tabs, _ := testShell()
	a, b := &page{name: "a"}, &page{name: "b"}
	s.push(a, t0)
	s.push(b, t0.Add(anim.Push))
	s.handle(press(input.B), t0.Add(3*anim.Push))
	draw(t, s, t0.Add(10*anim.Push))
	if a.drawn != 1 || b.drawn != 0 || tabs[0].drawn != 0 {
		t.Fatalf("after the pop only the page below should draw: root %d, a %d, b %d", tabs[0].drawn, a.drawn, b.drawn)
	}
}

func TestPopMidPush(t *testing.T) {
	s, tabs, _ := testShell()
	a := &page{name: "a"}
	s.push(a, t0)
	s.handle(press(input.B), t0.Add(anim.Push/2))
	draw(t, s, t0.Add(anim.Push/2+anim.Push/4))
	if a.drawn != 1 || tabs[0].drawn != 1 {
		t.Fatal("a page popped mid-push should fade out over the root")
	}
	draw(t, s, t0.Add(5*anim.Push))
	if a.drawn != 1 || tabs[0].drawn != 2 {
		t.Fatal("the popped page should be gone once the transition ends")
	}
}

func TestSecondPopMidPop(t *testing.T) {
	s, tabs, _ := testShell()
	a, b := &page{name: "a"}, &page{name: "b"}
	s.push(a, t0)
	s.push(b, t0.Add(anim.Push))
	at := t0.Add(3 * anim.Push)
	s.handle(press(input.B), at)
	s.handle(press(input.B), at.Add(anim.Push/4))
	draw(t, s, at.Add(anim.Push/2))
	if b.drawn != 0 || a.drawn != 1 || tabs[0].drawn != 1 {
		t.Fatalf("only the last page popped should fade: root %d, a %d, b %d", tabs[0].drawn, a.drawn, b.drawn)
	}
}

func TestTabSwitchMidTransition(t *testing.T) {
	s, tabs, _ := testShell()
	a := &page{name: "a"}
	s.push(a, t0)
	s.handle(press(input.R1), t0.Add(anim.Push/2))
	draw(t, s, t0.Add(anim.Push/2))
	if tabs[1].drawn != 1 || a.drawn != 0 || tabs[0].drawn != 0 {
		t.Fatal("the new tab should draw whole, with no leftover transition")
	}
	s.handle(press(input.L1), t0.Add(anim.Push/2))
	draw(t, s, t0.Add(anim.Push/2))
	if a.drawn != 1 {
		t.Fatal("back on the first tab its page should draw whole")
	}
}

func TestSwitchOverlays(t *testing.T) {
	s, tabs, np := testShell()
	q := &page{name: "queue"}
	s.overlays = append(s.overlays, overlay{button: input.Select, screen: q})
	s.handle(press(input.Start), t0)
	s.handle(press(input.Select), t0.Add(anim.Push))
	if s.open != 1 || s.showing != 1 {
		t.Fatalf("open %d, showing %d: the second overlay should replace the first", s.open, s.showing)
	}
	draw(t, s, t0.Add(anim.Push))
	if q.drawn != 1 || np.drawn != 0 || tabs[0].drawn != 0 {
		t.Fatalf("drawn: queue %d, now playing %d, tab %d; the second overlay should replace the first at once",
			q.drawn, np.drawn, tabs[0].drawn)
	}
	if s.animating(t0.Add(anim.Push)) {
		t.Fatal("replacing a fully shown overlay needs no transition")
	}
	draw(t, s, t0.Add(5*anim.Push))
	if q.drawn != 2 || np.drawn != 0 || tabs[0].drawn != 0 {
		t.Fatal("only the second overlay should draw")
	}
}

// An overlay opened while another is still sliding in carries on from where
// that one had got to.
func TestSwitchOverlaysMidOpen(t *testing.T) {
	s, _, _ := testShell()
	s.overlays = append(s.overlays, overlay{button: input.Select, screen: &page{name: "queue"}})
	s.handle(press(input.Start), t0)
	mid := t0.Add(anim.Push / 2)
	was := s.shown.At(mid)
	s.handle(press(input.Select), mid)
	if got := s.shown.At(mid); got != was || got <= 0 || got >= 1 {
		t.Fatalf("shown %v right after the switch, want %v", got, was)
	}
	if got := s.shown.At(mid.Add(anim.Push)); got != 1 {
		t.Fatalf("shown %v after the transition, want 1", got)
	}
}

func TestToastReplacedWhileShown(t *testing.T) {
	s, _, _ := testShell()
	s.showToast("one", t0)
	now := t0.Add(time.Second)
	s.showToast("two", now)
	if s.toast != "two" || s.toastAlpha(now) != 1 {
		t.Fatalf("toast %q at alpha %v: a replacement should not fade in again", s.toast, s.toastAlpha(now))
	}
	if s.toastAlpha(now.Add(toastFor)) != 0 {
		t.Fatal("the replacement should still end")
	}
}

// opener counts the times its overlay opens.
type opener struct {
	page
	opened int
}

func (o *opener) Opened() { o.opened++ }

func TestOpenerCalledOnEachOpen(t *testing.T) {
	s, _, _ := testShell()
	o := &opener{}
	s.overlays = append(s.overlays, overlay{button: input.Select, screen: o})
	s.handle(press(input.Select), t0)
	s.handle(press(input.Select), t0)
	s.handle(press(input.Select), t0)
	if o.opened != 2 {
		t.Fatalf("opened %d times, want 2", o.opened)
	}
	s.handle(press(input.Start), t0) // another overlay replaces it
	s.handle(press(input.Select), t0)
	if o.opened != 3 {
		t.Fatalf("opened %d times after reopening, want 3", o.opened)
	}
}

type ticking struct{ page }

func (ticking) Tick() time.Duration { return 250 * time.Millisecond }

func TestTickFor(t *testing.T) {
	if got := tickFor(nil); got != time.Second {
		t.Errorf("tabs tick every %v, want 1s", got)
	}
	if got := tickFor(&page{}); got != time.Second {
		t.Errorf("an overlay without Tick ticks every %v, want 1s", got)
	}
	if got := tickFor(&ticking{}); got != 250*time.Millisecond {
		t.Errorf("an overlay asking for 250ms ticks every %v", got)
	}
}

// claimer takes Start while claim is set.
type claimer struct {
	page
	claim bool
}

func (c *claimer) Claims(b input.Button) bool { return c.claim && b == input.Start }

func TestATabScreenCanClaimAButton(t *testing.T) {
	tab := &claimer{page: page{name: "youtube"}, claim: true}
	np := &page{name: "now playing"}
	s := newShell(tab)
	s.overlays = []overlay{{button: input.Start, screen: np}}
	s.handle(press(input.Start), t0)
	if s.open >= 0 || !slices.Equal(tab.got, []input.Button{input.Start}) {
		t.Fatal("Start didn't reach the screen claiming it")
	}
	tab.claim = false
	s.handle(press(input.Start), t0)
	if s.open != 0 {
		t.Fatal("Start no longer claimed didn't open Now Playing")
	}
}

// shower is a page that shows the download queue while on is set.
type shower struct {
	page
	on bool
}

func (s *shower) ShowsDownloads() bool { return s.on }

// A change to the download queue redraws only what shows it: the shown tab's
// top page, or the open overlay, which hides the tab.
func TestOnlyWhatShowsDownloadsRedrawsForThem(t *testing.T) {
	library, downloads := &page{name: "library"}, &shower{page: page{name: "downloads"}, on: true}
	np := &shower{page: page{name: "now playing"}}
	s := newShell(library, downloads)
	s.overlays = []overlay{{button: input.Start, screen: np}}
	if s.showsDownloads() {
		t.Fatal("the Library redraws for downloads")
	}
	s.handle(press(input.R1), t0)
	if !s.showsDownloads() {
		t.Fatal("the Downloads tab doesn't")
	}
	s.push(&page{name: "a page"}, t0)
	if s.showsDownloads() {
		t.Fatal("a page over the Downloads tab redraws for downloads")
	}
	s.handle(press(input.B), t0)
	s.handle(press(input.L1), t0)
	s.handle(press(input.Start), t0)
	if s.showsDownloads() {
		t.Fatal("Now Playing redraws for downloads with nothing to show")
	}
	np.on = true
	if !s.showsDownloads() {
		t.Fatal("Now Playing waiting on a download doesn't")
	}
	s.handle(press(input.Start), t0)
	if s.showsDownloads() {
		t.Fatal("Now Playing closed still redraws the Library")
	}
	s.handle(press(input.R1), t0)
	np.on = false
	s.handle(press(input.Start), t0)
	if s.showsDownloads() {
		t.Fatal("the Downloads tab redraws for downloads under Now Playing")
	}
}

func TestVolumePillIsShownThenGone(t *testing.T) {
	s, _, _ := testShell()
	s.showVolume(14, t0)
	if !s.volumeOn || s.volume != 14 || !s.animating(t0) {
		t.Fatal("the pill should fade in")
	}
	mid := t0.Add(volumeFor / 2)
	if s.toastAlpha(mid) != 1 || s.animating(mid) {
		t.Fatal("the pill at rest is fully shown and needs no frames")
	}
	if w, ok := s.wake(mid); !ok || !w.Equal(t0.Add(volumeFor-toastFade)) {
		t.Fatalf("wake = %v, %v: the loop must wake to fade the pill out", w, ok)
	}
	if !s.animating(t0.Add(volumeFor - toastFade/2)) {
		t.Fatal("the pill fades out")
	}
	if s.toastAlpha(t0.Add(volumeFor)) != 0 || s.animating(t0.Add(volumeFor)) {
		t.Fatal("the pill is gone after 1.5 s and the fade")
	}
	if _, ok := s.wake(t0.Add(volumeFor)); ok {
		t.Fatal("a finished pill needs no wake-up")
	}
	draw(t, s, mid) // the pill draws, not a toast
}

func TestVolumePillRestartsOnAChange(t *testing.T) {
	s, _, _ := testShell()
	s.showVolume(14, t0)
	now := t0.Add(time.Second)
	s.showVolume(15, now)
	if s.volume != 15 || s.toastAlpha(now) != 1 {
		t.Fatalf("level %d at alpha %v: a change should not fade the pill in again", s.volume, s.toastAlpha(now))
	}
	if s.toastAlpha(t0.Add(volumeFor)) == 0 || s.toastAlpha(now.Add(volumeFor)) != 0 {
		t.Fatal("the 1.5 s should restart at the change")
	}
}

func TestToastAndVolumePillReplaceEachOther(t *testing.T) {
	s, _, _ := testShell()
	s.showVolume(3, t0)
	now := t0.Add(500 * time.Millisecond)
	s.showToast("Added to queue", now)
	if s.volumeOn || s.toast != "Added to queue" || s.toastAlpha(now) != 1 {
		t.Fatalf("a toast should replace the pill: on %v, toast %q", s.volumeOn, s.toast)
	}
	if s.toastAlpha(now.Add(volumeFor)) == 0 || s.toastAlpha(now.Add(toastFor)) != 0 {
		t.Fatal("the toast keeps its own length")
	}
	s.showVolume(4, now)
	if !s.volumeOn || s.toast != "" || s.toastAlpha(now) != 1 || s.toastAlpha(now.Add(volumeFor)) != 0 {
		t.Fatal("the pill should replace the toast, and last 1.5 s")
	}
}

type closing struct {
	page
	closed int
}

func (c *closing) Close() { c.closed++ }

func TestAPageThatClosesIsToldSo(t *testing.T) {
	s, _, _ := testShell()
	c := &closing{}
	s.push(c, t0)
	if c.closed != 0 {
		t.Fatal("closed on opening")
	}
	s.pop(t0)
	if c.closed != 1 {
		t.Fatalf("closed %d times on popping", c.closed)
	}
}

package widgets

import (
	"fmt"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
)

// tabRow draws the status bar and the tab row with shown and active.
func tabRow(t *testing.T, shown []int, active int) (*ui.Context, *gfx.Offscreen) {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, int((StatusHeight+TabsHeight)*theme.Scale))
	u.Accent = theme.Derive(35, 0.12)
	u.Status = designStatus
	u.Tabs.Shown = shown
	StatusBar(u)
	Tabs(u, active)
	return u, off
}

func TestTabRowGoldens(t *testing.T) {
	for _, c := range []struct {
		name   string
		shown  []int
		active int
	}{
		{"tabs-2-last", []int{TabHome, TabLibrary}, TabLibrary},
		{"tabs-3-first", []int{TabHome, TabLibrary, TabYouTube}, TabHome},
		{"tabs-5-first", nil, TabHome},
		{"tabs-5-middle", nil, TabSpotify},
		{"tabs-5-last", nil, TabDownloads},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, off := tabRow(t, c.shown, c.active)
			uitest.Golden(t, c.name, off.Image())
		})
	}
}

// R1 ends at the right-hand padding: nothing is drawn in the last 32 px,
// which the five tabs of before ran into.
func TestTabRowKeepsR1Whole(t *testing.T) {
	for _, active := range []int{TabHome, TabSpotify, TabDownloads} {
		_, off := tabRow(t, nil, active)
		img := off.Image()
		for y := int(StatusHeight * theme.Scale); y < img.Bounds().Dy(); y++ {
			for x := int((theme.ScreenW - tabSide) * theme.Scale); x < img.Bounds().Dx(); x++ {
				if c := img.NRGBAAt(x, y); c != theme.Ground {
					t.Fatalf("active %d: pixel (%d, %d) is %v, want the ground", active, x, y, c)
				}
			}
		}
	}
}

// L1 and R1 sit at the row's paddings however many tabs are shown: R1's
// right-hand border is in the last column before the padding.
func TestTabRowSpansTheScreen(t *testing.T) {
	edge := int((theme.ScreenW-tabSide)*theme.Scale) - 1
	for _, shown := range [][]int{{TabHome, TabLibrary}, {TabHome, TabLibrary, TabYouTube}, nil} {
		_, off := tabRow(t, shown, TabHome)
		img := off.Image()
		found := false
		for y := int(StatusHeight * theme.Scale); y < img.Bounds().Dy() && !found; y++ {
			for x := edge - 2; x <= edge; x++ {
				if img.NRGBAAt(x, y) != theme.Ground {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%d tabs: nothing drawn at the right-hand padding, where R1 belongs", len(shown))
		}
	}
}

// The row scrolls as little as keeps the active name in view, and slides to
// a new one rather than jumping.
func TestTabRowScrollsToTheActiveTab(t *testing.T) {
	u, _ := tabRow(t, nil, TabHome)
	if got := u.Tabs.Scroll.Target(); got != 0 {
		t.Fatalf("on Home the row is scrolled %v, want 0", got)
	}
	u.Now = u.Now.Add(time.Second)
	u.BeginFrame(u.Now)
	Tabs(u, TabDownloads)
	far := u.Tabs.Scroll.Target()
	if far <= 0 {
		t.Fatalf("on Downloads the row is scrolled %v, want past 0", far)
	}
	if !u.Animating() {
		t.Error("the row should slide to Downloads")
	}
	u.Now = u.Now.Add(anim.Focus)
	u.BeginFrame(u.Now)
	Tabs(u, TabDownloads)
	if u.Animating() {
		t.Error("the row should rest once the slide is over")
	}
	if got := u.Tabs.Scroll.At(u.Now); got != far {
		t.Errorf("the row rests at %v, want %v", got, far)
	}
	// Library is in view from there: the row doesn't move.
	Tabs(u, TabYouTube)
	if got := u.Tabs.Scroll.Target(); got != far {
		t.Errorf("on YouTube the row moved to %v from %v", got, far)
	}
	// Wrapping to Home slides all the way back.
	Tabs(u, TabHome)
	if got := u.Tabs.Scroll.Target(); got != 0 {
		t.Errorf("back on Home the row heads for %v, want 0", got)
	}
}

// Tabs that fit don't scroll, whichever is active.
func TestTabRowThatFitsDoesNotScroll(t *testing.T) {
	for _, active := range []int{TabHome, TabYouTube} {
		u, _ := tabRow(t, []int{TabHome, TabLibrary, TabYouTube}, active)
		if got := u.Tabs.Scroll.Target(); got != 0 {
			t.Errorf("%s active: scrolled %v, want 0", fmt.Sprint(TabName(active)), got)
		}
	}
}

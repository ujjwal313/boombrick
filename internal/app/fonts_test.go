package app

import (
	"testing"

	"github.com/ujjwal313/boombrick/internal/ui/uitest"
)

// A font arriving has text drawn again, with it: the engine's fonts change
// and the frame is drawn.
func TestFontNewsDrawsTextAgain(t *testing.T) {
	u, _ := uitest.NewContext(t, 64, 64)
	a := &App{ui: u}
	gen := u.Fonts.Generation()
	if a.fontNews(); u.Fonts.Generation() != gen || a.dirty {
		t.Fatal("drawn again with no font arrived")
	}
	a.fontsIn.Store(true)
	if a.fontNews(); u.Fonts.Generation() == gen || !a.dirty {
		t.Fatal("not drawn again when a font arrived")
	}
}

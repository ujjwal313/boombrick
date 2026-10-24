package screens

import (
	"testing"

	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Each tab names its own place in the tab row; a tab added in the middle
// moves the rest, and a screen that kept the old number lights the wrong name.
func TestTabPlacesMatchTheNames(t *testing.T) {
	for place, name := range map[int]string{
		HomeTab: "Home", LibraryTab: "Library", BooksTab: "Books",
		SpotifyTab: "Spotify", YouTubeTab: "YouTube", DownloadsTab: "Downloads",
	} {
		if widgets.TabNames[place] != name {
			t.Errorf("place %d is %q in the tab row, not %q", place, widgets.TabNames[place], name)
		}
	}
	if len(widgets.TabNames) != DownloadsTab+1 {
		t.Errorf("%d tabs in the row, the last place is %d", len(widgets.TabNames), DownloadsTab)
	}
}

// Package screens defines the Screen interface and screens shared by tabs.
package screens

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Screen is one full frame of UI.
type Screen interface {
	// Handle applies a button event and reports whether the frame changed.
	Handle(ev input.Event, now time.Time) bool
	// Draw renders the whole frame.
	Draw(u *ui.Context)
	// Animating reports whether another frame is needed at now even without input.
	Animating(now time.Time) bool
}

// Ticker is a screen that shows the playing track's time and wants a frame
// that often while music plays. Screens without it get one a second. Tick
// returns a positive duration; the app divides elapsed time by it.
type Ticker interface {
	Tick() time.Duration
}

// Claimer is a tab screen that sometimes takes a button the app otherwise
// keeps for itself, as the YouTube keyboard takes Start to search.
type Claimer interface {
	Claims(b input.Button) bool
}

// DownloadView is a screen that shows the download queue's state. A change
// to the queue redraws the frame only while a screen shown says it shows it.
type DownloadView interface {
	ShowsDownloads() bool
}

// downloads is the folder Boombrick files the songs it downloads in, with a
// separator after it, or "" for none. The app sets it once, at start.
var downloads string

// SetDownloadsDir says where downloads are filed, so that their badges say
// Downloaded.
func SetDownloadsDir(dir string) {
	downloads = ""
	if dir != "" {
		downloads = filepath.Clean(dir) + string(filepath.Separator)
	}
}

// SourceOf is where the file at path came from. The SD card's FAT or exFAT
// ignores case, so the folder's name is matched without it.
func SourceOf(path string) media.Source {
	if downloads != "" && len(path) > len(downloads) && strings.EqualFold(path[:len(downloads)], downloads) {
		return media.Downloaded
	}
	return media.Local
}

// musicPlace is where the music folder is, in words.
var musicPlace = defaultMusicPlace

const defaultMusicPlace = "the Music folder on the SD card"

// SetMusicPlace says where the music folder is, in words, for the empty
// library; "" is the SD card's Music folder.
func SetMusicPlace(place string) {
	musicPlace = place
	if place == "" {
		musicPlace = defaultMusicPlace
	}
}

// EmptyLibraryText is what Home and the Library say when no music is indexed.
func EmptyLibraryText() string {
	return "Copy music to " + musicPlace + ", or find something on YouTube Music."
}

// DrawReading draws the state Home and the Library show while the first scan
// runs and nothing is indexed yet, its description's lines kept in c.
func DrawReading(u *ui.Context, c *widgets.Wrapped, musicDir string) {
	widgets.DrawBackground(u, widgets.StateGround)
	widgets.EmptyState(u, c, "Reading your music", "This takes a little while the first time.", musicDir)
}

// Placeholder stands in for a tab whose service isn't there.
type Placeholder struct {
	Tab   int
	Title string
}

// Handle ignores input.
func (p *Placeholder) Handle(input.Event, time.Time) bool { return false }

// Animating is always false.
func (p *Placeholder) Animating(time.Time) bool { return false }

// Draw shows the tab chrome and the tab's name.
func (p *Placeholder) Draw(u *ui.Context) {
	widgets.DrawBackground(u, widgets.TabGround)
	widgets.StatusBar(u)
	widgets.Tabs(u, p.Tab)
	u.Text(p.Title, text.Heading, 32, widgets.StatusHeight+widgets.TabsHeight+14, theme.Paper)
	widgets.HintBar(u, []widgets.Hint{{Key: "L1/R1", Label: "Tabs"}})
}

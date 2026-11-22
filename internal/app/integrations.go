package app

import (
	"github.com/ujjwal313/boombrick/internal/screens"
	settingsscreen "github.com/ujjwal313/boombrick/internal/screens/settings"
	keys "github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// The integrations' names, as Settings shows them.
const (
	spotifyName = "Spotify"
	youtubeName = "YouTube"
)

// spotifyOn and youtubeOn read the integrations' switches; without Prefs both
// are on.
func (a *App) spotifyOn() bool { return a.opts.Prefs == nil || a.opts.Prefs.Bool(keys.SpotifyOn) }
func (a *App) youtubeOn() bool { return a.opts.Prefs == nil || a.opts.Prefs.Bool(keys.YouTubeOn) }

// shownTabs is the tabs shown with the switches as given: Home and the
// Library always, Spotify's with it, and YouTube's and Downloads with
// YouTube.
func shownTabs(spotify, youtube bool) []int {
	ids := []int{widgets.TabHome, widgets.TabLibrary}
	if spotify {
		ids = append(ids, widgets.TabSpotify)
	}
	if youtube {
		ids = append(ids, widgets.TabYouTube, widgets.TabDownloads)
	}
	return ids
}

// integrations are the switches Settings' Integrations page lists: one for
// each integration there is.
func (a *App) integrations() []settingsscreen.Integration {
	var ins []settingsscreen.Integration
	if a.opts.Spotify != nil {
		ins = append(ins, settingsscreen.Integration{Key: keys.SpotifyOn, Name: spotifyName, Icon: "stream",
			About: "Stream your playlists and liked songs · needs Premium"})
	}
	if a.opts.YouTube != nil || a.opts.Downloads != nil {
		ins = append(ins, settingsscreen.Integration{Key: keys.YouTubeOn, Name: youtubeName, Icon: "circleplay",
			About: "Search YouTube Music and download songs"})
	}
	return ins
}

// applySwitches makes everything follow the switches: the tabs shown, the
// download queue's hold, the importer, the player (whose gate, which
// newPlayer sets, reads the switches itself), what the screens offer and
// Home's rows. Switching on takes effect at once; switching off starts
// nothing new, and what is under way finishes.
func (a *App) applySwitches() {
	sp, yt := a.spotifyOn(), a.youtubeOn()
	tabs := shownTabs(sp, yt)
	a.shell.setShown(tabs)
	a.ui.Tabs.Shown = tabs
	if a.opts.Hold != nil {
		a.opts.Hold(!yt)
	}
	if a.opts.ImportOn != nil {
		a.opts.ImportOn(sp && yt)
	}
	screens.SetDownloads(yt)
	if a.opts.Player != nil {
		a.opts.Player.Regate()
	}
	a.showHome(a.progress)
	a.dirty = true
}

// switched is Settings flipping key's switch. Switched off while it is busy,
// a toast says what finishes first.
func (a *App) switched(key string, on bool) {
	if !on {
		switch {
		case key == keys.SpotifyOn && a.last.Playing && a.last.Track.Streamed():
			a.Toast("This song finishes, then Spotify songs are skipped")
		case key == keys.YouTubeOn && a.opts.Downloads != nil && a.opts.Downloads.Busy():
			a.Toast("Downloads under way will finish")
		}
	}
	a.applySwitches()
}

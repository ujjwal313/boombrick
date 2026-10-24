package app

import (
	"slices"
	"time"

	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	spotifyscreen "github.com/ujjwal313/boombrick/internal/screens/spotify"
	keys "github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/spotify"
)

// spotifyTab is the Spotify tab's place in the tab row.
const spotifyTab = screens.SpotifyTab

// Spotify is the Spotify library: the tab's, Home's row's and Settings'.
// *spotify.Library is one.
type Spotify interface {
	spotifyscreen.Source
	// Saved is the playlists kept from the last time they were asked for,
	// without asking.
	Saved() ([]spotify.Playlist, bool)
	Logout()
	Ready() <-chan struct{}
}

// Importer is what the app needs from the importer. *importer.Importer is
// one.
type Importer interface {
	spotifyscreen.Importer
	Ready() <-chan struct{}
}

// spotifyEnv is what the Spotify screens work with. Importing needs the
// download queue as well as the importer.
func (a *App) spotifyEnv() *spotifyscreen.Env {
	e := &spotifyscreen.Env{Nav: a, Src: a.opts.Spotify, Player: a.opts.Player,
		Online: func() bool { return a.ui.Status.Online }}
	if a.opts.Importer == nil || a.opts.Downloads == nil {
		return e
	}
	e.Import, e.Queue, e.Estimate = a.opts.Importer, a.opts.Downloads, a.estimate
	e.Copies = func(id string) bool { return a.ix != nil && a.ix.Spotify(id) != nil }
	a.forward(a.opts.Importer.Ready(), &a.redraw)
	return e
}

// estimate is the download quality's MB per hour, as Settings gives it, and
// the SD card's free space, for the size of an import.
func (a *App) estimate() (float64, int64) {
	perHour := 72.0
	if a.opts.Prefs != nil && a.opts.Prefs.String(keys.DownloadQuality) == "smaller" {
		perHour = 32
	}
	_, free, _ := a.card()
	return perHour, free
}

// openPlaylist opens playlist p, or Liked Songs, on the tab shown.
func (a *App) openPlaylist(p spotify.Playlist) {
	if p.URI == spotify.LikedURI {
		a.Push(spotifyscreen.NewLikedPage(a.env))
		return
	}
	a.Push(spotifyscreen.NewPlaylistPage(a.env, p))
}

// importPlaylist starts p's import from Home's row, at its review. Offline
// it says why it can't, as the Spotify screens offer no import then.
func (a *App) importPlaylist(p spotify.Playlist) {
	if !a.env.CanImport() {
		a.Toast("Connect to Wi-Fi to import")
		return
	}
	a.Push(spotifyscreen.NewReview(a.env, []spotify.Playlist{p}, true))
}

// signIn opens the sign-in on the Spotify tab, over anything open.
func (a *App) signIn() {
	a.shell.showTab(spotifyTab, time.Now())
	a.Push(spotifyscreen.NewSignIn(a.env))
}

// spotifyNews takes the library's news: Home's row is made again when the
// playlists kept change, and once signed in again a later expiry may open
// the dialog again.
func (a *App) spotifyNews() {
	if pls, _ := a.opts.Spotify.Saved(); !slices.Equal(pls, a.playlists) {
		a.playlists = pls
		a.showHome(a.progress)
	}
	if st, _ := a.opts.Spotify.Status(); st.State == spotify.Ready {
		a.expiredOnce = false
	}
	a.dirty = true
}

// notice shows the player's news: a toast, or for an expired session the
// dialog that offers to sign in again, once until signed in again.
func (a *App) notice(n player.Notice, now time.Time) {
	if n.Kind != player.SignedOut || a.expired == nil {
		a.Toast(noticeText(n))
		return
	}
	if !a.expiredOnce {
		a.expiredOnce = true
		a.expired.Open(now)
		a.dirty = true
	}
}

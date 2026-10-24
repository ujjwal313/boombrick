// Package spotify is the Spotify tab: signing in, the user's library, their
// playlists' pages, and the dialog shown when the session expires.
package spotify

import (
	"errors"
	"strings"
	"time"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// tab is the Spotify tab's place in the tab row.
const tab = screens.SpotifyTab

// Source is the Spotify library as these screens ask it: each call returns
// what is known and asks for what isn't. *spotify.Library is one.
type Source interface {
	Status() (spotify.Status, error)
	Playlists() ([]spotify.Playlist, bool, error)
	Tracks(uri string) ([]spotify.Track, bool, error)
	Retry()
	Login()
	// LoginErr is why the last sign-in failed, until the next one starts.
	LoginErr() error
	// Version changes whenever what the calls return does.
	Version() uint64
}

// Player plays and queues a playlist's songs, and says which is playing.
// *player.Player is one.
type Player interface {
	SetShuffle(on bool)
	Play(tracks []player.Track, start int)
	Enqueue(tracks []player.Track)
	State() player.State
}

// Liked Songs as the screens show it.
const likedTitle = "Liked Songs"

// art is a picture by its link over the placeholder made from title.
func art(title, cover string) widgets.ArtSpec {
	s := widgets.SpecFor(title)
	if cover != "" {
		s.Cover = media.Cover{URL: cover}
	}
	return s
}

// Tracks are a playlist's songs as queue entries, streamed.
func Tracks(ts []spotify.Track) []player.Track {
	out := make([]player.Track, len(ts))
	for i, t := range ts {
		out[i] = player.Track{SpotifyID: t.ID, Title: t.Title, Artist: strings.Join(t.Artists, ", "),
			AlbumArtist: t.AlbumArtist, Album: t.Album, Duration: time.Duration(t.Duration) * time.Millisecond, Cover: t.Cover}
	}
	return out
}

// songs counts n songs: "1 song", "1,204 songs".
func songs(n int) string {
	if n == 1 {
		return "1 song"
	}
	return screens.Count(n) + " songs"
}

// owner is how a playlist's owner reads: Spotify's own mixes say so.
func owner(p spotify.Playlist) string {
	if strings.EqualFold(p.Owner, "spotify") {
		return "Spotify"
	}
	return p.Owner
}

// failText says why a list couldn't be had.
func failText(err error) string {
	switch {
	case errors.Is(err, spotify.ErrOffline):
		return "You’re offline. Connect to Wi-Fi to see your Spotify library."
	case errors.Is(err, spotify.ErrNoHelper):
		return "Spotify couldn’t start. Try again in a moment."
	case errors.Is(err, spotify.ErrSignedOut):
		return "Signed out of Spotify."
	}
	return "Couldn’t load from Spotify. Check the connection and try again."
}

// message draws s wrapped and centred in box, in ink2. The screen keeps the
// lines in c, so that the same message isn't wrapped every frame.
func message(u *ui.Context, c *widgets.Wrapped, s string, box gfx.Rect) {
	lines := c.Lines(u, s, text.Body, box.W)
	lh := u.LineHeight(text.Body)
	y := box.Y + (box.H-float32(len(lines))*lh)/2
	for _, l := range lines {
		u.Text(l, text.Body, box.X+(box.W-u.TextWidth(l, text.Body))/2, y, theme.Ink2)
		y += lh
	}
}

// Hints the screens share.
var (
	tabsHint  = widgets.Hint{Key: "L1/R1", Label: "Tabs"}
	backHint  = widgets.Hint{Key: "B", Label: "Back"}
	retryHint = widgets.Hint{Key: "A", Label: "Retry"}
)

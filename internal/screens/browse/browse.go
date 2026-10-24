// Package browse is the Library tab and the album and artist pages it opens.
package browse

import (
	"math/rand/v2"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
)

// Player is what browsing needs from the player.
type Player interface {
	State() player.State
	Play(tracks []player.Track, start int)
	PlayFrom(tracks []player.Track, start int, frame int64)
	SetShuffle(on bool)
	Enqueue(tracks []player.Track)
}

// The tabs' page layout (screens/layout.go), by the short names these
// screens use.
const (
	contentTop = screens.ContentTop
	contentBot = screens.ContentBot
	side       = screens.Side
	pad        = screens.Pad
	gridArt    = screens.GridArt
	gridX      = screens.GridX
)

// shuffle plays ts with shuffle on, from a random one of them.
func shuffle(p Player, ts []player.Track) {
	if len(ts) == 0 {
		return
	}
	p.SetShuffle(true)
	p.Play(ts, rand.IntN(len(ts)))
}

// artistTracks is all an artist's songs, their albums oldest first.
func artistTracks(ar *library.Artist) []player.Track {
	ts := make([]player.Track, 0, ar.Songs)
	for _, a := range ar.Albums {
		for j := range a.Tracks {
			ts = append(ts, screens.TrackOf(&a.Tracks[j]))
		}
	}
	return ts
}

// songTracks converts the index's songs to queue entries.
func songTracks(songs []*library.Track) []player.Track {
	ts := make([]player.Track, len(songs))
	for i, s := range songs {
		ts[i] = screens.TrackOf(s)
	}
	return ts
}

// artistLine is an artist's album and song counts.
func artistLine(albums, songs int) string {
	a := screens.Count(albums) + " albums"
	if albums == 1 {
		a = "1 album"
	}
	s := screens.Count(songs) + " songs"
	if songs == 1 {
		s = "1 song"
	}
	return a + " · " + s
}

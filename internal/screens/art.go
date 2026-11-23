package screens

import (
	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// AlbumArt is album a's art: its cover, looked for through its first track,
// over the placeholder made from its title.
func AlbumArt(a *library.Album) widgets.ArtSpec {
	s := widgets.SpecFor(a.Title)
	if len(a.Tracks) > 0 {
		s.Cover = media.Cover{Artist: a.Artist, Album: a.Title, Path: a.Tracks[0].Path}
	}
	return s
}

// ArtistArt is an artist's round art: their newest album's cover, over the
// placeholder made from their name.
func ArtistArt(ar *library.Artist) widgets.ArtSpec {
	s := widgets.SpecFor(ar.Name)
	if n := len(ar.Albums); n > 0 {
		s.Cover = AlbumArt(ar.Albums[n-1]).Cover
	}
	return s
}

// GenreArt is a genre's art: the placeholder made from its name.
func GenreArt(g *library.Genre) widgets.ArtSpec { return widgets.SpecFor(g.Name) }

// SongArt is the art of a song's album.
func SongArt(t *library.Track) widgets.ArtSpec {
	s := widgets.SpecFor(t.Album)
	s.Cover = media.Cover{Artist: t.AlbumArtist, Album: t.Album, Path: t.Path}
	return s
}

// StationArt is a station's logo, or a placeholder showing its initial.
func StationArt(name, logo string) widgets.ArtSpec {
	s := widgets.SpecFor(name)
	s.Variant = 2
	if logo != "" {
		s.Cover = media.Cover{URL: logo}
	}
	return s
}

// LikedArt is Spotify's Liked Songs: a heart on rose.
var LikedArt = widgets.ArtSpec{Hue: 330, Variant: 2, Glyph: "♥"}

// TrackArt is the art of a queued track's album: a streamed track's from its
// cover link. A station's is its logo.
func TrackArt(t player.Track) widgets.ArtSpec {
	if t.Live() {
		return StationArt(t.Title, t.Cover)
	}
	s := widgets.SpecFor(t.Album)
	s.Cover = media.Cover{Artist: t.AlbumArtist, Album: t.Album, Path: t.Path}
	if t.Streamed() && t.Cover != "" {
		s.Cover = media.Cover{URL: t.Cover}
	}
	return s
}

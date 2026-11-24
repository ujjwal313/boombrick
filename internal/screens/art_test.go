package screens

import (
	"testing"

	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func copperSeason() *library.Album {
	return &library.Album{Artist: "Odalys", Title: "Copper Season", Tracks: []library.Track{
		{Path: "/m/cs/1.flac", Title: "Rust", Artist: "Odalys", AlbumArtist: "Odalys", Album: "Copper Season"},
		{Path: "/m/cs/2.flac", Title: "Ember", Artist: "Odalys", AlbumArtist: "Odalys", Album: "Copper Season"},
	}}
}

func TestAlbumArt(t *testing.T) {
	a := copperSeason()
	s := AlbumArt(a)
	if s.Cover != (media.Cover{Artist: "Odalys", Album: "Copper Season", Path: "/m/cs/1.flac"}) {
		t.Fatalf("cover %+v", s.Cover)
	}
	if s.Hue != widgets.SpecFor("Copper Season").Hue {
		t.Fatal("the placeholder isn't the album's")
	}
	if AlbumArt(&library.Album{Title: "Empty"}).Cover.Key() != "" {
		t.Fatal("an album without tracks names a cover")
	}
}

func TestSongsAndQueuedTracksShareTheirAlbumsArt(t *testing.T) {
	a := copperSeason()
	key := AlbumArt(a).Cover.Key()
	if k := SongArt(&a.Tracks[1]).Cover.Key(); k != key {
		t.Errorf("a song's cover key %q, the album's %q", k, key)
	}
	if k := TrackArt(TrackOf(&a.Tracks[1])).Cover.Key(); k != key {
		t.Errorf("a queued track's cover key %q, the album's %q", k, key)
	}
	if p := TrackArt(TrackOf(&a.Tracks[1])).Cover.Path; p != "/m/cs/2.flac" {
		t.Errorf("a queued track looks for its art in %q", p)
	}
}

func TestStreamedTracksArtIsTheirCover(t *testing.T) {
	tr := player.Track{SpotifyID: "3UmaczJpikHgJFyBTAJVoz", Album: "The Marshall Mathers LP", AlbumArtist: "Eminem",
		Cover: "https://i.scdn.co/image/ab67616d0000b273dbb3dd82da45b7d7f31b1b42"}
	if c := TrackArt(tr).Cover; c != (media.Cover{URL: tr.Cover}) {
		t.Fatalf("cover %+v", c)
	}
	tr.Cover = ""
	if c := TrackArt(tr).Cover; c.URL != "" || c.Album != "The Marshall Mathers LP" {
		t.Fatalf("without a link: %+v", c)
	}
}

func TestArtistArtIsTheirNewestAlbum(t *testing.T) {
	old := &library.Album{Artist: "Odalys", Title: "Northbound", Tracks: []library.Track{{Path: "/m/n/1.flac"}}}
	ar := &library.Artist{Name: "Odalys", Albums: []*library.Album{old, copperSeason()}}
	s := ArtistArt(ar)
	if s.Cover.Album != "Copper Season" || s.Hue != widgets.SpecFor("Odalys").Hue {
		t.Fatalf("artist art %+v", s)
	}
}

// A station's art is its logo, over a placeholder that always shows its
// initial.
func TestStationArt(t *testing.T) {
	logo := "https://logos.radio.test/city.png"
	s := StationArt("Radio City", logo)
	if s.Cover != (media.Cover{URL: logo}) || s.Variant != 2 || s.Glyph != "R" || s.Hue != widgets.SpecFor("Radio City").Hue {
		t.Fatalf("station art %+v", s)
	}
	if s := StationArt("Radio City", ""); s.Cover != (media.Cover{}) || s.Variant != 2 {
		t.Fatalf("without a logo: %+v", s)
	}
}

// A queued station shows its logo, on Now Playing, the Queue and Lyrics.
func TestQueuedStationsArtIsTheirLogo(t *testing.T) {
	tr := player.Track{Title: "Radio City", Artist: "India", Cover: "https://logos.radio.test/city.png",
		StationID: "96062a7b-0601-11e8-ae97-52543be04c81", Stream: "http://stream.radio.test/city"}
	if s := TrackArt(tr); s != StationArt("Radio City", tr.Cover) {
		t.Fatalf("station art %+v", s)
	}
}

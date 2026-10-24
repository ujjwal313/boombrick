// Package youtube is the YouTube tab: searching YouTube Music with the
// on-screen keyboard, the results, and albums' and artists' pages.
package youtube

import (
	"slices"
	"strconv"
	"strings"

	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

// tab is the YouTube tab's place in the tab row.
const tab = screens.YouTubeTab

// Source is YouTube Music as these screens ask it: each call returns what
// has come and asks for what hasn't. *ytmusic.Searcher is one.
type Source interface {
	Search(query string, f ytmusic.Filter) ([]ytmusic.Item, ytmusic.State)
	Album(id string) (ytmusic.AlbumPage, ytmusic.State)
	Artist(id string) (ytmusic.ArtistPage, ytmusic.State)
}

// Downloads plays and downloads YouTube Music songs, and says how far each
// one's download has got, and how far through it is, from 0 to 1. Without
// one the screens only browse, and every song shows as available.
type Downloads interface {
	State(videoID string) (media.DownloadState, float32)
	// Get downloads songs; those saved or on their way are left as they are.
	Get(songs []ytmusic.Song)
	// Play plays songs from start, downloading those not saved, the one
	// starting first of all.
	Play(songs []ytmusic.Song, start int)
	// Enqueue adds songs to the play queue, downloading those not saved.
	Enqueue(songs []ytmusic.Song)
}

// play plays songs from start, reporting false without downloads.
func play(dl Downloads, songs []ytmusic.Song, start int) bool {
	if dl == nil || len(songs) == 0 {
		return false
	}
	dl.Play(songs, start)
	return true
}

// get downloads songs and says so, counting those not saved yet. When all
// are saved it says that instead, asking for nothing.
func get(nav screens.Nav, dl Downloads, songs []ytmusic.Song) bool {
	if dl == nil || len(songs) == 0 {
		return false
	}
	n, last := 0, 0
	for i, s := range songs {
		if st, _ := dl.State(s.VideoID); st != media.Saved {
			n, last = n+1, i
		}
	}
	switch n {
	case 0:
		nav.Toast("Already downloaded")
		return true
	case 1:
		nav.Toast("Downloading “" + songs[last].Title + "”")
	default:
		nav.Toast("Downloading " + strconv.Itoa(n) + " songs")
	}
	dl.Get(songs)
	return true
}

// enqueue adds songs to the play queue and says so.
func enqueue(nav screens.Nav, dl Downloads, songs []ytmusic.Song) bool {
	if dl == nil || len(songs) == 0 {
		return false
	}
	dl.Enqueue(songs)
	nav.Toast(screens.AddedToQueue)
	return true
}

// pageSongs are an album's tracks as downloads.
func pageSongs(a ytmusic.AlbumPage) []ytmusic.Song {
	songs := make([]ytmusic.Song, len(a.Tracks))
	for i := range a.Tracks {
		songs[i] = a.Song(i)
	}
	return songs
}

// shared is what the YouTube tab's screens share. The typing screen makes it
// and hands it to the screens it opens, so that an album asked for on one
// is still got once that screen has closed.
type shared struct {
	nav    screens.Nav
	src    Source
	dl     Downloads
	albums []string // albums whose songs are got once their pages come, oldest first
}

// getAlbum gets album id's songs once its page comes, reporting false
// without downloads. An album already waited on is left as it is.
func (sh *shared) getAlbum(id string) bool {
	if sh.dl == nil {
		return false
	}
	if !slices.Contains(sh.albums, id) {
		sh.albums = append(sh.albums, id)
	}
	return true
}

// pollAlbums gets the songs of each album waited on whose page has come; a
// page that can't come is given up with a toast. The screen drawing the
// frame calls it.
func (sh *shared) pollAlbums() {
	if len(sh.albums) == 0 {
		return
	}
	waiting := sh.albums[:0]
	for _, id := range sh.albums {
		page, st := sh.src.Album(id)
		switch st {
		case ytmusic.Waiting:
			waiting = append(waiting, id)
			continue
		case ytmusic.Done:
			if !get(sh.nav, sh.dl, pageSongs(page)) {
				sh.nav.Toast(noSongs)
			}
		default:
			sh.nav.Toast("Couldn’t get the album’s songs")
		}
	}
	sh.albums = waiting
}

// songState is videoID's download state from dl, which may be nil.
func songState(dl Downloads, videoID string) (media.DownloadState, float32) {
	if dl == nil {
		return media.Available, 0
	}
	return dl.State(videoID)
}

// circle is the download circle's size on the results screen and pages.
const circle = 44

// drawCircle draws videoID's download state at the right of a row's box,
// in the room the row kept for it.
func drawCircle(u *ui.Context, dl Downloads, videoID string, box gfx.Rect, size int) {
	st, p := songState(dl, videoID)
	tb := widgets.TrailBox(box, float32(size))
	widgets.DownloadCircle(u, st, p, size, tb.X, box.Y+(box.H-float32(size))/2)
}

// art is a result's picture over the placeholder made from its title.
func art(title, thumb string) widgets.ArtSpec {
	s := widgets.SpecFor(title)
	if thumb != "" {
		s.Cover = media.Cover{URL: thumb}
	}
	return s
}

// stateText says why there is nothing to show: the state of an answer that
// isn't Done.
func stateText(st ytmusic.State) string {
	switch st {
	case ytmusic.Waiting:
		return "Loading…"
	case ytmusic.Offline:
		return "You’re offline."
	case ytmusic.Changed:
		return "YouTube Music isn’t answering as Boombrick expects. An update to Boombrick should fix it."
	}
	return "Couldn’t reach YouTube Music. Check the connection and try again."
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

// openItem opens the page of an album or artist result, reporting false for
// a song.
func openItem(sh *shared, it ytmusic.Item) bool {
	switch it.Kind {
	case ytmusic.AlbumItem:
		sh.nav.Push(newAlbumPage(sh, it))
	case ytmusic.ArtistItem:
		sh.nav.Push(newArtistPage(sh, it))
	default:
		return false
	}
	return true
}

// subtitle is a result's second line: "Song · Artist · 3:51", "Album ·
// Artist · 2023", "Artist · 210K monthly audience". A song's length shows
// when length is set.
func subtitle(it ytmusic.Item, length bool) string {
	var parts []string
	switch it.Kind {
	case ytmusic.SongItem:
		parts = append(parts, "Song", it.Artist)
		if length && it.Duration > 0 {
			parts = append(parts, screens.Clock(it.Duration))
		}
	case ytmusic.AlbumItem:
		parts = append(parts, it.Type, it.Artist)
		if it.Year > 0 {
			parts = append(parts, strconv.Itoa(it.Year))
		}
	case ytmusic.ArtistItem:
		parts = append(parts, "Artist", it.Audience)
	}
	return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), " · ")
}

// resultRows makes the list rows of items once, as they come. Songs keep
// songTrail at the right for their download state, albums albumTrail.
func resultRows(items []ytmusic.Item, length bool, songTrail, albumTrail float32) []widgets.Row {
	rows := make([]widgets.Row, len(items))
	arts := make([]widgets.ArtSpec, len(items))
	for i, it := range items {
		arts[i] = art(it.Title, it.Thumb)
		rows[i] = widgets.Row{Art: &arts[i], Round: it.Kind == ytmusic.ArtistItem, Title: it.Title, Subtitle: subtitle(it, length)}
		switch it.Kind {
		case ytmusic.SongItem:
			rows[i].Trail = songTrail
		case ytmusic.AlbumItem:
			rows[i].Trail = albumTrail
		}
	}
	return rows
}

// Hints the result lists share.
var (
	tabsHint   = widgets.Hint{Key: "L1/R1", Label: "Tabs"}
	filterHint = widgets.Hint{Key: "L2/R2", Label: "Filter"}
)

// resultHints are the hints for a focused result of kind, then back, then
// more, the least useful last.
func resultHints(kind ytmusic.Kind, downloads bool, back widgets.Hint, more ...widgets.Hint) []widgets.Hint {
	var h []widgets.Hint
	switch {
	case kind == ytmusic.SongItem && downloads:
		h = append(h, widgets.Hint{Key: "A", Label: "Play"}, widgets.Hint{Key: "X", Label: "Download"},
			widgets.Hint{Key: "Y", Label: "Add to queue"})
	case kind == ytmusic.AlbumItem && downloads:
		h = append(h, widgets.Hint{Key: "A", Label: "Open"}, widgets.Hint{Key: "X", Label: "Download album"})
	case kind != ytmusic.SongItem:
		h = append(h, widgets.Hint{Key: "A", Label: "Open"})
	}
	return append(append(h, back), more...)
}

// fitHints drops hints from the end, the least useful, until the rest fit
// the hint bar.
func fitHints(u *ui.Context, h []widgets.Hint) []widgets.Hint {
	for len(h) > 1 && widgets.HintsWidth(u, h) > widgets.MaxHintsWidth {
		h = h[:len(h)-1]
	}
	return h
}

// kindHints are a list's hints by the focused result's kind, made on the
// screen's first frame, where they can be measured.
type kindHints [ytmusic.ArtistItem + 1][]widgets.Hint

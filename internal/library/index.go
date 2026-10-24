package library

import (
	"cmp"
	"database/sql"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Artist is an album artist with their albums.
type Artist struct {
	Name   string
	Albums []*Album // oldest first
	Songs  int
}

// Genre is the songs tagged with one genre: by album artist, each artist's
// albums oldest first as on their page, then by disc and track.
type Genre struct {
	Name     string
	Songs    []*Track
	Artists  int           // how many artists the songs are by, each song's own
	Duration time.Duration // the songs' lengths added up
}

// Index is the library sorted for browsing. It isn't changed once built, so
// the goroutine that loads it can hand it to the UI.
type Index struct {
	Albums  []*Album  // by title
	Recent  []*Album  // most recently added first
	Artists []*Artist // by name
	Songs   []*Track  // by title
	Genres  []*Genre  // by name
	Books   []*Album  // audiobooks, by author and title; they are in no other list
	byKey   map[[2]string]*Album
	spotify map[string]*Track // the files imports downloaded, by Spotify ID
}

// Load reads the library and sorts it for browsing.
func Load(db *sql.DB) (*Index, error) {
	albums, err := Albums(db)
	if err != nil {
		return nil, err
	}
	return NewIndex(albums), nil
}

// NewIndex sorts albums, most recently added first as Albums returns them, for
// browsing. The index points into albums.
func NewIndex(albums []Album) *Index {
	ix := &Index{byKey: make(map[[2]string]*Album, len(albums)), spotify: map[string]*Track{}}
	artists := map[string]*Artist{}
	for i := range albums {
		a := &albums[i]
		if a.IsBook() {
			ix.Books = append(ix.Books, a)
			ix.byKey[[2]string{a.Artist, a.Title}] = a // for Continue listening
			continue
		}
		ix.Recent = append(ix.Recent, a)
		ix.byKey[[2]string{a.Artist, a.Title}] = a
		ar := artists[a.Artist]
		if ar == nil {
			ar = &Artist{Name: a.Artist}
			artists[a.Artist] = ar
			ix.Artists = append(ix.Artists, ar)
		}
		ar.Albums = append(ar.Albums, a)
		ar.Songs += len(a.Tracks)
		for j := range a.Tracks {
			ix.Songs = append(ix.Songs, &a.Tracks[j])
			if id := a.Tracks[j].SpotifyID; id != "" {
				ix.spotify[id] = &a.Tracks[j]
			}
		}
	}
	ix.Albums = slices.Clone(ix.Recent)
	slices.SortStableFunc(ix.Albums, func(x, y *Album) int {
		return cmp.Or(Compare(x.Title, y.Title), Compare(x.Artist, y.Artist))
	})
	slices.SortStableFunc(ix.Artists, func(x, y *Artist) int { return Compare(x.Name, y.Name) })
	slices.SortStableFunc(ix.Books, func(x, y *Album) int {
		return cmp.Or(Compare(x.Artist, y.Artist), Compare(x.Title, y.Title))
	})
	for _, ar := range ix.Artists {
		slices.SortStableFunc(ar.Albums, func(x, y *Album) int {
			return cmp.Or(cmp.Compare(x.Year, y.Year), Compare(x.Title, y.Title))
		})
	}
	slices.SortStableFunc(ix.Songs, func(x, y *Track) int {
		return cmp.Or(Compare(x.Title, y.Title), Compare(x.Artist, y.Artist))
	})
	ix.Genres = genresOf(ix.Artists)
	return ix
}

// genresOf groups the artists' songs by genre, as one genre whatever its
// case, named as it is first spelled.
func genresOf(artists []*Artist) []*Genre {
	var genres []*Genre
	byName := map[string]*Genre{}
	type byArtist struct {
		g      *Genre
		artist string
	}
	counted := map[byArtist]bool{}
	for _, ar := range artists {
		for _, a := range ar.Albums {
			for j := range a.Tracks {
				t := &a.Tracks[j]
				for _, name := range t.Genres {
					k := strings.ToLower(name)
					g := byName[k]
					if g == nil {
						g = &Genre{Name: name}
						byName[k] = g
						genres = append(genres, g)
					}
					g.Songs = append(g.Songs, t)
					g.Duration += t.Duration
					if k := (byArtist{g, t.Artist}); !counted[k] {
						counted[k] = true
						g.Artists++
					}
				}
			}
		}
	}
	slices.SortStableFunc(genres, func(x, y *Genre) int { return Compare(x.Name, y.Name) })
	return genres
}

// Album finds an album by its artist and title, or returns nil.
func (ix *Index) Album(artist, title string) *Album { return ix.byKey[[2]string{artist, title}] }

// Spotify finds the file an import downloaded of Spotify song id, or
// returns nil.
func (ix *Index) Spotify(id string) *Track { return ix.spotify[id] }

// Compare orders names as people read them: ignoring case, and comparing runs
// of digits by value, so "Track 2" sorts before "Track 10".
func Compare(a, b string) int {
	for a != "" && b != "" {
		if isDigit(a[0]) && isDigit(b[0]) {
			na, nb := digits(a), digits(b)
			if c := compareNumbers(a[:na], b[:nb]); c != 0 {
				return c
			}
			a, b = a[na:], b[nb:]
			continue
		}
		ra, sa := utf8.DecodeRuneInString(a)
		rb, sb := utf8.DecodeRuneInString(b)
		if c := cmp.Compare(unicode.ToLower(ra), unicode.ToLower(rb)); c != 0 {
			return c
		}
		a, b = a[sa:], b[sb:]
	}
	return cmp.Compare(len(a), len(b))
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func digits(s string) int {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}

// compareNumbers compares two runs of digits by value.
func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	return cmp.Or(cmp.Compare(len(a), len(b)), strings.Compare(a, b))
}

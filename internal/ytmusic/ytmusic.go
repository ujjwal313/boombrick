// Package ytmusic reads YouTube Music's search results and album and artist
// pages through the endpoints its web app uses, without signing in.
package ytmusic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/ujjwal313/boombrick/internal/netx"
)

// Kind is what a search result is.
type Kind uint8

const (
	SongItem Kind = iota
	AlbumItem
	ArtistItem
)

// Filter narrows a search to one kind of result.
type Filter uint8

const (
	All Filter = iota
	Songs
	Albums
	Artists
	filterCount
)

// FilterNames are the filters as the results screen's tabs name them.
var FilterNames = [filterCount]string{"All", "Songs", "Albums", "Artists"}

// params narrow a search as the web app's filter chips do.
var params = [filterCount]string{
	Songs:   "EgWKAQIIAWoMEA4QChADEAQQCRAF",
	Albums:  "EgWKAQIYAWoMEA4QChADEAQQCRAF",
	Artists: "EgWKAQIgAWoMEA4QChADEAQQCRAF",
}

// kinds is the kind of result each filter keeps.
var kinds = [filterCount]Kind{Songs: SongItem, Albums: AlbumItem, Artists: ArtistItem}

// Item is one search result.
type Item struct {
	Kind  Kind
	ID    string // a song's video ID, or an album's or artist's browse ID
	Title string // the song, album or artist name
	// Songs and albums: their artists as YouTube Music writes them, and the
	// first one's browse ID when it links to them.
	Artist, ArtistID string
	Album, AlbumID   string        // songs, when the result names the album
	Duration         time.Duration // songs, when the result shows it
	Year             int           // albums, when shown
	Type             string        // albums: "Album", "Single" or "EP"
	Audience         string        // artists, as "200M monthly audience"
	Thumb            string        // a picture, at whatever size YouTube Music chose
}

// Track is a song on an album's page.
type Track struct {
	VideoID  string
	Number   int
	Title    string
	Artist   string // "" when it is the album's artist
	Duration time.Duration
}

// AlbumPage is an album's details and the tracks that can be played.
type AlbumPage struct {
	ID, Title        string
	Artist, ArtistID string
	Type             string // "Album", "Single" or "EP"
	Year             int
	Thumb            string
	Tracks           []Track
}

// ArtistPage is an artist and the albums, singles and EPs their page lists.
type ArtistPage struct {
	ID, Name, Thumb string
	Albums          []Item
}

// Song is what downloading a song needs to know about it.
type Song struct {
	VideoID        string
	Title, Artist  string
	Album, AlbumID string
	AlbumArtist    string
	Number, Year   int
	Duration       time.Duration
	Thumb          string
	// SpotifyID and ISRC are the Spotify song an import matched this to,
	// written into the file's tags.
	SpotifyID, ISRC string
}

// Song is a song result as a download.
func (it Item) Song() Song {
	return Song{VideoID: it.ID, Title: it.Title, Artist: it.Artist, Album: it.Album, AlbumID: it.AlbumID,
		Duration: it.Duration, Thumb: it.Thumb}
}

// Song is track i as a download, with the album's details.
func (a AlbumPage) Song(i int) Song {
	t := a.Tracks[i]
	artist := t.Artist
	if artist == "" {
		artist = a.Artist
	}
	return Song{VideoID: t.VideoID, Title: t.Title, Artist: artist, Album: a.Title, AlbumID: a.ID,
		AlbumArtist: a.Artist, Number: t.Number, Year: a.Year, Duration: t.Duration, Thumb: a.Thumb}
}

// ErrChanged means YouTube Music answered in a shape this package can't read,
// as happens when its web app changes; a newer Boombrick is the cure.
var ErrChanged = errors.New("ytmusic: unexpected response")

// apiBase is where the web app sends its requests.
const apiBase = "https://music.youtube.com/youtubei/v1/"

// maxResponse bounds a response; an artist's page is about 600 KB.
const maxResponse = 4 << 20

// Client asks YouTube Music for search results and pages.
type Client struct {
	http *http.Client
	base string
	now  func() time.Time
	raw  func(path string, body []byte) // sees each response, for recording test data
}

// NewClient returns a client that asks through c.
func NewClient(c *http.Client) *Client { return &Client{http: c, base: apiBase, now: time.Now} }

// Search returns query's results under f in YouTube Music's order. All gives
// the top result first, then the songs, albums and artists among the rest;
// videos, playlists, profiles and podcasts are left out.
func (c *Client) Search(ctx context.Context, query string, f Filter) ([]Item, error) {
	fields := map[string]string{"query": query}
	if p := params[f]; p != "" {
		fields["params"] = p
	}
	body, err := c.post(ctx, "search", fields)
	if err != nil {
		return nil, err
	}
	return parseSearch(body, f)
}

// Album returns the album with browse ID id.
func (c *Client) Album(ctx context.Context, id string) (AlbumPage, error) {
	body, err := c.post(ctx, "browse", map[string]string{"browseId": id})
	if err != nil {
		return AlbumPage{}, err
	}
	a, err := parseAlbum(body)
	a.ID = id
	return a, err
}

// Artist returns the artist with browse ID id.
func (c *Client) Artist(ctx context.Context, id string) (ArtistPage, error) {
	body, err := c.post(ctx, "browse", map[string]string{"browseId": id})
	if err != nil {
		return ArtistPage{}, err
	}
	a, err := parseArtist(body)
	a.ID = id
	for i := range a.Albums {
		a.Albums[i].ArtistID = id
	}
	return a, err
}

// consent answers YouTube's question about cookies, asked in Europe, the UK
// and Switzerland, as yt-dlp and ytmusicapi answer it.
var consent = &http.Cookie{Name: "SOCS", Value: "CAI"}

// post sends the web app's request to path, with fields beside the context
// that names the client. English is asked for because the parser reads some
// labels, such as "Single" and the shelves' titles.
func (c *Client) post(ctx context.Context, path string, fields map[string]string) ([]byte, error) {
	msg := map[string]any{"context": map[string]any{"client": map[string]string{
		"clientName":    "WEB_REMIX",
		"clientVersion": c.version(),
		"hl":            "en",
	}}}
	for k, v := range fields {
		msg[k] = v
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path+"?prettyPrint=false", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(consent)
	body, err := netx.Do(c.http, req, maxResponse)
	if err == nil && c.raw != nil {
		c.raw(path, body)
	}
	return body, err
}

// oldestVersion is the earliest day the client version names. A Brick whose
// clock hasn't been set thinks it is 1970, and a web app that old would be
// refused.
var oldestVersion = time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)

// version is the web app's version as of today.
func (c *Client) version() string {
	day := c.now().UTC()
	if day.Before(oldestVersion) {
		day = oldestVersion
	}
	return "1." + day.Format("20060102") + ".01.00"
}

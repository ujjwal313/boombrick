package ytmusic

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The parts of the web app's responses read here. Every item is wrapped in a
// renderer named for how the app draws it; only the renderers and fields
// used are declared, and encoding/json skips the rest.

type runs struct {
	Runs []run `json:"runs"`
}

type run struct {
	Text string   `json:"text"`
	Nav  endpoint `json:"navigationEndpoint"`
}

type endpoint struct {
	Watch  *watchEndpoint  `json:"watchEndpoint"`
	Browse *browseEndpoint `json:"browseEndpoint"`
}

type watchEndpoint struct {
	VideoID string `json:"videoId"`
	Configs struct {
		Music struct {
			VideoType string `json:"musicVideoType"`
		} `json:"watchEndpointMusicConfig"`
	} `json:"watchEndpointMusicSupportedConfigs"`
}

type browseEndpoint struct {
	ID      string `json:"browseId"`
	Configs struct {
		Music struct {
			PageType string `json:"pageType"`
		} `json:"browseEndpointContextMusicConfig"`
	} `json:"browseEndpointContextSupportedConfigs"`
}

// The page and video types that matter here.
const (
	pageAlbum   = "MUSIC_PAGE_TYPE_ALBUM"
	pageArtist  = "MUSIC_PAGE_TYPE_ARTIST"
	pageChannel = "MUSIC_PAGE_TYPE_USER_CHANNEL"
	audioTrack  = "MUSIC_VIDEO_TYPE_ATV" // a song, rather than a music video or an upload
)

// page is the type of page e links to, or "".
func (e endpoint) page() string {
	if e.Browse == nil {
		return ""
	}
	return e.Browse.Configs.Music.PageType
}

type thumbnail struct {
	Renderer struct {
		Thumbnail struct {
			Thumbnails []struct {
				URL string `json:"url"`
			} `json:"thumbnails"`
		} `json:"thumbnail"`
	} `json:"musicThumbnailRenderer"`
}

// url is the largest picture's address; they are listed smallest first.
func (t thumbnail) url() string {
	th := t.Renderer.Thumbnail.Thumbnails
	if len(th) == 0 {
		return ""
	}
	return th[len(th)-1].URL
}

type column struct {
	Text runs `json:"text"`
}

// listItem is a row: a search result or an album's track.
type listItem struct {
	Thumbnail thumbnail `json:"thumbnail"`
	Flex      []struct {
		C column `json:"musicResponsiveListItemFlexColumnRenderer"`
	} `json:"flexColumns"`
	Fixed []struct {
		C column `json:"musicResponsiveListItemFixedColumnRenderer"`
	} `json:"fixedColumns"`
	Index runs `json:"index"`
	Data  struct {
		VideoID string `json:"videoId"`
	} `json:"playlistItemData"`
	Nav endpoint `json:"navigationEndpoint"`
}

// flex is the runs of flex column i, or none.
func (li *listItem) flex(i int) []run {
	if i >= len(li.Flex) {
		return nil
	}
	return li.Flex[i].C.Text.Runs
}

type rows struct {
	Contents []struct {
		Item *listItem `json:"musicResponsiveListItemRenderer"`
	} `json:"contents"`
}

type card struct {
	Title     runs      `json:"title"`
	Subtitle  runs      `json:"subtitle"`
	Thumbnail thumbnail `json:"thumbnail"`
}

type twoRow struct {
	Title     runs      `json:"title"`
	Subtitle  runs      `json:"subtitle"`
	Nav       endpoint  `json:"navigationEndpoint"`
	Thumbnail thumbnail `json:"thumbnailRenderer"`
}

type carousel struct {
	Header struct {
		Basic struct {
			Title runs `json:"title"`
		} `json:"musicCarouselShelfBasicHeaderRenderer"`
	} `json:"header"`
	Contents []struct {
		Item *twoRow `json:"musicTwoRowItemRenderer"`
	} `json:"contents"`
}

type albumHeader struct {
	Title     runs      `json:"title"`
	Subtitle  runs      `json:"subtitle"`
	Strapline runs      `json:"straplineTextOne"`
	Thumbnail thumbnail `json:"thumbnail"`
}

type artistHeader struct {
	Title     runs      `json:"title"`
	Thumbnail thumbnail `json:"thumbnail"`
}

// section is one entry of a section list; the renderer it holds is set.
type section struct {
	Shelf    *rows        `json:"musicShelfRenderer"`
	Items    *rows        `json:"itemSectionRenderer"`
	Card     *card        `json:"musicCardShelfRenderer"`
	Carousel *carousel    `json:"musicCarouselShelfRenderer"`
	Header   *albumHeader `json:"musicResponsiveHeaderRenderer"`
}

type sectionList struct {
	List *struct {
		Contents []section `json:"contents"`
	} `json:"sectionListRenderer"`
}

type tab struct {
	Tab struct {
		Content sectionList `json:"content"`
	} `json:"tabRenderer"`
}

// firstTab is the section list of tabs' first tab, or nil without one.
func firstTab(tabs []tab) *sectionList {
	if len(tabs) == 0 || tabs[0].Tab.Content.List == nil {
		return nil
	}
	return &tabs[0].Tab.Content
}

func (l *sectionList) sections() []section {
	if l == nil || l.List == nil {
		return nil
	}
	return l.List.Contents
}

type searchResponse struct {
	Contents struct {
		Tabbed *struct {
			Tabs []tab `json:"tabs"`
		} `json:"tabbedSearchResultsRenderer"`
	} `json:"contents"`
}

type browseResponse struct {
	Header struct {
		Immersive *artistHeader `json:"musicImmersiveHeaderRenderer"`
		Visual    *artistHeader `json:"musicVisualHeaderRenderer"`
	} `json:"header"`
	Contents struct {
		Two *struct {
			Tabs      []tab       `json:"tabs"`
			Secondary sectionList `json:"secondaryContents"`
		} `json:"twoColumnBrowseResultsRenderer"`
		Single *struct {
			Tabs []tab `json:"tabs"`
		} `json:"singleColumnBrowseResultsRenderer"`
	} `json:"contents"`
}

// decode reads a response, any JSON error meaning a shape not understood.
func decode(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("%w: %v", ErrChanged, err)
	}
	return nil
}

func parseSearch(body []byte, f Filter) ([]Item, error) {
	var r searchResponse
	if err := decode(body, &r); err != nil {
		return nil, err
	}
	if r.Contents.Tabbed == nil {
		return nil, ErrChanged
	}
	list := firstTab(r.Contents.Tabbed.Tabs)
	if list == nil {
		return nil, ErrChanged
	}
	var out []Item
	seen := map[string]bool{}
	add := func(it Item, ok bool) {
		if ok && !seen[it.ID] && (f == All || it.Kind == kinds[f]) {
			seen[it.ID] = true
			out = append(out, it)
		}
	}
	sections := list.sections()
	known := false
	results, recognised := 0, 0 // rows, and rows of a shape read here
	for _, s := range sections {
		if s.Card != nil {
			known = true
			add(fromCard(s.Card))
		}
		for _, rs := range []*rows{s.Shelf, s.Items} {
			if rs == nil {
				continue
			}
			known = true
			n := 0
			for _, c := range rs.Contents {
				if c.Item == nil {
					// A shelf holds results alone; an item section may hold
					// the message that there are none.
					if rs == s.Shelf {
						results++
					}
					continue
				}
				results++
				if c.Item.recognised() {
					n++
				}
				add(fromRow(c.Item))
			}
			recognised += n
			// A filtered shelf holds rows of the shapes read here, even when
			// they are kinds left out, as music videos among songs. Not one
			// row recognised means the rows have changed.
			if f != All && rs == s.Shelf && len(rs.Contents) > 0 && n == 0 {
				return nil, ErrChanged
			}
		}
	}
	// Finding nothing, YouTube Music still sends an item section, holding a
	// message. Sections none of which is known mean the shape has changed,
	// and so do results none of which is.
	if len(sections) > 0 && !known {
		return nil, ErrChanged
	}
	if results > 0 && recognised == 0 {
		return nil, fmt.Errorf("%w: none of %d results has a shape read here", ErrChanged, results)
	}
	return out, nil
}

// recognised reports whether a result's row has a shape read here, whether
// or not its kind is kept: a title, and a link to a page or a track of any
// video type.
func (li *listItem) recognised() bool {
	title := li.flex(0)
	if len(title) == 0 {
		return false
	}
	if li.Nav.page() != "" {
		return true
	}
	w := title[0].Nav.Watch
	return w != nil && w.Configs.Music.VideoType != ""
}

// fromRow reads a search result's row, reporting false for the kinds left
// out.
func fromRow(li *listItem) (Item, bool) {
	title := li.flex(0)
	if len(title) == 0 {
		return Item{}, false
	}
	return result(join(title), li.Nav, title[0].Nav.Watch, li.Data.VideoID, parseDetails(li.flex(1)), li.Thumbnail.url())
}

// fromCard reads the top result.
func fromCard(c *card) (Item, bool) {
	if len(c.Title.Runs) == 0 {
		return Item{}, false
	}
	t := c.Title.Runs[0]
	id := ""
	if t.Nav.Watch != nil {
		id = t.Nav.Watch.VideoID
	}
	return result(t.Text, t.Nav, t.Nav.Watch, id, parseDetails(c.Subtitle.Runs), c.Thumbnail.url())
}

// result makes an item of a title, the page it links to, the track it plays
// and its details: an album or artist when it links to their page, a song
// when it plays an audio track, and nothing otherwise.
func result(title string, link endpoint, watch *watchEndpoint, videoID string, d details, thumb string) (Item, bool) {
	it := Item{Title: title, Thumb: thumb}
	switch link.page() {
	case pageAlbum:
		it.Kind, it.ID, it.Year, it.Type = AlbumItem, link.Browse.ID, d.year, albumType(d.label)
		it.Artist, it.ArtistID = d.artist, d.artistID
		return it, true
	case pageArtist:
		it.Kind, it.ID = ArtistItem, link.Browse.ID
		if len(d.other) > 0 {
			it.Audience = d.other[0]
		}
		return it, true
	}
	if watch == nil || videoID == "" || watch.Configs.Music.VideoType != audioTrack {
		return Item{}, false
	}
	it.Kind, it.ID, it.Duration = SongItem, videoID, d.duration
	it.Artist, it.ArtistID, it.Album, it.AlbumID = d.artist, d.artistID, d.album, d.albumID
	return it, true
}

func parseAlbum(body []byte) (AlbumPage, error) {
	var r browseResponse
	if err := decode(body, &r); err != nil {
		return AlbumPage{}, err
	}
	two := r.Contents.Two
	if two == nil {
		return AlbumPage{}, ErrChanged
	}
	var h *albumHeader
	for _, s := range firstTab(two.Tabs).sections() {
		if s.Header != nil {
			h = s.Header
			break
		}
	}
	if h == nil {
		return AlbumPage{}, ErrChanged
	}
	d := parseDetails(h.Subtitle.Runs)
	a := AlbumPage{Title: join(h.Title.Runs), Artist: join(h.Strapline.Runs),
		ArtistID: linked(h.Strapline.Runs, pageArtist, pageChannel), Type: albumType(d.label), Year: d.year,
		Thumb: h.Thumbnail.url()}
	// The tracks are the first shelf. An album whose tracks can't be played
	// has the shelf still; without it, the page has changed.
	secondary := two.Secondary.sections()
	i := slices.IndexFunc(secondary, func(s section) bool { return s.Shelf != nil })
	if i < 0 {
		return AlbumPage{}, ErrChanged
	}
	for _, c := range secondary[i].Shelf.Contents {
		if t, ok := trackOf(c.Item); ok {
			a.Tracks = append(a.Tracks, t)
		}
	}
	return a, nil
}

// trackOf reads an album's track row. Tracks that can't be played have no
// video and are left out.
func trackOf(li *listItem) (Track, bool) {
	if li == nil || li.Data.VideoID == "" || len(li.flex(0)) == 0 {
		return Track{}, false
	}
	t := Track{VideoID: li.Data.VideoID, Title: join(li.flex(0))}
	t.Number, _ = strconv.Atoi(strings.TrimSpace(join(li.Index.Runs)))
	t.Artist = strings.TrimSpace(join(split(li.flex(1))[0]))
	if len(li.Fixed) > 0 {
		t.Duration, _ = parseClock(strings.TrimSpace(join(li.Fixed[0].C.Text.Runs)))
	}
	return t, true
}

// releases are the shelves of an artist's page that hold their albums, with
// the type their items have when they don't say.
var releases = map[string]string{"Albums": "Album", "Singles & EPs": "Single", "Singles": "Single", "EPs": "EP"}

func parseArtist(body []byte) (ArtistPage, error) {
	var r browseResponse
	if err := decode(body, &r); err != nil {
		return ArtistPage{}, err
	}
	h := r.Header.Immersive
	if h == nil {
		h = r.Header.Visual
	}
	if h == nil || r.Contents.Single == nil {
		return ArtistPage{}, ErrChanged
	}
	// An artist's page has a shelf of songs or carousels of releases, or
	// both: with neither, it has changed.
	sections := firstTab(r.Contents.Single.Tabs).sections()
	if !slices.ContainsFunc(sections, func(s section) bool { return s.Shelf != nil || s.Carousel != nil }) {
		return ArtistPage{}, ErrChanged
	}
	a := ArtistPage{Name: join(h.Title.Runs), Thumb: h.Thumbnail.url()}
	for _, s := range sections {
		if s.Carousel == nil {
			continue
		}
		typ, ok := releases[join(s.Carousel.Header.Basic.Title.Runs)]
		if !ok {
			continue
		}
		for _, c := range s.Carousel.Contents {
			tr := c.Item
			if tr == nil || tr.Nav.page() != pageAlbum {
				continue
			}
			d := parseDetails(tr.Subtitle.Runs)
			it := Item{Kind: AlbumItem, ID: tr.Nav.Browse.ID, Title: join(tr.Title.Runs), Artist: a.Name, Year: d.year,
				Type: typ, Thumb: tr.Thumbnail.url()}
			if d.label != "" {
				it.Type = albumType(d.label)
			}
			a.Albums = append(a.Albums, it)
		}
	}
	return a, nil
}

// details is what a result's second line says: "Song • Radiohead • OK
// Computer • 4:25", "Album • Radiohead • 1997", "Artist • 200M monthly
// audience". Parts are told apart by their links and their form, not their
// place, which differs between filtered and unfiltered results.
type details struct {
	label            string
	artist, artistID string
	album, albumID   string
	year             int
	duration         time.Duration
	other            []string
}

// labels start the second line of unfiltered results.
var labels = map[string]bool{"Song": true, "Video": true, "Album": true, "Single": true, "EP": true, "Artist": true,
	"Playlist": true, "Episode": true, "Podcast": true, "Profile": true}

func parseDetails(rs []run) details {
	var d details
	for i, g := range split(rs) {
		t := strings.TrimSpace(join(g))
		switch {
		case t == "":
		case i == 0 && len(g) == 1 && g[0].Nav.Browse == nil && labels[t]:
			d.label = t
		case linked(g, pageAlbum) != "":
			d.album, d.albumID = t, linked(g, pageAlbum)
		case linked(g, pageArtist, pageChannel) != "":
			if d.artist == "" {
				d.artist, d.artistID = t, linked(g, pageArtist, pageChannel)
			}
		default:
			if dur, ok := parseClock(t); ok {
				d.duration = dur
			} else if y, ok := parseYear(t); ok {
				d.year = y
			} else {
				d.other = append(d.other, t)
			}
		}
	}
	// An artist with no channel of their own is plain text: the first part
	// nothing else claimed.
	if d.artist == "" && d.label != "Artist" && len(d.other) > 0 {
		d.artist, d.other = d.other[0], d.other[1:]
	}
	return d
}

// split cuts runs at the " • " between parts.
func split(rs []run) [][]run {
	var out [][]run
	start := 0
	for i, r := range rs {
		if r.Text == " • " {
			out = append(out, rs[start:i])
			start = i + 1
		}
	}
	return append(out, rs[start:])
}

func join(rs []run) string {
	var b strings.Builder
	for _, r := range rs {
		b.WriteString(r.Text)
	}
	return b.String()
}

// linked is the browse ID of the first run in rs that links to a page of
// one of types, or "".
func linked(rs []run, types ...string) string {
	for _, r := range rs {
		for _, t := range types {
			if r.Nav.page() == t {
				return r.Nav.Browse.ID
			}
		}
	}
	return ""
}

// albumType names a release from its label, an album unless it says
// otherwise.
func albumType(label string) string {
	if label == "Single" || label == "EP" {
		return label
	}
	return "Album"
}

// parseClock reads a length such as "4:25" or "1:02:03".
func parseClock(s string) (time.Duration, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	secs := 0
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || i > 0 && (len(p) != 2 || n > 59) {
			return 0, false
		}
		secs = secs*60 + n
	}
	return time.Duration(secs) * time.Second, true
}

// parseYear reads a four-digit year.
func parseYear(s string) (int, bool) {
	y, err := strconv.Atoi(s)
	return y, err == nil && len(s) == 4 && y >= 1000
}

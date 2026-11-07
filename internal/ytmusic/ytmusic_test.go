package ytmusic

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/netx"
)

// Building blocks of the web app's responses.

func q(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

const dot = `{"text":" • "}`

func plain(s string) string { return `{"text":` + q(s) + `}` }

func browseNav(id, page string) string {
	return `{"browseEndpoint":{"browseId":` + q(id) +
		`,"browseEndpointContextSupportedConfigs":{"browseEndpointContextMusicConfig":{"pageType":"MUSIC_PAGE_TYPE_` + page + `"}}}}`
}

func link(s, id, page string) string {
	return `{"text":` + q(s) + `,"navigationEndpoint":` + browseNav(id, page) + `}`
}

func play(s, id, kind string) string {
	return `{"text":` + q(s) + `,"navigationEndpoint":{"watchEndpoint":{"videoId":` + q(id) +
		`,"watchEndpointMusicSupportedConfigs":{"watchEndpointMusicConfig":{"musicVideoType":"MUSIC_VIDEO_TYPE_` + kind + `"}}}}}`
}

func textRuns(rs ...string) string { return `{"runs":[` + strings.Join(rs, ",") + `]}` }

func thumb(url string) string {
	return `{"musicThumbnailRenderer":{"thumbnail":{"thumbnails":[{"url":"https://example.com/small"},{"url":` + q(url) + `}]}}}`
}

// row is a result row: its browse link (or "" for songs and videos), its
// video ID (or ""), its picture and its columns.
func row(nav, videoID, pic string, cols ...string) string {
	s := `{"musicResponsiveListItemRenderer":{"thumbnail":` + thumb(pic) + `,"flexColumns":[`
	for i, c := range cols {
		if i > 0 {
			s += ","
		}
		s += `{"musicResponsiveListItemFlexColumnRenderer":{"text":` + c + `}}`
	}
	s += "]"
	if videoID != "" {
		s += `,"playlistItemData":{"videoId":` + q(videoID) + `}`
	}
	if nav != "" {
		s += `,"navigationEndpoint":` + nav
	}
	return s + "}}"
}

func searchPage(sections ...string) string {
	return `{"contents":{"tabbedSearchResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[` +
		strings.Join(sections, ",") + `]}}}}]}}}`
}

func shelf(rows ...string) string {
	return `{"musicShelfRenderer":{"contents":[` + strings.Join(rows, ",") + `]}}`
}

func itemSection(r string) string { return `{"itemSectionRenderer":{"contents":[` + r + `]}}` }

func topCard(title, subtitle, pic string) string {
	return `{"musicCardShelfRenderer":{"title":` + title + `,"subtitle":` + subtitle + `,"thumbnail":` + thumb(pic) + `}}`
}

func albumResponse(header string, tracks ...string) string {
	return `{"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[` +
		`{"musicResponsiveHeaderRenderer":` + header + `}]}}}}],"secondaryContents":{"sectionListRenderer":{"contents":[` +
		`{"musicShelfRenderer":{"contents":[` + strings.Join(tracks, ",") + `]}},{"musicCarouselShelfRenderer":{"contents":[]}}]}}}}}`
}

// track is an album page's track row; artists is a text object, "{}" when the
// track is the album artist's.
func track(n, title, videoID, kind, artists, length string) string {
	s := `{"musicResponsiveListItemRenderer":{"index":` + textRuns(plain(n)) + `,"flexColumns":[` +
		`{"musicResponsiveListItemFlexColumnRenderer":{"text":` + textRuns(play(title, videoID, kind)) + `}},` +
		`{"musicResponsiveListItemFlexColumnRenderer":{"text":` + artists + `}},` +
		`{"musicResponsiveListItemFlexColumnRenderer":{"text":` + textRuns(plain("19M plays")) + `}}],` +
		`"fixedColumns":[{"musicResponsiveListItemFixedColumnRenderer":{"text":` + textRuns(plain(length)) + `}}]`
	if videoID != "" {
		s += `,"playlistItemData":{"videoId":` + q(videoID) + `}`
	}
	return s + "}}"
}

func artistResponse(name, pic string, shelves ...string) string {
	return `{"header":{"musicImmersiveHeaderRenderer":{"title":` + textRuns(plain(name)) + `,"thumbnail":` + thumb(pic) + `}},` +
		`"contents":{"singleColumnBrowseResultsRenderer":{"tabs":[{"tabRenderer":{"content":{"sectionListRenderer":{"contents":[` +
		strings.Join(shelves, ",") + `]}}}}]}}}`
}

func carouselOf(title string, items ...string) string {
	return `{"musicCarouselShelfRenderer":{"header":{"musicCarouselShelfBasicHeaderRenderer":{"title":` + textRuns(plain(title)) +
		`}},"contents":[` + strings.Join(items, ",") + `]}}`
}

func twoRowItem(title, nav, pic string, sub ...string) string {
	return `{"musicTwoRowItemRenderer":{"title":` + textRuns(plain(title)) + `,"subtitle":` + textRuns(sub...) +
		`,"navigationEndpoint":` + nav + `,"thumbnailRenderer":` + thumb(pic) + `}}`
}

func TestSearchSongs(t *testing.T) {
	body := searchPage(shelf(
		row("", "4IJI6soiQhI", "https://yt3.googleusercontent.com/kp=w120-h120-l90-rj",
			textRuns(play("Karma Police", "4IJI6soiQhI", "ATV")),
			textRuns(link("Radiohead", "UCr", "ARTIST"), dot, link("OK Computer", "MPREb_ok", "ALBUM"), dot, plain("4:25")),
			textRuns(plain("259M plays"))),
		row("", "ldS1", "https://yt3.googleusercontent.com/oc=w120-h120-l90-rj",
			textRuns(play("Ok Computer", "ldS1", "ATV")),
			textRuns(link("Lemaitre", "UCl", "ARTIST"), plain(" & "), link("Rebmoe", "UCm", "ARTIST"), dot, plain("3:10"))),
		row("", "live1", "https://i.ytimg.com/vi/live1/hqdefault.jpg",
			textRuns(play("Karma Police (Live)", "live1", "UGC")),
			textRuns(link("Someone", "UCs", "USER_CHANNEL"), dot, plain("1.2M views"))),
	))
	got, err := parseSearch([]byte(body), Songs)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{
		{Kind: SongItem, ID: "4IJI6soiQhI", Title: "Karma Police", Artist: "Radiohead", ArtistID: "UCr", Album: "OK Computer",
			AlbumID: "MPREb_ok", Duration: 265 * time.Second, Thumb: "https://yt3.googleusercontent.com/kp=w120-h120-l90-rj"},
		{Kind: SongItem, ID: "ldS1", Title: "Ok Computer", Artist: "Lemaitre & Rebmoe", ArtistID: "UCl",
			Duration: 190 * time.Second, Thumb: "https://yt3.googleusercontent.com/oc=w120-h120-l90-rj"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestSearchAll(t *testing.T) {
	body := searchPage(
		topCard(textRuns(link("OK Computer", "MPREb_ok", "ALBUM")),
			textRuns(plain("Album"), dot, link("Radiohead", "UCr", "ARTIST"), dot, plain("1997")),
			"https://yt3.googleusercontent.com/ok=w544-h544-l90-rj"),
		itemSection(row(browseNav("UCr", "ARTIST"), "", "https://lh3.googleusercontent.com/rh=w120-h120-p-l90-rj",
			textRuns(plain("Radiohead")), textRuns(plain("Artist"), dot, plain("200M monthly audience")))),
		itemSection(row("", "letdown", "https://yt3.googleusercontent.com/ld=w120-h120-l90-rj",
			textRuns(play("Let Down", "letdown", "ATV")), textRuns(plain("Song"), dot, link("Radiohead", "UCr", "ARTIST")))),
		itemSection(row(browseNav("VLPL1", "PLAYLIST"), "", "https://example.com/pl",
			textRuns(plain("Radiohead - OK Computer")), textRuns(plain("Playlist"), dot, link("James", "UCj", "USER_CHANNEL")))),
		itemSection(row("", "airbag", "https://i.ytimg.com/vi/airbag/hqdefault.jpg",
			textRuns(play("Airbag (Live)", "airbag", "UGC")), textRuns(plain("Video"), dot, link("Bells", "UCb", "USER_CHANNEL")))),
		itemSection(row(browseNav("MPREb_kida", "ALBUM"), "", "https://yt3.googleusercontent.com/ka=w120-h120-l90-rj",
			textRuns(plain("Kid A")), textRuns(plain("Album"), dot, link("Radiohead", "UCr", "ARTIST"), dot, plain("2000")))),
		// The top result again, further down.
		itemSection(row(browseNav("MPREb_ok", "ALBUM"), "", "https://example.com/again",
			textRuns(plain("OK Computer")), textRuns(plain("Album"), dot, link("Radiohead", "UCr", "ARTIST"), dot, plain("1997")))),
		itemSection(row(browseNav("MPSP1", "PODCAST_SHOW_DETAIL_PAGE"), "", "https://example.com/pod",
			textRuns(plain("Breakdowns")), textRuns(plain("Podcast")))),
	)
	got, err := parseSearch([]byte(body), All)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{
		{Kind: AlbumItem, ID: "MPREb_ok", Title: "OK Computer", Artist: "Radiohead", ArtistID: "UCr", Year: 1997, Type: "Album",
			Thumb: "https://yt3.googleusercontent.com/ok=w544-h544-l90-rj"},
		{Kind: ArtistItem, ID: "UCr", Title: "Radiohead", Audience: "200M monthly audience",
			Thumb: "https://lh3.googleusercontent.com/rh=w120-h120-p-l90-rj"},
		{Kind: SongItem, ID: "letdown", Title: "Let Down", Artist: "Radiohead", ArtistID: "UCr",
			Thumb: "https://yt3.googleusercontent.com/ld=w120-h120-l90-rj"},
		{Kind: AlbumItem, ID: "MPREb_kida", Title: "Kid A", Artist: "Radiohead", ArtistID: "UCr", Year: 2000, Type: "Album",
			Thumb: "https://yt3.googleusercontent.com/ka=w120-h120-l90-rj"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// A filtered search keeps only its kind, and reads singles as albums.
func TestSearchAlbums(t *testing.T) {
	body := searchPage(shelf(
		row(browseNav("MPREb_s", "ALBUM"), "", "https://yt3.googleusercontent.com/s=w544-h544-l90-rj",
			textRuns(plain("Radiohead")), textRuns(plain("Single"), dot, link("Lawrence Chris", "UCc", "ARTIST"), dot, plain("2024"))),
		row(browseNav("UCr", "ARTIST"), "", "https://example.com/a",
			textRuns(plain("Radiohead")), textRuns(plain("Artist"), dot, plain("200M monthly audience"))),
	))
	got, err := parseSearch([]byte(body), Albums)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{{Kind: AlbumItem, ID: "MPREb_s", Title: "Radiohead", Artist: "Lawrence Chris", ArtistID: "UCc", Year: 2024,
		Type: "Single", Thumb: "https://yt3.googleusercontent.com/s=w544-h544-l90-rj"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestAlbumPage(t *testing.T) {
	header := `{"title":` + textRuns(plain("OK Computer")) + `,"subtitle":` + textRuns(plain("Album"), dot, plain("1997")) +
		`,"straplineTextOne":` + textRuns(link("Radiohead", "UCr", "ARTIST")) +
		`,"thumbnail":` + thumb("https://yt3.googleusercontent.com/ok=w544-h544-l90-rj") +
		`,"secondSubtitle":` + textRuns(plain("12 songs"), dot, plain("53 minutes")) + `}`
	body := albumResponse(header,
		track("1", "Airbag", "jNY", "ATV", "{}", "4:48"),
		track("2", "Paranoid Android", "fHi", "OMV", "{}", "6:28"),
		track("3", "Not Available", "", "ATV", "{}", "3:00"),
		track("4", "Lucky", "luck", "ATV", textRuns(link("Radiohead", "UCr", "ARTIST"), plain(" & "), link("Guest", "UCg", "ARTIST")), "4:19"),
	)
	got, err := parseAlbum([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := AlbumPage{Title: "OK Computer", Artist: "Radiohead", ArtistID: "UCr", Type: "Album", Year: 1997,
		Thumb: "https://yt3.googleusercontent.com/ok=w544-h544-l90-rj", Tracks: []Track{
			{VideoID: "jNY", Number: 1, Title: "Airbag", Duration: 288 * time.Second},
			{VideoID: "fHi", Number: 2, Title: "Paranoid Android", Duration: 388 * time.Second},
			{VideoID: "luck", Number: 4, Title: "Lucky", Artist: "Radiohead & Guest", Duration: 259 * time.Second},
		}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	// As downloads: the album's artist stands in for tracks without their own.
	if s := got.Song(0); s.Artist != "Radiohead" || s.AlbumArtist != "Radiohead" || s.Album != "OK Computer" ||
		s.Number != 1 || s.Year != 1997 || s.Thumb != got.Thumb {
		t.Fatalf("Song(0) = %+v", s)
	}
	if s := got.Song(2); s.Artist != "Radiohead & Guest" || s.AlbumArtist != "Radiohead" {
		t.Fatalf("Song(2) = %+v", s)
	}
}

func TestArtistPage(t *testing.T) {
	body := artistResponse("Radiohead", "https://lh3.googleusercontent.com/rh=w2880-h1200-p-l90-rj",
		shelf(row("", "kp", "https://example.com/kp", textRuns(play("Karma Police", "kp", "ATV")))),
		carouselOf("Albums",
			twoRowItem("KID A MNESIA", browseNav("MPREb_E7", "ALBUM"), "https://yt3.googleusercontent.com/km=w544-h544-l90-rj", plain("2021")),
			twoRowItem("A Moon Shaped Pool", browseNav("MPREb_zw", "ALBUM"), "https://yt3.googleusercontent.com/ms=w544-h544-l90-rj", plain("2016"))),
		carouselOf("Singles & EPs",
			twoRowItem("Follow Me Around", browseNav("MPREb_fm", "ALBUM"), "https://yt3.googleusercontent.com/fm=w544-h544-l90-rj",
				plain("Single"), dot, plain("2021"))),
		carouselOf("Videos",
			twoRowItem("Creep", `{"watchEndpoint":{"videoId":"cr"}}`, "https://i.ytimg.com/vi/cr/hq.jpg", plain("Radiohead"))),
		carouselOf("Featured on",
			twoRowItem("This Is Radiohead", browseNav("VLRD1", "PLAYLIST"), "https://example.com/p", plain("Playlist"))),
	)
	got, err := parseArtist([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	want := ArtistPage{Name: "Radiohead", Thumb: "https://lh3.googleusercontent.com/rh=w2880-h1200-p-l90-rj", Albums: []Item{
		{Kind: AlbumItem, ID: "MPREb_E7", Title: "KID A MNESIA", Artist: "Radiohead", Year: 2021, Type: "Album",
			Thumb: "https://yt3.googleusercontent.com/km=w544-h544-l90-rj"},
		{Kind: AlbumItem, ID: "MPREb_zw", Title: "A Moon Shaped Pool", Artist: "Radiohead", Year: 2016, Type: "Album",
			Thumb: "https://yt3.googleusercontent.com/ms=w544-h544-l90-rj"},
		{Kind: AlbumItem, ID: "MPREb_fm", Title: "Follow Me Around", Artist: "Radiohead", Year: 2021, Type: "Single",
			Thumb: "https://yt3.googleusercontent.com/fm=w544-h544-l90-rj"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

// Responses that aren't what the parser knows say so; an empty answer is
// just empty.
func TestUnknownShapes(t *testing.T) {
	for _, body := range []string{`{"contents":{"singleColumnBrowseResultsRenderer":{}}}`, `<html>`, `{}`} {
		if _, err := parseSearch([]byte(body), All); !errors.Is(err, ErrChanged) {
			t.Errorf("search %s: %v", body, err)
		}
		if _, err := parseAlbum([]byte(body)); !errors.Is(err, ErrChanged) {
			t.Errorf("album %s: %v", body, err)
		}
		if _, err := parseArtist([]byte(body)); !errors.Is(err, ErrChanged) {
			t.Errorf("artist %s: %v", body, err)
		}
	}

	// Shapes that parse but have lost what is read from them.
	unknownType := `{"text":"Karma Police","navigationEndpoint":{"watchEndpoint":{"videoId":"kp"}}}`
	changed := map[string]struct {
		body string
		f    Filter
	}{
		"no tabs":                  {`{"contents":{"tabbedSearchResultsRenderer":{"tabs":[]}}}`, All},
		"renamed sections":         {searchPage(`{"musicNewShelfRenderer":{"contents":[]}}`), All},
		"renamed rows":             {searchPage(shelf(`{"musicNewListItemRenderer":{}}`, `{"musicNewListItemRenderer":{}}`)), Songs},
		"renamed rows, unfiltered": {searchPage(shelf(`{"musicNewListItemRenderer":{}}`, `{"musicNewListItemRenderer":{}}`)), All},
		"moved video type":         {searchPage(shelf(row("", "kp", "https://example.com/kp", textRuns(unknownType)))), Songs},
		"moved video type, unfiltered": {searchPage(
			itemSection(row("", "kp", "https://example.com/kp", textRuns(unknownType))),
			itemSection(row("", "nt", "https://example.com/nt",
				textRuns(`{"text":"No Surprises","navigationEndpoint":{"watchEndpoint":{"videoId":"nt"}}}`)))), All},
	}
	for name, c := range changed {
		if got, err := parseSearch([]byte(c.body), c.f); !errors.Is(err, ErrChanged) {
			t.Errorf("search, %s: %v, %v", name, got, err)
		}
	}
	album := albumResponse(`{"title":`+textRuns(plain("OK Computer"))+`}`, track("1", "Airbag", "jNY", "ATV", "{}", "4:48"))
	for renderer, what := range map[string]string{"musicShelfRenderer": "its track shelf", "musicResponsiveHeaderRenderer": "its header"} {
		body := strings.Replace(album, renderer, "musicNewRenderer", 1)
		if got, err := parseAlbum([]byte(body)); !errors.Is(err, ErrChanged) {
			t.Errorf("album without %s: %+v, %v", what, got, err)
		}
	}
	for _, body := range []string{artistResponse("Radiohead", "https://example.com/r"),
		artistResponse("Radiohead", "https://example.com/r", `{"musicNewCarouselRenderer":{}}`)} {
		if got, err := parseArtist([]byte(body)); !errors.Is(err, ErrChanged) {
			t.Errorf("artist without shelves: %+v, %v", got, err)
		}
	}

	// YouTube Music's answer when it finds nothing.
	nothing := itemSection(`{"messageRenderer":{"text":` + textRuns(plain("No results found")) + `}}`)
	for _, body := range []string{searchPage(), searchPage(nothing)} {
		for _, f := range []Filter{All, Songs} {
			if got, err := parseSearch([]byte(body), f); err != nil || len(got) != 0 {
				t.Errorf("empty search under %s = %v, %v", FilterNames[f], got, err)
			}
		}
	}
	// Songs can come back as music videos alone, which are read and left out.
	videos := searchPage(shelf(
		row("", "bs", "https://example.com/bs", textRuns(play("Boom Shaka", "bs", "OMV")), textRuns(link("Someone", "UCs", "ARTIST"))),
		row("", "we", "https://example.com/we", textRuns(play("Wedding", "we", "UGC")), textRuns(link("Else", "UCe", "USER_CHANNEL")))))
	if got, err := parseSearch([]byte(videos), Songs); err != nil || len(got) != 0 {
		t.Errorf("songs that are all videos = %v, %v", got, err)
	}
	if got, err := parseAlbum([]byte(albumResponse(`{"title":` + textRuns(plain("Empty")) + `}`))); err != nil || len(got.Tracks) != 0 {
		t.Errorf("album without tracks = %+v, %v", got, err)
	}
	if got, err := parseArtist([]byte(artistResponse("Radiohead", "https://example.com/r", carouselOf("Videos")))); err != nil ||
		len(got.Albums) != 0 {
		t.Errorf("artist without releases = %+v, %v", got, err)
	}
}

func TestParseClock(t *testing.T) {
	good := map[string]time.Duration{"4:25": 265 * time.Second, "0:59": 59 * time.Second, "1:02:03": 3723 * time.Second}
	for s, want := range good {
		if got, ok := parseClock(s); !ok || got != want {
			t.Errorf("parseClock(%q) = %v, %v", s, got, ok)
		}
	}
	for _, s := range []string{"4:5", "4:60", "a:10", "12", "1:2:3:4", "", "2021"} {
		if _, ok := parseClock(s); ok {
			t.Errorf("parseClock(%q) accepted", s)
		}
	}
}

func TestClientAsksAsTheWebAppDoes(t *testing.T) {
	type request struct {
		Context struct {
			Client struct {
				Name    string `json:"clientName"`
				Version string `json:"clientVersion"`
				Lang    string `json:"hl"`
			} `json:"client"`
		} `json:"context"`
		Query  string `json:"query"`
		Params string `json:"params"`
		Browse string `json:"browseId"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		// The cookie answers YouTube's question about cookies, asked in Europe.
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || r.URL.Query().Get("prettyPrint") != "false" ||
			req.Context.Client.Name != "WEB_REMIX" || req.Context.Client.Version != "1.20261017.01.00" ||
			req.Context.Client.Lang != "en" || r.Header.Get("Cookie") != "SOCS=CAI" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		switch {
		case r.URL.Path == "/search" && req.Query == "karma police" && req.Params == params[Songs]:
			w.Write([]byte(searchPage(shelf(row("", "kp", "https://example.com/kp",
				textRuns(play("Karma Police", "kp", "ATV")), textRuns(link("Radiohead", "UCr", "ARTIST"), dot, plain("4:25")))))))
		case r.URL.Path == "/browse" && req.Browse == "MPREb_ok":
			w.Write([]byte(albumResponse(`{"title":`+textRuns(plain("OK Computer"))+`}`, track("1", "Airbag", "jNY", "ATV", "{}", "4:48"))))
		case r.URL.Path == "/browse" && req.Browse == "UCr":
			w.Write([]byte(artistResponse("Radiohead", "https://example.com/r",
				carouselOf("Albums", twoRowItem("Kid A", browseNav("MPREb_ka", "ALBUM"), "https://example.com/ka", plain("2000"))))))
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	c := NewClient(srv.Client())
	c.base = srv.URL + "/"
	c.now = func() time.Time { return time.Date(2026, 10, 17, 22, 0, 0, 0, time.UTC) }
	ctx := context.Background()

	items, err := c.Search(ctx, "karma police", Songs)
	if err != nil || len(items) != 1 || items[0].ID != "kp" || items[0].Duration != 265*time.Second {
		t.Fatalf("Search = %+v, %v", items, err)
	}
	a, err := c.Album(ctx, "MPREb_ok")
	if err != nil || a.ID != "MPREb_ok" || a.Title != "OK Computer" || len(a.Tracks) != 1 {
		t.Fatalf("Album = %+v, %v", a, err)
	}
	ar, err := c.Artist(ctx, "UCr")
	if err != nil || ar.ID != "UCr" || len(ar.Albums) != 1 || ar.Albums[0].ArtistID != "UCr" {
		t.Fatalf("Artist = %+v, %v", ar, err)
	}
	var se *netx.StatusError
	if _, err := c.Search(ctx, "nothing", All); !errors.As(err, &se) || se.Code != 400 {
		t.Fatalf("a refused request: %v", err)
	}
}

// A Brick whose clock hasn't been set still names a recent web app.
func TestClientVersionHasAFloor(t *testing.T) {
	c := NewClient(http.DefaultClient)
	cases := []struct {
		now  time.Time
		want string
	}{
		{time.Unix(0, 0), "1.20261017.01.00"},
		{time.Date(2026, 10, 16, 23, 0, 0, 0, time.UTC), "1.20261017.01.00"},
		{time.Date(2027, 3, 2, 12, 0, 0, 0, time.UTC), "1.20270302.01.00"},
	}
	for _, tc := range cases {
		c.now = func() time.Time { return tc.now }
		if got := c.version(); got != tc.want {
			t.Errorf("at %v: %q, want %q", tc.now, got, tc.want)
		}
	}
}

// recorded are responses saved from YouTube Music by TestLive with
// BOOMBRICK_RECORD=1, to check the parser against the real thing offline.
var recorded = []struct {
	file  string
	parse func([]byte) (int, error)
}{
	{"search-all.json", func(b []byte) (int, error) { items, err := parseSearch(b, All); return checkItems(items), err }},
	{"search-songs.json", func(b []byte) (int, error) { items, err := parseSearch(b, Songs); return checkItems(items), err }},
	{"search-albums.json", func(b []byte) (int, error) { items, err := parseSearch(b, Albums); return checkItems(items), err }},
	{"search-artists.json", func(b []byte) (int, error) { items, err := parseSearch(b, Artists); return checkItems(items), err }},
	// Songs for a nonsense query, saved by hand: whatever is nearest, a music
	// video first.
	{"search-songs-nonsense.json", func(b []byte) (int, error) { items, err := parseSearch(b, Songs); return checkItems(items), err }},
	{"album.json", func(b []byte) (int, error) {
		a, err := parseAlbum(b)
		n := 0
		for _, t := range a.Tracks {
			if t.VideoID != "" && t.Title != "" && t.Duration > 0 && a.Title != "" && a.Artist != "" {
				n++
			}
		}
		return n, err
	}},
	{"artist.json", func(b []byte) (int, error) { a, err := parseArtist(b); return checkItems(a.Albums), err }},
}

// checkItems counts the items that have what the screens show.
func checkItems(items []Item) int {
	n := 0
	for _, it := range items {
		if it.ID != "" && it.Title != "" && it.Thumb != "" && (it.Kind == ArtistItem || it.Artist != "") {
			n++
		}
	}
	return n
}

func TestRecordedResponses(t *testing.T) {
	for _, r := range recorded {
		path := filepath.Join("testdata", r.file)
		body, err := os.ReadFile(path)
		if err != nil {
			t.Skipf("%s is missing; record it with BOOMBRICK_RECORD=1 go test -tags live -run TestLive ./internal/ytmusic/", path)
		}
		if n, err := r.parse(body); err != nil || n == 0 {
			t.Errorf("%s: %d complete results, %v", r.file, n, err)
		}
	}
}

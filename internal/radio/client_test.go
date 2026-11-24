package radio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ujjwal313/boombrick/internal/netx"
)

// rb is a fake Radio Browser server: it records the requests it gets, with
// their paths as sent, and answers each path from body, or with status when
// that is set.
type rb struct {
	srv    *httptest.Server
	mu     sync.Mutex
	asked  []string
	status int
	body   map[string]string
}

func newRB(t *testing.T, status int, body map[string]string) *rb {
	t.Helper()
	s := &rb{status: status, body: body}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Boombrick/") {
			t.Errorf("no user agent on %s", r.URL)
		}
		s.mu.Lock()
		s.asked = append(s.asked, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		s.mu.Unlock()
		if s.status != 0 {
			w.WriteHeader(s.status)
			return
		}
		b, ok := s.body[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(b))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *rb) requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.asked)
}

// query returns the path and parsed query of the n'th request.
func (s *rb) query(t *testing.T, n int) (string, url.Values) {
	t.Helper()
	a := s.requests()
	if len(a) <= n {
		t.Fatalf("only %d requests: %v", len(a), a)
	}
	path, raw, _ := strings.Cut(a[n], "?")
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatal(err)
	}
	return path, q
}

// clientFor returns a client whose servers are the given fakes, and a count
// of how often it asked for the list.
func clientFor(servers ...*rb) (*Client, *int) {
	c := NewClient(netx.NewClient(), nil)
	n := new(int)
	c.servers = func(context.Context) ([]string, error) {
		*n++
		urls := make([]string, len(servers))
		for i, s := range servers {
			urls[i] = s.srv.URL
		}
		return urls, nil
	}
	return c, n
}

func one(t *testing.T, body string) (*Client, *rb) {
	t.Helper()
	s := newRB(t, 0, map[string]string{"/json/stations/search": body})
	c, _ := clientFor(s)
	return c, s
}

func TestTopKeepsPlayableStations(t *testing.T) {
	var parts []string
	for i := range 150 {
		hls, codec, u := 0, "MP3", fmt.Sprintf("http://s%d.example/stream", i)
		// Every third station is unplayable, in turn for each reason: 50 in all.
		if i%3 == 0 {
			switch i / 3 % 3 {
			case 0:
				hls = 1
			case 1:
				codec = "OGG"
			case 2:
				u = ""
			}
		}
		name := fmt.Sprintf("Station %d", i)
		tags := ""
		if i == 1 {
			name = "  Radio Three "
			tags = " news, hindi ,"
		}
		parts = append(parts, fmt.Sprintf(`{"stationuuid":"u%d","name":%q,"url_resolved":%q,"codec":%q,"hls":%d,"bitrate":128,"tags":%q,"countrycode":"IN"}`, i, name, u, codec, hls, tags))
	}
	c, s := one(t, "["+strings.Join(parts, ",")+"]")
	got, err := c.Top(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Fatalf("Top kept %d stations, want 100", len(got))
	}
	var three Station
	for _, st := range got {
		if st.URL == "" || st.Codec != "MP3" {
			t.Fatalf("kept %+v", st)
		}
		if st.ID == "u1" {
			three = st
		}
	}
	if three.Name != "Radio Three" || !slices.Equal(three.Tags, []string{"news", "hindi"}) {
		t.Fatalf("station 1 = %+v", three)
	}
	path, q := s.query(t, 0)
	if path != "/json/stations/search" || q.Get("order") != "clickcount" || q.Get("reverse") != "true" ||
		q.Get("hidebroken") != "true" || q.Get("limit") != "150" {
		t.Fatalf("asked %s %v", path, q)
	}
}

func TestAACIsPlayable(t *testing.T) {
	c, _ := one(t, `[
		{"stationuuid":"a","name":"A","url_resolved":"http://a","codec":"AAC+"},
		{"stationuuid":"b","name":"B","url_resolved":"http://b","codec":"aac"},
		{"stationuuid":"c","name":"C","url_resolved":"http://c","codec":"FLAC"},
		{"name":"No id","url_resolved":"http://d","codec":"MP3"}
	]`)
	got, err := c.Top(context.Background())
	if err != nil || len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("Top = %+v, %v", got, err)
	}
}

func TestSearches(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		call func(*Client) ([]Station, error)
		want map[string]string
	}{
		{"country", func(c *Client) ([]Station, error) { return c.ByCountry(ctx, "IN") },
			map[string]string{"countrycode": "IN", "limit": "300"}},
		{"tag", func(c *Client) ([]Station, error) { return c.ByTag(ctx, "jazz") },
			map[string]string{"tag": "jazz", "tagExact": "true", "limit": "300"}},
		{"name", func(c *Client) ([]Station, error) { return c.Search(ctx, "radio x") },
			map[string]string{"name": "radio x", "limit": "100"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, s := one(t, `[{"stationuuid":"a","name":"A","url_resolved":"http://a","codec":"MP3"}]`)
			got, err := tt.call(c)
			if err != nil || len(got) != 1 {
				t.Fatalf("got %+v, %v", got, err)
			}
			path, q := s.query(t, 0)
			if path != "/json/stations/search" || q.Get("order") != "clickcount" || q.Get("reverse") != "true" || q.Get("hidebroken") != "true" {
				t.Fatalf("asked %s %v", path, q)
			}
			for k, v := range tt.want {
				if q.Get(k) != v {
					t.Fatalf("%s = %q, want %q in %v", k, q.Get(k), v, q)
				}
			}
		})
	}
	// The name is query-escaped, so a space is a plus.
	c, s := one(t, `[]`)
	c.Search(ctx, "radio x")
	if a := s.requests(); !strings.Contains(a[0], "name=radio+x") {
		t.Fatalf("asked %v", a)
	}
}

func TestCountries(t *testing.T) {
	s := newRB(t, 0, map[string]string{"/json/countries": `[
		{"name":"India","iso_3166_1":"IN","stationcount":1204},
		{"name":"Nowhere","iso_3166_1":"","stationcount":5},
		{"name":"Empty","iso_3166_1":"XX","stationcount":0}
	]`})
	c, _ := clientFor(s)
	got, err := c.Countries(context.Background())
	if err != nil || len(got) != 1 || got[0] != (Count{Name: "India", Code: "IN", Stations: 1204}) {
		t.Fatalf("Countries = %+v, %v", got, err)
	}
	path, q := s.query(t, 0)
	if path != "/json/countries" || q.Get("order") != "stationcount" || q.Get("reverse") != "true" || q.Get("hidebroken") != "true" {
		t.Fatalf("asked %s %v", path, q)
	}
}

func TestRateLimitedServerIsPassedOver(t *testing.T) {
	limited := newRB(t, http.StatusTooManyRequests, nil)
	good := newRB(t, 0, map[string]string{"/json/stations/search": `[]`})
	c, _ := clientFor(limited, good)
	if _, err := c.Top(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(limited.requests()) != 1 || len(good.requests()) != 1 {
		t.Fatalf("limited %v, good %v", limited.requests(), good.requests())
	}
}

func TestGenres(t *testing.T) {
	s := newRB(t, 0, map[string]string{"/json/tags": `[{"name":"jazz","stationcount":900},{"name":"","stationcount":3},{"name":"none","stationcount":0}]`})
	c, _ := clientFor(s)
	got, err := c.Genres(context.Background())
	if err != nil || len(got) != 1 || got[0] != (Count{Name: "jazz", Stations: 900}) {
		t.Fatalf("Genres = %+v, %v", got, err)
	}
	path, q := s.query(t, 0)
	if path != "/json/tags" || q.Get("order") != "stationcount" || q.Get("reverse") != "true" ||
		q.Get("hidebroken") != "true" || q.Get("limit") != "200" {
		t.Fatalf("asked %s %v", path, q)
	}
}

func TestClick(t *testing.T) {
	s := newRB(t, 0, map[string]string{"/json/url/u1": `{"ok":true}`})
	c, _ := clientFor(s)
	if err := c.Click(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	if a := s.requests(); len(a) != 1 || !strings.HasPrefix(a[0], "/json/url/u1") {
		t.Fatalf("asked %v", a)
	}
}

// The station's ID is escaped in the path, so it can't end the path or
// start a query.
func TestClickEscapesTheID(t *testing.T) {
	s := newRB(t, 0, map[string]string{"/json/url/a/b c?d": `{"ok":true}`})
	c, _ := clientFor(s)
	if err := c.Click(context.Background(), "a/b c?d"); err != nil {
		t.Fatal(err)
	}
	if path, q := s.query(t, 0); path != "/json/url/a%2Fb%20c%3Fd" || len(q) != 0 {
		t.Fatalf("asked %s %v", path, q)
	}
}

// Each search keeps at most its limit of the stations Radio Browser sends.
func TestSearchesKeepTheirLimits(t *testing.T) {
	var parts []string
	for i := range 400 {
		parts = append(parts, fmt.Sprintf(`{"stationuuid":"u%d","name":"Station %d","url_resolved":"http://s%d.example","codec":"MP3"}`, i, i, i))
	}
	body := "[" + strings.Join(parts, ",") + "]"
	ctx := context.Background()
	tests := []struct {
		name string
		call func(*Client) ([]Station, error)
		keep int
	}{
		{"top", func(c *Client) ([]Station, error) { return c.Top(ctx) }, 100},
		{"name", func(c *Client) ([]Station, error) { return c.Search(ctx, "station") }, 100},
		{"country", func(c *Client) ([]Station, error) { return c.ByCountry(ctx, "IN") }, 300},
		{"tag", func(c *Client) ([]Station, error) { return c.ByTag(ctx, "jazz") }, 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := one(t, body)
			got, err := tt.call(c)
			if err != nil || len(got) != tt.keep {
				t.Fatalf("kept %d stations, %v; want %d", len(got), err, tt.keep)
			}
			if got[0].ID != "u0" || got[tt.keep-1].ID != fmt.Sprintf("u%d", tt.keep-1) {
				t.Fatalf("kept %s to %s", got[0].ID, got[tt.keep-1].ID)
			}
		})
	}
}

// An answer that isn't the JSON asked for is an error, not an empty list.
func TestMalformedAnswersAreErrors(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name, path string
		call       func(*Client) (int, error)
	}{
		{"stations", "/json/stations/search", func(c *Client) (int, error) { s, err := c.Top(ctx); return len(s), err }},
		{"countries", "/json/countries", func(c *Client) (int, error) { s, err := c.Countries(ctx); return len(s), err }},
		{"genres", "/json/tags", func(c *Client) (int, error) { s, err := c.Genres(ctx); return len(s), err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, body := range []string{`[{"stationuuid":"a","name":`, `{"error":"busy"}`, ``, `<html>`} {
				c, _ := clientFor(newRB(t, 0, map[string]string{tt.path: body}))
				if n, err := tt.call(c); err == nil || n != 0 {
					t.Errorf("answered %q: %d, %v; want an error", body, n, err)
				}
			}
		})
	}
}

func TestFailsOverAndRemembers(t *testing.T) {
	bad := newRB(t, http.StatusInternalServerError, nil)
	good := newRB(t, 0, map[string]string{"/json/stations/search": `[]`})
	c, lists := clientFor(bad, good)
	if _, err := c.Top(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Top(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bad.requests()) != 1 || len(good.requests()) != 2 || *lists != 1 {
		t.Fatalf("bad %v, good %v, listed %d times", bad.requests(), good.requests(), *lists)
	}
}

func TestNotFoundIsReturnedAtOnce(t *testing.T) {
	first := newRB(t, 0, nil)
	second := newRB(t, 0, map[string]string{"/json/stations/search": `[]`})
	c, _ := clientFor(first, second)
	_, err := c.Top(context.Background())
	var se *netx.StatusError
	if !errors.As(err, &se) || se.Code != 404 || errors.Is(err, ErrUnreachable) {
		t.Fatalf("Top = %v, want a 404", err)
	}
	if len(second.requests()) != 0 {
		t.Fatalf("second asked %v", second.requests())
	}
}

func TestEveryServerFailing(t *testing.T) {
	a := newRB(t, http.StatusInternalServerError, nil)
	b := newRB(t, http.StatusBadGateway, nil)
	c, lists := clientFor(a, b)
	for range 2 {
		if _, err := c.Top(context.Background()); !errors.Is(err, ErrUnreachable) {
			t.Fatalf("Top = %v, want ErrUnreachable", err)
		}
	}
	if *lists != 2 {
		t.Fatalf("listed servers %d times, want 2", *lists)
	}
}

func TestCancelledContextIsReturned(t *testing.T) {
	s := newRB(t, 0, map[string]string{"/json/stations/search": `[]`})
	c, _ := clientFor(s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Top(ctx); !errors.Is(err, context.Canceled) || errors.Is(err, ErrUnreachable) {
		t.Fatalf("Top = %v, want context.Canceled", err)
	}
}

package radio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/ujjwal313/boombrick/internal/netx"
)

// Count is a country or a genre with the number of stations it holds. Code is
// the country's ISO 3166-1 code, empty for a genre.
type Count struct {
	Name, Code string
	Stations   int
}

// Client asks Radio Browser for stations. It finds the servers when first
// needed, and keeps the one that last answered at the front.
type Client struct {
	http    *http.Client
	servers func(context.Context) ([]string, error)

	mu   sync.Mutex
	list []string
}

// NewClient returns a client that finds Radio Browser's servers through r.
func NewClient(c *http.Client, r Resolver) *Client {
	return &Client{http: c, servers: func(ctx context.Context) ([]string, error) {
		return Servers(ctx, r, func(s []string) {
			rand.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] })
		})
	}}
}

// get asks the servers in turn for path until one answers. A status below 500
// is the answer to the question and is returned as it is, except 429: mirrors
// rate-limit, so that and anything else moves on to the next server.
func (c *Client) get(ctx context.Context, path string, q url.Values) ([]byte, error) {
	c.mu.Lock()
	list := slices.Clone(c.list)
	c.mu.Unlock()
	if len(list) == 0 {
		var err error
		if list, err = c.servers(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.list = slices.Clone(list)
		c.mu.Unlock()
	}
	var last error
	for _, base := range list {
		body, err := netx.Get(ctx, c.http, base+path+"?"+q.Encode(), 4<<20)
		if err == nil {
			c.prefer(base)
			return body, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var se *netx.StatusError
		if errors.As(err, &se) && se.Code < 500 && se.Code != http.StatusTooManyRequests {
			return nil, err
		}
		last = err
	}
	c.mu.Lock()
	c.list = nil
	c.mu.Unlock()
	return nil, fmt.Errorf("%w: %v", ErrUnreachable, last)
}

// prefer moves base to the front of the server list.
func (c *Client) prefer(base string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := slices.Index(c.list, base); i > 0 {
		c.list = slices.Insert(slices.Delete(c.list, i, i+1), 0, base)
	}
}

type wireStation struct {
	ID          string `json:"stationuuid"`
	Name        string `json:"name"`
	URL         string `json:"url_resolved"`
	Logo        string `json:"favicon"`
	Country     string `json:"country"`
	CountryCode string `json:"countrycode"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
	Tags        string `json:"tags"`
	HLS         int    `json:"hls"`
}

// playable reports whether Boombrick can play w: not HLS, with a resolved
// stream, in MP3 or AAC (AAC+ is HE-AAC, which faad2 decodes with SBR).
func (w wireStation) playable() bool {
	switch strings.ToUpper(strings.TrimSpace(w.Codec)) {
	case "MP3", "AAC", "AAC+":
		return w.HLS == 0 && w.ID != "" && strings.TrimSpace(w.URL) != ""
	}
	return false
}

func (w wireStation) station() Station {
	s := Station{
		ID:          w.ID,
		Name:        strings.TrimSpace(w.Name),
		URL:         strings.TrimSpace(w.URL),
		Logo:        strings.TrimSpace(w.Logo),
		Country:     strings.TrimSpace(w.Country),
		CountryCode: strings.TrimSpace(w.CountryCode),
		Codec:       strings.ToUpper(strings.TrimSpace(w.Codec)),
		Bitrate:     w.Bitrate,
	}
	for _, t := range strings.Split(w.Tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			s.Tags = append(s.Tags, t)
		}
	}
	return s
}

// stations searches with q, most listened first, and keeps the first keep
// stations Boombrick can play.
func (c *Client) stations(ctx context.Context, q url.Values, keep int) ([]Station, error) {
	q.Set("hidebroken", "true")
	q.Set("order", "clickcount")
	q.Set("reverse", "true")
	body, err := c.get(ctx, "/json/stations/search", q)
	if err != nil {
		return nil, err
	}
	var wire []wireStation
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("radio: decode stations: %w", err)
	}
	var out []Station
	for _, w := range wire {
		if len(out) == keep {
			break
		}
		if w.playable() {
			out = append(out, w.station())
		}
	}
	return out, nil
}

// Top returns the most listened stations.
func (c *Client) Top(ctx context.Context) ([]Station, error) {
	return c.stations(ctx, url.Values{"limit": {"150"}}, 100)
}

// Search returns the stations whose name contains name.
func (c *Client) Search(ctx context.Context, name string) ([]Station, error) {
	return c.stations(ctx, url.Values{"name": {name}, "limit": {"100"}}, 100)
}

// ByCountry returns a country's stations, by its ISO 3166-1 code.
func (c *Client) ByCountry(ctx context.Context, code string) ([]Station, error) {
	return c.stations(ctx, url.Values{"countrycode": {code}, "limit": {"300"}}, 300)
}

// ByTag returns the stations tagged exactly tag.
func (c *Client) ByTag(ctx context.Context, tag string) ([]Station, error) {
	return c.stations(ctx, url.Values{"tag": {tag}, "tagExact": {"true"}, "limit": {"300"}}, 300)
}

// Countries returns the countries that have stations.
func (c *Client) Countries(ctx context.Context) ([]Count, error) {
	body, err := c.get(ctx, "/json/countries", url.Values{"order": {"stationcount"}, "reverse": {"true"}, "hidebroken": {"true"}})
	if err != nil {
		return nil, err
	}
	var wire []struct {
		Name     string `json:"name"`
		Code     string `json:"iso_3166_1"`
		Stations int    `json:"stationcount"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("radio: decode countries: %w", err)
	}
	var out []Count
	for _, w := range wire {
		if code := strings.TrimSpace(w.Code); code != "" && w.Stations > 0 {
			out = append(out, Count{Name: strings.TrimSpace(w.Name), Code: code, Stations: w.Stations})
		}
	}
	return out, nil
}

// Genres returns the most used tags, the biggest first.
func (c *Client) Genres(ctx context.Context) ([]Count, error) {
	q := url.Values{"order": {"stationcount"}, "reverse": {"true"}, "hidebroken": {"true"}, "limit": {"200"}}
	body, err := c.get(ctx, "/json/tags", q)
	if err != nil {
		return nil, err
	}
	var wire []struct {
		Name     string `json:"name"`
		Stations int    `json:"stationcount"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("radio: decode genres: %w", err)
	}
	var out []Count
	for _, w := range wire {
		if name := strings.TrimSpace(w.Name); name != "" && w.Stations > 0 {
			out = append(out, Count{Name: name, Stations: w.Stations})
		}
	}
	return out, nil
}

// Click tells Radio Browser that a station was played, which is how it ranks
// stations by popularity.
func (c *Client) Click(ctx context.Context, id string) error {
	_, err := c.get(ctx, "/json/url/"+url.PathEscape(id), url.Values{})
	return err
}

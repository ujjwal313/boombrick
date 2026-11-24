//go:build live

package radio

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/netx"
)

// TestLive asks the real Radio Browser and plays a few seconds of real
// stations, so changes in either show up. It needs the internet.
func TestLive(t *testing.T) {
	web := &http.Client{Timeout: 20 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	servers, err := Servers(ctx, net.DefaultResolver, func([]string) {})
	if err != nil || len(servers) == 0 {
		t.Fatalf("servers: %v, %v", servers, err)
	}
	t.Logf("servers: %v", servers)

	c := NewClient(web, net.DefaultResolver)
	top, err := c.Top(ctx)
	if err != nil || len(top) == 0 {
		t.Fatalf("top: %d stations, %v", len(top), err)
	}
	countries, err := c.Countries(ctx)
	if err != nil || len(countries) == 0 {
		t.Fatalf("countries: %d, %v", len(countries), err)
	}
	genres, err := c.Genres(ctx)
	if err != nil || len(genres) == 0 {
		t.Fatalf("genres: %d, %v", len(genres), err)
	}
	found, err := c.Search(ctx, "jazz")
	if err != nil || len(found) == 0 {
		t.Fatalf("search: %d stations, %v", len(found), err)
	}
	t.Logf("top %d, %d countries, %d genres, %d for jazz", len(top), len(countries), len(genres), len(found))

	streams := StreamClient()
	play := func(s Station) bool {
		l, err := Open(ctx, s.URL, Options{HTTP: streams, Online: func() bool { return true }, Name: s.Name})
		if err != nil {
			t.Logf("%s (%s) didn't open: %v", s.Name, s.Codec, err)
			return false
		}
		defer l.Close()
		n := listen(t, l, 10*time.Second)
		t.Logf("%s (%s): %d Hz, %d kbps (listed %d), %s, %d frames in 10 s, title %q", s.Name, s.Codec, l.SampleRate(), l.Kbps(), s.Bitrate, l.Format(), n, l.Title())
		return n > 0
	}

	// The first MP3 station of Top that plays, and the first plain AAC one.
	played := map[string]bool{}
	for _, s := range top {
		if (s.Codec == "MP3" || s.Codec == "AAC") && !played[s.Codec] && play(s) {
			played[s.Codec] = true
		}
		if played["MP3"] && played["AAC"] {
			break
		}
	}
	if !played["MP3"] {
		t.Errorf("no MP3 station of Top played")
	}

	// AAC+ is rare in Top, so it is asked for by codec. It is required: one of
	// these playing shows such a station plays, though not that its SBR was
	// used, as without SBR faad2 would still play the core, duller.
	plus, err := c.stations(ctx, url.Values{"codec": {"AAC+"}, "limit": {"150"}}, 100)
	if err != nil {
		t.Fatalf("AAC+ stations: %v", err)
	}
	tried := 0
	for _, s := range plus {
		if s.Codec != "AAC+" {
			continue
		}
		if tried++; tried > 15 {
			break
		}
		if play(s) {
			played["AAC+"] = true
			break
		}
	}
	t.Logf("%d AAC+ stations listed, %d tried", len(plus), tried)
	if !played["AAC+"] {
		t.Errorf("no AAC+ station played: %d listed, %d tried", len(plus), tried)
	}

	// Radio Browser may already follow playlists for url_resolved; report
	// any station among the first 40 whose address still answers with one.
	check := &http.Client{Timeout: 10 * time.Second}
	first := top
	if len(first) > 40 {
		first = first[:40]
	}
	// Each is asked in turn within its own 10 s, so a slow one can't use up
	// the rest's time; those that don't answer are counted, not hidden.
	playlists, silent := 0, 0
	for _, s := range first {
		ask, stop := context.WithTimeout(context.Background(), 10*time.Second)
		req, err := http.NewRequestWithContext(ask, http.MethodGet, s.URL, nil)
		if err != nil {
			stop()
			continue
		}
		req.Header.Set("User-Agent", netx.UserAgent()) // some stations answer by it
		resp, err := check.Do(req)
		if err != nil {
			stop()
			silent++
			continue
		}
		ct := resp.Header.Get("Content-Type")
		resp.Body.Close()
		stop()
		if _, playlist := codecOf(ct); playlist {
			playlists++
			t.Logf("playlist: %s answers %q", s.Name, ct)
		}
	}
	t.Logf("%d of the first %d stations answered with a playlist; %d didn't answer", playlists, len(first), silent)
}

// listen reads l for d and returns the frames it gave.
func listen(t *testing.T, l *Live, d time.Duration) int {
	t.Helper()
	buf := make([]float32, 4096)
	frames := 0
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		n, err := l.Read(buf)
		frames += n
		if err != nil && err != io.EOF {
			t.Logf("read: %v", err)
			break
		}
		if n == 0 {
			time.Sleep(20 * time.Millisecond)
		}
	}
	return frames
}

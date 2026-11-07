package helper

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/spotify"
)

// fake is a Session that answers from its fields and records what it was
// asked.
type fake struct {
	status    spotify.Status
	playlists []spotify.Playlist
	tracks    map[string][]spotify.Track
	stream    *fakeStream
	err       error // every call's answer, when set

	asked struct {
		id    string
		ms    int64
		kbps  int
		level bool
	}
}

func (f *fake) Status() spotify.Status { return f.status }
func (f *fake) Login(context.Context) (spotify.Login, error) {
	return spotify.Login{URL: "https://spotify.com/pair?code=ABCD", Code: "ABCD"}, f.err
}
func (f *fake) Logout() error { return f.err }
func (f *fake) Playlists(context.Context) ([]spotify.Playlist, error) {
	return f.playlists, f.err
}
func (f *fake) Tracks(_ context.Context, uri string) ([]spotify.Track, error) {
	return f.tracks[uri], f.err
}
func (f *fake) Stream(_ context.Context, id string, ms int64, kbps int, level bool) (Stream, error) {
	f.asked.id, f.asked.ms, f.asked.kbps, f.asked.level = id, ms, kbps, level
	if f.err != nil {
		return nil, f.err
	}
	return f.stream, nil
}

// fakeStream plays samples, then ends with end (io.EOF for a clean end).
type fakeStream struct {
	samples  []float32
	end      error
	released bool
}

func (s *fakeStream) SampleRate() int { return 44100 }
func (s *fakeStream) Duration() int64 { return 213000 }
func (s *fakeStream) Read(p []float32) (int, error) {
	if len(s.samples) == 0 {
		return 0, s.end
	}
	n := copy(p, s.samples)
	s.samples = s.samples[n:]
	return n, nil
}
func (s *fakeStream) Release() { s.released = true }

func serve(t *testing.T, f *fake) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(Handler(f, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	return srv
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
}

func TestStatusAndLists(t *testing.T) {
	f := &fake{
		status:    spotify.Status{State: spotify.Ready, User: "sam", Premium: true},
		playlists: []spotify.Playlist{{URI: "spotify:playlist:1", Name: "Gym", Owner: "sam", Count: 42}},
		tracks: map[string][]spotify.Track{spotify.LikedURI: {{ID: "4uLU6hMCjMI75M1A2tKUQC", Title: "Stan",
			Artists: []string{"Eminem", "Dido"}, Album: "The Marshall Mathers LP", Duration: 404000, ISRC: "USIR10000449"}}},
	}
	srv := serve(t, f)

	var st spotify.Status
	getJSON(t, srv.URL+"/status", &st)
	if !reflect.DeepEqual(st, f.status) {
		t.Fatalf("status %+v, want %+v", st, f.status)
	}
	var ps []spotify.Playlist
	getJSON(t, srv.URL+"/playlists", &ps)
	if !reflect.DeepEqual(ps, f.playlists) {
		t.Fatalf("playlists %+v", ps)
	}
	var ts []spotify.Track
	getJSON(t, srv.URL+"/tracks?uri="+spotify.LikedURI, &ts)
	if !reflect.DeepEqual(ts, f.tracks[spotify.LikedURI]) {
		t.Fatalf("tracks %+v", ts)
	}

	resp, err := http.Post(srv.URL+"/login", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var l spotify.Login
	json.NewDecoder(resp.Body).Decode(&l)
	resp.Body.Close()
	if l.Code != "ABCD" {
		t.Fatalf("login %+v", l)
	}
}

// Each error the session gives has a status of its own, and the client turns
// it back into the same error.
func TestErrorsBecomeStatuses(t *testing.T) {
	for _, c := range []struct {
		err   error
		code  int
		retry string
	}{
		{spotify.ErrSignedOut, http.StatusUnauthorized, ""},
		{spotify.ErrUnavailable, http.StatusForbidden, ""},
		{spotify.ErrPremium, http.StatusPaymentRequired, ""},
		{&spotify.Throttled{Wait: spotify.KeyWait}, http.StatusTooManyRequests, "40"},
		{spotify.ErrOffline, http.StatusServiceUnavailable, ""},
	} {
		srv := serve(t, &fake{err: c.err})
		resp, err := http.Get(srv.URL + "/stream/4uLU6hMCjMI75M1A2tKUQC")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.code || resp.Header.Get("Retry-After") != c.retry {
			t.Fatalf("%v: %d, Retry-After %q", c.err, resp.StatusCode, resp.Header.Get("Retry-After"))
		}
		back := spotify.ErrorOf(resp.StatusCode, resp.Header.Get("Retry-After"), "")
		var th *spotify.Throttled
		if errors.As(c.err, &th) {
			if !errors.As(back, &th) || th.Wait != 40*time.Second {
				t.Fatalf("throttle came back as %v", back)
			}
			continue
		}
		if !errors.Is(back, c.err) {
			t.Fatalf("%v came back as %v", c.err, back)
		}
	}
}

func TestStreamSendsSamples(t *testing.T) {
	samples := []float32{0.5, -0.25, 1, -1, 0.125, 0}
	f := &fake{stream: &fakeStream{samples: samples, end: io.EOF}}
	srv := serve(t, f)

	resp, err := http.Get(srv.URL + "/stream/4uLU6hMCjMI75M1A2tKUQC?ms=61500&kbps=320&level=1")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get(spotify.HeaderRate) != "44100" || resp.Header.Get(spotify.HeaderDuration) != "213000" {
		t.Fatalf("headers %v", resp.Header)
	}
	var got []float32
	for i := 0; i+4 <= len(body); i += 4 {
		got = append(got, math.Float32frombits(binary.LittleEndian.Uint32(body[i:])))
	}
	if !reflect.DeepEqual(got, samples) {
		t.Fatalf("samples %v, want %v", got, samples)
	}
	if a := f.asked; a.id != "4uLU6hMCjMI75M1A2tKUQC" || a.ms != 61500 || a.kbps != 320 || !a.level {
		t.Fatalf("asked %+v", a)
	}
	if !f.stream.released {
		t.Fatal("the stream wasn't handed back")
	}
}

// A stream that fails part way ends the response abruptly, so the reader can
// tell it from the track's end.
func TestStreamCutShort(t *testing.T) {
	f := &fake{stream: &fakeStream{samples: make([]float32, 3*chunk), end: errors.New("connection reset")}}
	srv := serve(t, f)

	resp, err := http.Get(srv.URL + "/stream/4uLU6hMCjMI75M1A2tKUQC")
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil {
		t.Fatal("a cut-short stream read as a whole one")
	}
}

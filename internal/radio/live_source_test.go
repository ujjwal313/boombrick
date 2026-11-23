package radio

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
	"github.com/ujjwal313/boombrick/internal/netx"
)

// station is a radio station for the tests: an httptest server streaming a
// fixture over and over. It answers each request with the next of its
// replies, and with the last one again once they run out.
type station struct {
	url     string
	replies []reply
	got     chan struct{} // a request came
	ended   chan struct{} // a request ended: its client went, or it was answered

	mu     sync.Mutex
	asked  int
	header http.Header // the last request's
}

// reply is a station's answer to one request.
type reply struct {
	file    []byte            // streamed over and over
	header  map[string]string // Content-Type and the like
	metaint int               // audio bytes between title blocks; 0 for none
	title   string
	status  int  // a status other than 200 to answer with instead
	cut     int  // audio bytes after which the connection breaks; 0 for never
	hang    bool // at cut, send nothing more but keep the connection open
	silent  bool // never answer
}

func serve(t *testing.T, replies ...reply) *station {
	t.Helper()
	s := &station{replies: replies, got: make(chan struct{}, 64), ended: make(chan struct{}, 64)}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
	})
	s.url = srv.URL
	return s
}

func (s *station) serve(w http.ResponseWriter, r *http.Request) {
	context.AfterFunc(r.Context(), func() { tell(s.ended) })
	s.mu.Lock()
	rp := s.replies[min(s.asked, len(s.replies)-1)]
	s.asked++
	s.header = r.Header.Clone()
	s.mu.Unlock()
	tell(s.got)
	switch {
	case rp.silent:
		<-r.Context().Done()
		return
	case rp.status != 0:
		w.WriteHeader(rp.status)
		return
	}
	for k, v := range rp.header {
		w.Header().Set(k, v)
	}
	if rp.metaint > 0 {
		w.Header().Set("icy-metaint", strconv.Itoa(rp.metaint))
	}
	w.WriteHeader(http.StatusOK)
	block := []byte{0}
	if rp.title != "" {
		block = pad([]byte("StreamTitle='" + rp.title + "';"))
	}
	sent, at, left := 0, 0, rp.metaint
	for r.Context().Err() == nil {
		k := min(len(rp.file)-at, 4096)
		if rp.metaint > 0 {
			k = min(k, left)
		}
		if rp.cut > 0 {
			k = min(k, rp.cut-sent)
		}
		if _, err := w.Write(rp.file[at : at+k]); err != nil {
			return
		}
		sent, at = sent+k, (at+k)%len(rp.file)
		if rp.metaint > 0 {
			if left -= k; left == 0 {
				if _, err := w.Write(block); err != nil {
					return
				}
				left = rp.metaint
			}
		}
		if rp.cut > 0 && sent == rp.cut {
			w.(http.Flusher).Flush()
			if rp.hang {
				<-r.Context().Done()
				return
			}
			panic(http.ErrAbortHandler) // breaks the connection
		}
	}
}

func tell(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

func (s *station) requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked
}

func (s *station) lastHeader() http.Header {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.header
}

// waitEnded waits for a request to s to end. The server knows its client went
// as soon as the connection closes, but a handler blocked writing to it may
// only learn it at TCP's FIN timeout, when the client closed with nothing
// left unread and its last window zero.
func (s *station) waitEnded(t *testing.T) {
	t.Helper()
	select {
	case <-s.ended:
	case <-time.After(5 * time.Second):
		t.Fatal("the station still serves the request")
	}
}

// quick is the Options for a test: the stream client, giving up after giveUp
// and waiting retry between tries.
func quick(giveUp, retry time.Duration) Options {
	return Options{HTTP: StreamClient(), GiveUp: giveUp, Retry: func(int) time.Duration { return retry }}
}

func open(t *testing.T, url string, o Options) *Live {
	t.Helper()
	l, err := Open(context.Background(), url, o)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// play reads frames of audio from l, failing on an error or if they don't
// come within 5 s.
func play(t *testing.T, l *Live, frames int) {
	t.Helper()
	buf := make([]float32, 2*1024)
	deadline := time.Now().Add(5 * time.Second)
	for got := 0; got < frames; {
		if time.Now().After(deadline) {
			t.Fatalf("%d frames in 5 s, want %d", got, frames)
		}
		n, err := l.Read(buf)
		if err != nil {
			t.Fatalf("Read after %d frames: %v", got, err)
		}
		got += n
	}
}

// failure reads l until it fails, and returns the error, failing the test if
// that takes longer than within.
func failure(t *testing.T, l *Live, within time.Duration) error {
	t.Helper()
	buf := make([]float32, 2*1024)
	for deadline := time.Now().Add(within); time.Now().Before(deadline); {
		if _, err := l.Read(buf); err != nil {
			return err
		}
	}
	t.Fatalf("Read didn't fail within %v", within)
	return nil
}

// goroutines counts the goroutines and returns their stacks, leaving out the
// test server's for its connections, which can outlive a client that closed
// as waitEnded says.
func goroutines() (int, string) {
	buf := make([]byte, 1<<20)
	all := string(buf[:runtime.Stack(buf, true)])
	n := 0
	for _, g := range strings.Split(all, "\n\n") {
		if !strings.Contains(g, "net/http.(*conn).serve") && !strings.Contains(g, "net/http.(*connReader).backgroundRead") {
			n++
		}
	}
	return n, all
}

// settles waits up to a second for the number of goroutines to come back to
// n, so a closed or failed stream is known to leave none behind.
func settles(t *testing.T, n int) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); ; time.Sleep(10 * time.Millisecond) {
		got, all := goroutines()
		if got <= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines, want %d:\n%s", got, n, all)
		}
	}
}

var (
	mpeg = map[string]string{"Content-Type": "audio/mpeg"}
	aacp = map[string]string{"Content-Type": "audio/aacp"}
)

func TestLiveMP3(t *testing.T) {
	file := fixtureBytes(t, "stream-44100.mp3")
	s := serve(t, reply{file: file, header: map[string]string{"Content-Type": "audio/mpeg", "icy-br": "128"},
		metaint: 8192, title: "Artist - Song"})
	l := open(t, s.url, quick(time.Minute, time.Second))
	if l.SampleRate() != 44100 || l.Format() != "MP3" || l.Kbps() != 128 || l.Frames() != -1 {
		t.Errorf("rate %d, format %q, %d kbit/s, %d frames; want 44100, MP3, 128, -1",
			l.SampleRate(), l.Format(), l.Kbps(), l.Frames())
	}
	h := s.lastHeader()
	if h.Get("Icy-MetaData") != "1" || h.Get("User-Agent") != netx.UserAgent() {
		t.Errorf("asked with Icy-MetaData %q, User-Agent %q", h.Get("Icy-MetaData"), h.Get("User-Agent"))
	}
	play(t, l, 44100)
	if got := l.Title(); got != "Artist - Song" {
		t.Errorf("Title() = %q, want %q", got, "Artist - Song")
	}
	if l.Stalled() {
		t.Error("stalled while the stream plays")
	}
	if err := l.SeekFrame(1000); err != nil {
		t.Errorf("SeekFrame: %v", err)
	}
	play(t, l, 4410)
}

func TestLiveAAC(t *testing.T) {
	tests := []struct {
		name, file, contentType, format string
	}{
		{"aacp", "stream-44100.aac", "audio/aacp", "AAC"},
		{"sniffed", "stream-44100.aac", "application/octet-stream", "AAC"},
		{"sniffed mono 22050", "stream-22050.aac", "application/octet-stream", "AAC"},
		{"sniffed mp3", "stream-44100.mp3", "application/octet-stream", "MP3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serve(t, reply{file: fixtureBytes(t, tt.file), header: map[string]string{"Content-Type": tt.contentType}})
			l := open(t, s.url, quick(time.Minute, time.Second))
			if l.Format() != tt.format || l.SampleRate() != 44100 {
				t.Errorf("format %q, rate %d; want %s, 44100", l.Format(), l.SampleRate(), tt.format)
			}
			play(t, l, 44100)
		})
	}
}

func TestLiveRefuses(t *testing.T) {
	mp3File := fixtureBytes(t, "stream-44100.mp3")
	tests := []struct {
		name string
		r    reply
		want func(error) bool
	}{
		{"playlist", reply{file: []byte("http://example.com/stream\n"),
			header: map[string]string{"Content-Type": "audio/x-mpegurl"}}, isUnsupported},
		{"no frames", reply{file: make([]byte, 64<<10),
			header: map[string]string{"Content-Type": "application/octet-stream"}}, isUnsupported},
		{"MP3 sent as AAC", reply{file: mp3File, header: aacp}, isUnsupported},
		{"not found", reply{status: http.StatusNotFound}, netx.IsNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serve(t, tt.r)
			before, _ := goroutines()
			o := quick(time.Minute, time.Second)
			l, err := Open(context.Background(), s.url, o)
			if l != nil {
				l.Close()
			}
			if err == nil || !tt.want(err) {
				t.Fatalf("Open: %v", err)
			}
			s.waitEnded(t)
			o.HTTP.CloseIdleConnections() // kept from a 404, as any client keeps them
			settles(t, before)
		})
	}
}

func isUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }

func TestLiveOpenCancelled(t *testing.T) {
	s := serve(t, reply{silent: true})
	before, _ := goroutines()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		l, err := Open(ctx, s.url, quick(time.Minute, time.Second))
		if l != nil {
			l.Close()
		}
		done <- err
	}()
	select {
	case <-s.got:
	case <-time.After(5 * time.Second):
		t.Fatal("no request came")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if d := time.Since(start); d > time.Second { // against the 15 s the open would take
			t.Errorf("Open returned %v after its ctx was cancelled", d)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Open: %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Open didn't return when its ctx was cancelled")
	}
	s.waitEnded(t)
	settles(t, before)
}

// The player cancels the open's context once it has the source, which must
// play on, on its first request: for 10 s, more than the ring and the MP3
// decoder's buffer hold between them (2 s and 64 KB, 4 s at 128 kbit/s).
func TestLiveOutlivesItsOpen(t *testing.T) {
	s := serve(t, reply{file: fixtureBytes(t, "stream-44100.mp3"), header: mpeg})
	ctx, cancel := context.WithCancel(context.Background())
	l, err := Open(ctx, s.url, quick(time.Minute, time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	cancel()
	play(t, l, 10*44100)
	if n := s.requests(); n != 1 {
		t.Errorf("%d requests, want the stream to play on its first", n)
	}
}

func TestLiveTitleNamingTheStation(t *testing.T) {
	s := serve(t, reply{file: fixtureBytes(t, "stream-44100.mp3"), header: mpeg, metaint: 8192, title: "Radio ONE"})
	o := quick(time.Minute, time.Second)
	o.Name = "Radio One"
	l := open(t, s.url, o)
	play(t, l, 44100) // more than 8192 bytes: a title has come
	if got := l.Title(); got != "" {
		t.Errorf("Title() = %q, want none", got)
	}
	l.setTitle("Artist - Song")
	l.setTitle("radio one")
	if got := l.Title(); got != "" {
		t.Errorf("Title() after the station's name = %q, want none", got)
	}
}

func TestLiveReconnects(t *testing.T) {
	file := fixtureBytes(t, "stream-44100.mp3")
	s := serve(t, reply{file: file, header: mpeg, cut: 64 << 10}, reply{file: file, header: mpeg})
	l := open(t, s.url, quick(time.Minute, 200*time.Millisecond))
	buf := make([]float32, 2*1024)
	stalled, after := false, 0
	for deadline := time.Now().Add(5 * time.Second); after < 44100; {
		if time.Now().After(deadline) {
			t.Fatalf("stalled %v, then %d frames in 5 s", stalled, after)
		}
		n, err := l.Read(buf)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if l.Stalled() {
			stalled = true
		} else if stalled {
			after += n
		}
	}
	if n := s.requests(); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

// A station that sends a burst and closes, every time, brings audio on each
// connection, so it is never lost; but it is asked again less and less
// often, as one that refused would be, rather than every second for ever.
func TestLiveFlappingStationWaitsLonger(t *testing.T) {
	s := serve(t, reply{file: fixtureBytes(t, "stream-44100.mp3"), header: mpeg, cut: 16 << 10})
	var mu sync.Mutex
	var tries []int
	o := quick(time.Minute, 0)
	o.Retry = func(n int) time.Duration {
		mu.Lock()
		defer mu.Unlock()
		tries = append(tries, n)
		return 10 * time.Millisecond
	}
	tried := func() []int {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(tries)
	}
	l := open(t, s.url, o)
	buf := make([]float32, 2*1024)
	for deadline := time.Now().Add(5 * time.Second); len(tried()) < 5; {
		if time.Now().After(deadline) {
			t.Fatalf("%d tries in 5 s, want 5", len(tried()))
		}
		if _, err := l.Read(buf); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}
	got := tried()
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("waited Retry(n) for n %v, want n to grow", got)
		}
	}
}

// A station that dropped and doesn't come back is given up on in time, whether
// it refuses each try or never answers one: a try that would run past GiveUp
// is cut short.
func TestLiveGivesUp(t *testing.T) {
	file := fixtureBytes(t, "stream-44100.mp3")
	tests := []struct {
		name  string
		then  reply
		tries int // requests at least, the first included
	}{
		{"refused", reply{status: http.StatusServiceUnavailable}, 3},
		{"no answer", reply{silent: true}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serve(t, reply{file: file, header: mpeg, cut: 64 << 10}, tt.then)
			before, _ := goroutines()
			o := quick(300*time.Millisecond, 20*time.Millisecond)
			l := open(t, s.url, o)
			if err := failure(t, l, 2*time.Second); !errors.Is(err, ErrLost) {
				t.Errorf("Read: %v, want ErrLost", err)
			}
			if n := s.requests(); n < tt.tries {
				t.Errorf("%d requests, want %d or more", n, tt.tries)
			}
			l.Close()
			o.HTTP.CloseIdleConnections() // kept from a 503, as any client keeps them
			settles(t, before)
		})
	}
}

// A station that keeps its connection but sends nothing more has dropped once
// it has been quiet for 15 s, or half GiveUp when that is shorter, as here: it
// is stalled, tried again, and lost if it doesn't come back. Without that,
// Read would return silence for ever. What the decoder still held when the
// station went quiet is stale by then, and isn't played.
func TestLiveSilenceIsADrop(t *testing.T) {
	tests := []struct {
		name, file string
		header     map[string]string
	}{
		{"MP3", "stream-44100.mp3", mpeg},
		{"AAC", "stream-44100.aac", aacp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serve(t, reply{file: fixtureBytes(t, tt.file), header: tt.header, cut: 64 << 10, hang: true},
				reply{status: http.StatusServiceUnavailable})
			l := open(t, s.url, quick(time.Second, 50*time.Millisecond))
			buf := make([]float32, 2*1024)
			stalled, quiet, stale := false, false, 0
			heard := time.Now()
			var err error
			for deadline := time.Now().Add(3 * time.Second); err == nil; {
				if time.Now().After(deadline) {
					t.Fatal("Read didn't fail within 3 s")
				}
				var n int
				n, err = l.Read(buf)
				switch {
				case n > 0 && quiet:
					stale += n // audio after the silence, when the station sent none
				case n > 0:
					heard = time.Now()
				case time.Since(heard) > 250*time.Millisecond:
					quiet = true
				}
				stalled = stalled || l.Stalled()
			}
			if !errors.Is(err, ErrLost) || !stalled {
				t.Errorf("Read: %v, stalled %v; want ErrLost after a stall", err, stalled)
			}
			if stale > 0 {
				t.Errorf("%d frames played after the station went quiet", stale)
			}
			if n := s.requests(); n < 2 {
				t.Errorf("%d requests, want it to have tried again", n)
			}
			s.waitEnded(t)
		})
	}
}

func TestLiveRateChanged(t *testing.T) {
	s := serve(t, reply{file: fixtureBytes(t, "stream-44100.aac"), header: aacp, cut: 64 << 10},
		reply{file: fixtureBytes(t, "stream-48000.aac"), header: aacp})
	l := open(t, s.url, quick(time.Minute, 20*time.Millisecond))
	if l.SampleRate() != 44100 {
		t.Fatalf("rate %d, want 44100", l.SampleRate())
	}
	if err := failure(t, l, 2*time.Second); !errors.Is(err, audio.ErrRateChanged) {
		t.Errorf("Read: %v, want audio.ErrRateChanged", err)
	}
}

// Close stops the download at once, whether the goroutine waits for room in
// the ring or for the station, here one that sent 20 KB and went quiet.
func TestLiveClose(t *testing.T) {
	file := fixtureBytes(t, "stream-44100.mp3")
	tests := []struct {
		name string
		r    reply
	}{
		{"streaming", reply{file: file, header: mpeg}},
		{"waiting for the station", reply{file: file, header: mpeg, cut: 20 << 10, hang: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := serve(t, tt.r)
			before, _ := goroutines()
			l := open(t, s.url, quick(time.Minute, time.Second))
			play(t, l, 100)
			closed := make(chan error, 1)
			go func() { closed <- l.Close() }()
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close didn't return")
			}
			if n, err := l.Read(make([]float32, 2048)); n != 0 || err != io.EOF {
				t.Errorf("Read after Close = %d, %v; want 0, io.EOF", n, err)
			}
			s.waitEnded(t)
			settles(t, before)
		})
	}
}

func TestKbpsOf(t *testing.T) {
	for in, want := range map[string]int{"128": 128, "128,128": 128, " 64 ": 64, "": 0, "x": 0} {
		if got := kbpsOf(in); got != want {
			t.Errorf("kbpsOf(%q) = %d, want %d", in, got, want)
		}
	}
}

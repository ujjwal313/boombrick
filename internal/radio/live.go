package radio

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
	"github.com/ujjwal313/boombrick/internal/netx"
)

var (
	// ErrUnsupported is a stream that isn't MP3 or AAC, as a playlist.
	ErrUnsupported = errors.New("radio: the stream isn't MP3 or AAC")
	// ErrLost ends a stream that dropped and didn't come back.
	ErrLost = errors.New("radio: the station is gone")

	errSlow  = errors.New("radio: the station took too long to start")
	errQuiet = errors.New("radio: the station sends no audio")
)

const (
	openTimeout = 15 * time.Second
	ringSeconds = 2
	readWait    = 50 * time.Millisecond // as long as Read waits for audio before returning none
	silentBytes = 256 << 10             // a stream this far in with no audio isn't one we can play
	sniffBytes  = 8 << 10

	giveUp    = 2 * time.Minute
	firstWait = time.Second
	lastWait  = 30 * time.Second
	steady    = 30 * time.Second // audio this long on one connection starts the waits over
)

// Options are how a station's stream is fetched and given up on.
type Options struct {
	HTTP   *http.Client // StreamClient's, when nil
	Online func() bool  // whether to try connecting again; always, when nil
	Name   string       // the station's: a title that only repeats it is none
	Log    *slog.Logger
	// GiveUp is how long a stream may go without audio, 2 minutes when 0.
	GiveUp time.Duration
	// Retry is the wait before the nth try to connect again, counting the
	// tries since audio last flowed for 30 s on one connection: 1 s,
	// doubling to 30 s, when nil.
	Retry func(n int) time.Duration
}

func (o Options) withDefaults() Options {
	if o.HTTP == nil {
		o.HTTP = StreamClient()
	}
	if o.Online == nil {
		o.Online = func() bool { return true }
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.GiveUp <= 0 {
		o.GiveUp = giveUp
	}
	if o.Retry == nil {
		o.Retry = backoff
	}
	return o
}

func backoff(n int) time.Duration {
	if n >= 5 {
		return lastWait
	}
	return min(firstWait<<n, lastWait)
}

// Live is a radio station's stream as the player's audio Source. A goroutine
// of its own fetches it, takes the ICY titles out and decodes it into a ring
// of 2 s, connecting again when it drops; Read plays silence meanwhile. It has
// no length and doesn't seek.
type Live struct {
	o           Options
	url         string
	rate        int
	cancel      context.CancelFunc // stops the stream
	done        chan struct{}      // closed when the goroutine returns
	more, space chan struct{}      // signalled when audio arrives or is read
	dst         []float32          // a frame, decoded: the goroutine's
	heard       time.Time          // when audio last came: the goroutine's

	mu      sync.Mutex
	ring    []float32
	r, n    int   // where the next sample is read, and how many are held
	err     error // the stream failed for good
	stalled bool  // the stream dropped, and is being connected again
	closed  bool
	title   string
	kbps    int
	codec   codec
}

// conn is one connection to the stream.
type conn struct {
	ctx   context.Context // the request's
	end   context.CancelCauseFunc
	body  io.Closer
	dec   decoder
	codec codec
	kbps  int
}

// close ends the request and frees the decoder. It does nothing on a zero
// conn.
func (c conn) close() {
	if c.end != nil {
		c.end(nil)
	}
	if c.body != nil {
		c.body.Close()
	}
	if c.dec != nil {
		c.dec.close()
	}
}

// why is the reason the connection failed with err: what ended it, when that
// was the open taking too long or a silence.
func (c conn) why(err error) error {
	if cause := context.Cause(c.ctx); errors.Is(cause, errSlow) || errors.Is(cause, errQuiet) {
		return cause
	}
	return err
}

// Open connects to the stream at url and decodes until its first audio, so
// its rate is known. ctx bounds only the opening: the stream then runs on its
// own context until Close, since the player cancels the open's context once
// it has the source.
func Open(ctx context.Context, url string, o Options) (*Live, error) {
	o = o.withDefaults()
	run, cancel := context.WithCancel(context.Background())
	l := &Live{o: o, url: url, cancel: cancel, done: make(chan struct{}),
		more: make(chan struct{}, 1), space: make(chan struct{}, 1), dst: make([]float32, 2*maxFrame)}
	stop := context.AfterFunc(ctx, cancel)
	c, first, rate, err := l.connect(run, openTimeout)
	if !stop() {
		err = ctx.Err() // given up while it connected, which cancelled run
	}
	if err != nil {
		cancel()
		c.close()
		return nil, err
	}
	l.rate, l.kbps, l.codec, l.heard = rate, c.kbps, c.codec, time.Now()
	l.ring = make([]float32, 2*ringSeconds*rate)
	l.putLocked(first)
	go l.run(run, c)
	return l, nil
}

// connect asks for the stream and decodes it to its first audio, within
// limit. It returns the connection, the first frames, in l.dst, and their
// rate; on an error it leaves nothing open.
func (l *Live) connect(ctx context.Context, limit time.Duration) (c conn, first []float32, rate int, err error) {
	cctx, end := context.WithCancelCause(ctx)
	c.ctx, c.end = cctx, end
	slow := time.AfterFunc(limit, func() { end(errSlow) }) // not c's: c is cleared below
	defer func() {
		if !slow.Stop() && err == nil {
			err = errSlow
		}
		if err != nil {
			err = c.why(err)
			c.close()
			c, first, rate = conn{}, nil, 0
		}
	}()
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, l.url, nil)
	if err != nil {
		return c, nil, 0, err
	}
	req.Header.Set("User-Agent", netx.UserAgent())
	req.Header.Set("Icy-MetaData", "1")
	resp, err := l.o.HTTP.Do(req)
	if err != nil {
		return c, nil, 0, err
	}
	c.body = resp.Body
	if resp.StatusCode != http.StatusOK {
		return c, nil, 0, &netx.StatusError{Code: resp.StatusCode}
	}
	c.kbps = kbpsOf(resp.Header.Get("icy-br"))
	var r io.Reader = resp.Body
	// A metadata block cut short fails the read with io.ErrUnexpectedEOF, a
	// drop like any other.
	if n, _ := strconv.Atoi(resp.Header.Get("icy-metaint")); n > 0 {
		r = newICYReader(r, n, l.setTitle)
	}
	lim := &limited{r: r, left: silentBytes}
	r = lim
	c.codec, r, err = pick(resp.Header.Get("Content-Type"), r)
	if err != nil {
		return c, nil, 0, err
	}
	switch c.codec {
	case mp3:
		c.dec = newMP3Decoder(r)
	case aac:
		c.dec = newAACDecoder(r, l.o.Log)
	}
	for {
		n, rate, err := c.dec.next(l.dst)
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // the stream ended before its first audio
		}
		if err != nil {
			return c, nil, 0, err
		}
		if n > 0 {
			lim.left = -1
			return c, l.dst[:2*n], rate, nil
		}
	}
}

// pick finds the stream's codec: the one its Content-Type names, or else the
// one its first frames show. A playlist, or anything else that isn't audio, is
// ErrUnsupported. It returns the reader to decode from, which holds the bytes
// looked at.
func pick(contentType string, r io.Reader) (codec, io.Reader, error) {
	c, playlist := codecOf(contentType)
	switch {
	case playlist:
		return unknown, nil, ErrUnsupported
	case c != unknown:
		return c, r, nil
	}
	br := bufio.NewReaderSize(r, sniffBytes)
	b, err := br.Peek(sniffBytes)
	if c = sniff(b); c != unknown {
		return c, br, nil
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return unknown, nil, err // it dropped while we looked
	}
	return unknown, nil, ErrUnsupported
}

// limited fails with ErrUnsupported once it has read left bytes, unless left
// is below 0: a stream that long with no audio in it isn't one we can play.
type limited struct {
	r    io.Reader
	left int
}

func (l *limited) Read(b []byte) (int, error) {
	if l.left < 0 {
		return l.r.Read(b)
	}
	if l.left == 0 {
		return 0, ErrUnsupported
	}
	n, err := l.r.Read(b[:min(len(b), l.left)])
	l.left -= n
	return n, err
}

// kbpsOf reads icy-br, which some servers give as "128,128".
func kbpsOf(h string) int {
	s, _, _ := strings.Cut(h, ",")
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// run decodes the stream into the ring until it is closed or fails for good,
// connecting again each time it drops. The waits between tries start over
// only once audio has flowed for steady on a connection, so a station that
// sends a burst and closes, over and over, is asked less and less often.
func (l *Live) run(ctx context.Context, c conn) {
	defer close(l.done)
	tries := 0 // to connect again, since audio last flowed for steady
	for {
		from := l.heard // when c brought its first audio
		err := l.pump(ctx, c)
		c.close()
		if ctx.Err() != nil {
			return // closed
		}
		if errors.Is(err, audio.ErrRateChanged) {
			l.fail(err)
			return
		}
		l.mu.Lock()
		l.stalled = true
		l.mu.Unlock()
		l.o.Log.Info("station dropped", "station", l.o.Name, "err", err)
		if l.heard.Sub(from) >= steady {
			tries = 0
		}
		if c, tries, err = l.reconnect(ctx, l.heard, tries); err != nil {
			if ctx.Err() == nil {
				l.fail(err)
			}
			return
		}
		l.o.Log.Info("station back", "station", l.o.Name)
	}
}

// pump decodes c into the ring until it fails, or brings no audio for as long
// as an open may take (or half GiveUp, when shorter, so a station gone quiet is
// still tried again before it is given up on). That second limit is for a
// server that stops sending without closing the connection: the read would
// wait for ever, and the source would play silence, never stalled and never
// lost, holding the Wi-Fi. It drops the connection instead, so the station is
// connected again and given up on as after any drop. The wait for room in the
// ring doesn't count.
func (l *Live) pump(ctx context.Context, c conn) error {
	wait := min(openTimeout, l.o.GiveUp/2)
	quiet := time.AfterFunc(wait, func() { c.end(errQuiet) })
	defer quiet.Stop()
	for {
		n, rate, err := c.dec.next(l.dst)
		switch {
		case err != nil:
			return c.why(err)
		case errors.Is(context.Cause(c.ctx), errQuiet):
			// The decoder plays on from what it held after the request
			// ends, but that came before the silence: stale now, and it
			// would put off giving up.
			return errQuiet
		case n == 0:
			continue
		case rate != l.rate:
			return audio.ErrRateChanged
		}
		quiet.Stop()
		if !l.put(ctx, l.dst[:2*n]) {
			return context.Canceled // closed
		}
		l.heard = time.Now()
		quiet.Reset(wait)
	}
}

// reconnect connects again, waiting longer before each try, while the
// station has been away less than GiveUp. n is the tries made before, since
// audio last flowed steadily; it returns that count with the connection, so
// a drop soon after waits longer again. A stream back at another rate can't
// carry on in this source: the player opens the station again.
func (l *Live) reconnect(ctx context.Context, since time.Time, n int) (conn, int, error) {
	for ; ; n++ {
		left := l.o.GiveUp - time.Since(since)
		if left <= 0 {
			return conn{}, n, ErrLost
		}
		select {
		case <-ctx.Done():
			return conn{}, n, ctx.Err()
		case <-time.After(min(l.o.Retry(n), left)):
		}
		if !l.o.Online() {
			continue
		}
		if left = l.o.GiveUp - time.Since(since); left <= 0 {
			return conn{}, n, ErrLost
		}
		c, first, rate, err := l.connect(ctx, min(openTimeout, left)) // a try doesn't outlast GiveUp
		if err != nil {
			if ctx.Err() != nil {
				return conn{}, n, ctx.Err()
			}
			l.o.Log.Info("station not back yet", "station", l.o.Name, "err", err)
			continue
		}
		if rate != l.rate {
			c.close()
			return conn{}, n, audio.ErrRateChanged
		}
		l.mu.Lock()
		l.kbps, l.codec = c.kbps, c.codec
		l.mu.Unlock()
		if !l.put(ctx, first) {
			c.close()
			return conn{}, n, context.Canceled // closed
		}
		l.heard = time.Now()
		return c, n + 1, nil
	}
}

// put adds the frames in b to the ring, waiting for room; false once the
// stream is closed.
func (l *Live) put(ctx context.Context, b []float32) bool {
	for len(b) > 0 {
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			return false
		}
		l.stalled = false
		b = b[l.putLocked(b):]
		l.mu.Unlock()
		if len(b) > 0 {
			select {
			case <-l.space:
			case <-ctx.Done():
				return false
			}
		}
	}
	return true
}

// putLocked copies as much of b as there is room for into the ring, and
// returns how much.
func (l *Live) putLocked(b []float32) int {
	k := min(len(l.ring)-l.n, len(b))
	w := (l.r + l.n) % len(l.ring)
	c := copy(l.ring[w:], b[:k])
	copy(l.ring, b[c:k])
	l.n += k
	if k > 0 {
		signal(l.more)
	}
	return k
}

// fail ends the stream with err, which Read returns once what is held is
// read.
func (l *Live) fail(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.err, l.stalled = err, false
	signal(l.more)
}

func (l *Live) setTitle(t string) {
	if strings.EqualFold(t, strings.TrimSpace(l.o.Name)) {
		t = ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.title = t
}

// SampleRate is the stream's rate, as it was when opened.
func (l *Live) SampleRate() int { return l.rate }

// Frames is -1: a stream has no end.
func (l *Live) Frames() int64 { return -1 }

// SeekFrame does nothing: a station plays what is live. The engine's own
// seeks, as after an output error, play on.
func (l *Live) SeekFrame(int64) error { return nil }

// Read fills buf with up to len(buf)/2 frames. With none held it waits a
// moment, then returns none, so the engine isn't held up while the stream
// connects again.
func (l *Live) Read(buf []float32) (int, error) {
	l.mu.Lock()
	if l.n == 0 && l.err == nil && !l.closed {
		l.mu.Unlock()
		select {
		case <-l.more:
		case <-time.After(readWait):
		}
		l.mu.Lock()
	}
	defer l.mu.Unlock()
	switch {
	case l.closed:
		return 0, io.EOF
	case l.n == 0:
		return 0, l.err
	}
	k := min(len(buf)/2, l.n/2)
	c := copy(buf[:2*k], l.ring[l.r:])
	copy(buf[c:2*k], l.ring)
	l.r, l.n = (l.r+2*k)%len(l.ring), l.n-2*k
	signal(l.space)
	return k, nil
}

// Stalled reports whether the stream dropped and is being connected again.
func (l *Live) Stalled() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stalled
}

// Kbps is the bitrate the server gives, 0 when it gives none.
func (l *Live) Kbps() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.kbps
}

// Format is the stream's codec: "MP3" or "AAC".
func (l *Live) Format() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.codec.String()
}

// Title is what the station says is playing, or "" when it says nothing or
// only its own name.
func (l *Live) Title() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.title
}

// Close stops the stream and its download, and returns once they have
// stopped. Read then returns io.EOF.
func (l *Live) Close() error {
	l.cancel() // first, so the goroutine finds its context done once it sees closed
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	signal(l.more)
	signal(l.space)
	<-l.done
	return nil
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

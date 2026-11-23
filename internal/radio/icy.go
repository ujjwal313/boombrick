package radio

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/ujjwal313/boombrick/internal/netx"
)

// icyConn reads a SHOUTcast server's status line, "ICY 200 OK", as
// "HTTP/1.0 200 OK": Go's HTTP client rejects ICY's. Anything else passes
// unchanged. It sits under TLS, so it sees an https stream's bytes still
// encrypted: an ICY status line over https isn't rewritten, and the request
// fails.
type icyConn struct {
	net.Conn
	looked bool
	held   []byte
	err    error
}

func (c *icyConn) Read(b []byte) (int, error) {
	if !c.looked {
		c.looked = true
		head := make([]byte, 4)
		n, err := io.ReadFull(c.Conn, head)
		c.held, c.err = head[:n], err
		if n == 4 && string(head) == "ICY " {
			c.held = []byte("HTTP/1.0 ")
		}
	}
	if len(c.held) > 0 {
		n := copy(b, c.held)
		c.held = c.held[n:]
		return n, nil
	}
	if c.err != nil {
		return 0, c.err
	}
	return c.Conn.Read(b)
}

// StreamClient is the HTTP client for stations' streams. It has a transport,
// and so connections, of its own, set up as netx's and made on the first
// request, that reads ICY status lines. It has no overall timeout, as a
// stream never ends; requests are bounded by their context.
func StreamClient() *http.Client {
	return netx.NewClientWith(func(c net.Conn) net.Conn { return &icyConn{Conn: c} })
}

// icyReader passes a stream's audio on, taking out the metadata block a
// server puts after every interval bytes of it (icy-metaint), and tells title
// of each StreamTitle in them.
type icyReader struct {
	r        io.Reader
	interval int
	left     int // audio bytes before the next block
	title    func(string)
	meta     []byte
}

func newICYReader(r io.Reader, interval int, title func(string)) *icyReader {
	return &icyReader{r: r, interval: interval, left: interval, title: title}
}

func (m *icyReader) Read(b []byte) (int, error) {
	if m.left == 0 {
		if err := m.block(); err != nil {
			return 0, err
		}
		m.left = m.interval
	}
	n, err := m.r.Read(b[:min(len(b), m.left)])
	m.left -= n
	return n, err
}

// block reads one metadata block: a byte giving its length in 16s, then
// that many bytes.
func (m *icyReader) block() error {
	var size [1]byte
	if _, err := io.ReadFull(m.r, size[:]); err != nil {
		return err
	}
	n := int(size[0]) * 16
	if n == 0 {
		return nil
	}
	if cap(m.meta) < n {
		m.meta = make([]byte, n)
	}
	if _, err := io.ReadFull(m.r, m.meta[:n]); err != nil {
		return err
	}
	if t, ok := streamTitle(m.meta[:n]); ok {
		m.title(t)
	}
	return nil
}

// streamTitle reads StreamTitle='…'; from a metadata block, as UTF-8, or as
// Latin-1 when it isn't valid UTF-8; ok is false when the block has none.
func streamTitle(b []byte) (string, bool) {
	b = bytes.TrimRight(b, "\x00")
	i := bytes.Index(b, []byte("StreamTitle='"))
	if i < 0 {
		return "", false
	}
	v := b[i+len("StreamTitle='"):]
	if end := bytes.Index(v, []byte("';")); end >= 0 {
		v = v[:end]
	} else {
		v = bytes.TrimSuffix(v, []byte("'"))
	}
	if !utf8.Valid(v) {
		v, _ = charmap.ISO8859_1.NewDecoder().Bytes(v)
	}
	t := strings.TrimSpace(string(v))
	// Many servers send " - " between songs, an artist and a song with nothing
	// in them. A title with no letter or digit isn't one.
	if !strings.ContainsFunc(t, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		t = ""
	}
	return t, true
}

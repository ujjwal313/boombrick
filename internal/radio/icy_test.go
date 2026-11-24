package radio

import (
	"bytes"
	"io"
	"net"
	"testing"
	"testing/iotest"
)

// rawServer answers each connection with reply, after reading the request.
func rawServer(t *testing.T, reply string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 4096)
				c.Read(buf)
				io.WriteString(c, reply)
			}()
		}
	}()
	return "http://" + l.Addr().String() + "/"
}

func TestStreamClientReadsICYStatusLine(t *testing.T) {
	audio := bytes.Repeat([]byte{7}, 100)
	url := rawServer(t, "ICY 200 OK\r\nContent-Type: audio/mpeg\r\nicy-metaint: 0\r\n\r\n"+string(audio))
	resp, err := StreamClient().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "audio/mpeg" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := resp.Header.Get("icy-metaint"); got != "0" {
		t.Errorf("icy-metaint = %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, audio) {
		t.Errorf("body = %d bytes, want the 100 sent", len(body))
	}
}

func TestStreamClientLeavesHTTPAlone(t *testing.T) {
	url := rawServer(t, "HTTP/1.1 200 OK\r\nContent-Length: 5\r\nContent-Type: audio/aac\r\n\r\nhello")
	resp, err := StreamClient().Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "hello" || resp.Header.Get("Content-Type") != "audio/aac" {
		t.Errorf("got %d %q %q", resp.StatusCode, body, resp.Header.Get("Content-Type"))
	}
}

func icyStream(interval int) (stream, audio []byte) {
	blocks := [][]byte{
		pad([]byte("StreamTitle='One';")),
		{0},
		pad([]byte("StreamTitle='Two';StreamUrl='';")),
	}
	var s, a bytes.Buffer
	n := 0
	for _, blk := range blocks {
		for i := 0; i < interval; i++ {
			s.WriteByte(byte(n % 256))
			a.WriteByte(byte(n % 256))
			n++
		}
		s.Write(blk)
	}
	for i := 0; i < 5; i++ { // a little audio after the last block
		s.WriteByte(byte(n % 256))
		a.WriteByte(byte(n % 256))
		n++
	}
	return s.Bytes(), a.Bytes()
}

// pad is a metadata block: its length in 16s, then the text padded with NULs.
func pad(text []byte) []byte {
	n := (len(text) + 15) / 16
	b := make([]byte, 1+n*16)
	b[0] = byte(n)
	copy(b[1:], text)
	return b
}

func TestICYReader(t *testing.T) {
	stream, audio := icyStream(16)
	for name, wrap := range map[string]func(io.Reader) io.Reader{
		"one byte": iotest.OneByteReader,
		"4 KB":     func(r io.Reader) io.Reader { return r },
	} {
		t.Run(name, func(t *testing.T) {
			var titles []string
			r := newICYReader(wrap(bytes.NewReader(stream)), 16, func(s string) { titles = append(titles, s) })
			var got []byte
			buf := make([]byte, 4096)
			for {
				n, err := r.Read(buf)
				got = append(got, buf[:n]...)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Equal(got, audio) {
				t.Errorf("audio = %d bytes, want %d, or differs", len(got), len(audio))
			}
			if len(titles) != 2 || titles[0] != "One" || titles[1] != "Two" {
				t.Errorf("titles = %q", titles)
			}
		})
	}
}

func TestStreamTitle(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{"latin-1", "StreamTitle='Caf\xe9';", "Café", true},
		{"utf-8", "StreamTitle='Café';", "Café", true},
		{"spaces", "StreamTitle='  A - B  ';\x00\x00", "A - B", true},
		{"empty", "StreamTitle='';", "", true},
		{"none", "StreamUrl='x';\x00\x00", "", false},
		{"placeholder dash", "StreamTitle=' - ';", "", true},
		{"lone hyphen", "StreamTitle='-';", "", true},
		{"two hyphens", "StreamTitle=' -- ';", "", true},
		{"dots", "StreamTitle='...';", "", true},
		{"artist and song", "StreamTitle='A - B';", "A - B", true},
		{"a digit", "StreamTitle='1';", "1", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := streamTitle([]byte(tt.in))
			if got != tt.want || ok != tt.ok {
				t.Errorf("streamTitle(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}

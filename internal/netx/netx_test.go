package netx

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetSendsTheUserAgentAndReadsTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); !strings.HasPrefix(ua, "Boombrick/") || !strings.Contains(ua, "github.com/ujjwal313/boombrick") {
			t.Errorf("user agent %q", ua)
		}
		w.Write([]byte("hello"))
	}))
	defer srv.Close()
	body, err := Get(context.Background(), NewClient(), srv.URL, 100)
	if err != nil || string(body) != "hello" {
		t.Fatalf("Get = %q, %v", body, err)
	}
}

func TestGetFollowsRedirects(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/front", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/image", http.StatusTemporaryRedirect)
	})
	mux.HandleFunc("/image", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "Boombrick/") {
			t.Error("the user agent was lost on the redirect")
		}
		w.Write([]byte("jpeg"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if body, err := Get(context.Background(), NewClient(), srv.URL+"/front", 100); err != nil || string(body) != "jpeg" {
		t.Fatalf("Get = %q, %v", body, err)
	}
}

func TestGetReportsTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/busy" {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	_, err := Get(context.Background(), NewClient(), srv.URL+"/missing", 100)
	if !IsNotFound(err) {
		t.Fatalf("404 gave %v", err)
	}
	_, err = Get(context.Background(), NewClient(), srv.URL+"/busy", 100)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 503 || se.RetryAfter != 7*time.Second {
		t.Fatalf("503 gave %v", err)
	}
	if IsNotFound(err) {
		t.Fatal("503 counted as not found")
	}
}

func TestGetRefusesLargeBodies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 101))
	}))
	defer srv.Close()
	if _, err := Get(context.Background(), NewClient(), srv.URL, 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a 101-byte body with a 100-byte limit gave %v", err)
	}
}

func TestGetStopsAtTheDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Get(ctx, NewClient(), srv.URL, 100); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get past its deadline = %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("Get took %v to give up", time.Since(start))
	}
}

func TestTLSChecksTheRoots(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("secure")) }))
	defer srv.Close()
	ctx := context.Background()
	var unknown x509.UnknownAuthorityError
	if _, err := Get(ctx, NewClient(), srv.URL, 100); !errors.As(err, &unknown) {
		t.Fatalf("a server no root vouches for: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	defer func(r func() *x509.CertPool) { roots = r }(roots)
	roots = func() *x509.CertPool { return pool }
	if body, err := Get(ctx, NewClient(), srv.URL, 100); err != nil || string(body) != "secure" {
		t.Fatalf("with its certificate among the roots: %q, %v", body, err)
	}
}

func TestRootsFrom(t *testing.T) {
	sys := bundled()
	if sys.Equal(x509.NewCertPool()) {
		t.Fatal("no certificates are compiled in")
	}
	if got := rootsFrom(sys, nil, func() *x509.CertPool { t.Fatal("the system's were passed over"); return nil }); got != sys {
		t.Fatal("the system's certificates weren't used")
	}
	for _, c := range []struct {
		name string
		sys  *x509.CertPool
		err  error
	}{
		{"none", x509.NewCertPool(), nil},
		{"unreadable", nil, errors.New("permission denied")},
	} {
		if got := rootsFrom(c.sys, c.err, bundled); got == nil || !got.Equal(sys) {
			t.Errorf("%s: the compiled-in certificates weren't used", c.name)
		}
	}
}

// The Brick has no CA certificates of its own: without those compiled in,
// every lookup and download fails. The test runs itself again with the
// system's hidden, as on the Brick.
func TestRootsWithoutTheSystems(t *testing.T) {
	if os.Getenv("BOOMBRICK_ROOTS_CHILD") == "1" {
		if !roots().Equal(bundled()) {
			t.Fatal("the compiled-in certificates weren't used")
		}
		return
	}
	none := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRootsWithoutTheSystems$")
	cmd.Env = append(os.Environ(), "BOOMBRICK_ROOTS_CHILD=1",
		"SSL_CERT_FILE="+filepath.Join(none, "none.pem"), "SSL_CERT_DIR="+none)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Do sends the request as the caller made it, with Boombrick's user agent.
func TestDo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
			r.Header.Get("User-Agent") != UserAgent() {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Write(append([]byte("got "), b...))
	}))
	defer srv.Close()
	post := func(contentType string, body []byte, limit int64) ([]byte, error) {
		req, err := http.NewRequest(http.MethodPost, srv.URL, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		return Do(srv.Client(), req, limit)
	}

	body, err := post("application/json", []byte(`{"a":1}`), 1<<10)
	if err != nil || string(body) != `got {"a":1}` {
		t.Fatalf("Do = %q, %v", body, err)
	}
	var se *StatusError
	if _, err := post("text/plain", nil, 1<<10); !errors.As(err, &se) || se.Code != 400 {
		t.Fatalf("wrong type: %v", err)
	}
	if _, err := post("application/json", bytes.Repeat([]byte("x"), 10), 8); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("too large: %v", err)
	}
}

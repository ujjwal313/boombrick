// Package netx is the HTTP client Boombrick's online features share: short
// timeouts, a user agent that names the app, and CA certificates compiled in
// for a device that has none of its own.
package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/x509roots/fallback/bundle"

	"github.com/ujjwal313/boombrick/internal/version"
)

// UserAgent identifies Boombrick and where to find its authors, as
// MusicBrainz and LRCLIB ask of the apps that use them.
func UserAgent() string {
	return "Boombrick/" + version.Version + " (https://github.com/ujjwal313/boombrick)"
}

// NewClient returns a client that gives up connecting after 5 s and waiting
// for a response's headers after 10 s. Callers bound whole requests with
// their context. Its transport is made on the first request, when the CA
// certificates are read.
func NewClient() *http.Client {
	return &http.Client{Transport: lazyTransport(sync.OnceValue(newTransport))}
}

// lazyTransport is a transport made on its first request.
type lazyTransport func() *http.Transport

func (t lazyTransport) RoundTrip(r *http.Request) (*http.Response, error) { return t().RoundTrip(r) }

func newTransport() *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 5 * time.Second
	tr.ResponseHeaderTimeout = 10 * time.Second
	tr.MaxIdleConnsPerHost = 2
	tr.IdleConnTimeout = 30 * time.Second
	tr.TLSClientConfig = &tls.Config{RootCAs: roots()}
	return tr
}

// roots is the CA certificates servers are checked against: the system's, or
// on a device with none, as the Brick is, those compiled in. They are read
// on the first request rather than at start, as parsing the compiled-in ones
// takes a while and many runs make no request.
var roots = sync.OnceValue(func() *x509.CertPool {
	sys, err := x509.SystemCertPool()
	return rootsFrom(sys, err, bundled)
})

// rootsFrom is sys, the system's certificates, unless reading them failed
// or found none; then it is bundled's.
func rootsFrom(sys *x509.CertPool, err error, bundled func() *x509.CertPool) *x509.CertPool {
	if err == nil && !sys.Equal(x509.NewCertPool()) {
		return sys
	}
	return bundled()
}

// bundled is the CA certificates compiled in, Mozilla's, with the limits some
// of them carry.
func bundled() *x509.CertPool {
	pool := x509.NewCertPool()
	for r := range bundle.Roots() {
		c, err := x509.ParseCertificate(r.Certificate)
		if err != nil {
			continue
		}
		if r.Constraint != nil {
			pool.AddCertWithConstraint(c, r.Constraint)
		} else {
			pool.AddCert(c)
		}
	}
	return pool
}

// StatusError is a response other than 200 OK.
type StatusError struct {
	Code       int
	RetryAfter time.Duration // from Retry-After, when it gives seconds
}

func (e *StatusError) Error() string { return fmt.Sprintf("http status %d", e.Code) }

// IsNotFound reports whether err is a 404 response.
func IsNotFound(err error) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Code == http.StatusNotFound
}

// ErrTooLarge is returned for a body longer than the caller allows.
var ErrTooLarge = errors.New("netx: response too large")

// Get fetches url and returns its body, which may be at most limit bytes.
// Any status but 200 is a *StatusError.
func Get(ctx context.Context, c *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return Do(c, req, limit)
}

// Do sends req, for a request Get can't make, and returns the response's body
// as Get does.
func Do(c *http.Client, req *http.Request, limit int64) ([]byte, error) {
	resp, err := send(c, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, ErrTooLarge
	}
	return body, nil
}

// send sends req with Boombrick's user agent. Any status but 200 is a
// *StatusError, and that response is read and closed here.
func send(c *http.Client, req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", UserAgent())
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		e := &StatusError{Code: resp.StatusCode}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.RetryAfter = time.Duration(s) * time.Second
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10)) // so the connection can be reused
		return nil, e
	}
	return resp, nil
}

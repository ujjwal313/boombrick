// Package audible keeps an audiobook's place in step with Audible: it reads
// where the listener last stopped, on the phone or the web player, and writes
// back where they stop here. It speaks to Audible's API as the Audible app
// does, signing each request with the key a device registration gave (see
// NewLogin and Load).
//
// The protocol, which Audible does not document, is the one the Python
// library audible by mkb79 (https://github.com/mkb79/Audible, AGPL-3.0)
// worked out and describes: the sign-in, the registration, the signature of
// a request and the layout of auth.json. This package is written afresh in
// Go and takes no code from it.
package audible

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// domains maps a marketplace to the domain of its API.
var domains = map[string]string{
	"us": "com", "uk": "co.uk", "de": "de", "fr": "fr", "ca": "ca", "au": "com.au",
	"it": "it", "es": "es", "jp": "co.jp", "in": "in",
}

// ErrNoAuth means no usable registration was found: Audible is not set up.
var ErrNoAuth = errors.New("audible: not signed in")

// ErrOldDevice means the registration is of the older iPhone profile, which
// Audible refuses licences to (see appVersion in login.go). It is an
// ErrNoAuth too: the listener has to sign in again, which makes a new one.
var ErrOldDevice = fmt.Errorf("%w: the device was registered with the older iPhone profile, which Audible refuses; sign in again", ErrNoAuth)

// oldSerial matches the serial of that profile: 32 uppercase hex digits.
var oldSerial = regexp.MustCompile(`^[0-9A-F]{32}$`)

// userAgent is what the Audible app calls itself; the API serves it.
const userAgent = "Audible/671 CFNetwork/1240.0.4 Darwin/20.6.0"

// Device is what a registration says of the device and its owner. Audible
// seals a book's key to the three together, so downloading needs them; keeping
// a place does not.
type Device struct {
	Serial, Type, CustomerID string
}

// Client talks to Audible as a registered device.
type Client struct {
	http *http.Client
	host string
	adp  string
	key  *rsa.PrivateKey
	dev  Device
	now  func() time.Time
}

// auth is the part of audible-tui's auth.json the client needs.
type auth struct {
	ADPToken string `json:"adp_token"`
	Key      string `json:"device_private_key"`
	Locale   string `json:"locale_code"`
	Device   struct {
		Serial string `json:"device_serial_number"`
		Type   string `json:"device_type"`
	} `json:"device_info"`
	Customer struct {
		UserID string `json:"user_id"`
	} `json:"customer_info"`
}

// Load reads the registration at path: a JSON file with the device's
// adp_token, device_private_key and locale_code, as the Python audible
// library writes it. A missing file gives ErrNoAuth. hc makes the requests.
func Load(path string, hc *http.Client) (*Client, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoAuth
	}
	if err != nil {
		return nil, err
	}
	var a auth
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("audible: %s: %w", path, err)
	}
	if oldSerial.MatchString(a.Device.Serial) {
		return nil, ErrOldDevice
	}
	dev := Device{Serial: a.Device.Serial, Type: a.Device.Type, CustomerID: a.Customer.UserID}
	return New(a.ADPToken, a.Key, a.Locale, dev, hc)
}

// New makes a client from a registration's adp token, its RSA key in PEM, the
// marketplace's code, such as "it", and the device it was made for.
func New(adpToken, keyPEM, locale string, dev Device, hc *http.Client) (*Client, error) {
	d, ok := domains[strings.ToLower(locale)]
	if !ok || adpToken == "" || keyPEM == "" {
		return nil, ErrNoAuth
	}
	key, err := parseKey(keyPEM)
	if err != nil {
		return nil, err
	}
	return &Client{http: hc, host: "api.audible." + d, adp: adpToken, key: key, dev: dev, now: time.Now}, nil
}

func parseKey(s string) (*rsa.PrivateKey, error) {
	blk, _ := pem.Decode([]byte(s))
	if blk == nil {
		return nil, errors.New("audible: the device key is not PEM")
	}
	if k, err := x509.ParsePKCS1PrivateKey(blk.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, fmt.Errorf("audible: the device key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("audible: the device key is not RSA")
	}
	return rk, nil
}

// sign returns the headers that sign a request. Audible checks the signature
// over the method, the path with its query, the date, the body and the token,
// each on a line of its own. The date has the shape the Python library's has,
// "2026-10-22T10:24:00.123456+00:00Z", which is what the API is used to.
func (c *Client) sign(method, pathQuery string, body []byte) (http.Header, error) {
	date := c.now().UTC().Format("2006-01-02T15:04:05.000000") + "+00:00Z"
	data := method + "\n" + pathQuery + "\n" + date + "\n" + string(body) + "\n" + c.adp
	sum := sha256.Sum256([]byte(data))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, sum[:])
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("x-adp-token", c.adp)
	h.Set("x-adp-alg", "SHA256withRSA:1.0")
	h.Set("x-adp-signature", base64.StdEncoding.EncodeToString(sig)+":"+date)
	return h, nil
}

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) ([]byte, error) {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	pq := path
	if len(q) > 0 {
		pq += "?" + q.Encode()
	}
	h, err := c.sign(method, pq, b)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://"+c.host+pq, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header = h
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("audible: %s %s: %s", method, path, resp.Status)
	}
	return out, nil
}

// Position is where the listener last stopped in a book.
type Position struct {
	At      time.Duration // from the start of the book
	Updated time.Time     // when Audible was told
}

// RemotePosition asks Audible where the listener last stopped in the book
// with that ASIN. ok is false when Audible has no place for it.
func (c *Client) RemotePosition(ctx context.Context, asin string) (p Position, ok bool, err error) {
	out, err := c.do(ctx, "GET", "/1.0/annotations/lastpositions", url.Values{"asins": {asin}}, nil)
	if err != nil {
		return Position{}, false, err
	}
	return parsePosition(out, asin)
}

func parsePosition(b []byte, asin string) (Position, bool, error) {
	var r struct {
		Annots []struct {
			ASIN string `json:"asin"`
			Last struct {
				Updated string `json:"last_updated"`
				MS      int64  `json:"position_ms"`
				Status  string `json:"status"`
			} `json:"last_position_heard"`
		} `json:"asin_last_position_heard_annots"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return Position{}, false, err
	}
	for _, a := range r.Annots {
		if a.ASIN != asin || a.Last.Status != "Exists" {
			continue
		}
		// "2026-10-19 09:27:16.727", in UTC.
		t, _ := time.ParseInLocation("2006-01-02 15:04:05.000", a.Last.Updated, time.UTC)
		return Position{At: time.Duration(a.Last.MS) * time.Millisecond, Updated: t}, true, nil
	}
	return Position{}, false, nil
}

// SetPosition tells Audible the listener has stopped at pos in the book. acr
// is the book's content reference, from its licence (see ACR).
func (c *Client) SetPosition(ctx context.Context, asin, acr string, pos time.Duration) error {
	_, err := c.do(ctx, "PUT", "/1.0/lastpositions/"+asin, nil,
		map[string]any{"acr": acr, "asin": asin, "position_ms": pos.Milliseconds()})
	return err
}

// ACR asks Audible for the book's content reference, which a position needs.
// It is the same for every listener of the book, so one is kept with the book
// on the card; this is for a book that has none.
func (c *Client) ACR(ctx context.Context, asin string) (string, error) {
	out, err := c.do(ctx, "POST", "/1.0/content/"+asin+"/licenserequest", nil, map[string]any{
		"supported_drm_types": []string{"Mpeg", "Adrm"}, "quality": "High", "consumption_type": "Download",
		"response_groups": "content_reference",
	})
	if err != nil {
		return "", err
	}
	var r struct {
		License struct {
			ACR  string `json:"acr"` // also in a refusal
			Meta struct {
				Ref struct {
					ACR string `json:"acr"`
				} `json:"content_reference"`
			} `json:"content_metadata"`
		} `json:"content_license"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		return "", err
	}
	acr := r.License.Meta.Ref.ACR
	if acr == "" {
		acr = r.License.ACR
	}
	if acr == "" {
		return "", errors.New("audible: the licence has no content reference")
	}
	return acr, nil
}

var asinInName = regexp.MustCompile(`\[([A-Z0-9]{10})\]\.m4b$`)

// ASINOf is the ASIN in the name of a book's file, as audible-tui names
// them, "Title [B0GV1924QD].m4b"; "" when there is none.
func ASINOf(path string) string {
	m := asinInName.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return m[1]
}

// SidecarACR reads the content reference kept next to the book's file, in
// "Title [ASIN].audible.json"; "" when there is none.
func SidecarACR(bookPath string) string {
	b, err := os.ReadFile(strings.TrimSuffix(bookPath, ".m4b") + ".audible.json")
	if err != nil {
		return ""
	}
	var s struct {
		ACR string `json:"acr"`
	}
	if json.Unmarshal(b, &s) != nil {
		return ""
	}
	return s.ACR
}

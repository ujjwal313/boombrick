package audible

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
}

// rewrite sends the client's requests to srv whatever the host.
type rewrite struct{ to string }

func (r rewrite) RoundTrip(q *http.Request) (*http.Response, error) {
	q = q.Clone(q.Context())
	q.URL.Scheme, q.URL.Host = "http", strings.TrimPrefix(r.to, "http://")
	return http.DefaultTransport.RoundTrip(q)
}

func TestSignatureCoversTheRequest(t *testing.T) {
	k, pemKey := testKey(t)
	c, err := New("ADP-TOKEN", pemKey, "it", Device{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return time.Date(2026, 10, 22, 10, 24, 0, 123456000, time.UTC) }
	h, err := c.sign("PUT", "/1.0/lastpositions/B0GV1924QD", []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	sig, date, _ := strings.Cut(h.Get("x-adp-signature"), ":")
	if date != "2026-10-22T10:24:00.123456+00:00Z" {
		t.Fatalf("date %q", date)
	}
	raw, _ := base64.StdEncoding.DecodeString(sig)
	sum := sha256.Sum256([]byte("PUT\n/1.0/lastpositions/B0GV1924QD\n" + date + "\n{\"a\":1}\nADP-TOKEN"))
	if err := rsa.VerifyPKCS1v15(&k.PublicKey, crypto.SHA256, sum[:], raw); err != nil {
		t.Fatalf("the signature doesn't verify: %v", err)
	}
	if h.Get("x-adp-token") != "ADP-TOKEN" || h.Get("x-adp-alg") != "SHA256withRSA:1.0" {
		t.Fatalf("headers %v", h)
	}
}

func TestPositionRoundTrip(t *testing.T) {
	_, pemKey := testKey(t)
	var put map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-adp-signature") == "" {
			http.Error(w, "unsigned", 401)
			return
		}
		switch r.Method {
		case "GET":
			if r.URL.Path != "/1.0/annotations/lastpositions" || r.URL.Query().Get("asins") != "B0GV1924QD" {
				http.Error(w, "bad", 400)
				return
			}
			io.WriteString(w, `{"asin_last_position_heard_annots":[{"asin":"B0GV1924QD","last_position_heard":{"last_updated":"2026-10-19 09:27:16.727","position_ms":93500,"status":"Exists"}}]}`)
		case "PUT":
			b, _ := io.ReadAll(r.Body)
			json.Unmarshal(b, &put)
			io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()
	c, err := New("T", pemKey, "it", Device{}, &http.Client{Transport: rewrite{srv.URL}})
	if err != nil {
		t.Fatal(err)
	}
	p, ok, err := c.RemotePosition(context.Background(), "B0GV1924QD")
	if err != nil || !ok || p.At != 93500*time.Millisecond || p.Updated.Year() != 2026 || p.Updated.Day() != 19 {
		t.Fatalf("got %+v, %v, %v", p, ok, err)
	}
	if err := c.SetPosition(context.Background(), "B0GV1924QD", "CR!X", 2*time.Minute+500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if put["acr"] != "CR!X" || put["asin"] != "B0GV1924QD" || put["position_ms"] != float64(120500) {
		t.Fatalf("put %v", put)
	}
}

func TestNoPositionWhenAudibleHasNone(t *testing.T) {
	_, ok, err := parsePosition([]byte(`{"asin_last_position_heard_annots":[{"asin":"X","last_position_heard":{"status":"DoesNotExist"}}]}`), "X")
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestLoad(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "none.json"), nil); err != ErrNoAuth {
		t.Fatalf("a missing file gave %v", err)
	}
	_, pemKey := testKey(t)
	path := filepath.Join(t.TempDir(), "auth.json")
	b, _ := json.Marshal(map[string]string{"adp_token": "T", "device_private_key": pemKey, "locale_code": "it"})
	os.WriteFile(path, b, 0o600)
	if c, err := Load(path, nil); err != nil || c.host != "api.audible.it" {
		t.Fatalf("got %+v, %v", c, err)
	}
	// A registration of the older iPhone profile is refused, as not signed in.
	old, _ := json.Marshal(map[string]any{"adp_token": "T", "device_private_key": pemKey, "locale_code": "it",
		"device_info": map[string]string{"device_serial_number": "BFD2B5BFA3A36DDC2C15FE487A8A9E4B"}})
	os.WriteFile(path, old, 0o600)
	if _, err := Load(path, nil); !errors.Is(err, ErrOldDevice) || !errors.Is(err, ErrNoAuth) {
		t.Fatalf("an old-profile registration gave %v", err)
	}
	cur, _ := json.Marshal(map[string]any{"adp_token": "T", "device_private_key": pemKey, "locale_code": "it",
		"device_info": map[string]string{"device_serial_number": "xK3mQ9vRt2LpZa7WnB4cYe8HdU5sJf1GgTo6iNqA"}})
	os.WriteFile(path, cur, 0o600)
	if _, err := Load(path, nil); err != nil {
		t.Fatalf("a new-profile registration gave %v", err)
	}
	if _, err := New("T", pemKey, "xx", Device{}, nil); err != ErrNoAuth {
		t.Fatalf("an unknown marketplace gave %v", err)
	}
}

func TestASINAndSidecar(t *testing.T) {
	if got := ASINOf("/m/Audiobooks/La casa del giudice [B0GV1924QD].m4b"); got != "B0GV1924QD" {
		t.Fatalf("ASIN %q", got)
	}
	if got := ASINOf("/m/Audiobooks/Libro senza.m4b"); got != "" {
		t.Fatalf("ASIN %q", got)
	}
	dir := t.TempDir()
	book := filepath.Join(dir, "Libro [B0GV1924QD].m4b")
	if SidecarACR(book) != "" {
		t.Fatal("a sidecar out of nowhere")
	}
	os.WriteFile(filepath.Join(dir, "Libro [B0GV1924QD].audible.json"), []byte(`{"asin":"B0GV1924QD","acr":"CR!ABC"}`), 0o644)
	if got := SidecarACR(book); got != "CR!ABC" {
		t.Fatalf("sidecar %q", got)
	}
}

func TestParseLibrary(t *testing.T) {
	books, err := parseLibrary([]byte(`{"items":[
	 {"asin":"B0FH5CCLRJ","title":"La chiusa n.1 &amp; altro","authors":[{"name":"Georges Simenon"}],"narrators":[{"name":"A"},{"name":"B"}],
	  "series":[{"title":"Maigret","sequence":"3"}],"runtime_length_min":251,"percent_complete":12.5,
	  "listening_status":{"is_finished":false},"product_images":{"500":"http://x/500.jpg","1024":"http://x/1024.jpg","bad":"http://x/bad.jpg"},
	  "merchandising_summary":"<p>Un caso <b>difficile</b>.</p><p>Per l&apos;ispettore.<br />Fine.</p>"},
	 {"asin":"B0GV1924QD","title":"Finito","runtime_length_min":60,"listening_status":{"percent_complete":100,"is_finished":true}},
	 {"title":"senza asin"}]}`))
	if err != nil || len(books) != 2 {
		t.Fatalf("got %d books, %v", len(books), err)
	}
	b := books[0]
	if b.Title != "La chiusa n.1 & altro" || b.Authors != "Georges Simenon" || b.Narrators != "A, B" || b.Series != "Maigret" ||
		b.Sequence != "3" || b.Length != 251*time.Minute || b.Percent != 12.5 || b.Cover != "http://x/1024.jpg" ||
		b.Summary != "Un caso difficile. Per l'ispettore. Fine." {
		t.Fatalf("book %+v", b)
	}
	if f := books[1]; f.Summary != "" {
		t.Fatalf("a summary out of nowhere: %q", f.Summary)
	}
	if f := books[1]; !f.Finished || f.Percent != 100 {
		t.Fatalf("finished book %+v", f)
	}
}

// seal makes a voucher as Audible does, for a device and a book.
func seal(t *testing.T, dev Device, asin, plain string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(dev.Type + dev.Serial + dev.CustomerID + asin))
	blk, _ := aes.NewCipher(sum[:16])
	b := []byte(plain)
	for len(b)%16 != 0 {
		b = append(b, 0)
	}
	cipher.NewCBCEncrypter(blk, sum[16:]).CryptBlocks(b, b)
	return base64.StdEncoding.EncodeToString(b)
}

func TestVoucherIsOpenedWithTheDeviceAndTheBook(t *testing.T) {
	_, pemKey := testKey(t)
	dev := Device{Serial: "SERIAL", Type: "A2CZJZGLK2JJVM", CustomerID: "amzn1.account.X"}
	c, _ := New("T", pemKey, "it", dev, nil)
	key, iv, err := c.openVoucher("B0GV1924QD", seal(t, dev, "B0GV1924QD", `{"key":"00112233445566778899aabbccddeeff","iv":"ffeeddccbbaa99887766554433221100","rules":{}}`))
	if err != nil || key != "00112233445566778899aabbccddeeff" || iv != "ffeeddccbbaa99887766554433221100" {
		t.Fatalf("got %q %q %v", key, iv, err)
	}
	// A voucher that isn't JSON through to its end is read by its fields.
	key, iv, err = c.openVoucher("B0GV1924QD", seal(t, dev, "B0GV1924QD", `{"key":"aa","iv":"bb","rules":[{"broken`))
	if err != nil || key != "aa" || iv != "bb" {
		t.Fatalf("fallback: %q %q %v", key, iv, err)
	}
	// For another book, or another device, it is noise.
	if _, _, err := c.openVoucher("B000000000", seal(t, dev, "B0GV1924QD", `{"key":"aa","iv":"bb"}`)); err == nil {
		t.Fatal("opened a voucher sealed for another book")
	}
	if _, _, err := c.openVoucher("B0GV1924QD", "not base64!"); err == nil {
		t.Fatal("opened garbage")
	}
}

func TestLicenseAndFetch(t *testing.T) {
	_, pemKey := testKey(t)
	dev := Device{Serial: "S", Type: "T", CustomerID: "C"}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/licenserequest"):
			v := seal(t, dev, "B0FH5CCLRJ", `{"key":"aa","iv":"bb","rules":{}}`)
			fmt.Fprintf(w, `{"content_license":{"asin":"B0FH5CCLRJ","license_response":%q,"content_metadata":{"content_url":{"offline_url":%q},"content_reference":{"acr":"CR!1"}}}}`, v, srv.URL+"/file.aax")
		case r.URL.Path == "/file.aax":
			io.WriteString(w, "0123456789")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, _ := New("T", pemKey, "it", dev, &http.Client{Transport: rewrite2{srv.URL}})
	l, err := c.License(context.Background(), "B0FH5CCLRJ")
	if err != nil || l.Key != "aa" || l.IV != "bb" || l.ACR != "CR!1" || l.URL != srv.URL+"/file.aax" {
		t.Fatalf("licence %+v, %v", l, err)
	}
	var got strings.Builder
	var last int64
	if err := c.Fetch(context.Background(), l, &got, 0, func(done, total int64) { last = done }); err != nil || got.String() != "0123456789" || last != 10 {
		t.Fatalf("fetched %q (%d), %v", got.String(), last, err)
	}
}

// rewrite2 sends only requests for the API host to srv; others go through.
type rewrite2 struct{ to string }

func (r rewrite2) RoundTrip(q *http.Request) (*http.Response, error) {
	if strings.HasPrefix(q.URL.Host, "api.audible.") {
		q = q.Clone(q.Context())
		q.URL.Scheme, q.URL.Host = "http", strings.TrimPrefix(r.to, "http://")
	}
	return http.DefaultTransport.RoundTrip(q)
}

func TestADeniedLicenceSaysWhy(t *testing.T) {
	_, pemKey := testKey(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content_license":{"acr":"CR!1","asin":"B0FH5CCLRJ","status_code":"Denied","license_denial_reasons":[{"message":"Customer is not part of any plans"},{"message":"Customer id [X] being throttled"}]}}`)
	}))
	defer srv.Close()
	c, _ := New("T", pemKey, "it", Device{}, &http.Client{Transport: rewrite{srv.URL}})
	_, err := c.License(context.Background(), "B0FH5CCLRJ")
	var d *Denied
	if !errors.As(err, &d) || !d.Throttled || !d.NoPlan() || !strings.Contains(d.Reason, "not part of any plans") {
		t.Fatalf("got %v", err)
	}
	if acr, err := c.ACR(context.Background(), "B0FH5CCLRJ"); err != nil || acr != "CR!1" {
		t.Fatalf("a refusal still names the book's content reference: %q %v", acr, err)
	}
}

// A download carries on from the bytes written when the server honours Range;
// when it sends the whole file again or has gone away, Fetch says so.
func TestFetchResumes(t *testing.T) {
	const body = "0123456789"
	mode := "range"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "whole":
			io.WriteString(w, body)
		case "416":
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		case "503":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.ServeContent(w, r, "f.aax", time.Time{}, strings.NewReader(body))
		}
	}))
	defer srv.Close()
	_, pemKey := testKey(t)
	c, _ := New("T", pemKey, "it", Device{}, srv.Client())
	l := License{URL: srv.URL + "/f.aax"}
	var got strings.Builder
	var last, total int64
	if err := c.Fetch(context.Background(), l, &got, 4, func(d, n int64) { last, total = d, n }); err != nil || got.String() != "456789" || last != 10 || total != 10 {
		t.Fatalf("resumed %q (%d of %d), %v", got.String(), last, total, err)
	}
	for _, m := range []string{"whole", "416"} {
		mode = m
		if err := c.Fetch(context.Background(), l, io.Discard, 4, func(int64, int64) {}); !errors.Is(err, ErrNoResume) {
			t.Fatalf("%s: %v, want ErrNoResume", m, err)
		}
	}
	mode = "503"
	var se *StatusError
	if err := c.Fetch(context.Background(), l, io.Discard, 0, func(int64, int64) {}); !errors.As(err, &se) || se.Code != 503 {
		t.Fatalf("503: %v", err)
	}
}

// A download whose bytes stop coming is given up as stalled, and one that the
// caller cancels is not.
func TestFetchStalls(t *testing.T) {
	defer func(d time.Duration) { stallAfter = d }(stallAfter)
	stallAfter = 50 * time.Millisecond
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		io.WriteString(w, "01234")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	_, pemKey := testKey(t)
	c, _ := New("T", pemKey, "it", Device{}, srv.Client())
	l := License{URL: srv.URL}
	var got strings.Builder
	if err := c.Fetch(context.Background(), l, &got, 0, func(int64, int64) {}); !errors.Is(err, ErrStalled) || got.String() != "01234" {
		t.Fatalf("stalled download gave %q, %v", got.String(), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.Fetch(ctx, l, io.Discard, 0, func(int64, int64) {}); errors.Is(err, ErrStalled) {
		t.Fatalf("a cancelled download is not stalled: %v", err)
	}
}

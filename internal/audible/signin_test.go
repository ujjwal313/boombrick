package audible

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func signInServer(t *testing.T, answer string) (*SignIn, *httptest.Server, *int) {
	t.Helper()
	_, pemKey := testKey(t)
	amazon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Replace(answer, `"KEY"`, string(mustJSON(pemKey)), 1))
	}))
	t.Cleanup(amazon.Close)
	signed := 0
	s := NewSignIn(filepath.Join(t.TempDir(), "audible", "auth.json"), amazon.Client(), func(*Client) { signed++ })
	s.base = amazon.URL
	web := httptest.NewServer(s.Handler())
	t.Cleanup(web.Close)
	return s, web, &signed
}

// noRedirect is a client that shows a redirect instead of following it.
var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func open(t *testing.T, web *httptest.Server, locale string) *http.Response {
	t.Helper()
	r, err := noRedirect.Get(web.URL + "/go?locale=" + locale)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	return r
}

func post(t *testing.T, web *httptest.Server, address string) string {
	t.Helper()
	r, err := http.PostForm(web.URL+"/register", url.Values{"address": {address}})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func TestThePageOffersTheCountries(t *testing.T) {
	_, web, _ := signInServer(t, registered)
	req, _ := http.NewRequest("GET", web.URL+"/", nil)
	req.Header.Set("Accept-Language", "it-IT,it;q=0.9,en;q=0.8")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(b), `<option value="it" selected>Italia</option>`) {
		t.Fatalf("Italia isn't chosen:\n%s", b)
	}
	if r, _ := http.Get(web.URL + "/nothing"); r.StatusCode != 404 {
		t.Errorf("/nothing: %d", r.StatusCode)
	}
}

func TestGuess(t *testing.T) {
	for in, want := range map[string]string{
		"it-IT,it;q=0.9": "it", "en-GB,en": "uk", "en-US": "us", "de": "de", "ja-JP": "jp",
		"en-AU": "au", "pt-BR": "us", "": "us",
	} {
		if got := guess(in); got != want {
			t.Errorf("%q: %s, want %s", in, got, want)
		}
	}
}

func TestChoosingACountrySendsThePhoneToAmazon(t *testing.T) {
	s, web, _ := signInServer(t, registered)
	r := open(t, web, "it")
	loc := r.Header.Get("Location")
	if r.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "https://www.amazon.it/ap/signin?") {
		t.Fatalf("%d %s", r.StatusCode, loc)
	}
	if strings.Contains(loc, s.login.verifier) {
		t.Fatal("the link leaks the code verifier")
	}
	if r, _ := noRedirect.Get(web.URL + "/go?locale=zz"); r.StatusCode != 400 {
		t.Errorf("an unknown country: %d", r.StatusCode)
	}
}

func TestPastingTheAddressSignsIn(t *testing.T) {
	s, web, signed := signInServer(t, registered)
	open(t, web, "it")
	body := post(t, web, "https://www.amazon.it/ap/maplanding?openid.oa2.authorization_code=C1")
	if !strings.Contains(body, "Done.") {
		t.Fatalf("page:\n%s", body)
	}
	if st, err := s.State(); st != SignedIn || err != nil {
		t.Fatalf("state %v, err %v", st, err)
	}
	if *signed != 1 {
		t.Fatalf("done called %d times", *signed)
	}
	if _, err := os.Stat(s.file); err != nil {
		t.Fatal(err)
	}
	if post(t, web, "again"); *signed != 1 {
		t.Fatalf("a second paste registered again (%d)", *signed)
	}
}

func TestPastingBeforeOpeningAmazonSaysSo(t *testing.T) {
	s, web, _ := signInServer(t, registered)
	if body := post(t, web, "x?openid.oa2.authorization_code=C"); !strings.Contains(body, "Open Amazon&#39;s sign-in first") {
		t.Fatalf("page:\n%s", body)
	}
	if st, _ := s.State(); st != Waiting {
		t.Fatalf("state %v", st)
	}
}

func TestAWrongAddressSaysSo(t *testing.T) {
	s, web, signed := signInServer(t, registered)
	open(t, web, "it")
	body := post(t, web, "https://www.amazon.it/ap/signin")
	if !strings.Contains(body, "no sign-in code") {
		t.Fatalf("page:\n%s", body)
	}
	if st, _ := s.State(); st != Failed || *signed != 0 {
		t.Fatalf("state %v, signed %d", st, *signed)
	}
	if _, err := os.Stat(s.file); err == nil {
		t.Fatal("a registration was saved")
	}
}

func TestARefusalLeavesTheSignInOpen(t *testing.T) {
	s, web, _ := signInServer(t, `{"response":{"success":{"tokens":{"mac_dms":{}}}}}`)
	open(t, web, "it")
	body := post(t, web, "x?openid.oa2.authorization_code=C")
	if !strings.Contains(body, "didn&#39;t accept") {
		t.Fatalf("page:\n%s", body)
	}
	if st, _ := s.State(); st != Failed {
		t.Fatalf("state %v", st)
	}
}

func TestStartServesOnThePrivateAddress(t *testing.T) {
	if LocalIP() == "" {
		t.Skip("no private network address on this machine")
	}
	s := NewSignIn(filepath.Join(t.TempDir(), "auth.json"), http.DefaultClient, nil)
	addr, err := s.Start()
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Start(); again != addr {
		t.Fatalf("a second Start gave %s, not %s", again, addr)
	}
	if !strings.HasPrefix(addr, "http://"+LocalIP()+":") {
		t.Fatalf("address %s", addr)
	}
	r, err := http.Get(addr)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	s.Stop()
	if _, err := http.Get(addr); err == nil {
		t.Fatal("still serving after Stop")
	}
}

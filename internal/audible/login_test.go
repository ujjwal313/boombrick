package audible

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestLoginURLAsksForADeviceOnTheMarketplace(t *testing.T) {
	l, err := NewLogin("it")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(l.URL())
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "www.amazon.it" || u.Path != "/ap/signin" {
		t.Fatalf("url %s", u)
	}
	q := u.Query()
	want := map[string]string{
		"marketPlaceId":                    "A2N7FU2W2BU2ZC",
		"openid.assoc_handle":              "amzn_audible_ios_it",
		"openid.return_to":                 "https://www.amazon.it/ap/maplanding",
		"openid.oa2.code_challenge_method": "S256",
		"openid.oa2.scope":                 "device_auth_access",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	sum := sha256.Sum256([]byte(l.verifier))
	if q.Get("openid.oa2.code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Error("the challenge is not the verifier's SHA-256")
	}
	if got := q.Get("openid.oa2.client_id"); !strings.HasPrefix(got, "device:") {
		t.Errorf("client id %q", got)
	}
}

// Audible denies licences to devices registered with the older iPhone profile
// (a 32-character hex serial, app 3.56.2 on iOS 15), so a registration must
// present the newer one.
func TestLoginProfile(t *testing.T) {
	for range 32 {
		l, err := NewLogin("it")
		if err != nil {
			t.Fatal(err)
		}
		if !regexp.MustCompile(`^[A-Za-z0-9]{40}$`).MatchString(l.serial) {
			t.Fatalf("serial %q is not 40 letters and digits", l.serial)
		}
	}
	if appVersion != "4.60" || osVersion != "26.2" || softwareVersion != "46000856" {
		t.Errorf("profile %s, %s, %s", appVersion, osVersion, softwareVersion)
	}
}

func TestLoginsDiffer(t *testing.T) {
	a, _ := NewLogin("it")
	b, _ := NewLogin("it")
	if a.verifier == b.verifier || a.serial == b.serial {
		t.Fatal("two logins share a verifier or a serial")
	}
	if _, err := NewLogin("zz"); err == nil {
		t.Fatal("an unknown marketplace was accepted")
	}
}

func TestEveryMarketplaceCanSignIn(t *testing.T) {
	for loc := range domains {
		if _, err := NewLogin(loc); err != nil {
			t.Errorf("%s: %v", loc, err)
		}
	}
}

func TestCodeOf(t *testing.T) {
	code, err := CodeOf(" https://www.amazon.it/ap/maplanding?openid.assoc_handle=x&openid.oa2.authorization_code=ABC123&a=b \n")
	if err != nil || code != "ABC123" {
		t.Fatalf("code %q, err %v", code, err)
	}
	for _, bad := range []string{"", "https://www.amazon.it/ap/signin?x=1", "not an address"} {
		if _, err := CodeOf(bad); !errors.Is(err, ErrNoCode) {
			t.Errorf("%q: err %v", bad, err)
		}
	}
}

const registered = `{"response":{"success":{
 "tokens":{
  "mac_dms":{"adp_token":"ADP","device_private_key":"KEY"},
  "bearer":{"access_token":"AT","refresh_token":"RT","expires_in":"3600"},
  "store_authentication_cookie":{"cookie":"C"},
  "website_cookies":[{"Name":"session-id","Value":"\"123\""}]},
 "extensions":{
  "device_info":{"device_serial_number":"SER","device_type":"A2CZJZGLK2JJVM"},
  "customer_info":{"user_id":"amzn1.account.X","name":"Davide"}}}}}`

func TestCompleteRegistersTheDevice(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/auth/register" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		io.WriteString(w, registered)
	}))
	defer srv.Close()
	l, _ := NewLogin("it")
	l.base = srv.URL

	reg, err := l.Complete(context.Background(), srv.Client(), "https://www.amazon.it/ap/maplanding?openid.oa2.authorization_code=CODE9")
	if err != nil {
		t.Fatal(err)
	}
	auth := got["auth_data"].(map[string]any)
	if auth["authorization_code"] != "CODE9" || auth["code_verifier"] != l.verifier || auth["client_id"] != l.clientID() {
		t.Errorf("auth_data %v", auth)
	}
	rd := got["registration_data"].(map[string]any)
	if rd["device_serial"] != l.serial || rd["app_version"] != appVersion ||
		rd["os_version"] != osVersion || rd["software_version"] != softwareVersion {
		t.Errorf("registration_data %v", rd)
	}
	if reg.ADPToken != "ADP" || reg.DeviceKey != "KEY" || reg.Locale != "it" || reg.WebsiteCookies["session-id"] != "123" {
		t.Errorf("registration %+v", reg)
	}
	if time.Until(time.Unix(int64(reg.Expires), 0)) < 59*time.Minute {
		t.Errorf("expires %v", reg.Expires)
	}
}

func TestCompleteRefusesAnAddressWithoutACode(t *testing.T) {
	l, _ := NewLogin("it")
	if _, err := l.Complete(context.Background(), http.DefaultClient, "https://www.amazon.it/ap/signin"); !errors.Is(err, ErrNoCode) {
		t.Fatalf("err %v", err)
	}
}

func TestCompleteReportsARefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"response":{"error":{"message":"bad"}}}`, http.StatusBadRequest)
	}))
	defer srv.Close()
	l, _ := NewLogin("it")
	l.base = srv.URL
	if _, err := l.Complete(context.Background(), srv.Client(), "x?openid.oa2.authorization_code=C"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("err %v", err)
	}
}

func TestASavedRegistrationLoads(t *testing.T) {
	_, pemKey := testKey(t)
	reg, err := parseRegistration([]byte(strings.Replace(registered, `"KEY"`, string(mustJSON(pemKey)), 1)), "it", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "audible", "auth.json")
	if err := reg.Save(path); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if _, err := os.Stat(path + ".part"); err == nil {
		t.Error("a .part file was left")
	}
	c, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.dev.Serial != "SER" || c.dev.CustomerID != "amzn1.account.X" || c.host != "api.audible.it" {
		t.Errorf("client %+v", c)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

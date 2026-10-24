package audible

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// marketplaces maps a marketplace to the id Amazon's sign-in wants.
var marketplaces = map[string]string{
	"us": "AF2M0KC94RCEA", "uk": "A2I9A3Q2GNFNGQ", "de": "AN7V1F1VY261K", "fr": "A2728XDNODOQ8T",
	"ca": "A2CQZ5RBY40XE", "au": "AN7EY7DTAW63G", "it": "A2N7FU2W2BU2ZC", "es": "ALMIKO4SZCSAR",
	"jp": "A1QAP3MOU4173J", "in": "AJO3FBRUE6J4S",
}

// deviceType is what the iPhone Audible app calls itself when it registers.
const deviceType = "A2CZJZGLK2JJVM"

// The iPhone profile a registration presents: app 4.60 on iOS 26.2, as audible
// 0.13's newer profile does. Audible refuses licences (Denied, with
// CustomerThrottled) to devices registered as the app 3.56.2 on iOS 15 with a
// 32-character hex serial, which is why these and the serial's shape matter.
const (
	appVersion      = "4.60"
	osVersion       = "26.2"
	softwareVersion = "46000856"
)

// serialLetters are what a device serial is made of.
const serialLetters = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// newSerial makes a random 40-character serial of letters and digits.
func newSerial() (string, error) {
	b := make([]byte, 40)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i, c := range b {
		// 248 is the biggest multiple of 62 a byte holds; above it would skew.
		for c >= 248 {
			var r [1]byte
			if _, err := rand.Read(r[:]); err != nil {
				return "", err
			}
			c = r[0]
		}
		b[i] = serialLetters[int(c)%len(serialLetters)]
	}
	return string(b), nil
}

// Login registers this device as one of the listener's Audible devices, the
// way the iPhone app does. The listener signs in to Amazon in a browser, which
// ends on a page that doesn't exist; its address holds an authorization code,
// and Complete trades that for the device's keys. The steps follow the iPhone
// app's, as the Python library audible by mkb79
// (https://github.com/mkb79/Audible) describes them.
type Login struct {
	locale, domain string
	serial         string
	verifier       string
	base           string // the API's address; empty for Amazon's own
}

// NewLogin starts a registration on the marketplace with that code, such as
// "it". An unknown marketplace gives an error.
func NewLogin(locale string) (*Login, error) {
	locale = strings.ToLower(locale)
	domain, ok := domains[locale]
	if !ok || marketplaces[locale] == "" {
		return nil, fmt.Errorf("audible: no marketplace %q", locale)
	}
	v := make([]byte, 32)
	if _, err := rand.Read(v); err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	return &Login{
		locale: locale, domain: domain,
		verifier: base64.RawURLEncoding.EncodeToString(v),
		serial:   serial,
	}, nil
}

// clientID is the device's id, made of its serial and type.
func (l *Login) clientID() string {
	return hex.EncodeToString([]byte(l.serial + "#" + deviceType))
}

// URL is the Amazon sign-in page to open in a browser.
func (l *Login) URL() string {
	sum := sha256.Sum256([]byte(l.verifier))
	q := url.Values{
		"openid.oa2.response_type":         {"code"},
		"openid.oa2.code_challenge_method": {"S256"},
		"openid.oa2.code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"openid.return_to":                 {"https://www.amazon." + l.domain + "/ap/maplanding"},
		"openid.assoc_handle":              {"amzn_audible_ios_" + l.locale},
		"openid.identity":                  {"http://specs.openid.net/auth/2.0/identifier_select"},
		"pageId":                           {"amzn_audible_ios"},
		"accountStatusPolicy":              {"P1"},
		"openid.claimed_id":                {"http://specs.openid.net/auth/2.0/identifier_select"},
		"openid.mode":                      {"checkid_setup"},
		"openid.ns.oa2":                    {"http://www.amazon.com/ap/ext/oauth/2"},
		"openid.oa2.client_id":             {"device:" + l.clientID()},
		"openid.ns.pape":                   {"http://specs.openid.net/extensions/pape/1.0"},
		"marketPlaceId":                    {marketplaces[l.locale]},
		"openid.oa2.scope":                 {"device_auth_access"},
		"forceMobileLayout":                {"true"},
		"openid.ns":                        {"http://specs.openid.net/auth/2.0"},
		"openid.pape.max_auth_age":         {"0"},
	}
	return "https://www.amazon." + l.domain + "/ap/signin?" + q.Encode()
}

// ErrNoCode means the address the listener pasted holds no authorization code:
// it is not the page the sign-in ended on.
var ErrNoCode = errors.New("audible: that address has no sign-in code")

// CodeOf finds the authorization code in the address the sign-in ended on.
func CodeOf(address string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(address))
	if err != nil {
		return "", ErrNoCode
	}
	code := u.Query().Get("openid.oa2.authorization_code")
	if code == "" {
		return "", ErrNoCode
	}
	return code, nil
}

// Registration is what registering a device gives: the keys that sign its
// requests, and what Audible says of the device and its owner. It is written
// as the Python audible library writes it, so audible-tui reads it too.
type Registration struct {
	ADPToken        string            `json:"adp_token"`
	DeviceKey       string            `json:"device_private_key"`
	AccessToken     string            `json:"access_token"`
	RefreshToken    string            `json:"refresh_token"`
	Expires         float64           `json:"expires"`
	WebsiteCookies  map[string]string `json:"website_cookies"`
	StoreAuthCookie json.RawMessage   `json:"store_authentication_cookie"`
	DeviceInfo      json.RawMessage   `json:"device_info"`
	CustomerInfo    json.RawMessage   `json:"customer_info"`
	Locale          string            `json:"locale_code"`
	WithUsername    bool              `json:"with_username"`
	ActivationBytes *string           `json:"activation_bytes"`
}

// Complete registers the device with the address the sign-in ended on. hc
// makes the request.
func (l *Login) Complete(ctx context.Context, hc *http.Client, address string) (*Registration, error) {
	code, err := CodeOf(address)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"requested_token_type": []string{"bearer", "mac_dms", "website_cookies", "store_authentication_cookie"},
		"cookies":              map[string]any{"website_cookies": []any{}, "domain": ".amazon." + l.domain},
		"registration_data": map[string]string{
			"domain": "Device", "app_version": appVersion, "device_serial": l.serial,
			"device_type": deviceType,
			"device_name": "%FIRST_NAME%%FIRST_NAME_POSSESSIVE_STRING%%DUPE_STRATEGY_1ST%Audible for iPhone",
			"os_version":  osVersion, "software_version": softwareVersion, "device_model": "iPhone", "app_name": "Audible",
		},
		"auth_data": map[string]string{
			"client_id": l.clientID(), "authorization_code": code, "code_verifier": l.verifier,
			"code_algorithm": "SHA-256", "client_domain": "DeviceLegacy",
		},
		"requested_extensions": []string{"device_info", "customer_info"},
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	base := l.base
	if base == "" {
		base = "https://api.amazon." + l.domain
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/auth/register", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("audible: registering the device: %s", resp.Status)
	}
	return parseRegistration(out, l.locale, time.Now())
}

func parseRegistration(b []byte, locale string, now time.Time) (*Registration, error) {
	var r struct {
		Response struct {
			Success struct {
				Tokens struct {
					MacDMS struct {
						ADPToken  string `json:"adp_token"`
						DeviceKey string `json:"device_private_key"`
					} `json:"mac_dms"`
					Bearer struct {
						AccessToken  string `json:"access_token"`
						RefreshToken string `json:"refresh_token"`
						ExpiresIn    string `json:"expires_in"`
					} `json:"bearer"`
					StoreAuthCookie json.RawMessage `json:"store_authentication_cookie"`
					WebsiteCookies  []struct {
						Name, Value string
					} `json:"website_cookies"`
				} `json:"tokens"`
				Extensions struct {
					DeviceInfo   json.RawMessage `json:"device_info"`
					CustomerInfo json.RawMessage `json:"customer_info"`
				} `json:"extensions"`
			} `json:"success"`
		} `json:"response"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("audible: registering the device: %w", err)
	}
	s := r.Response.Success
	if s.Tokens.MacDMS.ADPToken == "" || s.Tokens.MacDMS.DeviceKey == "" {
		return nil, errors.New("audible: registering the device: no keys in the answer")
	}
	cookies := map[string]string{}
	for _, c := range s.Tokens.WebsiteCookies {
		cookies[c.Name] = strings.ReplaceAll(c.Value, `"`, "")
	}
	var secs int64
	fmt.Sscan(s.Tokens.Bearer.ExpiresIn, &secs)
	return &Registration{
		ADPToken: s.Tokens.MacDMS.ADPToken, DeviceKey: s.Tokens.MacDMS.DeviceKey,
		AccessToken: s.Tokens.Bearer.AccessToken, RefreshToken: s.Tokens.Bearer.RefreshToken,
		Expires:        float64(now.Add(time.Duration(secs) * time.Second).Unix()),
		WebsiteCookies: cookies, StoreAuthCookie: s.Tokens.StoreAuthCookie,
		DeviceInfo: s.Extensions.DeviceInfo, CustomerInfo: s.Extensions.CustomerInfo,
		Locale: locale,
	}, nil
}

// Save writes the registration to path, readable by its owner alone, for Load.
// The file is made whole or not at all.
func (r *Registration) Save(path string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

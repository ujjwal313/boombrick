package audible

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// SignInState is how a sign-in stands.
type SignInState uint8

const (
	Waiting     SignInState = iota // the page waits for the address the browser ended on
	Registering                    // Audible is being asked to register the device
	SignedIn                       // the registration is saved
	Failed                         // the last try failed; Err says why
)

// SignIn is a sign-in a phone does for the device: the device serves a page on
// the local network, the phone opens Amazon's sign-in from it, and pastes
// back the address the sign-in ends on. The device then registers itself.
// Amazon's page needs a real browser, with its captcha and one-time codes,
// which a handheld doesn't have.
type SignIn struct {
	hc   *http.Client
	file string // where the registration is saved
	done func(*Client)
	base string // the API's address; empty for Amazon's own, set by tests

	mu    sync.Mutex
	login *Login // the marketplace the phone chose, once it has
	state SignInState
	err   error
	srv   *http.Server
	url   string
}

// NewSignIn prepares a sign-in. The phone chooses the marketplace. A
// registration is saved to file, and done hears of the client it gives. hc
// makes the requests to Amazon.
func NewSignIn(file string, hc *http.Client, done func(*Client)) *SignIn {
	return &SignIn{hc: hc, file: file, done: done}
}

// Start serves the page and gives its address. It listens on the first
// private address of the device, where a phone on the same Wi-Fi can reach it.
func (s *SignIn) Start() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv != nil {
		return s.url, nil
	}
	ip := LocalIP()
	if ip == "" {
		return "", errors.New("audible: no network address to serve the sign-in on")
	}
	l, err := net.Listen("tcp", net.JoinHostPort(ip, "0"))
	if err != nil {
		return "", err
	}
	s.srv = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s.url = "http://" + l.Addr().String() + "/"
	go s.srv.Serve(l)
	return s.url, nil
}

// Handler is the sign-in's page and the address it sends the pasted address to.
func (s *SignIn) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("GET /go", s.amazon)
	mux.HandleFunc("POST /register", s.register)
	return mux
}

// Stop stops serving the page.
func (s *SignIn) Stop() {
	s.mu.Lock()
	srv := s.srv
	s.srv = nil
	s.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}
}

// State says how the sign-in stands, and why it failed, if it did.
func (s *SignIn) State() (SignInState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.err
}

func (s *SignIn) set(st SignInState, err error) {
	s.mu.Lock()
	s.state, s.err = st, err
	s.mu.Unlock()
}

type pageData struct {
	Locales []localeChoice
	Chosen  string
	Message string
	Done    bool
}

type localeChoice struct{ Code, Name string }

// choices are the marketplaces a phone can sign in to.
var choices = []localeChoice{
	{"us", "United States"}, {"uk", "United Kingdom"}, {"de", "Deutschland"}, {"fr", "France"},
	{"it", "Italia"}, {"es", "España"}, {"ca", "Canada"}, {"au", "Australia"}, {"in", "India"}, {"jp", "日本"},
}

// guess is the marketplace a browser's language points to, or "us".
func guess(acceptLanguage string) string {
	for _, l := range strings.Split(acceptLanguage, ",") {
		tag, _, _ := strings.Cut(strings.TrimSpace(l), ";")
		lang, region, _ := strings.Cut(strings.ToLower(tag), "-")
		switch region {
		case "gb":
			return "uk"
		case "au", "ca", "in", "de", "fr", "it", "es":
			return region
		}
		switch lang {
		case "de", "fr", "it", "es":
			return lang
		case "ja":
			return "jp"
		}
		return "us"
	}
	return "us"
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Boombrick: sign in to Audible</title>
<style>
body{font:17px/1.45 system-ui,sans-serif;margin:0;padding:20px;max-width:34em;margin-inline:auto;background:#15110d;color:#f4ecdc}
h1{font-size:1.4em}button,select{display:block;box-sizing:border-box;width:100%;padding:14px;margin:10px 0;border:0;border-radius:12px;
background:#e3b04b;color:#15110d;font:inherit;font-weight:600;text-align:center}
select{background:#221b14;color:inherit;border:1px solid #6b5d49;font-weight:400}
textarea{box-sizing:border-box;width:100%;height:7em;padding:10px;border-radius:10px;border:1px solid #6b5d49;background:#221b14;color:inherit;font:14px monospace}
.m{padding:12px;border-radius:10px;background:#3a2a1a}.ok{background:#1f3a22}
</style></head><body>
<h1>Sign in to Audible</h1>
{{if .Done}}<p class="m ok">Done. Your Brick is signed in to Audible. You can close this page.</p>
{{else}}
{{if .Message}}<p class="m">{{.Message}}</p>{{end}}
<p><b>1.</b> Choose your Audible country, then open Amazon's sign-in and sign in. A one-time code or a captcha is fine.</p>
<form method="get" action="/go" target="_blank">
<select name="locale">{{range .Locales}}<option value="{{.Code}}"{{if eq .Code $.Chosen}} selected{{end}}>{{.Name}}</option>{{end}}</select>
<button type="submit">Open Amazon sign-in</button></form>
<p><b>2.</b> You end on a page that is blank or says it wasn't found. That is expected. Copy its address from the address bar and paste it here.</p>
<form method="post" action="/register">
<textarea name="address" placeholder="https://www.amazon.…/ap/maplanding?…" required></textarea>
<button type="submit">Sign in</button></form>
{{end}}</body></html>`))

func (s *SignIn) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	st, _ := s.State()
	s.render(w, pageData{Locales: choices, Chosen: guess(r.Header.Get("Accept-Language")), Done: st == SignedIn})
}

func (s *SignIn) render(w http.ResponseWriter, d pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if d.Locales == nil {
		d.Locales = choices
	}
	pageTmpl.Execute(w, d)
}

// amazon sends the phone to Amazon's sign-in on the marketplace it chose. Each
// visit makes a new login, so the one the address pasted back belongs to is
// the last one opened.
func (s *SignIn) amazon(w http.ResponseWriter, r *http.Request) {
	l, err := NewLogin(r.URL.Query().Get("locale"))
	if err != nil {
		http.Error(w, "unknown country", http.StatusBadRequest)
		return
	}
	l.base = s.base
	s.mu.Lock()
	s.login = l
	s.mu.Unlock()
	http.Redirect(w, r, l.URL(), http.StatusSeeOther)
}

func (s *SignIn) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if st, _ := s.State(); st == SignedIn {
		s.render(w, pageData{Done: true})
		return
	}
	s.mu.Lock()
	l := s.login
	s.mu.Unlock()
	if l == nil {
		s.render(w, pageData{Message: "Open Amazon's sign-in first (step 1), then paste the address you end on."})
		return
	}
	s.set(Registering, nil)
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	reg, err := l.Complete(ctx, s.hc, r.PostForm.Get("address"))
	if err == nil {
		err = reg.Save(s.file)
	}
	var c *Client
	if err == nil {
		c, err = Load(s.file, s.hc)
	}
	if err != nil {
		s.set(Failed, err)
		s.render(w, pageData{Message: signInMessage(err)})
		return
	}
	s.set(SignedIn, nil)
	if s.done != nil {
		s.done(c)
	}
	s.render(w, pageData{Done: true})
}

// signInMessage says in a few words why a sign-in failed, for the phone.
func signInMessage(err error) string {
	if errors.Is(err, ErrNoCode) {
		return "That address has no sign-in code. Copy the address of the page you ended on after signing in, the one with openid.oa2.authorization_code in it."
	}
	return fmt.Sprintf("Audible didn't accept the sign-in (%v). Open Amazon's sign-in again and try once more.", err)
}

// LocalIP is the device's address on the local network, such as its Wi-Fi's,
// or "" without one.
func LocalIP() string {
	ifs, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok {
				if ip := n.IP.To4(); ip != nil && ip.IsPrivate() {
					return ip.String()
				}
			}
		}
	}
	return ""
}

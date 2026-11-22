package spotify

import (
	"bytes"
	"errors"
	"image"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/uitest"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

func TestMain(m *testing.M) { uitest.Main(m) }

var t0 = time.Unix(1000, 0)

func press(b input.Button) input.Event { return input.Event{Button: b, Kind: input.Press} }

// fakeSource answers from its fields; a test changing them after the first
// frame bumps version. It counts the times the status is asked.
type fakeSource struct {
	status    spotify.Status
	statusErr error
	playlists []spotify.Playlist
	have      bool
	listErr   error
	tracks    map[string][]spotify.Track
	logins    int
	loginErr  error
	retries   int
	version   uint64
	asked     int
}

func (s *fakeSource) Status() (spotify.Status, error) { s.asked++; return s.status, s.statusErr }
func (s *fakeSource) Playlists() ([]spotify.Playlist, bool, error) {
	return s.playlists, s.have, s.listErr
}
func (s *fakeSource) Tracks(uri string) ([]spotify.Track, bool, error) {
	ts, ok := s.tracks[uri]
	return ts, ok, s.listErr
}
func (s *fakeSource) Retry()          { s.retries++ }
func (s *fakeSource) Login()          { s.logins++ }
func (s *fakeSource) LoginErr() error { return s.loginErr }
func (s *fakeSource) Version() uint64 { return s.version }

// fakePlayer records what the screens ask it.
type fakePlayer struct {
	shuffle bool
	played  []string // "id@start" for the song started, with "~" when shuffled
	queued  [][]string
	state   player.State
}

func ids(ts []player.Track) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.SpotifyID)
	}
	return out
}

func (p *fakePlayer) SetShuffle(on bool) { p.shuffle = on }
func (p *fakePlayer) Play(ts []player.Track, start int) {
	s := ""
	if p.shuffle {
		s = "~"
	}
	p.played = append(p.played, s+ts[start].SpotifyID+"@"+strconv.Itoa(start))
}
func (p *fakePlayer) Enqueue(ts []player.Track) { p.queued = append(p.queued, ids(ts)) }
func (p *fakePlayer) State() player.State       { return p.state }

// fakeNav records pushes, pops and toasts.
type fakeNav struct {
	pushed []screens.Screen
	toasts []string
	tabs   []int
}

func (n *fakeNav) Push(s screens.Screen) { n.pushed = append(n.pushed, s) }
func (n *fakeNav) Pop()                  { n.pushed = n.pushed[:len(n.pushed)-1] }
func (n *fakeNav) Toast(msg string)      { n.toasts = append(n.toasts, msg) }
func (n *fakeNav) ShowTab(t int)         { n.tabs = append(n.tabs, t) }

const cover = "https://i.scdn.co/image/ab67616d0000b273"

var (
	ready = spotify.Status{State: spotify.Ready, User: "bmfjclzv7pstc3zzlq0nteyni", Name: "Ujjwal", Premium: true}
	lists = []spotify.Playlist{
		{URI: "spotify:playlist:37i9dQZF1DX76Wlfdnj7AP", Name: "This Is Eminem", Owner: "Spotify", Count: 50, Cover: cover + "ee"},
		{URI: "spotify:playlist:5ABHKGoOzxkaa28ttQV9sE", Name: "Road trip", Owner: "Ujjwal", Count: 2, Cover: cover + "rt"},
	}
	road = []spotify.Track{
		{ID: "4Q0qVhFQa7j6jRKzo3HDmP", Title: "Sapphire", Artists: []string{"Ed Sheeran"}, Album: "Sapphire",
			AlbumArtist: "Ed Sheeran", Duration: 179082, Cover: cover + "sa"},
		{ID: "2IRZnDFmlqMuOrYOLnZZyc", Title: "Going Bad (feat. Drake)", Artists: []string{"Meek Mill", "Drake"},
			Album: "Championships", AlbumArtist: "Meek Mill", Duration: 180522, Cover: cover + "gb"},
	}
)

func signedIn() *fakeSource {
	return &fakeSource{status: ready, playlists: lists, have: true,
		tracks: map[string][]spotify.Track{spotify.LikedURI: road[:1], lists[1].URI: road}}
}

// render draws s twice, as the app would: the first frame asks for its
// pictures and the second shows them, faded in.
func render(t *testing.T, s screens.Screen) *image.NRGBA {
	t.Helper()
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	uitest.WithCovers(u)
	u.Accent = theme.Derive(35, 0.12)
	u.Status = fixture.Status()
	u.BeginFrame(t0)
	s.Draw(u)
	u.BeginFrame(t0.Add(anim.ArtFade))
	s.Draw(u)
	return off.Image()
}

func TestTracksAreStreamedQueueEntries(t *testing.T) {
	got := Tracks(road[1:])
	want := []player.Track{{SpotifyID: "2IRZnDFmlqMuOrYOLnZZyc", Title: "Going Bad (feat. Drake)", Artist: "Meek Mill, Drake",
		AlbumArtist: "Meek Mill", Album: "Championships", Duration: 180522 * time.Millisecond, Cover: cover + "gb"}}
	if !reflect.DeepEqual(got, want) || !got[0].Streamed() {
		t.Fatalf("tracks %+v", got)
	}
}

func TestSignedOutTabOpensTheSignIn(t *testing.T) {
	src := &fakeSource{status: spotify.Status{State: spotify.SignedOut}}
	nav := &fakeNav{}
	tab := NewTab(&Env{Nav: nav, Src: src, Player: &fakePlayer{}})
	uitest.Golden(t, "spotify-signed-out", render(t, tab))
	tab.Handle(press(input.A), t0)
	if len(nav.pushed) != 1 || src.logins != 1 {
		t.Fatalf("pushed %d pages, %d logins", len(nav.pushed), src.logins)
	}
	if _, ok := nav.pushed[0].(*SignIn); !ok {
		t.Fatalf("pushed %T", nav.pushed[0])
	}
}

func TestTabOpensLikedSongsAndPlaylists(t *testing.T) {
	nav := &fakeNav{}
	tab := NewTab(&Env{Nav: nav, Src: signedIn(), Player: &fakePlayer{}})
	uitest.Golden(t, "spotify-library", render(t, tab))
	tab.Handle(press(input.A), t0)
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.Down), t0)
	tab.Handle(press(input.A), t0)
	if len(nav.pushed) != 2 {
		t.Fatalf("pushed %d pages", len(nav.pushed))
	}
	if pg := nav.pushed[0].(*PlaylistPage); pg.pl.URI != spotify.LikedURI || pg.pl.Name != "Liked Songs" {
		t.Fatalf("first page %q %q", pg.pl.URI, pg.pl.Name)
	}
	if pg := nav.pushed[1].(*PlaylistPage); pg.pl.URI != lists[1].URI || pg.who != "Ujjwal" {
		t.Fatalf("second page %q %q", pg.pl.URI, pg.who)
	}
}

// A library that can't be had says why, and X asks again.
func TestTabSaysWhyThereAreNoPlaylists(t *testing.T) {
	src := &fakeSource{status: ready, listErr: spotify.ErrOffline}
	tab := NewTab(&Env{Nav: &fakeNav{}, Src: src, Player: &fakePlayer{}})
	render(t, tab)
	if !tab.listFailed {
		t.Fatal("the playlists' failure isn't shown")
	}
	tab.Handle(press(input.X), t0)
	if src.retries != 1 {
		t.Fatalf("%d retries", src.retries)
	}
	if got := failText(spotify.ErrOffline); got != "You’re offline. Connect to Wi-Fi to see your Spotify library." {
		t.Fatalf("offline says %q", got)
	}
}

// Offline with no playlists kept, the tab says it is offline, and A opens
// Downloads; with playlists kept, they are still shown.
func TestTabOffline(t *testing.T) {
	nav := &fakeNav{}
	src := &fakeSource{status: spotify.Status{State: spotify.Offline, User: "ujjwal", Name: "Ujjwal", Premium: true}}
	tab := NewTab(&Env{Nav: nav, Src: src, Player: &fakePlayer{}})
	uitest.Golden(t, "spotify-offline", render(t, tab))
	if !tab.Handle(press(input.A), t0) || len(nav.tabs) != 1 || nav.tabs[0] != screens.DownloadsTab {
		t.Fatalf("A offline showed tabs %v", nav.tabs)
	}
	src.playlists, src.have = lists, true
	src.version++
	render(t, tab)
	if tab.offline {
		t.Fatal("offline with playlists kept, the tab doesn't show them")
	}
}

// The tab asks the library again only once it has changed, or a second on.
func TestTabAsksWhenTheLibraryChanges(t *testing.T) {
	src := signedIn()
	tab := NewTab(&Env{Nav: &fakeNav{}, Src: src, Player: &fakePlayer{}})
	render(t, tab)
	n := src.asked
	if render(t, tab); src.asked != n {
		t.Fatalf("asked %d times more with nothing changed", src.asked-n)
	}
	src.playlists = lists[:1]
	src.version++
	if render(t, tab); src.asked != n+1 || len(tab.rows) != 2 {
		t.Fatalf("changed, asked %d times more, %d rows", src.asked-n, len(tab.rows))
	}
	tab.refresh(t0.Add(time.Second))
	if src.asked != n+2 {
		t.Fatal("not asked again a second on")
	}
}

func TestPlaylistPagePlaysShufflesAndQueues(t *testing.T) {
	src, p, nav := signedIn(), &fakePlayer{}, &fakeNav{}
	pg := NewPlaylistPage(&Env{Nav: nav, Src: src, Player: p}, lists[1])
	p.state.Track = Tracks(road)[1]
	uitest.Golden(t, "spotify-playlist", render(t, pg))

	pg.Handle(press(input.A), t0) // Play
	pg.Handle(press(input.Right), t0)
	pg.Handle(press(input.A), t0) // Shuffle
	pg.Handle(press(input.Right), t0)
	pg.Handle(press(input.Down), t0)
	pg.Handle(press(input.A), t0) // the second song
	pg.Handle(press(input.Y), t0)
	if len(p.played) != 3 || p.played[0] != "4Q0qVhFQa7j6jRKzo3HDmP@0" || p.played[1][0] != '~' ||
		p.played[2] != "2IRZnDFmlqMuOrYOLnZZyc@1" {
		t.Fatalf("played %v", p.played)
	}
	if !reflect.DeepEqual(p.queued, [][]string{{"2IRZnDFmlqMuOrYOLnZZyc"}}) || len(nav.toasts) != 1 {
		t.Fatalf("queued %v, toasts %v", p.queued, nav.toasts)
	}
}

// The sign-in shows the code and its link as a QR code, and closes once the
// code is approved.
func TestSignInClosesWhenApproved(t *testing.T) {
	src := &fakeSource{status: spotify.Status{State: spotify.SigningIn,
		Login: &spotify.Login{URL: "https://www.spotify.com/pair?code=QZ7KM4", Code: "QZ7KM4", Expires: t0.Add(4*time.Minute + 32*time.Second)}}}
	nav := &fakeNav{}
	s := NewSignIn(&Env{Nav: nav, Src: src})
	nav.pushed = append(nav.pushed, s)
	uitest.Golden(t, "spotify-sign-in", render(t, s))
	s.Handle(press(input.X), t0)
	if src.logins != 2 {
		t.Fatalf("%d logins after X", src.logins)
	}
	src.status = ready
	render(t, s)
	if len(nav.pushed) != 0 || !slices.Equal(nav.toasts, []string{"Signed in as Ujjwal"}) {
		t.Fatalf("pages %d, toasts %v", len(nav.pushed), nav.toasts)
	}
}

// A sign-in that failed before a code came says why, and X tries again.
func TestSignInSaysWhyItFailed(t *testing.T) {
	src := &fakeSource{status: spotify.Status{State: spotify.SignedOut}, loginErr: errors.New("x509: certificate signed by unknown authority")}
	s := NewSignIn(&Env{Nav: &fakeNav{}, Src: src})
	failed := render(t, s)
	waiting := render(t, NewSignIn(&Env{Nav: &fakeNav{}, Src: &fakeSource{status: src.status}}))
	if bytes.Equal(failed.Pix, waiting.Pix) {
		t.Fatal("a sign-in that failed looks like one waiting for its code")
	}
	s.Handle(press(input.X), t0)
	if src.logins != 2 {
		t.Fatalf("%d logins after X", src.logins)
	}
	uitest.Golden(t, "spotify-sign-in-failed", failed)
}

func TestQRCodeImage(t *testing.T) {
	img := qrImage("https://www.spotify.com/pair?code=QZ7KM4", 275)
	if img == nil || img.Bounds().Dx() != 275 {
		t.Fatalf("image %v", img)
	}
	dark, light := 0, 0
	for i := 3; i < len(img.Pix); i += 4 {
		if img.Pix[i] == 0 {
			light++
		} else {
			dark++
		}
	}
	if dark == 0 || light == 0 {
		t.Fatalf("%d dark and %d clear pixels", dark, light)
	}
}

func TestExpiredDialog(t *testing.T) {
	asked := 0
	e := NewExpired(func() { asked++ })
	e.Open(t0)
	u, off := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	u.Accent = theme.Derive(35, 0.12)
	u.BeginFrame(t0.Add(time.Second))
	e.Draw(u)
	uitest.Golden(t, "spotify-expired", off.Image())
	e.Handle(press(input.A), t0)
	if asked != 1 || e.IsOpen() {
		t.Fatalf("sign in asked %d times, open %v", asked, e.IsOpen())
	}
	e.Open(t0)
	e.Handle(press(input.Right), t0)
	e.Handle(press(input.A), t0)
	if asked != 1 || e.IsOpen() {
		t.Fatalf("Not now: sign in asked %d times, open %v", asked, e.IsOpen())
	}
}

// With importing, the list spans the full width and Y opens Choose; offline,
// Y does nothing.
func TestTabWithImport(t *testing.T) {
	nav := &fakeNav{}
	e, _, _ := importEnv(nav, signedIn())
	tab := NewTab(e)
	uitest.Golden(t, "spotify-library-import", render(t, tab))
	online := false
	e.Online = func() bool { return online }
	uitest.Golden(t, "spotify-library-offline", render(t, tab))
	tab.Handle(press(input.Y), t0)
	if len(nav.pushed) != 0 {
		t.Fatal("Y opened Choose offline")
	}
	online = true
	tab.Handle(press(input.Y), t0)
	if len(nav.pushed) != 1 {
		t.Fatal("Y opened nothing")
	}
	if _, ok := nav.pushed[0].(*Choose); !ok {
		t.Fatalf("Y opened %T", nav.pushed[0])
	}
}

// A playlist's songs with a file are marked; X imports it.
func TestPlaylistPageMarksAndImports(t *testing.T) {
	nav := &fakeNav{}
	e, _, _ := importEnv(nav, signedIn())
	pg := NewPlaylistPage(e, lists[1])
	uitest.Golden(t, "spotify-playlist-import", render(t, pg))
	e.Online = func() bool { return false }
	pg.Handle(press(input.X), t0)
	if len(nav.pushed) != 0 {
		t.Fatal("X imported offline")
	}
	e.Online = func() bool { return true }
	pg.Handle(press(input.X), t0)
	if len(nav.pushed) != 1 {
		t.Fatal("X opened nothing")
	}
	if r, ok := nav.pushed[0].(*Review); !ok || !r.first || r.pls[0].URI != lists[1].URI {
		t.Fatalf("X opened %+v", nav.pushed[0])
	}
}

// X on a song imports that song alone, as an import of its own, leaving the
// playlist's import as it was.
func TestPlaylistPageImportsTheSongFocused(t *testing.T) {
	nav := &fakeNav{}
	e, im, _ := importEnv(nav, signedIn())
	pg := NewPlaylistPage(e, lists[1])
	render(t, pg)
	pg.Handle(press(input.Right), t0)
	pg.Handle(press(input.Right), t0) // onto the songs
	pg.Handle(press(input.Down), t0)  // Going Bad
	u, _ := uitest.NewContext(t, theme.DeviceW, theme.DeviceH)
	h := pg.songHints()
	if !slices.Contains(h, widgets.Hint{Key: "X", Label: "Import song"}) {
		t.Fatalf("on a song, the hints are %v", h)
	}
	if w := widgets.HintsWidth(u, h); w > widgets.MaxHintsWidth {
		t.Fatalf("hints %v are %.1f wide, more than %.1f", h, w, widgets.MaxHintsWidth)
	}
	pg.Handle(press(input.X), t0)
	if len(nav.pushed) != 1 {
		t.Fatalf("X opened %d pages", len(nav.pushed))
	}
	r, ok := nav.pushed[0].(*Review)
	if !ok || !r.first {
		t.Fatalf("X opened %+v", nav.pushed[0])
	}
	render(t, r)
	uri := "spotify:track:" + road[1].ID
	if !slices.Equal(im.imported, []string{uri}) {
		t.Fatalf("imported %v", im.imported)
	}
	if j := im.jobs[uri]; j.Name != road[1].Title || len(j.Items) != 1 || j.Items[0].Track.ID != road[1].ID {
		t.Fatalf("the song's import %+v", j)
	}
	if r.job.URI != uri || len(r.rows) != 1 {
		t.Fatalf("the review shows %q, %d rows", r.job.URI, len(r.rows))
	}
}

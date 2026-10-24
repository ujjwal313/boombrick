package audiobooks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/audible"
)

type fakeAPI struct {
	books    []audible.Book
	err      error
	licErr   error
	licenses int
	body     string

	licFails int     // the first licence requests that time out
	cuts     []int   // each Fetch gives this many bytes of the file, then is cut off, in turn
	noResume bool    // a Fetch from the middle says the server won't
	froms    []int64 // where each Fetch started
}

func (f *fakeAPI) Library(context.Context) ([]audible.Book, error) { return f.books, f.err }
func (f *fakeAPI) License(_ context.Context, asin string) (audible.License, error) {
	f.licenses++
	if f.licenses <= f.licFails {
		return audible.License{}, fmt.Errorf("Post: %w", context.DeadlineExceeded)
	}
	if f.licErr != nil {
		return audible.License{}, f.licErr
	}
	return audible.License{ASIN: asin, URL: "http://x/" + asin, ACR: "CR!" + asin, Key: "aa", IV: "bb"}, nil
}
func (f *fakeAPI) Fetch(_ context.Context, _ audible.License, w io.Writer, from int64, progress func(done, total int64)) error {
	f.froms = append(f.froms, from)
	file := strings.Repeat(f.body, 4)
	if from > 0 && f.noResume {
		return audible.ErrNoResume
	}
	rest := file[from:]
	if len(f.cuts) > 0 {
		n := min(f.cuts[0], len(rest))
		f.cuts = f.cuts[1:]
		io.WriteString(w, rest[:n])
		progress(from+int64(n), int64(len(file)))
		return io.ErrUnexpectedEOF
	}
	for i := 0; i < 4; i++ {
		part := rest[i*len(rest)/4 : (i+1)*len(rest)/4]
		io.WriteString(w, part)
		progress(from+int64((i+1)*len(rest)/4), int64(len(file)))
	}
	return nil
}

var maigret = audible.Book{ASIN: "B0FH5CCLRJ", Title: "La chiusa n.1: parte/due", Authors: "Georges Simenon, Altri", Length: 4 * time.Hour}

func newService(t *testing.T, api *fakeAPI, mod func(*Options)) (*Service, string, chan string) {
	t.Helper()
	dir := t.TempDir()
	filed := make(chan string, 4)
	o := Options{API: api, Dir: filepath.Join(dir, "Audiobooks"), Cache: filepath.Join(dir, "lib.json"),
		Filed: func(p string) { filed <- p },
		Open: func(_ context.Context, in, out, key, iv string) error {
			b, err := os.ReadFile(in)
			if err != nil {
				return err
			}
			if key != "aa" || iv != "bb" {
				return errors.New("wrong key")
			}
			return os.WriteFile(out, []byte("M4B:"+string(b)), 0o644)
		}}
	if mod != nil {
		mod(&o)
	}
	s := New(o)
	t.Cleanup(s.Close)
	return s, dir, filed
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWithoutAnAccountThereIsNothingToDo(t *testing.T) {
	s := New(Options{Dir: t.TempDir()})
	defer s.Close()
	if st, _ := s.Status(); st != NotSignedIn || s.SignedIn() || len(s.Entries()) != 0 {
		t.Fatal("a service with no API isn't empty")
	}
	s.Refresh()
	s.Download("B0FH5CCLRJ") // nothing happens, and nothing breaks
}

func TestRefreshListsAndKeepsTheLibrary(t *testing.T) {
	api := &fakeAPI{books: []audible.Book{maigret}}
	s, dir, _ := newService(t, api, nil)
	if st, _ := s.Status(); st != Loading {
		t.Fatalf("before the first fetch: %v", st)
	}
	s.Refresh()
	waitFor(t, "the list", func() bool { st, _ := s.Status(); return st == OK })
	if es := s.Entries(); len(es) != 1 || es[0].Title != maigret.Title || es[0].State != Idle || es[0].Path != "" {
		t.Fatalf("entries %+v", es)
	}
	// A new run shows the list kept, even without Wi-Fi.
	s2 := New(Options{API: &fakeAPI{err: errors.New("down")}, Dir: filepath.Join(dir, "Audiobooks"), Cache: filepath.Join(dir, "lib.json"),
		Online: func() bool { return false }})
	defer s2.Close()
	if st, _ := s2.Status(); st != OK || len(s2.Entries()) != 1 {
		t.Fatalf("kept list: %v, %d", st, len(s2.Entries()))
	}
	s2.Refresh()
	waitFor(t, "the offline refresh", func() bool { return true })
	if st, _ := s2.Status(); st != OK {
		t.Fatalf("going offline lost the list: %v", st)
	}
}

func TestOfflineWithNoListSaysSo(t *testing.T) {
	s := New(Options{API: &fakeAPI{}, Dir: t.TempDir(), Online: func() bool { return false }})
	defer s.Close()
	s.Refresh()
	waitFor(t, "offline", func() bool { st, _ := s.Status(); return st == Offline })
}

func TestABrokenFetchSaysWhy(t *testing.T) {
	s, _, _ := newService(t, &fakeAPI{err: errors.New("audible: GET /1.0/library: 401 Unauthorized")}, nil)
	s.Refresh()
	waitFor(t, "the failure", func() bool { st, msg := s.Status(); return st == Broken && strings.Contains(msg, "401") })
}

// download runs a download of the book on api and says how it ended: the
// path it was filed at, or the failure shown.
func download(t *testing.T, api *fakeAPI) (path, failure string) {
	t.Helper()
	api.books = []audible.Book{maigret}
	s, _, filed := newService(t, api, func(o *Options) { o.retry = time.Millisecond })
	s.Refresh()
	waitFor(t, "the list", func() bool { st, _ := s.Status(); return st == OK })
	s.Download(maigret.ASIN)
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		select {
		case path = <-filed:
			return path, ""
		default:
		}
		for _, e := range s.Entries() {
			if e.State == Failed {
				return "", e.Err
			}
		}
	}
	t.Fatal("the download neither finished nor failed")
	return
}

// A download that is cut off carries on from the bytes it has, with the
// licence it has, and the book comes out whole.
func TestDownloadResumesAfterACut(t *testing.T) {
	api := &fakeAPI{body: "0123456789", cuts: []int{15, 5}}
	path, failure := download(t, api)
	if failure != "" {
		t.Fatalf("failed: %s", failure)
	}
	b, _ := os.ReadFile(path)
	if want := "M4B:" + strings.Repeat("0123456789", 4); string(b) != want {
		t.Fatalf("the book is %q, want %q", b, want)
	}
	if api.licenses != 1 || !slices.Equal(api.froms, []int64{0, 15, 20}) {
		t.Fatalf("%d licences, fetches from %v; want one licence and fetches from 0, 15 and 20", api.licenses, api.froms)
	}
}

// A server that won't resume gets the file from the start again.
func TestDownloadStartsAgainWhereTheServerWontResume(t *testing.T) {
	api := &fakeAPI{body: "0123456789", cuts: []int{15}, noResume: true}
	path, failure := download(t, api)
	if failure != "" {
		t.Fatalf("failed: %s", failure)
	}
	b, _ := os.ReadFile(path)
	if want := "M4B:" + strings.Repeat("0123456789", 4); string(b) != want {
		t.Fatalf("the book is %q, want %q", b, want)
	}
	if !slices.Equal(api.froms, []int64{0, 15, 0}) {
		t.Fatalf("fetches from %v, want 0, 15, 0", api.froms)
	}
}

// After a few cuts in a row the failure is shown, with no more tries.
func TestDownloadGivesUpAfterSomeTries(t *testing.T) {
	api := &fakeAPI{body: "0123456789", cuts: []int{1, 1, 1, 1, 1, 1}}
	_, failure := download(t, api)
	if failure == "" || len(api.froms) != tries {
		t.Fatalf("failure %q after %d fetches, want one after %d", failure, len(api.froms), tries)
	}
}

// A licence that timed out is asked for once more; a refusal isn't asked
// again.
func TestLicenceTimeoutIsAskedAgainButARefusalIsNot(t *testing.T) {
	api := &fakeAPI{body: "0123456789", licFails: 1}
	if _, failure := download(t, api); failure != "" || api.licenses != 2 {
		t.Fatalf("a timed-out licence: failure %q, %d requests, want none and 2", failure, api.licenses)
	}
	api = &fakeAPI{body: "0123456789", licErr: &audible.Denied{Throttled: true}}
	if _, failure := download(t, api); failure == "" || api.licenses != 1 {
		t.Fatalf("a refused licence: failure %q, %d requests, want one and 1", failure, api.licenses)
	}
}

func TestTransient(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{io.ErrUnexpectedEOF, true},
		{audible.ErrStalled, true},
		{context.DeadlineExceeded, true},
		{&net.DNSError{Err: "i/o timeout", IsTimeout: true}, true},
		{errors.New("http2: timeout awaiting response headers"), true},
		{&audible.StatusError{Code: 503, Status: "503 Service Unavailable"}, true},
		{&audible.StatusError{Code: 403, Status: "403 Forbidden"}, false},
		{&audible.Denied{Throttled: true}, false},
		{context.Canceled, false},
		{errors.New("no space left on device"), false},
	} {
		if got := transient(c.err); got != c.want {
			t.Errorf("transient(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestDownloadFilesTheBookWithItsSidecar(t *testing.T) {
	api := &fakeAPI{books: []audible.Book{maigret}, body: "0123456789"}
	s, dir, filed := newService(t, api, nil)
	s.Refresh()
	waitFor(t, "the list", func() bool { st, _ := s.Status(); return st == OK })
	s.Download(maigret.ASIN)
	s.Download(maigret.ASIN) // asked twice: one download
	select {
	case path := <-filed:
		want := filepath.Join(dir, "Audiobooks", "Georges Simenon", "La chiusa n.1- parte-due [B0FH5CCLRJ].m4b")
		if path != want {
			t.Fatalf("filed at %q, want %q", path, want)
		}
		if b, _ := os.ReadFile(path); string(b) != "M4B:"+strings.Repeat("0123456789", 4) {
			t.Fatalf("the book holds %q", b)
		}
		if acr := audible.SidecarACR(path); acr != "CR!B0FH5CCLRJ" {
			t.Fatalf("sidecar acr %q", acr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("never filed")
	}
	if api.licenses != 1 {
		t.Fatalf("%d licences asked for, want 1", api.licenses)
	}
	waitFor(t, "the entry to show the file", func() bool {
		es := s.Entries()
		return len(es) == 1 && es[0].Path != "" && es[0].State == Idle
	})
	// Nothing is left but the book and its sidecar.
	files, _ := filepath.Glob(filepath.Join(dir, "Audiobooks", "*", ".*"))
	if len(files) != 0 {
		t.Fatalf("leftovers %v", files)
	}
	s.Download(maigret.ASIN) // already there
	time.Sleep(20 * time.Millisecond)
	if api.licenses != 1 {
		t.Fatal("downloaded a book already on the card")
	}
}

func TestADeniedDownloadFailsWithAReasonAndCanBeTriedAgain(t *testing.T) {
	api := &fakeAPI{books: []audible.Book{maigret}, body: "x", licErr: &audible.Denied{Reason: "Audible refused the download", Throttled: true}}
	s, _, filed := newService(t, api, nil)
	s.Refresh()
	waitFor(t, "the list", func() bool { st, _ := s.Status(); return st == OK })
	s.Download(maigret.ASIN)
	waitFor(t, "the failure", func() bool { es := s.Entries(); return es[0].State == Failed })
	if e := s.Entries()[0]; !strings.Contains(e.Err, "wait") {
		t.Fatalf("error %q", e.Err)
	}
	if len(filed) != 0 {
		t.Fatal("a denied book was filed")
	}
	api.licErr = nil
	s.Download(maigret.ASIN)
	select {
	case <-filed:
	case <-time.After(3 * time.Second):
		t.Fatal("never filed after the retry")
	}
}

func TestSafeNameAndPath(t *testing.T) {
	if got := SafeName(`A: b/c?d. `); got != "A- b-c-d" {
		t.Fatalf("%q", got)
	}
	if got := SafeName(`???`); got != "-" {
		t.Fatalf("%q", got)
	}
	if got := PathFor("/m/Audiobooks", audible.Book{ASIN: "B000000001", Title: "T"}); got != "/m/Audiobooks/Unknown/T [B000000001].m4b" {
		t.Fatalf("%q", got)
	}
	if audible.ASINOf(PathFor("/m", maigret)) != maigret.ASIN {
		t.Fatal("a filed book's ASIN can't be read back from its name")
	}
}

func TestExistingFilesAreRecognised(t *testing.T) {
	api := &fakeAPI{books: []audible.Book{maigret}}
	dir := t.TempDir()
	p := PathFor(filepath.Join(dir, "Audiobooks"), maigret)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("x"), 0o644)
	s := New(Options{API: api, Dir: filepath.Join(dir, "Audiobooks")})
	defer s.Close()
	s.Refresh()
	waitFor(t, "the list", func() bool { return len(s.Entries()) == 1 })
	if e := s.Entries()[0]; e.Path != p {
		t.Fatalf("path %q, want %q", e.Path, p)
	}
}

func TestFriendlyTellsPlanFromThrottle(t *testing.T) {
	plan := &audible.Denied{Reason: "x", Reasons: []string{"Customer is not part of any plans"}, Throttled: true}
	if got := friendly(plan); !strings.Contains(got, "no plan") {
		t.Errorf("a refusal for want of a plan, which also mentions throttling, says %q", got)
	}
	if got := friendly(&audible.Denied{Throttled: true}); !strings.Contains(got, "wait") {
		t.Errorf("a refusal for throttling alone says %q", got)
	}
	// A timeout or a missing name says what is wrong, not the address asked.
	post := func(err error) error {
		return &url.Error{Op: "Post", URL: "https://api.audible.it/1.0/content/B0FVG8SMPP/licenserequest", Err: err}
	}
	for _, c := range []struct {
		err  error
		want string
	}{
		{post(errors.New("http2: timeout awaiting response headers")), "didn't answer in time"},
		{post(context.DeadlineExceeded), "didn't answer in time"},
		{post(&net.DNSError{Err: "i/o timeout", Name: "api.audible.it", IsTimeout: true}), "check the Wi-Fi"},
		{post(errors.New("connection refused")), "couldn't reach Audible: connection refused"},
	} {
		if got := friendly(c.err); !strings.Contains(got, c.want) || strings.Contains(got, "licenserequest") || strings.Contains(got, "https") {
			t.Errorf("friendly(%v) = %q, want it to say %q and no address", c.err, got, c.want)
		}
	}
	if got := friendly(&audible.Denied{Reasons: []string{"Something else"}}); got != "Something else" {
		t.Errorf("another reason says %q", got)
	}
}

func TestSigningInGivesTheServiceItsLibrary(t *testing.T) {
	s := New(Options{Dir: t.TempDir()})
	defer s.Close()
	s.SetAPI(&fakeAPI{books: []audible.Book{maigret}})
	if !s.SignedIn() {
		t.Fatal("not signed in after SetAPI")
	}
	waitFor(t, "the list", func() bool { st, _ := s.Status(); return st == OK })
	if es := s.Entries(); len(es) != 1 || es[0].Title != maigret.Title {
		t.Fatalf("entries %+v", es)
	}
}

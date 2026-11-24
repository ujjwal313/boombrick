package fonts

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// release serves the test fonts gzipped, as the fonts release does, and counts
// the requests for each. A file named in wrong is served wrong that many times
// first.
type release struct {
	*httptest.Server
	mu    sync.Mutex
	files map[string][]byte
	wrong map[string]int
	hits  map[string]int
}

func (r *release) handle(w http.ResponseWriter, req *http.Request) {
	name := strings.TrimPrefix(req.URL.Path, "/")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hits[name]++
	b, ok := r.files[name]
	if !ok {
		http.NotFound(w, req)
		return
	}
	if r.wrong[name] > 0 {
		r.wrong[name]--
		b = bytes.Repeat([]byte{'x'}, len(b))
	}
	w.Write(b)
}

func (r *release) hit(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hits[name]
}

// testRelease serves the test fonts jp.ttf and arabic.ttf, and returns it with
// their catalog and the fonts themselves.
func testRelease(t *testing.T) (*release, []text.Download, map[string][]byte) {
	t.Helper()
	r := &release{files: map[string][]byte{}, wrong: map[string]int{}, hits: map[string]int{}}
	fonts := map[string][]byte{}
	var list []text.Download
	for _, n := range []string{"jp.ttf", "arabic.ttf"} {
		data, err := os.ReadFile(filepath.Join("..", "ui", "text", "testdata", "fonts", n))
		if err != nil {
			t.Skipf("%s is missing; run make fixtures", n)
		}
		var gz bytes.Buffer
		w := gzip.NewWriter(&gz)
		w.Write(data)
		w.Close()
		d, err := text.NewDownload(n, strings.TrimSuffix(n, ".ttf"), text.Regular, text.NoHan, data)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(gz.Bytes())
		d.Size, d.Sum = int64(gz.Len()), hex.EncodeToString(sum[:])
		list = append(list, d)
		fonts[n], r.files[n+".gz"] = data, gz.Bytes()
	}
	r.Server = httptest.NewServer(http.HandlerFunc(r.handle))
	t.Cleanup(r.Close)
	return r, list, fonts
}

// newStore is a store over dir fetching from r, online while online says so,
// with short waits.
func newStore(t *testing.T, r *release, list []text.Download, dir string, online *atomic.Bool) *Store {
	t.Helper()
	s := New(Options{Dir: dir, HTTP: r.Client(), Base: r.URL + "/", Fonts: list, Online: online.Load,
		retry: 20 * time.Millisecond, maxRetry: 80 * time.Millisecond, offline: 10 * time.Millisecond})
	t.Cleanup(s.Close)
	return s
}

func onLine() *atomic.Bool {
	var b atomic.Bool
	b.Store(true)
	return &b
}

// arrived waits for s to say a font has arrived.
func arrived(t *testing.T, s *Store) {
	t.Helper()
	select {
	case <-s.Ready():
	case <-time.After(5 * time.Second):
		t.Fatal("no font arrived")
	}
}

// leftovers lists the files in dir a fetch leaves while it runs.
func leftovers(dir string) []string {
	parts, _ := filepath.Glob(filepath.Join(dir, "*.part"))
	return parts
}

func TestFetchesWhatIsWanted(t *testing.T) {
	r, list, fonts := testRelease(t)
	dir := filepath.Join(t.TempDir(), "fonts") // made when the first font comes
	s := newStore(t, r, list, dir, onLine())
	if s.Has("jp.ttf") {
		t.Fatal("a font before it was fetched")
	}
	s.Want("jp.ttf")
	arrived(t, s)
	if !s.Has("jp.ttf") || s.Has("arabic.ttf") {
		t.Fatal("not the font asked for")
	}
	got, err := os.ReadFile(filepath.Join(dir, "jp.ttf"))
	if err != nil || !bytes.Equal(got, fonts["jp.ttf"]) {
		t.Fatalf("the font on the card isn't the one served: %v", err)
	}
	f, err := s.Open("jp.ttf")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if l := leftovers(dir); len(l) > 0 {
		t.Fatalf("left %v", l)
	}
	// A font that is there, or isn't in the catalog, isn't fetched.
	s.Want("jp.ttf")
	s.Want("missing.ttf")
	time.Sleep(50 * time.Millisecond)
	if r.hit("jp.ttf.gz") != 1 || r.hit("missing.ttf.gz") != 0 {
		t.Fatalf("fetched jp.ttf %d times, missing.ttf %d", r.hit("jp.ttf.gz"), r.hit("missing.ttf.gz"))
	}
	// A new store finds the font in the folder.
	if !newStore(t, r, list, dir, onLine()).Has("jp.ttf") {
		t.Fatal("a font in the folder wasn't found")
	}
}

// A download that doesn't match the catalog is thrown away and tried again
// later, the wait doubling.
func TestBadDownloadIsTriedAgain(t *testing.T) {
	r, list, _ := testRelease(t)
	r.wrong["arabic.ttf.gz"] = 2
	dir := t.TempDir()
	s := newStore(t, r, list, dir, onLine())
	start := time.Now()
	s.Want("arabic.ttf")
	arrived(t, s)
	if r.hit("arabic.ttf.gz") != 3 || !s.Has("arabic.ttf") {
		t.Fatalf("%d fetches", r.hit("arabic.ttf.gz"))
	}
	if took := time.Since(start); took < 60*time.Millisecond {
		t.Fatalf("tried three times in %v; the waits are 20 and 40 ms", took)
	}
	if l := leftovers(dir); len(l) > 0 {
		t.Fatalf("left %v", l)
	}
}

func TestWait(t *testing.T) {
	s := &Store{opts: Options{retry: time.Minute, maxRetry: time.Hour}}
	for fails, want := range map[int]time.Duration{1: time.Minute, 2: 2 * time.Minute, 3: 4 * time.Minute,
		7: time.Hour, 100: time.Hour} {
		if got := s.wait(fails); got != want {
			t.Errorf("after %d failures, wait %v; want %v", fails, got, want)
		}
	}
}

// Offline, nothing is fetched; the font comes once the network is back.
func TestWaitsWhileOffline(t *testing.T) {
	r, list, _ := testRelease(t)
	var online atomic.Bool
	s := newStore(t, r, list, t.TempDir(), &online)
	s.Want("jp.ttf")
	time.Sleep(50 * time.Millisecond)
	if r.hit("jp.ttf.gz") != 0 {
		t.Fatal("fetched while offline")
	}
	online.Store(true)
	arrived(t, s)
	if !s.Has("jp.ttf") {
		t.Fatal("not fetched once online")
	}
}

// A font that won't parse is removed and fetched again.
func TestBadFontIsFetchedAgain(t *testing.T) {
	r, list, fonts := testRelease(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "jp.ttf"), []byte("not a font"), 0o644)
	s := newStore(t, r, list, dir, onLine())
	if !s.Has("jp.ttf") {
		t.Fatal("the font in the folder wasn't found")
	}
	s.Bad("jp.ttf")
	arrived(t, s)
	if got, _ := os.ReadFile(filepath.Join(dir, "jp.ttf")); !bytes.Equal(got, fonts["jp.ttf"]) {
		t.Fatal("not fetched again")
	}
}

func TestSizeAndClear(t *testing.T) {
	r, list, fonts := testRelease(t)
	dir := t.TempDir()
	s := newStore(t, r, list, dir, onLine())
	if n, err := s.Size(); n != 0 || err != nil {
		t.Fatalf("size %d, %v before any font", n, err)
	}
	s.Want("jp.ttf")
	arrived(t, s)
	want := int64(len(fonts["jp.ttf"]))
	if n, _ := s.Size(); n != want {
		t.Fatalf("size %d, want %d", n, want)
	}
	if freed, err := s.Clear(); freed != want || err != nil || s.Has("jp.ttf") {
		t.Fatalf("freed %d, %v; still there %v", freed, err, s.Has("jp.ttf"))
	}
	if entries, _ := os.ReadDir(dir); len(entries) > 0 {
		t.Fatalf("%d files left", len(entries))
	}
	// Wanted again, it is fetched again.
	s.Want("jp.ttf")
	arrived(t, s)
	if r.hit("jp.ttf.gz") != 2 {
		t.Fatalf("%d fetches", r.hit("jp.ttf.gz"))
	}
	missing := New(Options{Dir: filepath.Join(dir, "none")})
	defer missing.Close()
	if n, err := missing.Size(); n != 0 || err != nil {
		t.Fatalf("a missing folder: %d, %v", n, err)
	}
}

// Each fonts release has a folder of its own, and the others' go: a font a new
// release recuts keeps its name, so the old file mustn't pass for it.
func TestPruneKeepsOnlyTheRelease(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"fonts-1/jp.ttf", "fonts-2/jp.ttf", "jp.ttf", "jp.ttf.part"} {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644)
	}
	if err := Prune(root, "fonts-2"); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 || entries[0].Name() != "fonts-2" {
		t.Fatalf("left %v, want fonts-2 alone", entries)
	}
	if _, err := os.Stat(filepath.Join(root, "fonts-2", "jp.ttf")); err != nil {
		t.Fatal("the release's own font went")
	}
	if err := Prune(filepath.Join(root, "none"), "fonts-2"); err != nil {
		t.Fatalf("no fonts folder yet: %v", err)
	}
}

// What a fetch cut short left in the folder goes when the store starts.
func TestLeftoversRemoved(t *testing.T) {
	r, list, _ := testRelease(t)
	dir := t.TempDir()
	for _, n := range []string{"jp.ttf.part", "jp.ttf.gz.part"} {
		os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644)
	}
	s := newStore(t, r, list, dir, onLine())
	if entries, _ := os.ReadDir(dir); len(entries) > 0 || s.Has("jp.ttf") {
		t.Fatalf("%d files left", len(entries))
	}
}

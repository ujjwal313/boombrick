//go:build live

package fonts

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ujjwal313/boombrick/internal/netx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// The fonts release serves every font in the catalog, each matching its
// checksum and drawing what the catalog says. Run once the release is up:
// go test -tags live ./internal/fonts/
func TestLiveRelease(t *testing.T) {
	dir := t.TempDir()
	s := New(Options{Dir: dir, HTTP: netx.NewClient(), Base: Release(text.FontsRelease), Fonts: text.Catalog,
		Online: func() bool { return true }, retry: time.Second, maxRetry: time.Second, offline: time.Second})
	defer s.Close()
	for _, d := range text.Catalog {
		s.Want(d.Name)
	}
	have := func() (n int) {
		for _, d := range text.Catalog {
			if s.Has(d.Name) {
				n++
			}
		}
		return n
	}
	for deadline := time.After(5 * time.Minute); have() < len(text.Catalog); {
		select {
		case <-s.Ready():
		case <-deadline:
			t.Fatalf("%d of %d fonts fetched", have(), len(text.Catalog))
		}
	}
	for _, d := range text.Catalog {
		data, err := os.ReadFile(filepath.Join(dir, d.Name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := text.NewDownload(d.Name, d.Group, d.Weight, d.Han, data)
		if err != nil || got.Covers != d.Covers {
			t.Errorf("%s doesn't draw what the catalog says: %v", d.Name, err)
		}
	}
}

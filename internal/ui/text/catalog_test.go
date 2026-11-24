package text

import (
	"slices"
	"testing"
)

// The catalog fontpack writes is one the engine can use: each font named once
// and drawing something, with its size and checksum; a regular font for every
// script; the regional forms only on the Chinese and Japanese fonts.
func TestCatalog(t *testing.T) {
	seen, regular := map[string]bool{}, map[string]bool{}
	for _, d := range Catalog {
		if d.Name == "" || d.Group == "" || seen[d.Name] {
			t.Fatalf("%q: no name or group, or named twice", d.Name)
		}
		seen[d.Name] = true
		if d.Weight == Regular {
			regular[d.Group] = true
		}
		if len(decodeCoverage(d.Covers)) == 0 || d.Size <= 0 || len(d.Sum) != 64 {
			t.Errorf("%s: coverage, size or checksum missing", d.Name)
		}
		if (d.Han != NoHan) != slices.Contains([]string{"sc", "jp", "tc"}, d.Group) {
			t.Errorf("%s: form %d", d.Name, d.Han)
		}
	}
	for _, d := range Catalog {
		if !regular[d.Group] {
			t.Errorf("%s has no regular font", d.Group)
		}
	}
	if len(Catalog) > 0 && FontsRelease == "" {
		t.Fatal("no release to fetch the fonts from")
	}
}

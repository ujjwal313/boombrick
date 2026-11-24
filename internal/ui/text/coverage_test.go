package text

import (
	"slices"
	"testing"
)

func TestCoverage(t *testing.T) {
	c := decodeCoverage(EncodeCoverage([]rune{'c', 'a', 'b', 'x', 'a', '東', 0x1f600}))
	want := Coverage{{'a', 'c'}, {'x', 'x'}, {'東', '東'}, {0x1f600, 0x1f600}}
	if !slices.Equal(c, want) {
		t.Fatalf("coverage %v, want %v", c, want)
	}
	for _, r := range []rune{'a', 'b', 'c', 'x', '東', 0x1f600} {
		if !c.Has(r) {
			t.Errorf("%q is missing", r)
		}
	}
	for _, r := range []rune{0, 'd', 'w', 'y', '京', 0x1f601} {
		if c.Has(r) {
			t.Errorf("%q is there", r)
		}
	}
	if decodeCoverage(EncodeCoverage(nil)) != nil {
		t.Fatal("nothing covers something")
	}
}

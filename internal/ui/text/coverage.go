package text

import (
	"encoding/binary"
	"slices"
	"sort"

	"github.com/go-text/typesetting/font"
)

// Coverage is the set of characters a font draws, as sorted ranges that
// neither overlap nor touch.
type Coverage []runeRange

type runeRange struct{ lo, hi rune }

// Has reports whether c holds r.
func (c Coverage) Has(r rune) bool {
	i := sort.Search(len(c), func(i int) bool { return c[i].hi >= r })
	return i < len(c) && c[i].lo <= r
}

// EncodeCoverage writes runes, in any order, as the catalog keeps a font's
// coverage: each range as two uvarints, its gap from the end of the range
// before and its length less one.
func EncodeCoverage(runes []rune) string {
	rs := slices.Clone(runes)
	slices.Sort(rs)
	rs = slices.Compact(rs)
	var b []byte
	end := rune(0) // one past the last range
	for i := 0; i < len(rs); {
		j := i + 1
		for j < len(rs) && rs[j] == rs[j-1]+1 {
			j++
		}
		b = binary.AppendUvarint(b, uint64(rs[i]-end))
		b = binary.AppendUvarint(b, uint64(j-i-1))
		end = rs[j-1] + 1
		i = j
	}
	return string(b)
}

// decodeCoverage reads what EncodeCoverage wrote. It stops at the first
// number it can't read.
func decodeCoverage(s string) Coverage {
	var c Coverage
	b := []byte(s)
	end := rune(0)
	for len(b) > 0 {
		gap, n := binary.Uvarint(b)
		if n <= 0 {
			break
		}
		length, m := binary.Uvarint(b[n:])
		if m <= 0 {
			break
		}
		b = b[n+m:]
		lo := end + rune(gap)
		c = append(c, runeRange{lo, lo + rune(length)})
		end = lo + rune(length) + 1
	}
	return c
}

// CoverageOf lists the characters face draws.
func CoverageOf(face *font.Face) []rune {
	var rs []rune
	for it := face.Cmap.Iter(); it.Next(); {
		r, _ := it.Char()
		rs = append(rs, r)
	}
	return rs
}

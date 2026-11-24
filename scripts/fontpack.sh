#!/usr/bin/env bash
# Makes the fonts Boombrick downloads the first time text needs them, cut with
# fonttools, which scripts/setup-wsl.sh installs, from google/fonts at the
# commit scripts/fonts.sh pins: the regular weight of the Chinese, Japanese
# and Korean fonts, cut to their national character sets; the regular and
# semibold weights of the other scripts' fonts, whole; and the one-colour
# emoji. Each goes to dist/fonts gzipped, with its licence, and
# internal/ui/text/catalog.go is written to match. dist/fonts is then
# published as the GitHub release $tag (docs/releasing.md); a new set of fonts
# takes a new tag.
set -euo pipefail

tag=fonts-1
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$root/dist/fonts"
venv="$HOME/.venvs/fonttools/bin"
gf=https://raw.githubusercontent.com/google/fonts/47d54ca1ebb14fd864695f1d39dc734f39017c33/ofl
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
rm -rf "$out"
mkdir -p "$out"

# The national character sets, each with ASCII, CJK punctuation and the
# full-width forms: JIS X 0208 with the characters Windows adds to it (髙, 﨑)
# and all of kana; GB 2312; Big5's symbols and frequently used characters;
# and every Hangul syllable and jamo, without hanja, which the other fonts
# draw.
"$venv/python" -I - "$tmp" <<'EOF'
import sys

def decoded(codec, leads, trails):
    out = set()
    for a in leads:
        for b in trails:
            try:
                out.add(bytes([a, b]).decode(codec))
            except UnicodeDecodeError:
                pass
    return out

def span(lo, hi):
    return {chr(c) for c in range(lo, hi + 1)}

common = span(0x20, 0x7E) | span(0x3000, 0x303F) | span(0xFF00, 0xFFEF)
big5 = decoded("big5", range(0xA1, 0xC7), list(range(0x40, 0x7F)) + list(range(0xA1, 0xFF)))
sets = {
    "jp": decoded("cp932", list(range(0x81, 0xA0)) + list(range(0xE0, 0xFD)), range(0x40, 0xFD))
          | span(0x3040, 0x30FF) | span(0x31F0, 0x31FF),
    "sc": decoded("gb2312", range(0xA1, 0xF8), range(0xA1, 0xFF)),
    "tc": {c for c in big5 if c.encode("big5") < b"\xc6\x7f"},
    "kr": span(0xAC00, 0xD7A3) | span(0x1100, 0x11FF) | span(0x3130, 0x318F),
}
for name, chars in sets.items():
    with open(f"{sys.argv[1]}/{name}.txt", "w", encoding="utf-8") as f:
        f.write("".join(sorted(chars | common)))
EOF

# cut family file weight axes out [subset options]: one weight of a font, cut
# down when subset options follow.
cut() {
	if [ ! -f "$tmp/$1.ttf" ]; then
		curl -fsSL -o "$tmp/$1.ttf" "$gf/$1/$2"
		curl -fsSL -o "$out/OFL-$1.txt" "$gf/$1/OFL.txt"
	fi
	"$venv/fonttools" varLib.instancer --no-recalc-timestamp "$tmp/$1.ttf" "wght=$3" $4 -o "$tmp/$5"
	if [ $# -gt 5 ]; then
		"$venv/fonttools" subset "$tmp/$5" "${@:6}" --layout-features='*' --no-recalc-timestamp --output-file="$out/$5"
	else
		cp "$tmp/$5" "$out/$5"
	fi
}
for c in SC JP TC KR; do
	lc="$(echo "$c" | tr '[:upper:]' '[:lower:]')"
	cut "notosans$lc" "NotoSans$c%5Bwght%5D.ttf" 400 '' "NotoSans$c-Regular.ttf" --text-file="$tmp/$lc.txt"
done
for s in Arabic Hebrew Thai Devanagari Bengali Gurmukhi Gujarati Tamil Telugu Kannada Malayalam; do
	f="notosans$(echo "$s" | tr '[:upper:]' '[:lower:]')"
	cut "$f" "NotoSans$s%5Bwdth,wght%5D.ttf" 400 wdth=100 "NotoSans$s-Regular.ttf"
	cut "$f" "NotoSans$s%5Bwdth,wght%5D.ttf" 600 wdth=100 "NotoSans$s-SemiBold.ttf"
done
# The emoji font keeps all it draws but its space, which is five times as wide
# as a text's, so that a space beside an emoji is the text's.
curl -fsSL -o "$tmp/notoemoji.ttf" "$gf/notoemoji/NotoEmoji%5Bwght%5D.ttf"
curl -fsSL -o "$out/OFL-notoemoji.txt" "$gf/notoemoji/OFL.txt"
"$venv/fonttools" varLib.instancer --no-recalc-timestamp "$tmp/notoemoji.ttf" wght=400 -o "$tmp/emoji.ttf"
"$venv/python" -I -c 'import sys
from fontTools.ttLib import TTFont
print("\n".join("U+%04X" % c for c in sorted(TTFont(sys.argv[1]).getBestCmap()) if c != 0x20))' "$tmp/emoji.ttf" > "$tmp/emoji.txt"
"$venv/fonttools" subset "$tmp/emoji.ttf" --unicodes-file="$tmp/emoji.txt" --layout-features='*' \
	--no-recalc-timestamp --output-file="$out/NotoEmoji-Regular.ttf"

# The files Boombrick fetches: each font gzipped, the same bytes every run.
for f in "$out"/*.ttf; do
	gzip -9nkf "$f"
done
(cd "$root" && go run ./scripts/fontcatalog -tag "$tag" "$out") > "$tmp/catalog.go"
mv "$tmp/catalog.go" "$root/internal/ui/text/catalog.go"
ls -l "$out"

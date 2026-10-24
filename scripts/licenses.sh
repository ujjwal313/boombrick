#!/usr/bin/env bash
# Prints the licences of what the programs given are built from: Go's own,
# every Go module linked into them, as `go version -m` lists them, and the C
# code, libraries and icons in third_party/licenses. The app ships it as
# THIRD-PARTY.txt, next to NOTICE. A module without a licence file stops it,
# so a new dependency isn't shipped unlisted.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

title() {
	printf '\n\n%s\n%s\n\n' "$1" "$(printf '%*s' "${#1}" '' | tr ' ' '=')"
}

echo "Third-party licences for Boombrick and its Spotify helper. NOTICE lists"
echo "the components; this file holds their licences in full."

title "Go $(go version "$1" | awk '{ print $NF }' | sed 's/^go//'), its standard library and runtime"
# Some distributions' Go keeps its licence apart from GOROOT.
cat "$(go env GOROOT)/LICENSE" 2>/dev/null || cat /usr/share/licenses/golang/LICENSE

# Each dep line, or the module replacing it. Modules replaced by a folder in
# this repository are Boombrick's own, or a modified copy whose licence is in
# third_party/licenses.
go version -m "$@" | awk '
	$1 == "dep" { if (d != "") print d; d = $2 " " $3 }
	$1 == "=>"  { d = ($2 ~ /^[.\/]/) ? "" : $2 " " $3 }
	END         { if (d != "") print d }' | sort -u | while read -r path version; do
	dir="$(go mod download -json "$path@$version" | sed -n 's/^\t"Dir": "\(.*\)",$/\1/p')"
	mapfile -t files < <(find "$dir" -maxdepth 1 -type f \( -iname 'LICEN[CS]E*' -o -iname 'COPYING*' \
		-o -iname 'NOTICE*' -o -iname 'PATENTS*' -o -iname 'UNLICENSE*' \) | sort)
	if [ "${#files[@]}" -eq 0 ]; then
		echo "licenses.sh: $path $version has no licence file" >&2
		exit 1
	fi
	title "$path $version"
	for f in "${files[@]}"; do
		cat "$f"
		echo
	done
done

for f in "$root"/third_party/licenses/*.txt; do
	title "$(head -1 "$f")"
	tail -n +2 "$f"
done

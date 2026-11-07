#!/usr/bin/env bash
# Generates the short tones the decoder tests read. They are kept out of git;
# tests that need them skip until this has run.
set -euo pipefail

out="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/internal/audio/decode/testdata"
mkdir -p "$out"

tone() { ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=440:sample_rate=$1:duration=1" "${@:2}"; }
tone 44100 -ac 2 -c:a libmp3lame -b:a 192k "$out/tone-44100.mp3"
tone 44100 -ac 2 -c:a libmp3lame -q:a 4 "$out/vbr-44100.mp3"
tone 44100 -ac 2 -c:a libmp3lame -b:a 128k -write_xing 0 "$out/noxing-44100.mp3"
# Noise shows a misplaced seek. The long one carries an ID3v1 tag after the
# audio as well as the LAME tag in front.
noise() { ffmpeg -loglevel error -y -f lavfi -i "anoisesrc=color=pink:sample_rate=$1:amplitude=0.3:seed=7:duration=$2" "${@:3}"; }
noise 44100 180 -ac 2 -c:a libmp3lame -b:a 128k -write_id3v1 1 -metadata title=Noise "$out/noise-180s.mp3"
noise 22050 60 -ac 1 -c:a libmp3lame -b:a 64k "$out/noise-60s-22050.mp3"
# Encodings that differ in how far a frame reaches back into earlier ones.
noise 44100 60 -ac 2 -c:a libmp3lame -b:a 320k "$out/noise-320k.mp3"
noise 44100 60 -ac 2 -c:a libmp3lame -b:a 128k -reservoir 0 "$out/noise-reservoir0.mp3"
noise 48000 45 -ac 2 -c:a libmp3lame -b:a 256k "$out/noise-256k-48000.mp3"
noise 44100 45 -ac 2 -c:a libmp3lame -q:a 0 "$out/noise-v0.mp3"
noise 44100 45 -ac 2 -c:a libmp3lame -q:a 4 "$out/noise-v4.mp3"
noise 11025 45 -ac 2 -c:a libmp3lame -b:a 16k "$out/noise-11025.mp3"
noise 8000 45 -ac 1 -c:a libmp3lame -b:a 8k "$out/noise-8000.mp3"
# No LAME tag, so no delay or padding to trim, and no ID3 tag either.
noise 44100 45 -ac 2 -c:a libmp3lame -b:a 128k -write_xing 0 -id3v2_version 0 "$out/noise-untagged.mp3"
# Cut part-way through a frame, as a recording of a stream can start: the
# first whole frames reach back into audio that isn't there.
tail -c +200001 "$out/noise-v4.mp3" > "$out/noise-cut.mp3"
# A stream cut at a frame and joined on to one encoded without a reservoir,
# so its first frame reaches back for data that isn't there part-way through.
noise 44100 20 -ac 2 -c:a libmp3lame -b:a 128k -reservoir 0 -write_xing 0 -id3v2_version 0 "$out/joined-a.mp3"
ffmpeg -loglevel error -y -ss 20 -i "$out/noise-untagged.mp3" -c:a copy -write_xing 0 -id3v2_version 0 "$out/joined-b.mp3"
cat "$out/joined-a.mp3" "$out/joined-b.mp3" > "$out/noise-joined.mp3"
rm "$out/joined-a.mp3" "$out/joined-b.mp3"
tone 48000 -ac 2 -c:a flac "$out/tone-48000.flac"
tone 44100 -ac 1 -c:a flac "$out/mono-44100.flac"

# Channels that differ, so a swapped or mis-strided conversion shows up.
sines() {
	local n=$# i inputs=()
	for i in "$@"; do inputs+=(-f lavfi -i "sine=frequency=$i:sample_rate=44100:duration=1"); done
	ffmpeg -loglevel error -y "${inputs[@]}" -filter_complex "amerge=inputs=$n" -c:a flac "$out/$OUT"
}
OUT=stereo-440-880.flac sines 440 880
OUT=three-440-880-220.flac sines 440 880 220
tone 48000 -ac 2 -c:a libopus -b:a 96k "$out/tone-48000.opus"
ffmpeg -loglevel error -y -f lavfi -i anullsrc=r=44100:cl=mono -f lavfi -i anullsrc=r=44100:cl=mono \
  -f lavfi -i "sine=frequency=440:sample_rate=44100:duration=1" \
  -filter_complex "[0][1][2]amerge=inputs=3[a]" -map "[a]" -t 1 -c:a flac "$out/centre-only.flac"
tone 44100 -ac 2 -c:a aac -b:a 128k "$out/tone-44100.m4a"
tone 22050 -ac 1 -c:a aac -b:a 64k "$out/mono-22050.m4a"
# Noise fills the high band that SBR rebuilds, so a seek that decodes it
# wrongly shows where a pure tone hides it. Noise substitution is off, as for
# odd-44100.m4a below.
noise 22050 1 -ac 1 -c:a aac -aac_pns 0 -b:a 64k "$out/noise-22050.m4a"
# ffmpeg's config says "no SBR"; flag SBR at 44.1 kHz instead, so faad2
# decodes these 22.05 kHz streams at double rate as it does HE-AAC.
python3 - "$out/mono-22050.m4a" "$out/noise-22050.m4a" <<'PY'
import sys
for p in sys.argv[1:]:
    d = open(p, "rb").read()
    old = bytes.fromhex("138856e500")
    assert d.count(old) == 1
    open(p, "wb").write(d.replace(old, bytes.fromhex("138856e5a0")))
PY
tone 44100 -ac 2 -c:a alac "$out/alac.m4a"
# A length that isn't a whole number of milliseconds, and a file with its
# index up front so a cut in the audio leaves it readable.
# Noise substitution is off: faad2 makes its noise from what it decoded
# before, so seeks into such frames can't match a straight decode.
tone 44100 -af atrim=end_sample=44022 -ac 2 -c:a aac -aac_pns 0 -b:a 128k "$out/odd-44100.m4a"
tone 44100 -ac 2 -c:a aac -b:a 128k -movflags +faststart "$out/faststart-44100.m4a"

lib="$(dirname "$out")/../../library/testdata"
mkdir -p "$lib"
meta=(-metadata title=Rust -metadata artist=Odalys -metadata album_artist="Various Artists"
  -metadata album="Copper Season" -metadata track=3/10 -metadata disc=2/2 -metadata date=2019
  -metadata REPLAYGAIN_TRACK_GAIN="-6.50 dB" -metadata REPLAYGAIN_TRACK_PEAK=0.900000
  -metadata REPLAYGAIN_ALBUM_GAIN="-7.25 dB" -metadata REPLAYGAIN_ALBUM_PEAK=0.950000)
tone 44100 -ac 2 -c:a libmp3lame -b:a 128k -id3v2_version 3 "${meta[@]}" "$lib/tagged.mp3"
tone 44100 -ac 2 -c:a flac "${meta[@]}" "$lib/tagged.flac"
tone 48000 -ac 2 -c:a libopus -b:a 96k "${meta[@]}" "$lib/tagged.opus"
tone 44100 -ac 2 -c:a aac -b:a 128k "${meta[@]}" "$lib/tagged.m4a"
ls -l "$lib"

# Short tones carrying a 64 x 64 red picture, one per container.
art="$(dirname "$out")/../../art/testdata"
mkdir -p "$art"
ffmpeg -loglevel error -y -f lavfi -i "color=c=red:s=64x64" -frames:v 1 "$art/cover.jpg"
withart() {
	ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=440:sample_rate=44100:duration=1" -i "$art/cover.jpg" \
		-map 0:a -map 1:v -c:v copy -disposition:v attached_pic "$@"
}
withart -c:a libmp3lame -b:a 128k -id3v2_version 3 "$art/art.mp3"
withart -c:a flac "$art/art.flac"
withart -c:a aac -b:a 128k "$art/art.m4a"
# Ogg carries pictures as a base64 FLAC picture block in its comments.
pic="$(python3 - "$art/cover.jpg" <<'PY'
import base64, struct, sys
d = open(sys.argv[1], "rb").read()
mime = b"image/jpeg"
block = struct.pack(">II", 3, len(mime)) + mime + struct.pack(">IIIIII", 0, 64, 64, 24, 0, len(d)) + d
print(base64.b64encode(block).decode())
PY
)"
ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=1" -c:a libopus -b:a 96k \
	-metadata METADATA_BLOCK_PICTURE="$pic" "$art/art.opus"
ls -l "$art"

# Tones carrying two lines of LRC in their tags. ID3's USLT is built by the
# test itself.
words="$(dirname "$out")/../../lyrics/testdata"
mkdir -p "$words"
lrc=$'[00:00.20]First line\n[00:00.60]Second line'
tone 44100 -ac 2 -c:a flac -metadata lyrics="$lrc" "$words/lyrics.flac"
tone 48000 -ac 2 -c:a libopus -b:a 96k -metadata lyrics="$lrc" "$words/lyrics.opus"
tone 44100 -ac 2 -c:a aac -b:a 128k -metadata lyrics="$lrc" "$words/lyrics.m4a"
ls -l "$words"

# Opus in WebM, as YouTube serves it, for the remux tests: with ffmpeg's own
# remux of it to Ogg and ffmpeg's decoding of it to compare against.
mux="$(dirname "$out")/../../remux/testdata"
mkdir -p "$mux"
ffmpeg -loglevel error -y -f lavfi -i "sine=frequency=440:sample_rate=48000:duration=3" -ac 2 -c:a libopus -b:a 128k \
	"$mux/tone.webm"
ffmpeg -loglevel error -y -i "$mux/tone.webm" -c:a copy "$mux/tone-ffmpeg.opus"
ffmpeg -loglevel error -y -i "$mux/tone.webm" -f f32le "$mux/tone.f32"
ls -l "$mux"

# Ogg Vorbis at 44.1 kHz, as Spotify serves it, for the Spotify helper's
# decoder, which the test puts a page of Spotify's own in front of: with
# ffmpeg's decoding of it to compare against. Noise shows a misplaced seek.
spot="$(dirname "$out")/../../spotify/helper/testdata"
mkdir -p "$spot"
noise 44100 30 -ac 2 -c:a libvorbis -b:a 160k "$spot/noise-160k.ogg"
ffmpeg -loglevel error -y -i "$spot/noise-160k.ogg" -f f32le "$spot/noise-160k.f32"
ls -l "$spot"

ls -l "$out"

# Fonts for the text engine's tests: a few characters each, cut with
# fonttools, as scripts/fonts.sh cuts the bundled ones, from the Noto fonts the
# downloads are made from.
fonts="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/internal/ui/text/testdata/fonts"
ft="$HOME/.venvs/fonttools/bin/fonttools"
gf=https://raw.githubusercontent.com/google/fonts/47d54ca1ebb14fd864695f1d39dc734f39017c33/ofl
src="${FONT_SOURCES:-$(mktemp -d)}"
mkdir -p "$fonts"
# Each keeps ASCII, as the downloads do, but the emoji font, which is cut
# without its space, keeping only the digits, # and * its keycaps are made of.
font() { # family file weight axes out text [unicodes]
	[ -f "$src/$1.ttf" ] || curl -fsSL -o "$src/$1.ttf" "$gf/$1/$2"
	"$ft" varLib.instancer --no-recalc-timestamp "$src/$1.ttf" "wght=$3" $4 -o "$src/$1-$3.ttf"
	"$ft" subset "$src/$1-$3.ttf" --text="$6" --unicodes="${7:-U+0020-007E}" --layout-features='*' \
		--no-recalc-timestamp --output-file="$fonts/$5"
}
font notosansjp 'NotoSansJP%5Bwght%5D.ttf' 400 '' jp.ttf 'あいうアイウのしいアルバム東京事変音楽大地新、。'
font notosanssc 'NotoSansSC%5Bwght%5D.ttf' 400 '' sc.ttf '东京音乐大地新的专辑'
font notosanstc 'NotoSansTC%5Bwght%5D.ttf' 400 '' tc.ttf '東京音樂大地新的專輯'
font notosanskr 'NotoSansKR%5Bwght%5D.ttf' 400 '' kr.ttf '한국어노래'
font notosansarabic 'NotoSansArabic%5Bwdth,wght%5D.ttf' 400 wdth=100 arabic.ttf 'مرحبا بالعالم'
font notosansarabic 'NotoSansArabic%5Bwdth,wght%5D.ttf' 600 wdth=100 arabic-semibold.ttf 'مرحبا بالعالم'
font notosansdevanagari 'NotoSansDevanagari%5Bwdth,wght%5D.ttf' 400 wdth=100 devanagari.ttf 'हिन्दी गाना'
font notoemoji 'NotoEmoji%5Bwght%5D.ttf' 400 '' emoji.ttf '👨‍👩‍👧❤️👍🏽🇯🇵' 'U+0023,U+002A,U+0030-0039'
[ -n "${FONT_SOURCES:-}" ] || rm -rf "$src"

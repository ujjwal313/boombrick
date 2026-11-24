package library

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"unicode/utf16"

	"github.com/dhowden/tag"
)

func taggedFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is missing; run make fixtures", path)
	}
	return path
}

func TestReadTags(t *testing.T) {
	for _, c := range []struct {
		name string
		gain bool // ffmpeg writes ReplayGain into MP3, FLAC and Opus tags, not MP4's
	}{{"tagged.mp3", true}, {"tagged.flac", true}, {"tagged.opus", true}, {"tagged.m4a", false}} {
		t.Run(c.name, func(t *testing.T) {
			tags, err := ReadTags(taggedFixture(t, c.name))
			if err != nil {
				t.Fatal(err)
			}
			want := Tags{Title: "Rust", Artist: "Odalys", AlbumArtist: "Various Artists", Album: "Copper Season",
				Disc: 2, Track: 3, Year: 2019}
			if c.gain {
				want.Gain, want.Peak, want.HasGain = -6.5, 0.9, true
				want.AlbumGain, want.AlbumPeak, want.HasAlbumGain = -7.25, 0.95, true
			}
			if math.Abs(tags.Gain-want.Gain) > 0.01 || math.Abs(tags.Peak-want.Peak) > 0.01 {
				t.Errorf("gain %v peak %v, want %v %v", tags.Gain, tags.Peak, want.Gain, want.Peak)
			}
			tags.Gain, tags.Peak = want.Gain, want.Peak
			if math.Abs(tags.AlbumGain-want.AlbumGain) > 0.01 || math.Abs(tags.AlbumPeak-want.AlbumPeak) > 0.01 {
				t.Errorf("album gain %v peak %v, want %v %v", tags.AlbumGain, tags.AlbumPeak, want.AlbumGain, want.AlbumPeak)
			}
			tags.AlbumGain, tags.AlbumPeak = want.AlbumGain, want.AlbumPeak
			if !reflect.DeepEqual(tags, want) {
				t.Fatalf("ReadTags = %+v\nwant        %+v", tags, want)
			}
		})
	}
}

// flacWith is a FLAC file of an empty stream info block and a Vorbis comment
// block holding comments, each "KEY=value".
func flacWith(comments ...string) []byte {
	le := func(n int) []byte { return binary.LittleEndian.AppendUint32(nil, uint32(n)) }
	vc := append(le(0), le(len(comments))...)
	for _, c := range comments {
		vc = append(append(vc, le(len(c))...), c...)
	}
	b := append([]byte("fLaC\x00\x00\x00\x22"), make([]byte, 34)...)
	b = append(b, 0x84, byte(len(vc)>>16), byte(len(vc)>>8), byte(len(vc)))
	return append(b, vc...)
}

// id3With is an ID3v2.3 or 2.4 tag holding one TCON frame of body, in text
// encoding enc.
func id3With(version, enc byte, body []byte) []byte {
	size := func(n int, syncsafe bool) []byte {
		if syncsafe {
			return []byte{byte(n >> 21 & 0x7f), byte(n >> 14 & 0x7f), byte(n >> 7 & 0x7f), byte(n & 0x7f)}
		}
		return binary.BigEndian.AppendUint32(nil, uint32(n))
	}
	data := append([]byte{enc}, body...)
	frame := append(append([]byte("TCON"), size(len(data), version == 4)...), 0, 0)
	frame = append(frame, data...)
	return append(append([]byte{'I', 'D', '3', version, 0, 0}, size(len(frame), true)...), frame...)
}

// mp4With is an M4A's file type and an ilst holding one atom, name, of data in
// class class: 1 is text, 0 is read by the atom's name.
func mp4With(name string, class byte, data []byte) []byte {
	atom := func(name string, body ...[]byte) []byte {
		b := bytes.Join(body, nil)
		return append(append(binary.BigEndian.AppendUint32(nil, uint32(8+len(b))), name...), b...)
	}
	ilst := atom("ilst", atom(name, atom("data", []byte{0, 0, 0, class, 0, 0, 0, 0}, data)))
	moov := atom("moov", atom("udta", atom("meta", []byte{0, 0, 0, 0}, ilst)))
	return append(atom("ftyp", []byte("M4A \x00\x00\x00\x00M4A ")), moov...)
}

// utf16List is ss in UTF-16 as an ID3 frame of encoding 1 lists them: each
// with a little-endian byte order mark, separated by a two-byte NUL.
func utf16List(ss ...string) []byte {
	var b []byte
	for i, s := range ss {
		if i > 0 {
			b = append(b, 0, 0)
		}
		b = append(b, 0xff, 0xfe)
		for _, u := range utf16.Encode([]rune(s)) {
			b = binary.LittleEndian.AppendUint16(b, u)
		}
	}
	return b
}

// A song's genres are every GENRE comment, every value of an ID3v2.4 list,
// ID3's and MP4's numbered genres by name and an ID3v2.3 genre's own text,
// each split at ";", trimmed, and kept once whatever its case. "/" doesn't
// split, as in iTunes' "Hip-Hop/Rap".
func TestReadTagsGenres(t *testing.T) {
	for _, c := range []struct {
		name string
		file []byte
		want []string
	}{
		{"two GENRE comments", flacWith("TITLE=Rust", "GENRE=Rock", "GENRE=Pop"), []string{"Rock", "Pop"}},
		{"a list in one comment", flacWith("GENRE=Rock; Pop;rock;"), []string{"Rock", "Pop"}},
		{"a genre with a slash", flacWith("genre=Hip-Hop/Rap"), []string{"Hip-Hop/Rap"}},
		{"no genre", flacWith("TITLE=Rust"), nil},
		{"an ID3v2.4 list", id3With(4, 3, []byte("Rock\x00Pop\x00")), []string{"Rock", "Pop"}},
		{"an ID3v2.4 list in UTF-16", id3With(4, 1, utf16List("Rock", "Jazz")), []string{"Rock", "Jazz"}},
		{"an ID3v2.4 numbered genre", id3With(4, 3, []byte("17\x00Jazz")), []string{"Rock", "Jazz"}},
		{"an ID3v2.3 numbered genre", id3With(3, 0, []byte("(17)")), []string{"Rock"}},
		{"an ID3v2.3 list", id3With(3, 0, []byte("Rock;Pop")), []string{"Rock", "Pop"}},
		{"ID3v2.3 numbered genres", id3With(3, 0, []byte("(17)(26)")), []string{"Rock", "Ambient"}},
		{"an ID3v2.3 numbered genre named again", id3With(3, 0, []byte("(17)Rock")), []string{"Rock"}},
		{"an ID3v2.3 numbered genre refined", id3With(3, 0, []byte("(4)Eurodisco")), []string{"Disco", "Eurodisco"}},
		{"an ID3v2.3 refinement in brackets", id3With(3, 0, []byte("(RX)((Live)")), []string{"Remix", "(Live)"}},
		{"ID3v2.4 remix and cover", id3With(4, 3, []byte("RX\x00CR\x00Electronic")), []string{"Remix", "Cover", "Electronic"}},
		{"a genre with a number in brackets", id3With(3, 0, []byte("Top 40 (80s)")), []string{"Top 40 (80s)"}},
		{"an MP4 genre", mp4With("\xa9gen", 1, []byte("Rock;Pop")), []string{"Rock", "Pop"}},
		{"an MP4 preset genre", mp4With("gnre", 0, []byte{0, 18}), []string{"Rock"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x")
			if err := os.WriteFile(path, c.file, 0o644); err != nil {
				t.Fatal(err)
			}
			tags, err := ReadTags(path)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(tags.Genres, c.want) {
				t.Fatalf("genres %q, want %q", tags.Genres, c.want)
			}
		})
	}
}

// Disc and track numbers the tag package can't read are read from the tags
// themselves: ID3's with a space before the number, and Vorbis's DISC.
func TestReadTagsDiscAndTrackFallbacks(t *testing.T) {
	for _, c := range []struct {
		name  string
		file  []byte
		disc  int
		track int
	}{
		{"ID3 numbers with spaces", []byte(id3(map[string]string{"TPOS": " 2", "TRCK": " 3"})), 2, 3},
		{"a Vorbis DISC", flacWith("DISC=2", "TRACKNUMBER=3"), 2, 3},
		{"a Vorbis DISC after an empty DISCNUMBER", flacWith("DISCNUMBER=", "DISC=2", "TRACKNUMBER=3"), 2, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "x")
			if err := os.WriteFile(path, c.file, 0o644); err != nil {
				t.Fatal(err)
			}
			tags, err := ReadTags(path)
			if err != nil {
				t.Fatal(err)
			}
			if tags.Disc != c.disc || tags.Track != c.track {
				t.Fatalf("disc %d track %d, want %d and %d", tags.Disc, tags.Track, c.disc, c.track)
			}
		})
	}
}

func TestReplayGainFromR128(t *testing.T) {
	// Opus R128 gains are Q7.8 dB against -23 LUFS, 5 dB below ReplayGain's
	// reference: -1280 is -5 dB, which is 0 dB in ReplayGain terms.
	gain, _, ok, _, _, _ := replayGains(map[string]interface{}{"r128_track_gain": "-1280"})
	if !ok || math.Abs(gain) > 1e-9 {
		t.Fatalf("R128 -1280 = %v dB (ok %v), want 0", gain, ok)
	}
}

func TestAlbumGainFromR128(t *testing.T) {
	_, _, _, gain, _, ok := replayGains(map[string]interface{}{"r128_album_gain": "-1792"})
	if !ok || math.Abs(gain-(-2)) > 1e-9 {
		t.Fatalf("R128 album -1792 = %v dB (ok %v), want -2", gain, ok)
	}
}

func TestRawText(t *testing.T) {
	raw := map[string]interface{}{
		"TXXX":                  &tag.Comm{Description: "REPLAYGAIN_TRACK_GAIN", Text: "-6.50 dB\x00"},
		"albumartist":           "  Odalys ",
		"album artist":          "Other",
		"replaygain_track_peak": "0.9",
	}
	if got := rawText(raw, "replaygain_track_gain"); got != "-6.50 dB" {
		t.Errorf("TXXX gain = %q", got)
	}
	if got := rawText(raw, "album_artist", "album artist", "albumartist"); got != "Other" {
		t.Errorf("first key present wins: got %q", got)
	}
	raw["album_artist"] = " \x00"
	if got := rawText(raw, "album_artist", "album artist"); got != "Other" {
		t.Errorf("an empty value counts as missing: got %q", got)
	}
	if got := rawText(raw, "albumartist"); got != "Odalys" {
		t.Errorf("plain value = %q, want it trimmed", got)
	}
	if got := rawText(raw, "missing"); got != "" {
		t.Errorf("missing = %q", got)
	}
}

func TestReplayGainValues(t *testing.T) {
	for _, c := range []struct {
		raw  map[string]interface{}
		gain float64
		ok   bool
	}{
		{map[string]interface{}{"replaygain_track_gain": "-6.50 db"}, -6.5, true},
		{map[string]interface{}{"replaygain_track_gain": "+3.2"}, 3.2, true},
		{map[string]interface{}{"replaygain_track_gain": "Inf"}, 0, false},
		{map[string]interface{}{"replaygain_track_gain": "NaN dB"}, 0, false},
		{map[string]interface{}{"replaygain_track_gain": "-400 dB"}, 0, false},
		{map[string]interface{}{"replaygain_track_gain": "loud", "r128_track_gain": "-1280"}, 0, true},
		{map[string]interface{}{"replaygain_track_gain": "-2 dB", "r128_track_gain": "-1280"}, -2, true},
	} {
		gain, _, ok, _, _, _ := replayGains(c.raw)
		if ok != c.ok || math.Abs(gain-c.gain) > 1e-9 {
			t.Errorf("%v = %v, %v; want %v, %v", c.raw, gain, ok, c.gain, c.ok)
		}
	}
	if _, peak, _, _, _, _ := replayGains(map[string]interface{}{"replaygain_track_peak": "NaN"}); peak != 0 {
		t.Errorf("NaN peak = %v", peak)
	}
}

// MP4 freeform atoms (----:com.apple.iTunes:name) arrive keyed by the name
// alone, the mean being dropped by the tag package.
func TestReplayGainMP4Key(t *testing.T) {
	gain, peak, ok, _, _, _ := replayGains(map[string]interface{}{
		"REPLAYGAIN_TRACK_GAIN": "-6.50 dB", "REPLAYGAIN_TRACK_PEAK": "0.9"})
	if !ok || gain != -6.5 || peak != 0.9 {
		t.Fatalf("got %v %v %v", gain, peak, ok)
	}
}

func TestYear(t *testing.T) {
	for _, tc := range []struct {
		y    int
		raw  map[string]interface{}
		want int
	}{
		{2019, map[string]interface{}{"date": "2019"}, 2019},
		{1, map[string]interface{}{"date": "20190514"}, 2019},
		{20190514, map[string]interface{}{"TDRC": "20190514"}, 2019},
		{0, map[string]interface{}{"tyer": "2007-05"}, 2007},
		{1, map[string]interface{}{"date": "May 2019"}, 0},
		{1, map[string]interface{}{"date": "0001-01-01"}, 0},
		{0, map[string]interface{}{}, 0},
	} {
		if got := year(tc.y, tc.raw); got != tc.want {
			t.Errorf("year(%d, %v) = %d, want %d", tc.y, tc.raw, got, tc.want)
		}
	}
}

func TestReadTagsSurvivesDamagedTags(t *testing.T) {
	cases := map[string][]byte{
		// An ID3 header and a frame claiming far more than the file holds.
		"id3": append([]byte("ID3\x03\x00\x00\x00\x00\x01\x00TIT2\x00\x00\x10\x00\x00\x00"), 0, 'a'),
		// An MP4 moov, udta, meta and ilst nested with sizes past the end.
		"mp4": []byte("\x00\x00\x00\x18ftypM4A \x00\x00\x00\x00M4A \x00\x00\x7f\x00moov\x00\x00\x7f\x00udta\x00\x00\x7f\x00meta\x00\x00\x00\x00\x00\x00\x7f\x00ilst\x00\x00\x7f\x00\xa9nam\x00\x00\x7f\x00data"),
	}
	for name, data := range cases {
		path := filepath.Join(t.TempDir(), "x.mp3")
		os.WriteFile(path, data, 0o644)
		ReadTags(path) // must not panic
		t.Log(name, "ok")
	}
}

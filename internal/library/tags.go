package library

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/dhowden/tag"
)

// Tags is the metadata the library takes from a file's own tags.
type Tags struct {
	Title, Artist, AlbumArtist, Album string
	Disc, Track, Year                 int
	Gain, Peak                        float64 // ReplayGain track gain in dB and sample peak
	HasGain                           bool
	AlbumGain, AlbumPeak              float64 // ReplayGain album gain in dB and sample peak
	HasAlbumGain                      bool
	SpotifyID                         string   // BOOMBRICK_SPOTIFY_ID, on a song an import downloaded
	Genres                            []string // nil when untagged
}

// SpotifyIDTag names the tag holding the Spotify ID of a song an import
// downloaded.
const SpotifyIDTag = "BOOMBRICK_SPOTIFY_ID"

// ReadTags reads ID3 (MP3), Vorbis comment (FLAC), OpusTags (Opus) and MP4
// (M4A) metadata.
func ReadTags(path string) (t Tags, err error) {
	// The tag package can panic on a truncated or damaged tag.
	defer func() {
		if r := recover(); r != nil {
			t, err = Tags{}, fmt.Errorf("read tags: %v", r)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return Tags{}, err
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return Tags{}, err
	}
	t = Tags{
		Title:       strings.TrimSpace(m.Title()),
		Artist:      strings.TrimSpace(m.Artist()),
		AlbumArtist: strings.TrimSpace(m.AlbumArtist()),
		Album:       strings.TrimSpace(m.Album()),
		Year:        year(m.Year(), m.Raw()),
	}
	t.Track, _ = m.Track()
	t.Disc, _ = m.Disc()
	// The tag package reads 0 for Vorbis's "3/10", for ID3's number with a
	// space before it, and for the disc some taggers write as Vorbis's DISC.
	if t.Track == 0 {
		t.Track = leadingInt(rawText(m.Raw(), "tracknumber", "trck", "trk"))
	}
	if t.Disc == 0 {
		t.Disc = leadingInt(rawText(m.Raw(), "discnumber", "disc", "tpos", "tpa"))
	}
	if t.AlbumArtist == "" {
		t.AlbumArtist = rawText(m.Raw(), "album_artist", "album artist", "albumartist")
	}
	t.Gain, t.Peak, t.HasGain, t.AlbumGain, t.AlbumPeak, t.HasAlbumGain = replayGains(m.Raw())
	t.SpotifyID = rawText(m.Raw(), SpotifyIDTag)
	t.Genres = genres(m.Genre())
	return t, nil
}

// genres splits a genre tag into its genres. The tag package separates a
// list's values with NULs (third_party/tag), and people type ";" between
// them. Each is trimmed, of the byte order marks UTF-16 lists repeat too,
// and kept once whatever its case.
func genres(s string) []string {
	var out []string
	for _, g := range strings.FieldsFunc(s, func(r rune) bool { return r == 0 || r == ';' }) {
		g = strings.TrimFunc(g, func(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff })
		if g != "" && !slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, g) }) {
			out = append(out, g)
		}
	}
	return out
}

// ReadTag reads the one tag named key, ignoring case, from the file at path,
// as Boombrick's BOOMBRICK_YOUTUBE_ID. It is "" when the file has none.
func ReadTag(path, key string) (v string, err error) {
	defer func() {
		if r := recover(); r != nil {
			v, err = "", fmt.Errorf("read tags: %v", r)
		}
	}()
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	m, err := tag.ReadFrom(f)
	if err != nil {
		return "", err
	}
	return rawText(m.Raw(), key), nil
}

// replayGains finds the track and album gains and peaks in raw tags:
// REPLAYGAIN_{TRACK,ALBUM}_{GAIN,PEAK}, or Opus's R128_{TRACK,ALBUM}_GAIN.
func replayGains(raw map[string]interface{}) (gain, peak float64, ok bool, albumGain, albumPeak float64, albumOK bool) {
	gain, peak, ok = scopeGain(raw, "track")
	albumGain, albumPeak, albumOK = scopeGain(raw, "album")
	return
}

// scopeGain finds the gain and peak for scope, "track" or "album":
// REPLAYGAIN_<SCOPE>_GAIN ("-6.50 dB") in Vorbis comments and ID3 TXXX frames,
// or Opus's R128_<SCOPE>_GAIN, which is Q7.8 dB against -23 LUFS, 5 dB below
// ReplayGain's reference. Values that don't parse, or that no real file would
// carry, are ignored.
func scopeGain(raw map[string]interface{}, scope string) (gain, peak float64, ok bool) {
	if s := rawText(raw, "replaygain_"+scope+"_gain"); s != "" {
		s = strings.TrimSpace(s)
		if len(s) >= 2 && strings.EqualFold(s[len(s)-2:], "db") {
			s = s[:len(s)-2]
		}
		if g, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && math.Abs(g) <= maxGain {
			gain, ok = g, true
		}
	}
	if s := rawText(raw, "r128_"+scope+"_gain"); s != "" && !ok {
		if q, err := strconv.Atoi(s); err == nil && math.Abs(float64(q)/256+5) <= maxGain {
			gain, ok = float64(q)/256+5, true
		}
	}
	if s := rawText(raw, "replaygain_"+scope+"_peak"); s != "" {
		if p, err := strconv.ParseFloat(s, 64); err == nil && p >= 0 && !math.IsInf(p, 0) {
			peak = p
		}
	}
	return gain, peak, ok
}

// maxGain is the largest gain in dB, either way, taken as real; ParseFloat
// would also accept Inf and NaN, which fail this too.
const maxGain = 60

// year is y, the package's reading of the date, unless that isn't a year: its Vorbis
// reader returns 1 for a date like yt-dlp's 20190514 and its ID3 reader the
// whole number. Then the first four digits of the raw date are used, if they
// make a year.
func year(y int, raw map[string]interface{}) int {
	if y >= 1000 && y <= 9999 {
		return y
	}
	s := rawText(raw, "date", "tdrc", "tyer", "year")
	if len(s) >= 4 {
		if n, err := strconv.Atoi(s[:4]); err == nil && n >= 1000 {
			return n
		}
	}
	return 0
}

// leadingInt reads the number at the start of s, as in "3/10".
func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	n, _ := strconv.Atoi(s[:i])
	return n
}

// rawText finds the first of keys with a value in raw tags, ignoring case and
// trying keys in the order given, whether stored as a plain value or as an ID3
// TXXX frame whose description is the key. The value is trimmed of spaces and
// of the NUL that ffmpeg ends ID3 text frames with; one left empty counts as
// missing.
func rawText(raw map[string]interface{}, keys ...string) string {
	for _, want := range keys {
		for k, v := range raw {
			var s string
			switch v := v.(type) {
			case string:
				if strings.EqualFold(k, want) {
					s = cleanText(v)
				}
			case *tag.Comm:
				if strings.EqualFold(v.Description, want) {
					s = cleanText(v.Text)
				}
			}
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func cleanText(s string) string {
	return strings.TrimSpace(strings.TrimRight(s, "\x00"))
}

package helper

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	downloadpb "github.com/devgianlu/go-librespot/proto/spotify/download"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	audiofilespb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata/audiofiles"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	"github.com/devgianlu/go-librespot/session"
)

// loudnessTarget is the loudness Spotify levels to, in LUFS.
const loudnessTarget = -14.0

// opened is a track opened from Spotify: its audio, decoded, and the
// download it is decoded from, which closing it stops.
type opened struct {
	*vorbisStream
	raw      io.Closer
	duration int64 // ms
}

func (o *opened) Close() error { return o.raw.Close() }

// open opens Spotify track id at about kbps, levelled or not, from ms. It
// takes the steps go-librespot's player takes for a track: its metadata, an
// alternative where it is restricted, the Vorbis file nearest kbps, the
// file's key, a CDN link and the download, decrypted. The audio is decoded
// here, by a decoder that can be closed.
func (s *Librespot) open(ctx context.Context, sess *session.Session, id librespot.SpotifyId, kbps int, level bool, ms int64) (*opened, error) {
	sp := sess.Spclient()
	var meta metadatapb.Track
	if err := sp.ExtendedMetadataSimple(ctx, id, extmetadatapb.ExtensionKind_TRACK_V4, &meta); err != nil {
		return nil, fmt.Errorf("track metadata: %w", err)
	}
	s.mu.Lock()
	country := s.country
	s.mu.Unlock()
	t, err := playable(&meta, country)
	if err != nil {
		return nil, err
	}
	media := librespot.NewMediaFromTrack(t)
	var files audiofilespb.AudioFilesExtensionResponse
	if err := sp.ExtendedMetadataSimple(ctx, media.Id(), extmetadatapb.ExtensionKind_AUDIO_FILES, &files); err != nil {
		return nil, fmt.Errorf("audio files: %w", err)
	}
	var all []*metadatapb.AudioFile
	for _, f := range files.GetFiles() {
		all = append(all, f.GetFile())
	}
	file := vorbisFile(all, kbps)
	if file == nil {
		return nil, librespot.ErrNoSupportedFormats
	}
	gain := float32(1)
	if p := files.GetDefaultFileNormalizationParams(); level && p != nil {
		gain = levelling(p.GetLoudnessDb(), p.GetTruePeakDb())
	}
	key, err := sess.AudioKey().Request(ctx, media.Id().Id(), file.GetFileId())
	if err != nil {
		return nil, fmt.Errorf("audio key: %w", err)
	}
	s.gotKey(sess) // even if the stream fails after this, the account has keys
	res, err := sp.ResolveStorageInteractive(ctx, file.GetFileId(), file.Format, false)
	if err != nil {
		return nil, fmt.Errorf("storage: %w", err)
	}
	raw, err := s.download(res)
	if err != nil {
		return nil, err
	}
	dec, err := audio.NewAesAudioDecryptor(raw, key)
	if err != nil {
		raw.Close()
		return nil, err
	}
	tr, err := newVorbis(dec, raw.Size(), gain)
	if err == nil && ms > 0 {
		err = tr.SeekMs(ms) // past the end, the stream ends at once
	}
	if err != nil {
		raw.Close()
		return nil, err
	}
	return &opened{vorbisStream: tr, raw: raw, duration: int64(media.Duration())}, nil
}

// download starts downloading the file storage resolved to, from the first
// CDN that answers.
func (s *Librespot) download(res *downloadpb.StorageResolveResponse) (*audio.HttpChunkedReader, error) {
	if res.GetResult() != downloadpb.StorageResolveResponse_CDN || len(res.GetCdnurl()) == 0 {
		return nil, fmt.Errorf("storage: %s", res.GetResult())
	}
	var err error
	for _, u := range res.GetCdnurl() {
		var r *audio.HttpChunkedReader
		if r, err = audio.NewHttpChunkedReader(s.lg, s.web, u); err == nil {
			return r, nil
		}
	}
	return nil, fmt.Errorf("download: %w", err)
}

// playable is t, or where it is restricted in country the first of its
// alternatives that isn't, with t's other fields.
func playable(t *metadatapb.Track, country string) (*metadatapb.Track, error) {
	if !restricted(t.GetRestriction(), country) {
		return t, nil
	}
	for _, alt := range t.GetAlternative() {
		if !restricted(alt.GetRestriction(), country) {
			t.Alternative = nil
			t.Gid, t.File, t.Preview, t.OriginalAudio = alt.Gid, alt.File, alt.Preview, alt.OriginalAudio
			return t, nil
		}
	}
	return nil, librespot.ErrMediaRestricted
}

// restricted reports whether rs keep a track from country; with no country
// known, nothing is.
func restricted(rs []*metadatapb.Restriction, country string) bool {
	if len(country) != 2 {
		return false
	}
	in := func(list string) bool {
		for i := 0; i+2 <= len(list); i += 2 {
			if strings.EqualFold(list[i:i+2], country) {
				return true
			}
		}
		return false
	}
	for _, r := range rs {
		switch c := r.GetCountryRestriction().(type) {
		case *metadatapb.Restriction_CountriesAllowed:
			return c.CountriesAllowed == "" || !in(c.CountriesAllowed)
		case *metadatapb.Restriction_CountriesForbidden:
			return in(c.CountriesForbidden)
		}
	}
	return false
}

// vorbisFile is the Ogg Vorbis file nearest kbps; nil when there is none.
func vorbisFile(files []*metadatapb.AudioFile, kbps int) *metadatapb.AudioFile {
	var best *metadatapb.AudioFile
	dist := math.MaxInt
	for _, f := range files {
		var rate int
		switch f.GetFormat() {
		case metadatapb.AudioFile_OGG_VORBIS_96:
			rate = 96
		case metadatapb.AudioFile_OGG_VORBIS_160:
			rate = 160
		case metadatapb.AudioFile_OGG_VORBIS_320:
			rate = 320
		default:
			continue
		}
		if d := max(rate-kbps, kbps-rate); d < dist {
			best, dist = f, d
		}
	}
	return best
}

// levelling is the gain that brings a track of loudness (LUFS) to Spotify's
// target, held down so its true peak (dBTP) stays under full scale.
func levelling(loudness, peak float32) float32 {
	gain := float32(math.Pow(10, (loudnessTarget-float64(loudness))/20))
	if p := float32(math.Pow(10, float64(peak)/20)); p > 0 && gain*p > 1 {
		gain = 1 / p
	}
	return gain
}

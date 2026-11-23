package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/ujjwal313/boombrick/internal/audio"
	"github.com/ujjwal313/boombrick/internal/audio/alsa"
	"github.com/ujjwal313/boombrick/internal/audio/decode"
	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/platform"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/spotify"
)

// errOffline is why a station wouldn't open with no network connection.
var errOffline = errors.New("offline")

// newPlayer makes the player both of Boombrick's modes play with: files
// through ALSA, Spotify songs from a downloaded copy when Prefer downloaded
// copies is on and there is one, or else streamed through the helper, and
// radio stations. rc may be nil, which only means no clicks are sent. notify
// hears the player's news about streaming.
func newPlayer(db *sql.DB, prefs *settings.Settings, sp *spotify.Helper, rc *radio.Client, online func() bool, log *slog.Logger,
	notify func(player.Notice)) *player.Player {
	p := player.New(alsa.New(log), decode.Open, log)
	p.SetLevelling(prefs.Bool(settings.VolumeLevelling))
	p.SetStreaming(func(ctx context.Context, t player.Track, frame int64, level bool) (audio.Source, error) {
		// A song an import downloaded plays from its file: no key, no Wi-Fi.
		if prefs.Bool(settings.PreferDownloaded) {
			if path, ok, err := library.FileOf(db, t.SpotifyID); err == nil && ok {
				return decode.Open(path)
			}
		}
		if !prefs.Bool(settings.SpotifyOn) {
			return nil, player.ErrSpotifyOff // the gate let it by for a copy, and there is none
		}
		if !online() {
			return nil, spotify.ErrOffline
		}
		s, err := sp.Open(ctx, t.SpotifyID, frame, prefs.Int(settings.StreamingKbps), level)
		if err != nil {
			return nil, err // not s: a nil *Stream isn't a nil Source
		}
		return s, nil
	}, notify)
	streams := radio.StreamClient()
	p.SetLive(func(ctx context.Context, t player.Track) (audio.Source, error) {
		if !online() {
			return nil, errOffline
		}
		l, err := radio.Open(ctx, t.Stream, radio.Options{HTTP: streams, Online: online, Name: t.Title, Log: log})
		if err != nil {
			return nil, err // not l: a nil *Live isn't a nil Source
		}
		if rc != nil && t.StationID != "" {
			// Radio Browser counts a click each time a station starts, so the
			// ones people listen to rise in Top. Nothing waits on it.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if err := rc.Click(ctx, t.StationID); err != nil {
					log.Info("radio click", "err", err)
				}
			}()
		}
		return l, nil
	})
	// The integrations switched off: the app's player and the background
	// player refuse the same tracks.
	p.SetGate(player.Switches(func() bool { return prefs.Bool(settings.SpotifyOn) },
		func() bool { return prefs.Bool(settings.YouTubeOn) },
		func() bool { return prefs.Bool(settings.RadioOn) },
		func() bool { return prefs.Bool(settings.PreferDownloaded) }))
	return p
}

// closePlayer closes p as the program ends. A sound card stuck in a call is
// left as it is, and the program ends regardless.
func closePlayer(p *player.Player, log *slog.Logger) {
	if err := p.Close(); err != nil {
		log.Warn("close the player", "err", err)
	}
}

// online reports whether the device has a network connection.
func online() bool { return platform.Online("/sys") }

// newHelper makes the Spotify helper both modes use: the program's, in its
// bin folder, with its sessions kept in the data folder.
func newHelper(dir string, paths platform.Paths, log *slog.Logger, out io.Writer) *spotify.Helper {
	return spotify.NewHelper(filepath.Join(dir, "bin", "boombrick-spotify"), filepath.Join(paths.Data, "spotify"), log, out)
}

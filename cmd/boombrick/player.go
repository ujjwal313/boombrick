package main

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/ujjwal313/boombrick/internal/audio"
	"github.com/ujjwal313/boombrick/internal/audio/alsa"
	"github.com/ujjwal313/boombrick/internal/audio/decode"
	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/platform"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/spotify"
)

// newPlayer makes the player both of Boombrick's modes play with: files
// through ALSA, and Spotify songs from a downloaded copy when Prefer
// downloaded copies is on and there is one, or else streamed through the
// helper. notify hears the player's news about streaming.
func newPlayer(db *sql.DB, prefs *settings.Settings, sp *spotify.Helper, online func() bool, log *slog.Logger,
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
		if !online() {
			return nil, spotify.ErrOffline
		}
		s, err := sp.Open(ctx, t.SpotifyID, frame, prefs.Int(settings.StreamingKbps), level)
		if err != nil {
			return nil, err // not s: a nil *Stream isn't a nil Source
		}
		return s, nil
	}, notify)
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

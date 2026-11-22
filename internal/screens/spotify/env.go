package spotify

import (
	"github.com/ujjwal313/boombrick/internal/importer"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/spotify"
)

// Importer imports playlists. *importer.Importer is one.
type Importer interface {
	Import(uri, name string, tracks []spotify.Track)
	// Changes is what changed of an import after a version, so that the
	// screens make again only the rows of the songs that did.
	Changes(uri string, since uint64) (importer.Changes, bool)
	Err() error
	Use(uri, id string, c int)
	Skip(uri, id string)
	Cancel(uri string)
	Version() uint64
}

// Queue is the download queue as the import screens show it.
// *download.Service is one.
type Queue interface {
	State(videoID string) (media.DownloadState, float32)
	Paused() bool
	SetPaused(on bool)
	Note() string
	// Version changes whenever a download's state or the queue does.
	Version() uint64
}

// Env is what the Spotify screens work with, shared by them all.
type Env struct {
	Nav    screens.Nav
	Src    Source
	Player Player
	// Importing; without Import there is none.
	Import Importer
	Queue  Queue
	// Copies reports whether the library has a file an import downloaded of
	// Spotify song id; nil for none.
	Copies func(id string) bool
	// Online reports the Wi-Fi; nil counts as online.
	Online func() bool
	// Importing reports whether importing is switched on, which takes YouTube
	// to find and download the songs; nil counts as on.
	Importing func() bool
	// Estimate is the download quality's MB per hour, and the SD card's free
	// space, for the size of an import.
	Estimate func() (mbPerHour float64, free int64)

	leaving bool // the import's pages are closing, back to where it began
}

func (e *Env) online() bool { return e.Online == nil || e.Online() }

// CanImport says whether an import may start: with importing, switched on,
// while online. Offline, matching would only wait, so nothing offers one.
func (e *Env) CanImport() bool {
	return e.Import != nil && e.online() && (e.Importing == nil || e.Importing())
}

func (e *Env) has(id string) bool { return e.Copies != nil && e.Copies(id) }

// liked is Liked Songs as a playlist, for the import screens.
func liked() spotify.Playlist { return spotify.Playlist{URI: spotify.LikedURI, Name: likedTitle} }

// Package spotify is Boombrick's side of Spotify. Spotify itself runs in a
// helper program, boombrick-spotify, built on go-librespot; this package
// starts it, asks it for the library and plays its streams. What the two say
// to each other over the helper's socket is defined here too.
package spotify

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// State is where the helper's Spotify session is.
type State string

const (
	SignedOut  State = "signed-out" // no account yet, or it was signed out
	SigningIn  State = "signing-in" // a device code waits for approval
	Connecting State = "connecting" // signed in; the session is being opened
	Ready      State = "ready"
	Offline    State = "offline" // signed in, but Spotify can't be reached
)

// Status is the helper's answer to /status.
type Status struct {
	State   State  `json:"state"`
	User    string `json:"user,omitempty"` // the account's ID
	Name    string `json:"name,omitempty"` // its display name, once known
	Premium bool   `json:"premium,omitempty"`
	// Login is the code waiting for approval while signing in.
	Login *Login `json:"login,omitempty"`
}

// Login is a device code: the user opens URL on a phone, enters Code if
// asked, and approves before Expires.
type Login struct {
	URL     string    `json:"url"`
	Code    string    `json:"code"`
	Expires time.Time `json:"expires"`
}

// Playlist is one of the user's playlists, Spotify's own mixes included.
type Playlist struct {
	URI   string `json:"uri"`
	Name  string `json:"name"`
	Owner string `json:"owner"` // the owner's display name, or ID when it can't be had
	Count int    `json:"count"`
	Cover string `json:"cover,omitempty"`
}

// Track is a song in a playlist or Liked Songs.
type Track struct {
	ID          string   `json:"id"` // base62, as in spotify:track:<id>
	Title       string   `json:"title"`
	Artists     []string `json:"artists"`
	Album       string   `json:"album"`
	AlbumArtist string   `json:"album_artist"`
	Duration    int64    `json:"duration_ms"`
	Cover       string   `json:"cover,omitempty"`
	ISRC        string   `json:"isrc,omitempty"`
}

// LikedURI names Liked Songs, for Tracks.
const LikedURI = "spotify:collection:tracks"

// DefaultKbps is the streaming quality used unless another is asked for, of
// the 96, 160 and 320 kbps Spotify offers.
const DefaultKbps = 160

var (
	// ErrSignedOut means there is no session: sign in again.
	ErrSignedOut = errors.New("spotify: signed out")
	// ErrUnavailable means the track can't be played here.
	ErrUnavailable = errors.New("spotify: not available here")
	// ErrPremium means the account is free, and streaming needs Premium.
	ErrPremium = errors.New("spotify: needs Premium")
	// ErrOffline means Spotify can't be reached.
	ErrOffline = errors.New("spotify: offline")
)

// Throttled is Spotify refusing for now; asking again before Wait has passed
// only makes it worse.
type Throttled struct{ Wait time.Duration }

func (t *Throttled) Error() string {
	return fmt.Sprintf("spotify: throttled for %v", t.Wait)
}

// KeyWait is how long a refused audio key is waited out: Spotify says nothing
// about it, and a refusal after a burst of keys is gone a minute later.
const KeyWait = 40 * time.Second

// The stream route's headers: the decoded audio's sample rate and the track's
// length.
const (
	HeaderRate     = "X-Sample-Rate"
	HeaderDuration = "X-Duration-Ms"
)

// StatusOf is the HTTP status the helper answers err with, and for a throttle
// how long to wait.
func StatusOf(err error) (int, time.Duration) {
	var th *Throttled
	switch {
	case errors.Is(err, ErrSignedOut):
		return http.StatusUnauthorized, 0
	case errors.Is(err, ErrUnavailable):
		return http.StatusForbidden, 0
	case errors.Is(err, ErrPremium):
		return http.StatusPaymentRequired, 0
	case errors.As(err, &th):
		return http.StatusTooManyRequests, th.Wait
	case errors.Is(err, ErrOffline):
		return http.StatusServiceUnavailable, 0
	}
	return http.StatusInternalServerError, 0
}

// ErrorOf turns the helper's answer back into the error it stands for; msg is
// what it said about one it has no status of its own for.
func ErrorOf(code int, retryAfter, msg string) error {
	switch code {
	case http.StatusUnauthorized:
		return ErrSignedOut
	case http.StatusForbidden:
		return ErrUnavailable
	case http.StatusPaymentRequired:
		return ErrPremium
	case http.StatusTooManyRequests:
		wait := KeyWait
		if s, err := strconv.Atoi(retryAfter); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		}
		return &Throttled{Wait: wait}
	case http.StatusServiceUnavailable:
		return ErrOffline
	}
	return fmt.Errorf("spotify helper: %d %s", code, msg)
}

// Package media defines identifiers shared by the player, the library and the UI.
package media

// Source is where a track's audio comes from.
type Source uint8

const (
	Local Source = iota
	Downloaded
	Spotify
	YouTube
	Radio
)

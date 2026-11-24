// Package radio finds internet radio stations in the Radio Browser directory,
// keeps the listener's saved stations, and plays their streams.
package radio

import "github.com/ujjwal313/boombrick/internal/player"

// Station is an internet radio station.
type Station struct {
	ID          string // Radio Browser's station UUID
	Name        string
	URL         string // the stream's address
	Logo        string
	Country     string
	CountryCode string
	Codec       string
	Bitrate     int // kbit/s
	Tags        []string
}

// Track returns the station as a queue entry: a track with a stream and no
// file.
func (s Station) Track() player.Track {
	return player.Track{Title: s.Name, Artist: s.Country, Cover: s.Logo, StationID: s.ID, Stream: s.URL}
}

// Package radio is the Radio tab: the stations saved, Radio Browser's most
// listened, its countries and genres, and the page of a country's or a
// genre's stations.
package radio

import (
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/radio"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// tab is the Radio tab's ID.
const tab = widgets.TabRadio

// Directory is Radio Browser's lists as these screens ask them: each call
// returns what is known at once, with have false until an answer has come,
// and asks for what isn't. *radio.Directory is one.
type Directory interface {
	Top() ([]radio.Station, bool, error)
	Countries() ([]radio.Count, bool, error)
	Genres() ([]radio.Count, bool, error)
	ByCountry(code string) ([]radio.Station, bool, error)
	ByTag(tag string) ([]radio.Station, bool, error)
	Search(q string) ([]radio.Station, bool, error)
	// Retry asks again for what failed, on the next call for it. Until that
	// asking ends, nothing reads as failed.
	Retry()
	// Version changes whenever an asking has ended.
	Version() uint64
}

// Saved is the stations saved, newest first. *radio.Stations is one.
type Saved interface {
	List() []radio.Station
	Has(id string) bool
	// Toggle saves a station, or removes it when it is saved, and reports
	// whether it is saved now.
	Toggle(s radio.Station) bool
	// Version changes whenever a station is saved or removed.
	Version() uint64
}

// Player plays a list of stations, and says which is playing.
// *player.Player is one.
type Player interface {
	Play(tracks []player.Track, start int)
	// SetShuffle turns shuffle on or off: a list of stations plays in order.
	SetShuffle(on bool)
	State() player.State
}

// Env is what the Radio screens work with, shared by them all. Whether the
// device is online they read from the frame's status, as the YouTube tab
// does.
type Env struct {
	Nav    screens.Nav
	Dir    Directory
	Saved  Saved
	Player Player
}

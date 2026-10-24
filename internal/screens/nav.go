package screens

import "time"

// AddedToQueue is the toast shown after something is added to the queue.
const AddedToQueue = "Added to queue"

// Scanning is the toast shown when a rescan is asked for.
const Scanning = "Scanning the SD card"

// Nav lets a screen open and close pages on its tab and show toasts.
type Nav interface {
	// Push opens s on top of the current tab.
	Push(s Screen)
	// Pop closes the current tab's top page.
	Pop()
	// Toast shows msg above the hint bar for a moment.
	Toast(msg string)
}

// Closer is a screen that has something to stop when its page closes, as the
// Audible sign-in has the web page it serves.
type Closer interface {
	Close()
}

// Backer is a screen that sometimes uses B itself, as the Queue does to
// cancel a move. Back reports whether it did; if not, B goes back as usual.
type Backer interface {
	Back() bool
}

// The tabs' places in the tab row, in the order of widgets.TabNames. Each tab
// draws the row with its own place active, so they are kept here, in one place.
const (
	HomeTab = iota
	LibraryTab
	BooksTab
	SpotifyTab
	YouTubeTab
	DownloadsTab
)

// Tabber is a Nav that can show another tab, as the app's does.
type Tabber interface {
	ShowTab(t int)
}

// OpenDownloads shows the Downloads tab, as the offline screens' Open
// Downloads does; it reports false when nav can't.
func OpenDownloads(nav Nav) bool {
	t, ok := nav.(Tabber)
	if ok {
		t.ShowTab(DownloadsTab)
	}
	return ok
}

// Opener is a screen that wants to know each time its overlay opens, to drop
// state left from the last time it was shown.
type Opener interface {
	Opened()
}

// Ticking is a screen that shows a time moving on by itself, as the sleep
// timer's time left, even with no music playing. Due reports when what it
// shows next changes after t, so a frame is drawn then.
type Ticking interface {
	Due(t time.Time) (time.Time, bool)
}

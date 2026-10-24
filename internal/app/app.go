// Package app runs Boombrick's window, event loop and top-level navigation.
package app

import (
	"fmt"
	"image/color"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/veandco/go-sdl2/sdl"

	"github.com/ujjwal313/boombrick/assets"
	"github.com/ujjwal313/boombrick/internal/fixture"
	"github.com/ujjwal313/boombrick/internal/library"
	lrc "github.com/ujjwal313/boombrick/internal/lyrics"
	"github.com/ujjwal313/boombrick/internal/media"
	"github.com/ujjwal313/boombrick/internal/perf"
	"github.com/ujjwal313/boombrick/internal/platform"
	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/resume"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/screens/browse"
	downloadsscreen "github.com/ujjwal313/boombrick/internal/screens/downloads"
	"github.com/ujjwal313/boombrick/internal/screens/home"
	lyricsscreen "github.com/ujjwal313/boombrick/internal/screens/lyrics"
	"github.com/ujjwal313/boombrick/internal/screens/nowplaying"
	"github.com/ujjwal313/boombrick/internal/screens/queue"
	"github.com/ujjwal313/boombrick/internal/screens/quickmenu"
	settingsscreen "github.com/ujjwal313/boombrick/internal/screens/settings"
	spotifyscreen "github.com/ujjwal313/boombrick/internal/screens/spotify"
	"github.com/ujjwal313/boombrick/internal/screens/youtube"
	"github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
)

// Options configure the app.
type Options struct {
	Fullscreen bool
	Log        *slog.Logger
	Started    time.Time // process start, for the launch-time log line
	Player     *player.Player
	MusicDir   string // shown by the empty library
	// Load reads the library and Progress the albums in progress; the app calls
	// them on a goroutine. Rescan asks for a scan. Scanned receives an update
	// when a scan has written to the library, at most every few seconds while
	// it runs, and when it ends; Saved receives a value after the saved
	// progress moved on to another track or stopped.
	Load     func() (*library.Index, error)
	Progress func() ([]resume.Progress, error)
	// BookStart, if set, says whether an audiobook was left further on
	// elsewhere (see browse.Remote).
	BookStart browse.Remote
	// Catalogue is the Audible library for the Books tab, with the channel
	// that says it has changed and the file a registration is read from.
	Catalogue      browse.Catalogue
	CatalogueReady <-chan struct{}
	AudibleSetup   string
	// AudibleSignIn makes the page that signs in to Audible with a phone; nil
	// leaves the Books tab asking for the registration file.
	AudibleSignIn func() screens.Screen
	Rescan        func()
	Scanned       <-chan ScanUpdate
	Saved         <-chan struct{}
	// Status reads the battery, network and Bluetooth for the status bar; the
	// app calls it on a goroutine every 10 s from start-up while the screen is
	// on, and as it comes back on.
	Status func() platform.Status
	// Volume watches the device volume for the volume pill. The app tells it
	// of every button pressed, as NextUI's volume keys may be one, and pauses
	// it while the screen is off. Without it no pill shows.
	Volume Volume
	// Covers supplies album art and the playing album's colour. Without it
	// the screens show placeholders and the accent stays gold.
	Covers Covers
	// Thumbs fetches pictures named by URL, such as YouTube Music's. Without
	// it they show as placeholders.
	Thumbs Thumbs
	// YouTube answers searches on the YouTube tab. Without it the tab is a
	// placeholder.
	YouTube YouTube
	// Downloads is the download queue: the Downloads tab and the YouTube
	// screens' actions. Without it the tab is a placeholder and the YouTube
	// screens only browse.
	Downloads Downloads
	// Lyrics finds tracks' lyrics. Without it there is no Lyrics screen.
	Lyrics Lyrics
	// Prefs are the user's settings. Without them there is no Settings
	// screen, and the quick menu's Settings row does nothing.
	Prefs settingsscreen.Prefs
	// Space measures and clears the caches for Settings' Storage page; nil
	// leaves the page out.
	Space Space
	// Backlight turns the screen's light off and on. Without it, or when it
	// fails, a screen turned off is drawn black instead, as on a desktop.
	Backlight Backlight
	// PowerOff asks the firmware to shut the device down once Boombrick has
	// quit; nil on a desktop, where holding Power only quits.
	PowerOff func() error
	// FirmwarePower leaves Power to the firmware, as Knulli suspends with it
	// and shuts down when it is held. A screen turned off from the quick
	// menu then comes back on with any button the music doesn't use.
	FirmwarePower bool
	// QuitChord quits when Start is pressed while Menu is held, as ports
	// quit under Knulli.
	QuitChord bool
	// Pads is how the firmware hands over the Brick's buttons.
	Pads input.Layout
	// Version and Device are shown at the foot of Settings.
	Version, Device string
	// ToolReleases and Releases find yt-dlp's and Boombrick's latest
	// releases for Settings' cards; without them the cards never say there
	// is one.
	ToolReleases, Releases Releases
	// Updater stages a newer Boombrick for the next launch. Without it, as on
	// a desktop, Settings doesn't offer Update Boombrick.
	Updater Updater
	// Perf records frame times in perf mode; nil when it is off.
	Perf *perf.Recorder
	// Notices brings the player's news about streaming, each shown as a
	// toast, or for an expired session as a dialog.
	Notices <-chan player.Notice
	// Spotify is the Spotify library. Without it the tab is a placeholder,
	// Home has no Spotify row and Settings no Spotify account.
	Spotify Spotify
	// Importer imports Spotify playlists to downloads. Without it, or
	// without Spotify or Downloads, nothing offers an import.
	Importer Importer
	// Background hands the music to the background player, and returns once
	// it plays; the app then quits. Without it the quick menu has no Play in
	// background.
	Background func() error
}

// Releases is what the app needs from a release checker. *release.Checker
// is one.
type Releases interface {
	settingsscreen.Releases
	Ready() <-chan struct{}
}

// Updater is what the app needs from the updater. *selfupdate.Updater is
// one.
type Updater interface {
	settingsscreen.Updater
	Ready() <-chan struct{}
}

// Space is what the app needs from the cache meter. *space.Meter is one.
type Space interface {
	settingsscreen.Space
	Ready() <-chan struct{}
}

// Backlight is the screen's light. *platform.Backlight is one.
type Backlight interface {
	Off() error
	On() error
}

// Volume is what the app tells the volume watcher, which tells the app of a
// change through VolumeChanged. *platform.VolumeWatcher is one.
type Volume interface {
	Pressed()
	Pause(paused bool)
}

// transport is what the buttons heard with the screen off do to the music.
// *player.Player is one.
type transport interface {
	Prev()
	Next()
	Toggle()
}

// powerHold is how long Power is held to power the device off, as NextUI's
// own apps take it; a shorter press turns the screen off or on.
const powerHold = time.Second

// Covers is what the app needs from the art cache.
type Covers interface {
	ui.Covers
	Colour(c media.Cover) (hue, chroma float64, colour, ok bool)
	Ready() <-chan struct{}
}

// YouTube is what the app needs from YouTube Music. *ytmusic.Searcher is
// one.
type YouTube interface {
	youtube.Source
	Ready() <-chan struct{}
}

// Lyrics is what the app needs from the lyrics service.
type Lyrics interface {
	Lyrics(t lrc.Track) (lrc.Lyrics, lrc.State)
	Prefetch(ts ...lrc.Track)
	Ready() <-chan struct{}
}

// tickFor is how often the loop wakes while music plays with s open: what s
// asks for, or once a second, which keeps the Queue's clock and the status
// bar's title current. s is nil on the tabs.
func tickFor(s screens.Screen) time.Duration {
	if t, ok := s.(screens.Ticker); ok {
		return t.Tick()
	}
	return time.Second
}

// App owns the window and dispatches input to the active tab or Now Playing.
type App struct {
	opts       Options
	window     *sdl.Window
	renderer   *sdl.Renderer
	ui         *ui.Context
	mapper     input.Mapper
	pads       map[sdl.JoystickID]map[uint8]bool // those read as game controllers, with the joystick buttons their mappings use
	repeat     *input.Repeater
	home       *home.Home
	nowPlaying *nowplaying.NowPlaying
	music      transport // the player, for the buttons heard with the screen off
	library    *browse.Library
	books      *browse.Books // nil without an Audible catalogue
	shelf      *browse.Shelf
	queue      *queue.Queue
	shell      *shell
	ix         *library.Index
	last       player.State
	dirty      bool
	fade       accentFade
	grounds    *grounds     // a new accent's backgrounds, rendered before its fade starts
	accentDue  bool         // what plays changed with the screen off: its accent is due when it is on
	arts       atomic.Bool  // art finished since poll last looked
	redraw     atomic.Bool  // lyrics, YouTube Music's answers, Settings' meter or releases, or an import have news to draw
	downloaded atomic.Bool  // the download queue changed since poll last looked
	landed     atomic.Bool  // and downloads landed, to show in the library
	skipped    atomic.Bool  // and a queued song was dropped, its download failed
	spotified  atomic.Bool  // the Spotify library has news
	handFailed atomic.Bool  // handing the music over failed, for poll to say
	handing    atomic.Bool  // the music is being handed over: input waits
	volume     atomic.Int32 // a volume level to show, plus one; 0 for none
	quit       bool
	drawn      bool
	drawnAt    time.Time // the start of the frame drawn last
	dark       bool      // the screen is off: nothing is drawn and only Power is heard
	lightOff   bool      // the backlight was turned off, to be turned on again
	powerAt    time.Time // when Power went down, while it is held
	menuHeld   bool      // Menu is down, for the quit chord
	scanEnded  bool      // a scan has ended, so an empty index is the library
	wasAnim    bool
	// Spotify: the session-expired dialog, whether it has opened since the
	// last sign-in, the playlists Home's row was made from, and the albums in
	// progress Home was last given.
	env         *spotifyscreen.Env // the Spotify screens', nil without Spotify
	expired     *spotifyscreen.Expired
	expiredOnce bool
	playlists   []spotify.Playlist
	progress    []resume.Progress

	loads *loader
	// set by the forwarding goroutines, taken by poll
	scanChanged, scanDone, saves atomic.Bool

	statuses chan platform.Status // the latest status reading, for poll
	lit      chan bool            // whether the screen is on, for the status reader
	waits    chan player.Track    // the track playback has begun to wait on, for followDownloads
	stop     chan struct{}        // closed by Close to end the status reader and followDownloads
	reader   sync.WaitGroup       // which Close waits for
	status   bool                 // a status reading has arrived

	mu     sync.Mutex // guards closed against wake and Quit racing Close
	closed bool
}

// New opens the window and builds the screens.
func New(opts Options) (*App, error) {
	sdl.SetHint(sdl.HINT_RENDER_SCALE_QUALITY, "1")
	subsystems := uint32(sdl.INIT_VIDEO | sdl.INIT_JOYSTICK)
	if opts.Pads.Controllers {
		subsystems |= sdl.INIT_GAMECONTROLLER
	}
	if err := sdl.Init(subsystems); err != nil {
		return nil, fmt.Errorf("sdl init: %w", err)
	}
	flags := uint32(sdl.WINDOW_SHOWN)
	if opts.Fullscreen {
		flags |= sdl.WINDOW_FULLSCREEN
	}
	win, err := sdl.CreateWindow("Boombrick", sdl.WINDOWPOS_CENTERED, sdl.WINDOWPOS_CENTERED, theme.DeviceW, theme.DeviceH, flags)
	if err != nil {
		sdl.Quit()
		return nil, fmt.Errorf("create window: %w", err)
	}
	r, err := sdl.CreateRenderer(win, -1, sdl.RENDERER_ACCELERATED|sdl.RENDERER_PRESENTVSYNC)
	if err != nil {
		opts.Log.Warn("accelerated renderer unavailable, using software", "err", err)
		if r, err = sdl.CreateRenderer(win, -1, sdl.RENDERER_SOFTWARE); err != nil {
			win.Destroy()
			sdl.Quit()
			return nil, fmt.Errorf("create renderer: %w", err)
		}
	}
	if info, err := r.GetInfo(); err == nil {
		opts.Log.Info("renderer", "name", info.Name)
	}
	r.SetLogicalSize(theme.DeviceW, theme.DeviceH)
	sdl.ShowCursor(sdl.DISABLE)
	pads := openPads(opts.Pads.Controllers, opts.Log)

	fonts, err := text.NewEngine(assets.Fonts())
	if err != nil {
		r.Destroy()
		win.Destroy()
		sdl.Quit()
		return nil, err
	}
	u := ui.NewContext(gfx.New(r), fonts, theme.Scale)
	// Until the first reading, which comes within a frame or so: no battery,
	// and the Wi-Fi icon rather than the Offline warning.
	u.Status = ui.Status{Online: true, Battery: -1}

	a := &App{
		opts:       opts,
		window:     win,
		renderer:   r,
		ui:         u,
		mapper:     input.Mapper{Labelled: opts.Pads.Labelled},
		pads:       pads,
		repeat:     input.NewRepeater(),
		home:       home.New(nil),
		nowPlaying: nowplaying.New(opts.Player),
		queue:      queue.New(opts.Player),
		statuses:   make(chan platform.Status, 1),
		lit:        make(chan bool, 1),
		waits:      make(chan player.Track, 1),
		stop:       make(chan struct{}),
		fade:       newAccentFade(theme.Fallback()),
		dirty:      true,
	}
	// The first accent's backgrounds are rendered as the first frames need
	// them; every later one's ahead, on a goroutine.
	a.grounds = newGrounds(a.fade.target(), renderGrounds, a.wake)
	if opts.Player != nil {
		a.music = opts.Player
	}
	if opts.Status != nil {
		a.reader.Go(func() { watchStatus(opts.Status, statusEvery, a.statuses, a.wake, a.lit, a.stop) })
	}
	a.home.SetEmpty(opts.MusicDir, func() { a.rescan(); a.Toast(screens.Scanning) })
	a.library = browse.New(a, opts.Player, opts.MusicDir, a.rescan)
	a.shelf = browse.NewShelf(opts.Player)
	a.shelf.SetRemote(opts.BookStart)
	a.library.SetShelf(a.shelf)
	var books screens.Screen = &screens.Placeholder{Tab: screens.BooksTab, Title: "Books"}
	if opts.Catalogue != nil {
		a.books = browse.NewBooks(a, opts.Player, a.shelf, opts.Catalogue, opts.AudibleSetup)
		a.books.SetSignIn(opts.AudibleSignIn)
		books = a.books
		if opts.CatalogueReady != nil {
			a.forward(opts.CatalogueReady, &a.redraw)
		}
	}
	if opts.Prefs != nil {
		a.nowPlaying.SetOnSpeed(func(v float64) { opts.Prefs.Set(settings.BookSpeed, strconv.FormatFloat(v, 'f', -1, 64)) })
	}
	a.loads = newLoader(opts.Load, opts.Progress, a.wake)
	if opts.Scanned != nil {
		go func() {
			for u := range opts.Scanned {
				if u.Changed {
					a.scanChanged.Store(true)
				}
				if u.Ended {
					a.scanDone.Store(true)
				}
				a.wake()
			}
		}()
	}
	if opts.Saved != nil {
		a.forward(opts.Saved, &a.saves)
	}
	if opts.Covers != nil || opts.Thumbs != nil {
		c := allCovers{web: opts.Thumbs}
		if opts.Covers != nil {
			c.albums = opts.Covers
			a.forward(opts.Covers.Ready(), &a.arts)
		}
		if opts.Thumbs != nil {
			a.forward(opts.Thumbs.Ready(), &a.arts)
		}
		u.Covers = c
	}
	if opts.MusicDir != "" {
		screens.SetDownloadsDir(filepath.Join(opts.MusicDir, "Boombrick"))
	}
	var ytTab, dlTab screens.Screen = &screens.Placeholder{Tab: screens.YouTubeTab, Title: "YouTube"}, &screens.Placeholder{Tab: screens.DownloadsTab, Title: "Downloads"}
	var acts youtube.Downloads // stays nil without a queue
	if opts.Downloads != nil {
		acts = ytActions{dl: opts.Downloads, p: opts.Player, toast: a.Toast}
		dlTab = downloadsscreen.New(a, opts.Downloads)
		a.nowPlaying.SetDownloads(opts.Downloads.State, opts.Downloads.Note)
		a.reader.Go(func() { a.followDownloads(opts.Downloads, opts.Player) })
	}
	if opts.YouTube != nil {
		ytTab = youtube.NewSearch(a, opts.YouTube, acts)
		a.forward(opts.YouTube.Ready(), &a.redraw)
	}
	var spTab screens.Screen = &screens.Placeholder{Tab: spotifyTab, Title: "Spotify"}
	if opts.Spotify != nil {
		a.env = a.spotifyEnv()
		spTab = spotifyscreen.NewTab(a.env)
		a.expired = spotifyscreen.NewExpired(a.signIn)
		a.forward(opts.Spotify.Ready(), &a.spotified)
		opts.Spotify.Saved() // reads the playlists kept, for Home, without starting the helper
	}
	a.shell = newShell(a.home,
		a.library,
		books,
		spTab,
		ytTab,
		dlTab)
	if a.expired != nil {
		a.shell.alert = a.expired
	}
	addOverlays(a.shell, a.nowPlaying, a.queue, func() bool { return a.last.Queued }, opts.Player, opts.Lyrics)
	if opts.Lyrics != nil {
		a.forward(opts.Lyrics.Ready(), &a.redraw)
	}
	a.shell.menu = a.quickMenu()
	return a, nil
}

// quickMenu makes the quick menu, and the Settings screen it opens.
func (a *App) quickMenu() *quickmenu.Menu {
	act := quickmenu.Actions{Sleep: a.opts.Player.Sleep, SetSleep: a.opts.Player.SetSleep,
		ScreenOff: func() { a.setDark(true) }, OpenSettings: func() {}, Quit: func() { a.quit = true }}
	act.Playing = func() bool { return a.last.Playing }
	if a.opts.Background != nil {
		act.PlayInBackground = a.playInBackground
	}
	if a.opts.Prefs != nil {
		o := settingsscreen.Options{Prefs: a.opts.Prefs, Player: a.opts.Player, Card: a.card, Toast: a.Toast,
			Version: a.opts.Version, Device: a.opts.Device, Quit: func() { a.quit = true }}
		if a.opts.Space != nil {
			o.Space = a.opts.Space
			a.forward(a.opts.Space.Ready(), &a.redraw)
		}
		if a.opts.Downloads != nil {
			o.Downloader = a.opts.Downloads
		}
		if a.opts.ToolReleases != nil {
			o.ToolReleases = a.opts.ToolReleases
			a.forward(a.opts.ToolReleases.Ready(), &a.redraw)
		}
		if a.opts.Releases != nil {
			o.Releases = a.opts.Releases
			a.forward(a.opts.Releases.Ready(), &a.redraw)
		}
		if a.opts.Updater != nil {
			o.Updater = a.opts.Updater
			a.forward(a.opts.Updater.Ready(), &a.redraw)
		}
		if a.opts.Spotify != nil {
			o.Spotify = a.opts.Spotify
			o.Copies = a.env.Import != nil
		}
		at := a.shell.add(overlay{button: input.None, screen: settingsscreen.New(o)})
		act.OpenSettings = func() { a.shell.openOverlay(at, time.Now()) }
	}
	return quickmenu.New(act)
}

// card is the SD card's space as the download queue last read it, for
// Settings.
func (a *App) card() (total, free, music int64) {
	if a.opts.Downloads == nil {
		return 0, 0, 0
	}
	s := a.opts.Downloads.Status().Storage
	return s.Total, s.Free, s.Music
}

// forward sets flag and wakes the loop each time ready receives, for poll to
// take.
func (a *App) forward(ready <-chan struct{}, flag *atomic.Bool) {
	go func() {
		for range ready {
			flag.Store(true)
			a.wake()
		}
	}()
}

// addOverlays puts the screens that open over the tabs on s: Now Playing on
// Start, when queued allows, the Queue on Select and, given lyrics, the Lyrics
// screen, which opens from Now Playing and goes back to it, having no button
// of its own.
func addOverlays(s *shell, np *nowplaying.NowPlaying, q screens.Screen, queued func() bool, p lyricsscreen.Player, lyrics lyricsscreen.Source) {
	nowPlayingAt := s.add(overlay{button: input.Start, screen: np, can: queued})
	s.add(overlay{button: input.Select, screen: q})
	if lyrics == nil {
		return
	}
	ly := lyricsscreen.New(p, lyrics, func() { s.openOverlay(nowPlayingAt, time.Now()) })
	lyricsAt := s.add(overlay{button: input.None, screen: ly})
	np.SetLyrics(func() { s.openOverlay(lyricsAt, time.Now()) })
}

// Push opens s on the current tab.
func (a *App) Push(s screens.Screen) { a.shell.push(s, time.Now()); a.dirty = true }

// Pop closes the current tab's top page.
func (a *App) Pop() { a.shell.pop(time.Now()); a.dirty = true }

// Toast shows msg above the hint bar.
func (a *App) Toast(msg string) { a.shell.showToast(msg, time.Now()); a.dirty = true }

// ShowTab shows tab t, closing any overlay over it.
func (a *App) ShowTab(t int) { a.shell.showTab(t, time.Now()); a.dirty = true }

// Close stops the status reader, turns the screen back on if it was off,
// and releases SDL.
func (a *App) Close() {
	a.mu.Lock()
	a.closed = true
	a.mu.Unlock()
	close(a.stop)
	a.reader.Wait()
	a.lightUp()
	a.renderer.Destroy()
	a.window.Destroy()
	sdl.Quit()
}

// Quit ends Run as if the window had been closed. It is safe to call from any
// goroutine.
func (a *App) Quit() { a.push(&sdl.QuitEvent{Type: sdl.QUIT}) }

// wake makes the loop's wait return. SDL_PushEvent may be called from any
// thread, but not once SDL has shut down.
func (a *App) wake() { a.push(&sdl.UserEvent{Type: sdl.USEREVENT}) }

func (a *App) push(ev sdl.Event) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.closed {
		sdl.PushEvent(ev)
	}
}

// Run draws and handles input until quit. Frames are drawn only when
// something changed, a screen is animating, or music is playing under Now
// Playing's progress bar; otherwise the loop sleeps until the next input, key
// repeat or clock minute.
func (a *App) Run() error {
	minute := -1
	for !a.quit {
		now := time.Now()
		if m := now.Minute(); m != minute {
			minute = m
			a.ui.Status.Time = fmt.Sprintf("%d:%02d", now.Hour(), now.Minute())
			a.dirty = true
		}
		if a.powerHeld(now) {
			continue // powering off
		}
		a.poll()
		if a.dark {
			a.darkFrame()
			a.wasAnim = false
			a.wait(now)
			continue
		}
		anim := a.animating(now)
		// Draw once more when an animation stops so its settled frame shows.
		if a.dirty || anim || a.wasAnim {
			a.draw(now)
			a.dirty, a.drawnAt = false, now
			if !a.drawn {
				a.drawn = true
				a.opts.Log.Info("first frame", "since_start", time.Since(a.opts.Started).Round(time.Millisecond))
				// The database holds last session's library; show it now and
				// refresh when a scan writes to it. Loading waits for the first
				// frame. What this reads includes any scan that already ended.
				if a.scanDone.Swap(false) {
					a.scanEnded = true
				}
				a.scanChanged.Store(false)
				a.loads.start(true, a.scanEnded)
			}
		}
		a.wasAnim = anim
		a.wait(now)
	}
	return nil
}

// poll picks up loaded data, the end of a library scan, the device status and
// player changes that show on screen.
func (a *App) poll() {
	a.volumeNews(time.Now())
	select {
	case s := <-a.statuses:
		if !a.status {
			a.status = true
			a.opts.Log.Info("status", "battery", s.Battery, "online", s.Online)
		}
		a.ui.Status.Battery, a.ui.Status.Online, a.ui.Status.Bluetooth = s.Battery, s.Online, s.Bluetooth
		a.dirty = true
	default:
	}
	st := a.opts.Player.State()
	l := a.last
	if a.drawn {
		ended, changed, saved := a.scanDone.Swap(false), a.scanChanged.Swap(false), a.saves.Swap(false)
		if ended {
			a.scanEnded = true
		}
		if read, index := reloadAfter(ended, changed, saved, a.ix != nil); read {
			a.loads.start(index, a.scanEnded)
		}
		if a.landed.Swap(false) {
			a.loads.start(true, a.scanEnded) // show the downloaded songs in Home and the Library
		}
		if r, ok := a.loads.take(a.scanEnded); ok {
			switch {
			case r.err != nil:
				a.opts.Log.Error("load library", "err", r.err)
			case r.ix != nil && len(r.ix.Albums) == 0 && !r.final:
				// The first scan hasn't ended, so an empty table says nothing yet
				// but that the scan is reading the music.
				a.home.SetReading()
				a.library.SetReading()
			default:
				if r.ix != nil {
					a.ix = r.ix
					a.library.SetIndex(r.ix)
					if a.books != nil {
						a.books.SetIndex(r.ix)
					}
					a.opts.Log.Info("loaded library", "albums", len(r.ix.Albums), "artists", len(r.ix.Artists),
						"songs", len(r.ix.Songs), "in_progress", len(r.progress), "took", r.took.Round(time.Millisecond))
				}
				a.showHome(r.progress)
			}
			a.dirty = true
		}
	}
	art := a.arts.Swap(false)
	if a.redraw.Swap(false) || art {
		a.dirty = true
	}
	// The Downloads tab, the YouTube screens' circles and Now Playing's
	// progress; a landing that changes the library comes through the loader,
	// and a dropped song's toast draws itself.
	if a.downloaded.Swap(false) && a.shell.showsDownloads() {
		a.dirty = true
	}
	if a.skipped.Swap(false) {
		a.Toast("Skipped a song that couldn’t download")
	}
	select {
	case n := <-a.opts.Notices:
		a.notice(n, time.Now())
	default:
	}
	if a.spotified.Swap(false) {
		a.spotifyNews()
	}
	a.backgroundNews()
	if st.Waiting && a.opts.Downloads != nil && (!l.Waiting || st.Track.VideoID != l.Track.VideoID) {
		a.askFirst(st.Track)
	}
	if wantsPrefetch(st, l) {
		a.prefetch(st)
	}
	a.followAccent(st, art || st.Track != l.Track || st.Queued != l.Queued, time.Now())
	if st.Err != nil && l.Err == nil {
		a.opts.Log.Warn("playback paused by an output error", "err", st.Err)
	}
	if st.Queued != l.Queued || st.Track != l.Track || st.Playing != l.Playing || st.Shuffle != l.Shuffle || st.Repeat != l.Repeat {
		a.ui.Status.NowPlaying = ""
		if st.Queued {
			a.ui.Status.NowPlaying = st.Track.Title
		}
		a.dirty = true
	}
	if p := tickFor(a.shell.overlay()); a.shell.open >= 0 && st.Elapsed/p != l.Elapsed/p {
		a.dirty = true
	}
	// A sleep timer's time left, in the quick menu or Settings, moves on
	// whether or not music plays.
	if due, ok := a.shell.due(a.drawnAt); ok && !time.Now().Before(due) {
		a.dirty = true
	}
	a.last = st
}

// prefetch asks for the lyrics of the track now playing and the one after,
// so they are there when the Lyrics screen opens.
func (a *App) prefetch(st player.State) {
	if a.opts.Lyrics == nil {
		return
	}
	ts := []lrc.Track{screens.PlayingLyricsTrack(st)}
	if t, ok := a.opts.Player.Upcoming(); ok {
		ts = append(ts, screens.LyricsTrack(t))
	}
	a.opts.Lyrics.Prefetch(ts...)
}

// followAccent keeps the accent with what is playing: when check is set it
// works out st's accent and asks for its backgrounds if it is new, and once
// they are rendered it puts them in the context and starts the fade. With the
// screen off none of it shows, so it waits for the screen to come back on.
func (a *App) followAccent(st player.State, check bool, now time.Time) {
	if a.dark {
		a.accentDue = a.accentDue || check
		return
	}
	if check || a.accentDue {
		a.accentDue = false
		if acc, ok := accentFor(a.colours(), st); ok {
			a.grounds.ask(acc)
		}
	}
	if acc, gs, ok := a.grounds.take(); ok {
		for _, g := range gs {
			a.ui.PutImage(g.key, g.img)
		}
		a.fade.set(acc, now)
	}
}

// colours is the art cache as the accent asks it, or nil without one.
func (a *App) colours() colours {
	if a.opts.Covers == nil {
		return nil
	}
	return a.opts.Covers
}

// showHome fills Home's rows from the index and the albums in progress.
func (a *App) showHome(progress []resume.Progress) {
	a.progress = progress
	a.shelf.SetProgress(progress)
	if a.ix == nil {
		return
	}
	var rows []fixture.Row
	if row, ok := home.ContinueRow(progress, a.ix, a.resumeAlbum, a.queueAlbum); ok {
		rows = append(rows, row)
	}
	if a.opts.Spotify != nil {
		var imp func(spotify.Playlist)
		if a.env.Import != nil {
			imp = a.importPlaylist
		}
		if row, ok := home.SpotifyRow(a.playlists, a.openPlaylist, imp); ok {
			rows = append(rows, row)
		}
	}
	if row, ok := home.AlbumRow(a.ix.Recent, a.playAlbum, a.queueAlbum); ok {
		rows = append(rows, row)
	}
	a.home.SetRows(rows)
}

func (a *App) playAlbum(al *library.Album) { playAlbum(a.opts.Player, al, 0, 0) }

func (a *App) resumeAlbum(al *library.Album, track int, frame int64) {
	playAlbum(a.opts.Player, al, track, frame)
}

// albumPlayer is what Home's albums need from the player.
type albumPlayer interface {
	SetShuffle(on bool)
	PlayFrom(tracks []player.Track, start int, frame int64)
}

// playAlbum plays al from track at frame, in order: shuffle left on by an
// album page's Shuffle pill doesn't carry over to albums started from Home.
func playAlbum(p albumPlayer, al *library.Album, track int, frame int64) {
	p.SetShuffle(false)
	p.PlayFrom(screens.Tracks(al.Tracks), track, frame)
}

func (a *App) queueAlbum(al *library.Album) {
	a.opts.Player.Enqueue(screens.Tracks(al.Tracks))
	a.Toast(screens.AddedToQueue)
}

func (a *App) rescan() {
	if a.opts.Rescan != nil {
		a.opts.Rescan()
	}
}

func (a *App) animating(now time.Time) bool {
	return a.shell.animating(now) || a.fade.animating(now) || a.ui.Animating()
}

func (a *App) draw(now time.Time) {
	var began time.Time
	if a.opts.Perf != nil {
		began = time.Now()
	}
	a.ui.BeginFrame(now)
	a.ui.Accent, a.ui.Fade = a.fade.at(now)
	a.shell.draw(a.ui)
	if a.opts.Perf == nil {
		a.ui.Canvas.Present()
		return
	}
	drawn := time.Now()
	a.ui.Canvas.Present()
	a.opts.Perf.Frame(drawn.Sub(began), time.Since(began))
}

func (a *App) wait(now time.Time) {
	timeout := a.timeout(now, time.Now())
	var ev sdl.Event
	if timeout <= 0 {
		ev = sdl.PollEvent()
	} else {
		ev = sdl.WaitEventTimeout(int(timeout/time.Millisecond) + 1)
	}
	for ; ev != nil; ev = sdl.PollEvent() {
		a.event(ev)
	}
	for _, e := range a.repeat.Due(time.Now()) {
		a.dispatch(e)
	}
}

// frameEvery is the time from one frame's start to the next while something
// moves. Where vsync works, presenting a frame waits for the display and this
// has passed already; where it doesn't, the loop still draws no faster than
// the display shows.
const frameEvery = 16 * time.Millisecond

// timeout is how long the loop may sleep, at now, after the frame begun at
// start: until the clock's next minute, or sooner the next key repeat, toast,
// animation frame or, while music plays, the open screen's next tick. The
// tick counts from the frame's start, so the time the frame took to draw
// doesn't lengthen it. A toast raised after the frame began, while it was
// polled or drawn, hasn't shown yet, so the next frame comes at once.
func (a *App) timeout(start, now time.Time) time.Duration {
	timeout := start.Truncate(time.Minute).Add(time.Minute).Sub(now)
	if !a.powerAt.IsZero() {
		timeout = min(timeout, a.powerAt.Add(powerHold).Sub(now)) // held that long, it powers off
	}
	if a.dark {
		return max(timeout, 0) // nothing shows, so nothing ticks
	}
	if due, ok := a.shell.due(start); ok {
		timeout = min(timeout, due.Sub(now))
	}
	if a.animating(start) {
		timeout = min(timeout, start.Add(frameEvery).Sub(now))
	}
	if a.shell.toastAt.After(start) {
		timeout = 0
	}
	if a.last.Playing {
		timeout = min(timeout, start.Add(tickFor(a.shell.overlay())).Sub(now))
	}
	if next, ok := a.repeat.Next(); ok {
		timeout = min(timeout, next.Sub(now))
	}
	if w, ok := a.shell.wake(start); ok {
		timeout = min(timeout, w.Sub(now))
	}
	return max(timeout, 0)
}

func (a *App) event(ev sdl.Event) {
	var evs []input.Event
	switch e := ev.(type) {
	case *sdl.QuitEvent:
		a.quit = true
	case *sdl.KeyboardEvent:
		if e.Type == sdl.KEYDOWN && e.Repeat == 0 {
			a.pressed()
			// Batocera's hotkeys, which Knulli has, ask a port to quit so.
			if e.Keysym.Scancode == sdl.SCANCODE_F4 && e.Keysym.Mod&sdl.KMOD_ALT != 0 {
				a.quit = true
			}
		}
		evs = a.mapper.Key(int(e.Keysym.Scancode), e.Type == sdl.KEYDOWN, e.Repeat != 0)
	case *sdl.JoyButtonEvent:
		// A controller's buttons come as its own events; a joystick button
		// its mapping leaves out still makes a chord of Menu.
		bound, controller := a.pads[e.Which]
		if e.State == sdl.PRESSED && !bound[e.Button] {
			a.pressed()
		}
		if !controller {
			evs = a.mapper.JoyButton(e.Button, e.State == sdl.PRESSED)
		}
	case *sdl.JoyHatEvent:
		if _, controller := a.pads[e.Which]; !controller {
			evs = a.mapper.JoyHat(e.Value)
		}
	case *sdl.JoyAxisEvent:
		if _, controller := a.pads[e.Which]; !controller {
			evs = a.mapper.JoyAxis(e.Axis, e.Value)
		}
	case *sdl.ControllerButtonEvent:
		if e.State == sdl.PRESSED {
			a.pressed()
		}
		evs = a.mapper.ControllerButton(e.Button, e.State == sdl.PRESSED)
	case *sdl.ControllerAxisEvent:
		evs = a.mapper.ControllerAxis(e.Axis, e.Value)
	case *sdl.WindowEvent:
		if e.Event == sdl.WINDOWEVENT_EXPOSED {
			a.dirty = true
		}
	}
	for _, e := range evs {
		a.dispatch(e)
	}
}

// openPads opens every pad: as a game controller where controllers is set
// and SDL has a mapping for it, as Knulli gives each port, and otherwise as
// a joystick read by NextUI's numbers. It logs each, and returns the
// controllers with the joystick buttons their mappings use.
func openPads(controllers bool, log *slog.Logger) map[sdl.JoystickID]map[uint8]bool {
	pads := make(map[sdl.JoystickID]map[uint8]bool)
	for i := range sdl.NumJoysticks() {
		name, guid := sdl.JoystickNameForIndex(i), sdl.JoystickGetGUIDString(sdl.JoystickGetDeviceGUID(i))
		if controllers && sdl.IsGameController(i) {
			if c := sdl.GameControllerOpen(i); c != nil {
				// The buttons are read from the mapping: go-sdl2's
				// GameControllerButtonBind.Button misreads their numbers.
				mapping := c.Mapping()
				pads[c.Joystick().InstanceID()] = input.MappedButtons(mapping)
				log.Info("pad", "name", name, "guid", guid, "mapping", mapping)
				continue
			}
		}
		j := sdl.JoystickOpen(i)
		if j == nil {
			log.Warn("open pad", "name", name, "guid", guid, "err", sdl.GetError())
			continue
		}
		args := []any{"name", name, "guid", guid, "buttons", j.NumButtons(), "axes", j.NumAxes(), "hats", j.NumHats()}
		if controllers {
			log.Warn("pad without a mapping, read by NextUI's numbers", args...)
		} else {
			log.Info("pad", args...)
		}
	}
	return pads
}

// pressed hears each button that goes down, before it is mapped: even one
// Boombrick doesn't use, as NextUI's volume keys may be, makes a chord of
// Menu, and has the volume read at once.
func (a *App) pressed() {
	a.shell.chord()
	if a.opts.Volume != nil {
		a.opts.Volume.Pressed()
	}
}

func (a *App) dispatch(e input.Event) {
	now := time.Now()
	switch e.Kind {
	case input.Press:
		a.repeat.Press(e.Button, now)
	case input.Release:
		a.repeat.Release(e.Button)
	}
	if e.Button == input.Menu {
		a.menuHeld = e.Kind != input.Release
	}
	if e.Button == input.Power {
		if !a.opts.FirmwarePower {
			a.power(e, now)
		}
		return
	}
	if a.handing.Load() {
		return // the music is being handed over
	}
	if a.opts.QuitChord && a.menuHeld && e.Button == input.Start && e.Kind == input.Press {
		a.quit = true
		return
	}
	if a.dark {
		// The screen is off: only Power and the music's buttons are heard,
		// or where the firmware keeps Power, any other button turns it on.
		if e.Kind == input.Press && !a.darkButton(e.Button) && a.opts.FirmwarePower {
			a.setDark(false)
		}
		return
	}
	if a.shell.handle(e, now) {
		a.dirty = true
	}
}

// playInBackground hands the music over on a goroutine, as the UI thread
// mustn't wait for the background player, and quits once it plays. Input waits
// meanwhile. When it can't, the music plays on here.
func (a *App) playInBackground() {
	if !a.handing.CompareAndSwap(false, true) {
		return
	}
	go func() {
		if err := a.opts.Background(); err != nil {
			a.opts.Log.Warn("play in background", "err", err)
			a.handing.Store(false)
			a.handFailed.Store(true)
			a.wake()
			return
		}
		a.Quit()
	}()
}

// backgroundNews says once that handing the music over failed.
func (a *App) backgroundNews() {
	if a.handFailed.Swap(false) {
		a.Toast("Couldn’t play in the background")
	}
}

// power turns the screen off or on when Power is let go within powerHold;
// Run powers the device off when it is held longer.
func (a *App) power(e input.Event, now time.Time) {
	switch e.Kind {
	case input.Press:
		a.powerAt = now
	case input.Release:
		if !a.powerAt.IsZero() && now.Sub(a.powerAt) < powerHold {
			a.setDark(!a.dark)
		}
		a.powerAt = time.Time{}
	}
}

// setDark turns the screen off or back on. Off, the backlight goes off where
// there is one, nothing is drawn and only Power and the music's buttons are
// heard; music plays on. The status bar's readings and the volume, which
// only show, wait for it to come back on.
func (a *App) setDark(dark bool) {
	if dark == a.dark {
		return
	}
	a.dark, a.dirty = dark, true
	if a.lit != nil {
		select {
		case <-a.lit:
		default:
		}
		a.lit <- !dark // the only sender, so there is room now
	}
	if a.opts.Volume != nil {
		a.opts.Volume.Pause(dark)
	}
	if !dark {
		a.opts.Log.Info("screen on")
		a.lightUp()
		return
	}
	a.opts.Log.Info("screen off")
	if a.opts.Backlight != nil {
		if err := a.opts.Backlight.Off(); err != nil {
			a.opts.Log.Info("backlight stays on", "err", err)
		} else {
			a.lightOff = true
		}
	}
}

// darkButton does what a button does with the screen off: L1 and R1 skip back
// and on, and A plays or pauses. It reports whether b was one of those.
func (a *App) darkButton(b input.Button) bool {
	if a.music == nil {
		return false
	}
	switch b {
	case input.L1:
		a.music.Prev()
	case input.R1:
		a.music.Next()
	case input.A:
		a.music.Toggle()
	default:
		return false
	}
	return true
}

// darkFrame draws what shows while the screen is off: nothing with the
// backlight off, and where it couldn't be turned off, a black frame when
// something changed.
func (a *App) darkFrame() {
	if a.dirty && !a.lightOff {
		a.ui.Canvas.Clear(color.NRGBA{A: 255})
		a.ui.Canvas.Present()
	}
	a.dirty = false
}

// lightUp turns the backlight on again if setDark turned it off.
func (a *App) lightUp() {
	if !a.lightOff {
		return
	}
	a.lightOff = false
	if err := a.opts.Backlight.On(); err != nil {
		a.opts.Log.Warn("turn the backlight on", "err", err)
	}
}

// powerHeld powers off once Power has been held for powerHold at now, and
// reports whether it did.
func (a *App) powerHeld(now time.Time) bool {
	if a.powerAt.IsZero() || now.Sub(a.powerAt) < powerHold {
		return false
	}
	a.powerAt = time.Time{}
	a.powerOff()
	return true
}

// powerOff quits, asking the firmware to shut the device down after, as
// holding Power does in NextUI's own apps. Quitting saves the queue as ever.
func (a *App) powerOff() {
	a.opts.Log.Info("powering off")
	if a.opts.PowerOff != nil {
		if err := a.opts.PowerOff(); err != nil {
			a.opts.Log.Error("ask the firmware to power off", "err", err)
		}
	}
	a.quit = true
}

// wantsPrefetch reports whether the lyrics of the track st is on are to be
// asked for, st being what the player gave after l: the track is new, or
// something is queued again. A streamed song has no path, so tracks are told
// apart by their lyrics key.
func wantsPrefetch(st, l player.State) bool {
	return st.Queued && (screens.LyricsTrack(st.Track).Key() != screens.LyricsTrack(l.Track).Key() || !l.Queued)
}

// VolumeChanged tells the app the device volume is now level, 0 to 20, for it
// to show. It is safe to call from any goroutine.
func (a *App) VolumeChanged(level int) {
	a.volume.Store(int32(level) + 1)
	a.wake()
}

// volumeNews shows a volume change the watcher reported, unless the screen is
// off.
func (a *App) volumeNews(now time.Time) {
	if v := a.volume.Swap(0); v > 0 && !a.dark {
		a.shell.showVolume(int(v-1), now)
		a.dirty = true
	}
}

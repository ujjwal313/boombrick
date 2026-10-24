// Command boombrick is the music player for the TrimUI Brick.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/ujjwal313/boombrick/internal/app"
	"github.com/ujjwal313/boombrick/internal/art"
	"github.com/ujjwal313/boombrick/internal/audible"
	"github.com/ujjwal313/boombrick/internal/audio/decode"
	"github.com/ujjwal313/boombrick/internal/audiobooks"
	"github.com/ujjwal313/boombrick/internal/background"
	"github.com/ujjwal313/boombrick/internal/download"
	"github.com/ujjwal313/boombrick/internal/importer"
	"github.com/ujjwal313/boombrick/internal/library"
	"github.com/ujjwal313/boombrick/internal/logx"
	"github.com/ujjwal313/boombrick/internal/lyrics"
	"github.com/ujjwal313/boombrick/internal/netx"
	"github.com/ujjwal313/boombrick/internal/perf"
	"github.com/ujjwal313/boombrick/internal/platform"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/release"
	"github.com/ujjwal313/boombrick/internal/resume"
	"github.com/ujjwal313/boombrick/internal/screens"
	audibleui "github.com/ujjwal313/boombrick/internal/screens/audible"
	"github.com/ujjwal313/boombrick/internal/selfupdate"
	"github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/space"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/store"
	"github.com/ujjwal313/boombrick/internal/version"
	"github.com/ujjwal313/boombrick/internal/ytmusic"
)

func init() {
	// SDL must be driven from the main OS thread.
	runtime.LockOSThread()
}

// releaseEvery is how often Settings asks GitHub for the latest releases.
const releaseEvery = 6 * time.Hour

func main() {
	started := time.Now()
	showVersion := flag.Bool("version", false, "print the version and exit")
	inBackground := flag.Bool("background", false, "play the saved queue with no screen until stopped, as Play in background starts it")
	flag.Parse()
	if *showVersion {
		fmt.Println(version.String())
		return
	}
	mode := func() error { return run(started) }
	if *inBackground {
		mode = runBackground
	}
	if err := mode(); err != nil {
		fmt.Fprintln(os.Stderr, "boombrick:", err)
		os.Exit(1)
	}
}

func run(started time.Time) error {
	// Keep the heap well inside the Brick's 1 GB, next to NextUI.
	debug.SetMemoryLimit(96 << 20)

	// The program's folder holds QuickJS and, on the Brick, the app that
	// updates are staged in; on CrossMix and Knulli, Boombrick's data too.
	dir := programDir()
	prof := platform.Detect(dir)
	paths := prof.Paths
	for _, d := range []string{paths.Data, paths.Logs} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	logFile, err := logx.Open(filepath.Join(paths.Logs, "Boombrick.txt"), 1<<20, 1)
	if err != nil {
		return err
	}
	defer logFile.Close()
	// On the device stderr is the crash file; keep the log out of it.
	var logOut io.Writer = logFile
	if !paths.OnDevice {
		logOut = io.MultiWriter(os.Stderr, logFile)
	}
	log := slog.New(slog.NewTextHandler(logOut, nil))
	log.Info("starting", "version", version.Version, "firmware", prof.Firmware, "data", paths.Data)
	// Perf mode logs the launch's stages, then memory, CPU and frame times
	// every 10 s.
	var rec *perf.Recorder
	if perf.Enabled(dir) {
		rec = perf.Start(log, started, 10*time.Second)
		defer rec.Close()
	}

	db, err := store.Open(filepath.Join(paths.Data, "boombrick.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	prefs, err := settings.Load(db.DB, log)
	if err != nil {
		return err
	}
	defer prefs.Close() // writes what is waiting before the database closes
	rec.Mark("database")

	// The files the library reads, whether a scan finds them or a download
	// files them.
	formats := library.Formats{Supported: decode.Supported, Duration: decode.Duration}

	// Album art and lyrics are found off the UI thread; what isn't with the
	// music is looked up online while the device is. They close after the
	// player, so the music stops at once on quitting, and before the database.
	web := netx.NewClient()
	covers := art.New(art.Options{
		DB:     db.DB,
		Dir:    filepath.Join(paths.Data, "cache", "art"),
		Lookup: art.NewLookup(web),
		Online: online,
		Log:    log,
	})
	defer covers.Close()
	words := lyrics.New(lyrics.Options{DB: db.DB, Fetch: lyrics.NewClient(web), Online: online, Log: log,
		FileOf: func(id string) (string, bool) {
			path, ok, err := library.FileOf(db.DB, id)
			return path, ok && err == nil
		}})
	defer words.Close()
	// YouTube Music is asked, its thumbnails fetched and its songs downloaded
	// off the UI thread too. The searcher falls back to yt-dlp's search.
	thumbs := art.NewWeb(web, online, log)
	defer thumbs.Close()
	ytc := ytmusic.NewClient(web)
	qjs := quickJS(dir)
	dl, err := download.New(download.Options{DB: db.DB, Music: paths.Music, Data: paths.Data, QJS: qjs,
		HTTP: web, Albums: ytc, Lyrics: lyrics.NewClient(web), Formats: formats, Online: online,
		Quality: func() string { return prefs.String(settings.DownloadQuality) }, Log: log})
	if err != nil {
		return err
	}
	defer dl.Close()
	log.Info("downloads", "qjs", qjs != "")
	yt := ytmusic.NewSearcher(ytmusic.Options{Client: ytc, Online: online, Log: log, Fallback: dl.Search})
	defer yt.Close()
	// Spotify playlists are imported by matching their songs on YouTube
	// Music, a search a second, and downloading the matches. The importer
	// closes before the queue it fills.
	im, err := importer.New(importer.Options{DB: db.DB, Search: ytc.Search, Get: dl.Get, State: dl.State,
		Remove: dl.Remove, Online: online, Log: log})
	if err != nil {
		return err
	}
	defer im.Close()
	// Settings' Storage page measures and clears the caches off the UI
	// thread; the meter closes before the caches it reaches into.
	meter := space.New(space.Options{Art: covers, Lyrics: words,
		Parts:      space.Funcs{SizeOf: dl.Leftovers, Empty: dl.ClearLeftovers},
		Downloader: filepath.Join(paths.Data, "bin"), Log: log})
	defer meter.Close()
	// The status bar names the Bluetooth headphones or speaker connected,
	// asking BlueZ on the status reader's goroutine.
	bt := platform.NewBluetooth("/sys")
	defer bt.Close()
	// On the Brick, Power turns the backlight off and holding it shuts down.
	var light app.Backlight
	var powerOff func() error
	if paths.OnDevice {
		light = platform.NewBacklight()
		powerOff = prof.PowerOff
	}
	// Settings says when yt-dlp or Boombrick has a newer release, asking
	// GitHub at most every 6 hours while it is open. On the Brick, Update
	// Boombrick stages a release in the pak's folder, for launch.sh to move
	// in on the next launch.
	toolReleases := release.NewChecker(web, "yt-dlp/yt-dlp", online, releaseEvery, log)
	defer toolReleases.Close()
	releases := release.NewChecker(web, "ujjwal313/boombrick", online, releaseEvery, log)
	defer releases.Close()
	var updater app.Updater
	if paths.OnDevice && dir != "" {
		up := selfupdate.New(web, dir, selfupdate.Pak{Asset: prof.ReleaseAsset, Dir: prof.ReleaseDir, Launcher: prof.Launcher}, log)
		defer up.Close()
		updater = up
	}

	// Spotify runs in its helper, started the first time a Spotify song is
	// played; it stops after the player.
	sp := newHelper(dir, paths, log, logOut)
	defer sp.Close()
	// The library the Spotify screens ask, kept in the database; it stops
	// before the helper.
	spLib := spotify.NewLibrary(sp, db.DB, log)
	defer spLib.Close()

	notices := make(chan player.Notice, 8)
	p := newPlayer(db.DB, prefs, sp, online, log, func(n player.Notice) {
		select {
		case notices <- n:
		default: // a burst of them: the toast shows the latest anyway
		}
	})
	defer p.Close()

	// A background player gives the music back before the saved queue is
	// read; neither must hold up the first frame. Every later release keeps
	// this take-back at launch, as only it can stop a background player an
	// earlier version left playing.
	restored := make(chan struct{})
	go func() {
		defer close(restored)
		background.TakeBack(prof.PlayerLock(), 5*time.Second, p, background.Store{
			Load:         func() (player.Snapshot, bool, error) { return resume.Load(db.DB) },
			LoadHandover: func() (resume.Handover, error) { return resume.LoadHandover(db.DB) },
			SaveHandover: func(h resume.Handover) error { return resume.SaveHandover(db.DB, h) },
		}, log)
		// A background player stopping by itself just as it was taken back can
		// remove the file the launcher has just written.
		if f := prof.StayAwake(); f != "" {
			if err := os.WriteFile(f, []byte("1\n"), 0o644); err != nil {
				log.Warn("keep the Brick awake", "err", err)
			}
		}
	}()
	gate := &saves{saver: resume.NewSaver(db.DB).Save}
	save := func() {
		if s, ok := p.Snapshot(); ok {
			if err := gate.save(s); err != nil {
				log.Warn("save queue", "err", err)
			}
		}
	}
	defer save() // runs before the player and the database close

	// Quitting cancels the scanner and the saver, and waits for them and the
	// restore before the database closes under them.
	ctx, cancel := context.WithCancel(context.Background())
	scanDone, saved := make(chan struct{}), make(chan struct{})
	booksDone := make(chan struct{}) // closed once the Audible sync, if any, has stopped
	defer func() { cancel(); <-scanDone; <-saved; <-restored; <-booksDone }()

	// Scans run one at a time on their own goroutine: at start-up, then each
	// time the app asks. Asking while a scan runs queues one more. The app
	// hears when a scan writes to the library, at most every 10 s while it
	// runs, and when each scan ends.
	scanReq, scanned := make(chan struct{}, 1), make(chan app.ScanUpdate, 1)
	// tell hands u to the app, merged with one it hasn't taken yet. The
	// scanner is the only sender, so there is room after the drain.
	tell := func(u app.ScanUpdate) {
		select {
		case old := <-scanned:
			u.Changed, u.Ended = u.Changed || old.Changed, u.Ended || old.Ended
		default:
		}
		scanned <- u
	}
	scanReq <- struct{}{}
	go func() {
		defer close(scanDone)
		defer close(scanned)
		for {
			select {
			case <-ctx.Done():
				return
			case <-scanReq:
			}
			if ctx.Err() != nil {
				return
			}
			start := time.Now()
			var told time.Time
			wroteAny := false
			wrote := func(library.Stats) {
				wroteAny = true
				if time.Since(told) >= 10*time.Second {
					told = time.Now()
					tell(app.ScanUpdate{Changed: true})
				}
			}
			st, err := library.ScanWithProgress(ctx, db.DB, paths.Music, formats, wrote)
			changed := false
			switch {
			case errors.Is(err, context.Canceled):
				return
			case err != nil:
				log.Error("scan", "root", paths.Music, "err", err)
				changed = wroteAny // batches already written are worth loading
			default:
				log.Info("scanned", "root", paths.Music, "tracks", st.Total, "added", st.Added,
					"updated", st.Updated, "removed", st.Removed, "took", time.Since(start).Round(time.Millisecond))
				changed = st.Added+st.Updated+st.Removed > 0
				// Downloads whose files were deleted, as on a PC, can be had again.
				dl.Prune()
			}
			tell(app.ScanUpdate{Changed: changed, Ended: true})
		}
	}()
	rescan := func() {
		select {
		case scanReq <- struct{}{}:
		default:
		}
	}
	// Save the queue every 5 s while music plays. The app reloads Continue
	// listening when a save moves to another track or playback stops.
	savedTrack := make(chan struct{}, 1)
	go func() {
		defer close(saved)
		defer close(savedTrack)
		keepSaving(ctx, 5*time.Second, p.State, save, savedTrack)
	}()

	// Audiobooks keep their place in step with Audible when the device has a
	// registration to sign in with (see internal/audible); without one, or
	// without Wi-Fi, they keep it on the card alone.
	var books *bookSync
	var audibleClient *audible.Client
	if ac, err := audible.Load(filepath.Join(paths.Data, "audible", "auth.json"), web); err == nil {
		audibleClient = ac
		books = newBookSync(ac, online, log)
		log.Info("audible", "signed in", true)
	} else if errors.Is(err, audible.ErrOldDevice) {
		log.Warn("audible", "err", err) // the Books tab asks to sign in again
	} else if !errors.Is(err, audible.ErrNoAuth) {
		log.Warn("audible", "err", err)
	}
	// The Books tab lists the Audible library and downloads from it, when
	// there is a registration; without one it says how to set one up.
	authFile := filepath.Join(paths.Data, "audible", "auth.json")
	var api audiobooks.API
	if audibleClient != nil {
		api = audibleClient
	}
	catalogue := audiobooks.New(audiobooks.Options{
		API: api, Dir: filepath.Join(paths.Music, "Audiobooks"), Cache: filepath.Join(paths.Data, "audible", "library.json"),
		FFmpeg: filepath.Join(dir, "bin", "ffmpeg"), Online: online, Log: log,
		Filed: func(string) { rescan() }, // the Library finds the book with the next scan
	})
	defer catalogue.Close()
	catalogue.Refresh()
	// A phone signs the device in from the Books tab. The library and the
	// downloads work at once; the places start to be kept in step with Audible
	// the next time Boombrick starts.
	var nav screens.Nav // the app, once it exists
	signIn := func() screens.Screen {
		si := audible.NewSignIn(authFile, web, func(c *audible.Client) {
			log.Info("audible", "signed in", true)
			catalogue.SetAPI(c)
		})
		return audibleui.NewSignIn(nav, si)
	}
	if books != nil {
		go func() {
			defer close(booksDone)
			keepBooksSynced(ctx, syncEvery/3, p.State, books)
		}()
	} else {
		close(booksDone)
	}

	// NextUI's volume keys change the volume NextUI keeps in a file, read for
	// the volume pill; the app has it read when a button is pressed.
	var volume *platform.VolumeWatcher
	if f := prof.VolumeSettings(); f != "" {
		var ok bool
		volume, ok = platform.NewVolumeWatcher(f)
		log.Info("volume", "source", f, "ok", ok)
	}

	opts := app.Options{
		Fullscreen: paths.OnDevice,
		Log:        log,
		Started:    started,
		Player:     p,
		MusicDir:   paths.Music,
		Load:       func() (*library.Index, error) { return library.Load(db.DB) },
		Progress:   func() ([]resume.Progress, error) { return resume.Recent(db.DB, 200) },
		BookStart:  bookStart(books),
		Catalogue:  catalogue, CatalogueReady: catalogue.Ready(), AudibleSetup: authFile, AudibleSignIn: signIn,
		Rescan:  rescan,
		Scanned: scanned,
		Saved:   savedTrack,
		Status: func() platform.Status {
			st := platform.ReadStatus("/sys")
			st.Bluetooth = bt.Connected()
			return st
		},
		Covers:        covers,
		Lyrics:        words,
		Thumbs:        thumbs,
		YouTube:       yt,
		Downloads:     dl,
		Prefs:         prefs,
		Space:         meter,
		Backlight:     light,
		PowerOff:      powerOff,
		FirmwarePower: prof.FirmwarePower(),
		QuitChord:     prof.QuitChord(),
		Pads:          prof.Pads(),
		Version:       version.Version,
		Device:        "TrimUI Brick",
		ToolReleases:  toolReleases,
		Releases:      releases,
		Updater:       updater,
		Perf:          rec,
		Notices:       notices,
		Spotify:       spLib,
		Importer:      im,
		Background: func() error {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			return gate.handOver(func() error {
				return background.HandOver(p, gate.now, func(h resume.Handover) error { return resume.SaveHandover(db.DB, h) },
					exe, os.Environ(), handOverWait(p.State()))
			})
		},
	}
	if volume != nil {
		opts.Volume = volume // a nil *VolumeWatcher would make a Volume that isn't nil
	}
	a, err := app.New(opts)
	if err != nil {
		return err
	}
	defer a.Close()
	nav = a
	// The volume watcher stops before the app and SDL close under it.
	if volume != nil {
		vol, stop := context.WithCancel(context.Background())
		defer stop()
		go volume.Watch(vol, a.VolumeChanged)
	}
	rec.Mark("window")

	// A signal from the launcher ends the run like a quit, so the queue is saved.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	// The first signal quits; if that hangs, or a second signal comes, the
	// process exits regardless.
	go func() {
		<-sig
		a.Quit()
		select {
		case <-sig:
			log.Warn("second signal, exiting")
		case <-time.After(3 * time.Second):
			log.Error("quit timed out")
		}
		os.Exit(1)
	}()
	return a.Run()
}

// programDir is the program's folder: the pak on the Brick, dist/desktop on a
// desktop. "" when it can't be found.
func programDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// quickJS is QuickJS, yt-dlp's JavaScript runtime, as the pak and the
// desktop build put it: bin/qjs in the program's folder, dir. "" when it
// isn't there.
func quickJS(dir string) string {
	if dir == "" {
		return ""
	}
	p := filepath.Join(dir, "bin", "qjs")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

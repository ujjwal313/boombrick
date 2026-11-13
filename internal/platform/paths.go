// Package platform holds device integration: the firmware Boombrick runs
// under and where its data lives, the battery, network and Bluetooth state
// the status bar shows, power, the volume and the screen. Input is in a
// subpackage.
package platform

import (
	"os"
	"path/filepath"

	"github.com/ujjwal313/boombrick/internal/platform/input"
)

// Firmware is what runs Boombrick: one of the Brick's firmwares, or a
// desktop.
type Firmware uint8

const (
	Desktop Firmware = iota
	NextUI
	CrossMix
	Knulli
)

func (f Firmware) String() string {
	switch f {
	case NextUI:
		return "NextUI"
	case CrossMix:
		return "CrossMix"
	case Knulli:
		return "Knulli"
	}
	return "desktop"
}

// Paths are the directories Boombrick reads and writes.
type Paths struct {
	Data     string // database, caches, credentials
	Logs     string
	Music    string // scanned library root
	OnDevice bool
}

// Profile is what differs from one firmware to another: where Boombrick
// keeps its files, how it powers the device off, and the release file that
// updates it.
type Profile struct {
	Firmware Firmware
	Paths    Paths
	// ReleaseAsset is the release file self-update installs, ReleaseDir the
	// folder in its zip that holds the app's own files, and Launcher the
	// script there that starts it; all "" on a desktop.
	ReleaseAsset, ReleaseDir, Launcher string
	tmp                                string // where the power-off files go: /tmp
	run                                string // Knulli's /var/run
}

// Detect works out the firmware from what its launcher passes. The launch
// scripts of CrossMix and Knulli, Boombrick's own, set BOOMBRICK_PLATFORM;
// NextUI passes USERDATA_PATH and its other paths; a desktop passes
// neither, and $BOOMBRICK_DATA (default dist/desktop/data) holds
// everything. dir is the program's folder, which on CrossMix and Knulli
// holds Boombrick's data, as their own apps and ports keep theirs.
func Detect(dir string) Profile {
	if os.Getenv("BOOMBRICK_PLATFORM") == "knulli" {
		if dir == "" {
			dir = "/userdata/roms/ports/boombrick"
		}
		return Profile{Firmware: Knulli, tmp: tmpDir(), run: envOr("BOOMBRICK_RUN", "/var/run"),
			Paths: Paths{
				Data:     filepath.Join(dir, "data"),
				Logs:     filepath.Join(dir, "logs"),
				Music:    "/userdata/music",
				OnDevice: true,
			},
			ReleaseAsset: "Boombrick-Knulli.zip", ReleaseDir: "roms/ports/boombrick/", Launcher: "launch"}
	}
	if os.Getenv("BOOMBRICK_PLATFORM") == "crossmix" {
		if dir == "" {
			dir = "/mnt/SDCARD/Apps/Boombrick"
		}
		return Profile{Firmware: CrossMix, tmp: tmpDir(),
			Paths: Paths{
				Data:     filepath.Join(dir, "data"),
				Logs:     filepath.Join(dir, "logs"),
				Music:    filepath.Join(envOr("SDCARD_PATH", "/mnt/SDCARD"), "Music"),
				OnDevice: true,
			},
			ReleaseAsset: "Boombrick-CrossMix.zip", ReleaseDir: "Apps/Boombrick/", Launcher: "launch.sh"}
	}
	if ud := os.Getenv("USERDATA_PATH"); ud != "" {
		return Profile{Firmware: NextUI, tmp: tmpDir(),
			Paths: Paths{
				Data:     filepath.Join(ud, "Boombrick"),
				Logs:     envOr("LOGS_PATH", filepath.Join(ud, "logs")),
				Music:    filepath.Join(envOr("SDCARD_PATH", "/mnt/SDCARD"), "Music"),
				OnDevice: true,
			},
			ReleaseAsset: "Boombrick.pak.zip", ReleaseDir: "Tools/tg5040/Boombrick.pak/", Launcher: "launch.sh"}
	}
	base := envOr("BOOMBRICK_DATA", filepath.Join("dist", "desktop", "data"))
	return Profile{Firmware: Desktop, tmp: tmpDir(), Paths: Paths{Data: base, Logs: base, Music: filepath.Join(base, "Music")}}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// tmpDir is where the power-off flags, the background player's lock and
// CrossMix's keep-awake file go: /tmp, or $BOOMBRICK_TMP, as CrossMix's
// launch.sh takes it too.
func tmpDir() string { return envOr("BOOMBRICK_TMP", "/tmp") }

// PlayerLock is the lock the background player holds while it owns the queue.
func (p Profile) PlayerLock() string { return filepath.Join(p.tmp, "boombrick-player.lock") }

// StayAwake is the file that keeps the device awake while it exists: on
// CrossMix its keep-awake file, and on Knulli a pause file for its battery
// saver, which otherwise mutes the sound after a few minutes without a
// button pressed, then suspends. "" under other firmwares, and on a Knulli
// without the battery saver.
func (p Profile) StayAwake() string {
	switch p.Firmware {
	case CrossMix:
		return filepath.Join(p.tmp, "stay_awake")
	case Knulli:
		dir := filepath.Join(p.run, "battery-saver")
		if _, err := os.Stat(dir); err != nil {
			return ""
		}
		return filepath.Join(dir, "boombrick.pause")
	}
	return ""
}

// Pads is how the firmware hands over the Brick's buttons: on Knulli as
// game controllers, by the mapping it gives each port, which names the
// buttons by place unless its Xbox layout for ports is switched on.
func (p Profile) Pads() input.Layout {
	if p.Firmware != Knulli {
		return input.Layout{}
	}
	_, err := os.Stat(filepath.Join(p.run, "ports_xbox_layout.flag"))
	return input.Layout{Controllers: true, Labelled: err == nil}
}

// FirmwarePower reports whether the firmware keeps Power for itself, as
// Knulli suspends with it and shuts down when it is held.
func (p Profile) FirmwarePower() bool { return p.Firmware == Knulli }

// QuitChord reports whether Start pressed with Menu held quits, as Knulli's
// ports quit with Function, the Brick's Menu, and Start.
func (p Profile) QuitChord() bool { return p.Firmware == Knulli }

// MusicPlace is where the music folder is, in words, as the empty library
// says it: on Knulli the music folder beside roms in the SHARE partition,
// as the card shows it on a computer. "" leaves the SD card's Music folder.
func (p Profile) MusicPlace() string {
	if p.Firmware == Knulli {
		return "the music folder beside roms"
	}
	return ""
}

package platform

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ujjwal313/boombrick/internal/platform/input"
)

func TestDetectNextUI(t *testing.T) {
	t.Setenv("BOOMBRICK_TMP", "")
	t.Setenv("BOOMBRICK_PLATFORM", "")
	t.Setenv("USERDATA_PATH", "/mnt/SDCARD/.userdata/tg5040")
	t.Setenv("SDCARD_PATH", "/mnt/SDCARD")
	t.Setenv("LOGS_PATH", "/mnt/SDCARD/.userdata/tg5040/logs")
	want := Profile{Firmware: NextUI, tmp: "/tmp",
		Paths: Paths{
			Data:     "/mnt/SDCARD/.userdata/tg5040/Boombrick",
			Logs:     "/mnt/SDCARD/.userdata/tg5040/logs",
			Music:    "/mnt/SDCARD/Music",
			OnDevice: true,
		},
		ReleaseAsset: "Boombrick.pak.zip", ReleaseDir: "Tools/tg5040/Boombrick.pak/", Launcher: "launch.sh"}
	if got := Detect("/mnt/SDCARD/Tools/tg5040/Boombrick.pak"); got != want {
		t.Fatalf("Detect() = %+v, want %+v", got, want)
	}
}

// CrossMix passes no paths: Boombrick keeps its data in its app folder, as
// CrossMix's own apps do.
func TestDetectCrossMix(t *testing.T) {
	t.Setenv("BOOMBRICK_TMP", "")
	t.Setenv("BOOMBRICK_PLATFORM", "crossmix")
	t.Setenv("USERDATA_PATH", "")
	t.Setenv("SDCARD_PATH", "")
	want := Profile{Firmware: CrossMix, tmp: "/tmp",
		Paths: Paths{
			Data:     "/mnt/SDCARD/Apps/Boombrick/data",
			Logs:     "/mnt/SDCARD/Apps/Boombrick/logs",
			Music:    "/mnt/SDCARD/Music",
			OnDevice: true,
		},
		ReleaseAsset: "Boombrick-CrossMix.zip", ReleaseDir: "Apps/Boombrick/", Launcher: "launch.sh"}
	if got := Detect("/mnt/SDCARD/Apps/Boombrick"); got != want {
		t.Fatalf("Detect() = %+v, want %+v", got, want)
	}
	if got := Detect(""); got != want {
		t.Fatalf("Detect without the program's folder = %+v, want %+v", got, want)
	}
	if s := want.Firmware.String(); s != "CrossMix" {
		t.Fatalf("logged as %q", s)
	}
}

// Knulli runs Boombrick as a port, which keeps its data in its folder under
// roms/ports. The music is in the SHARE partition's music folder.
func TestDetectKnulli(t *testing.T) {
	t.Setenv("BOOMBRICK_TMP", "")
	t.Setenv("BOOMBRICK_RUN", "")
	t.Setenv("BOOMBRICK_PLATFORM", "knulli")
	t.Setenv("USERDATA_PATH", "")
	want := Profile{Firmware: Knulli, tmp: "/tmp", run: "/var/run",
		Paths: Paths{
			Data:     "/userdata/roms/ports/boombrick/data",
			Logs:     "/userdata/roms/ports/boombrick/logs",
			Music:    "/userdata/music",
			OnDevice: true,
		},
		ReleaseAsset: "Boombrick-Knulli.zip", ReleaseDir: "roms/ports/boombrick/", Launcher: "launch"}
	if got := Detect("/userdata/roms/ports/boombrick"); got != want {
		t.Fatalf("Detect() = %+v, want %+v", got, want)
	}
	if got := Detect(""); got != want {
		t.Fatalf("Detect without the program's folder = %+v, want %+v", got, want)
	}
	if s := want.Firmware.String(); s != "Knulli" {
		t.Fatalf("logged as %q", s)
	}
}

// Knulli hands a port its pads' mappings, which name the buttons by place
// unless its Xbox layout for ports is switched on. Under the other
// firmwares the buttons are read by number.
func TestPads(t *testing.T) {
	run := t.TempDir()
	t.Setenv("BOOMBRICK_RUN", run)
	t.Setenv("BOOMBRICK_PLATFORM", "knulli")
	p := Detect("")
	if got := p.Pads(); got != (input.Layout{Controllers: true}) {
		t.Fatalf("Knulli: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(run, "ports_xbox_layout.flag"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := p.Pads(); got != (input.Layout{Controllers: true, Labelled: true}) {
		t.Fatalf("Knulli with the Xbox layout: %+v", got)
	}
	t.Setenv("BOOMBRICK_PLATFORM", "")
	t.Setenv("USERDATA_PATH", "/mnt/SDCARD/.userdata/tg5040")
	if got := Detect("").Pads(); got != (input.Layout{}) {
		t.Fatalf("NextUI: %+v", got)
	}
}

// Knulli suspends with Power and shuts down when it is held; the other
// firmwares leave it to Boombrick.
func TestFirmwarePower(t *testing.T) {
	for f, want := range map[Firmware]bool{NextUI: false, CrossMix: false, Desktop: false, Knulli: true} {
		if got := (Profile{Firmware: f}).FirmwarePower(); got != want {
			t.Errorf("%v: %v", f, got)
		}
	}
}

// Knulli's ports quit with Function, the Brick's Menu, and Start.
func TestQuitChord(t *testing.T) {
	for f, want := range map[Firmware]bool{NextUI: false, CrossMix: false, Desktop: false, Knulli: true} {
		if got := (Profile{Firmware: f}).QuitChord(); got != want {
			t.Errorf("%v: %v", f, got)
		}
	}
}

// Knulli's music folder is beside roms; the others say the SD card's Music
// folder.
func TestMusicPlace(t *testing.T) {
	for f, want := range map[Firmware]string{NextUI: "", CrossMix: "", Desktop: "", Knulli: "the music folder beside roms"} {
		if got := (Profile{Firmware: f}).MusicPlace(); got != want {
			t.Errorf("%v: %q", f, got)
		}
	}
}

// Knulli's battery saver mutes the sound after five minutes without a
// button pressed, then suspends; a pause file in its folder holds it off.
// Without the battery saver there is nothing to hold off.
func TestKnulliStaysAwake(t *testing.T) {
	run := t.TempDir()
	t.Setenv("BOOMBRICK_RUN", run)
	t.Setenv("BOOMBRICK_PLATFORM", "knulli")
	p := Detect("")
	if got := p.StayAwake(); got != "" {
		t.Fatalf("without the battery saver: %q", got)
	}
	if err := os.Mkdir(filepath.Join(run, "battery-saver"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, want := p.StayAwake(), filepath.Join(run, "battery-saver", "boombrick.pause"); got != want {
		t.Fatalf("StayAwake() = %q, want %q", got, want)
	}
}

func TestDetectDesktop(t *testing.T) {
	t.Setenv("BOOMBRICK_TMP", "")
	t.Setenv("BOOMBRICK_PLATFORM", "")
	t.Setenv("USERDATA_PATH", "")
	t.Setenv("BOOMBRICK_DATA", "/tmp/bb")
	want := Profile{Firmware: Desktop, tmp: "/tmp", Paths: Paths{Data: "/tmp/bb", Logs: "/tmp/bb", Music: "/tmp/bb/Music"}}
	if got := Detect("/home/me/boombrick/dist/desktop"); got != want {
		t.Fatalf("Detect() = %+v, want %+v", got, want)
	}
}

// The background player's lock and CrossMix's keep-awake file are in /tmp,
// or $BOOMBRICK_TMP, as tests and CrossMix's launch.sh use it.
func TestTempFiles(t *testing.T) {
	t.Setenv("USERDATA_PATH", "")
	t.Setenv("BOOMBRICK_PLATFORM", "crossmix")
	t.Setenv("BOOMBRICK_TMP", "")
	p := Detect("/mnt/SDCARD/Apps/Boombrick")
	if p.PlayerLock() != "/tmp/boombrick-player.lock" || p.StayAwake() != "/tmp/stay_awake" {
		t.Fatalf("CrossMix: %q, %q", p.PlayerLock(), p.StayAwake())
	}
	t.Setenv("USERDATA_PATH", "/mnt/SDCARD/.userdata/tg5040")
	t.Setenv("BOOMBRICK_PLATFORM", "")
	p = Detect("/mnt/SDCARD/Tools/tg5040/Boombrick.pak")
	if p.PlayerLock() != "/tmp/boombrick-player.lock" || p.StayAwake() != "" {
		t.Fatalf("NextUI: %q, %q", p.PlayerLock(), p.StayAwake())
	}
	t.Setenv("USERDATA_PATH", "")
	t.Setenv("BOOMBRICK_TMP", "/run/bb")
	p = Detect("")
	if p.PlayerLock() != "/run/bb/boombrick-player.lock" || p.StayAwake() != "" {
		t.Fatalf("desktop: %q, %q", p.PlayerLock(), p.StayAwake())
	}
}

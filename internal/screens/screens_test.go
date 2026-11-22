package screens

import "testing"

// The empty library names the music folder as the firmware has it.
func TestEmptyLibraryText(t *testing.T) {
	t.Cleanup(func() { SetMusicPlace("") })
	SetMusicPlace("the music folder beside roms")
	if got, want := EmptyLibraryText(), "Copy music to the music folder beside roms, or find something on YouTube Music."; got != want {
		t.Fatalf("Knulli: %q, want %q", got, want)
	}
	SetMusicPlace("")
	if got, want := EmptyLibraryText(), "Copy music to the Music folder on the SD card, or find something on YouTube Music."; got != want {
		t.Fatalf("default: %q, want %q", got, want)
	}
}

// tabber records the tabs asked for.
type tabber struct{ shown []int }

func (t *tabber) Push(Screen)     {}
func (t *tabber) Pop()            {}
func (t *tabber) Toast(string)    {}
func (t *tabber) ShowTab(tab int) { t.shown = append(t.shown, tab) }

// With YouTube switched off the empty library doesn't send anyone to it, and
// nothing opens the Downloads tab.
func TestWithoutYouTube(t *testing.T) {
	t.Cleanup(func() { SetDownloads(true) })
	SetDownloads(false)
	if got, want := EmptyLibraryText(), "Copy music to the Music folder on the SD card."; got != want {
		t.Fatalf("empty library: %q, want %q", got, want)
	}
	var tb tabber
	if OpenDownloads(&tb) || len(tb.shown) != 0 {
		t.Fatalf("opened Downloads with YouTube off: %v", tb.shown)
	}
	SetDownloads(true)
	if !OpenDownloads(&tb) || len(tb.shown) != 1 || tb.shown[0] != DownloadsTab {
		t.Fatalf("with YouTube on: %v", tb.shown)
	}
}

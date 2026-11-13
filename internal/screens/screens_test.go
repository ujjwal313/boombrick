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

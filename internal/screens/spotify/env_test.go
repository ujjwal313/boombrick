package spotify

import (
	"testing"

	"github.com/ujjwal313/boombrick/internal/importer"
)

// Importing needs YouTube switched on as well as Wi-Fi.
func TestCanImportFollowsTheSwitch(t *testing.T) {
	on := true
	e := &Env{Import: &fakeImporter{jobs: map[string]importer.Job{}}, Importing: func() bool { return on }}
	if !e.CanImport() {
		t.Fatal("with importing on, an import may start")
	}
	on = false
	if e.CanImport() {
		t.Fatal("with YouTube off, nothing should offer an import")
	}
}

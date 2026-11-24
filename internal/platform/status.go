package platform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Status is the device state the status bar shows.
type Status struct {
	Battery   int    // charge in percent, or -1 when there is no battery
	Charging  bool   // the battery is being charged, or is full on the charger
	Online    bool   // a network interface other than loopback is up
	Bluetooth string // the connected audio device, or ""
}

// ReadStatus reads the battery and the network from the sysfs tree at sys,
// normally "/sys". The battery is the first power supply of type Battery; on
// the Brick that is axp2202-battery. It is charging when its status says
// Charging, or Full, which it reads on the charger.
func ReadStatus(sys string) Status {
	st := Status{Battery: -1}
	supplies := filepath.Join(sys, "class", "power_supply")
	entries, _ := os.ReadDir(supplies)
	for _, e := range entries {
		dir := filepath.Join(supplies, e.Name())
		if readLine(dir, "type") != "Battery" {
			continue
		}
		if n, err := strconv.Atoi(readLine(dir, "capacity")); err == nil {
			st.Battery = min(max(n, 0), 100)
		}
		switch readLine(dir, "status") {
		case "Charging", "Full":
			st.Charging = true
		}
		break
	}
	st.Online = Online(sys)
	return st
}

// Online reports whether a network interface other than loopback is up, from
// the sysfs tree at sys. It reads only the network, for the services that ask
// before going online.
func Online(sys string) bool {
	nets := filepath.Join(sys, "class", "net")
	entries, _ := os.ReadDir(nets)
	for _, e := range entries {
		if e.Name() != "lo" && readLine(filepath.Join(nets, e.Name()), "operstate") == "up" {
			return true
		}
	}
	return false
}

// readLine is the contents of a one-line sysfs file, or "" if it can't be
// read.
func readLine(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

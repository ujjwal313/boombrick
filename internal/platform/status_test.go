package platform

import (
	"os"
	"path/filepath"
	"testing"
)

// sysfs writes files under a fake /sys: each key is a path below class/.
func sysfs(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, "class", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestReadStatus(t *testing.T) {
	for _, c := range []struct {
		name  string
		files map[string]string
		want  Status
	}{
		{"nothing there", nil, Status{Battery: -1}},
		{"the Brick on Wi-Fi", map[string]string{
			"power_supply/axp2202-battery/type":     "Battery",
			"power_supply/axp2202-battery/capacity": "57",
			"power_supply/axp2202-usb/type":         "USB",
			"power_supply/axp2202-usb/online":       "1",
			"net/lo/operstate":                      "unknown",
			"net/wlan0/operstate":                   "up",
		}, Status{Battery: 57, Online: true}},
		{"charging", map[string]string{
			"power_supply/axp2202-battery/type":     "Battery",
			"power_supply/axp2202-battery/capacity": "77",
			"power_supply/axp2202-battery/status":   "Charging",
		}, Status{Battery: 77, Charging: true}},
		{"full on the charger", map[string]string{
			"power_supply/axp2202-battery/type":     "Battery",
			"power_supply/axp2202-battery/capacity": "100",
			"power_supply/axp2202-battery/status":   "Full",
		}, Status{Battery: 100, Charging: true}},
		{"on the battery", map[string]string{
			"power_supply/axp2202-battery/type":     "Battery",
			"power_supply/axp2202-battery/capacity": "40",
			"power_supply/axp2202-battery/status":   "Discharging",
		}, Status{Battery: 40}},
		{"not charging", map[string]string{
			"power_supply/axp2202-battery/type":     "Battery",
			"power_supply/axp2202-battery/capacity": "80",
			"power_supply/axp2202-battery/status":   "Not charging",
		}, Status{Battery: 80}},
		{"a supply before the battery is skipped", map[string]string{
			"power_supply/AC/type":       "Mains",
			"power_supply/AC/capacity":   "100",
			"power_supply/BAT0/type":     "Battery",
			"power_supply/BAT0/capacity": "8",
		}, Status{Battery: 8}},
		{"the first battery counts", map[string]string{
			"power_supply/BAT0/type":     "Battery",
			"power_supply/BAT0/capacity": "8",
			"power_supply/BAT1/type":     "Battery",
			"power_supply/BAT1/capacity": "90",
		}, Status{Battery: 8}},
		{"no battery", map[string]string{
			"power_supply/usb/type":     "USB",
			"power_supply/usb/capacity": "90",
		}, Status{Battery: -1}},
		{"loopback alone is offline", map[string]string{
			"net/lo/operstate": "up",
		}, Status{Battery: -1}},
		{"Wi-Fi down is offline", map[string]string{
			"net/lo/operstate":    "unknown",
			"net/wlan0/operstate": "down",
		}, Status{Battery: -1}},
	} {
		sys := sysfs(t, c.files)
		if got := ReadStatus(sys); got != c.want {
			t.Errorf("%s: ReadStatus = %+v, want %+v", c.name, got, c.want)
		}
		if got := Online(sys); got != c.want.Online {
			t.Errorf("%s: Online = %v, want %v", c.name, got, c.want.Online)
		}
	}
}

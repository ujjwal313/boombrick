// Package quickmenu is the quick menu Menu opens over any screen: the sleep
// timer, the screen, playing in the background, Settings and quitting.
package quickmenu

import (
	"slices"
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Actions are what the menu's rows do.
type Actions struct {
	Sleep        func() player.Sleep
	SetSleep     func(m player.SleepMode)
	ScreenOff    func()
	OpenSettings func()
	Quit         func()
	// Playing reports whether music plays, as Play in background shows only
	// then; PlayInBackground hands the music over. Without it the row never
	// shows.
	Playing          func() bool
	PlayInBackground func()
	// Live reports whether a station plays, as the sleep timer then leaves out
	// End of track and End of album. Without it none does.
	Live func() bool
}

// The rows, top to bottom.
const (
	rowSleep = iota
	rowScreen
	rowBackground
	rowSettings
	rowQuit
	rowCount
)

// Layout: the dialog's panel, padding 36 40, the title, then the rows.
const (
	padTop  = 36
	padSide = 20 // the rows' own padding brings their text in to the title's 40
)

var title = text.Style{Face: text.OutfitSemiBold, Size: 42}

// Menu is the quick menu. It is drawn over the rest and takes all input
// while it is open.
type Menu struct {
	act     Actions
	open    bool
	shown   anim.Value
	focus   int
	k       [rowCount]anim.Value
	rows    [rowCount]widgets.Setting
	sleep   player.Sleep // the sleep timer as the sleep row last showed it
	at      time.Time    // and when, for its time left
	hints   [rowCount][]widgets.Hint
	visible []int // the rows shown, as they were when the menu opened
}

// New returns the menu, closed.
func New(act Actions) *Menu {
	m := &Menu{act: act}
	m.rows = [rowCount]widgets.Setting{
		rowSleep:      {Icon: "moon", Label: "Sleep timer"},
		rowScreen:     {Icon: "power", Label: "Screen off"},
		rowBackground: {Icon: "music", Label: "Play in background"},
		rowSettings:   {Icon: "settings", Label: "Settings", Chevron: true},
		rowQuit:       {Icon: "logout", Label: "Quit Boombrick"},
	}
	m.hints = [rowCount][]widgets.Hint{
		rowSleep:      {{Key: "◀ ▶", Label: "Change"}, {Key: "B", Label: "Close"}},
		rowScreen:     {{Key: "A", Label: "Turn off"}, {Key: "B", Label: "Close"}},
		rowBackground: {{Key: "A", Label: "Play in background"}, {Key: "B", Label: "Close"}},
		rowSettings:   {{Key: "A", Label: "Open"}, {Key: "B", Label: "Close"}},
		rowQuit:       {{Key: "A", Label: "Quit"}, {Key: "B", Label: "Close"}},
	}
	return m
}

// Open shows the menu with its first row focused.
func (m *Menu) Open(now time.Time) {
	m.visible = m.visible[:0]
	for r := range rowCount {
		if r != rowBackground || m.act.PlayInBackground != nil && m.act.Playing != nil && m.act.Playing() {
			m.visible = append(m.visible, r)
		}
	}
	m.open, m.focus = true, rowSleep
	for i := range m.k {
		m.k[i].Set(0)
	}
	m.k[rowSleep].Set(1)
	m.shown.To(1, now, anim.Dialog, anim.EaseOutCubic)
}

func (m *Menu) close(now time.Time) {
	m.open = false
	m.shown.To(0, now, anim.Dialog, anim.EaseOutCubic)
}

// IsOpen reports whether the menu takes input.
func (m *Menu) IsOpen() bool { return m.open }

// Visible reports whether the menu is drawn: open, or fading out.
func (m *Menu) Visible(now time.Time) bool { return m.open || m.shown.At(now) > 0 }

// Handle moves between the rows, acts with A, changes the sleep timer with
// the D-pad, and closes with B or Menu.
func (m *Menu) Handle(ev input.Event, now time.Time) bool {
	if !m.open || ev.Kind == input.Release {
		return false
	}
	switch ev.Button {
	case input.Up:
		return m.move(-1, now)
	case input.Down:
		return m.move(1, now)
	case input.Left, input.Right:
		if m.focus != rowSleep {
			return false
		}
		step := 1
		if ev.Button == input.Left {
			step = -1
		}
		m.act.SetSleep(screens.NextSleep(m.act.Sleep().Mode, step, m.live()))
		return true
	}
	if ev.Kind != input.Press {
		return false
	}
	switch ev.Button {
	case input.A:
		m.do(now)
	case input.B, input.Menu:
		m.close(now)
	default:
		return false
	}
	return true
}

func (m *Menu) move(step int, now time.Time) bool {
	i := slices.Index(m.visible, m.focus) + step
	if i < 0 || i >= len(m.visible) {
		return false
	}
	next := m.visible[i]
	m.k[m.focus].To(0, now, anim.Focus, anim.EaseOutCubic)
	m.focus = next
	m.k[next].To(1, now, anim.Focus, anim.EaseOutCubic)
	return true
}

// do acts on the focused row. The menu closes for the screen, the background,
// Settings and quitting; the sleep timer steps on to its next setting.
func (m *Menu) do(now time.Time) {
	switch m.focus {
	case rowSleep:
		m.act.SetSleep(screens.NextSleep(m.act.Sleep().Mode, 1, m.live()))
	case rowScreen:
		m.close(now)
		m.act.ScreenOff()
	case rowBackground:
		m.close(now)
		m.act.PlayInBackground()
	case rowSettings:
		m.close(now)
		m.act.OpenSettings()
	case rowQuit:
		m.act.Quit()
	}
}

// Animating reports whether the menu or its focus is moving.
func (m *Menu) Animating(now time.Time) bool {
	if !m.shown.Done(now) {
		return true
	}
	for i := range m.k {
		if !m.k[i].Done(now) {
			return true
		}
	}
	return false
}

// Due is when the sleep timer's time left, which the menu shows while it is
// open, next changes after t.
func (m *Menu) Due(t time.Time) (time.Time, bool) {
	if !m.open {
		return time.Time{}, false
	}
	return screens.SleepDue(m.act.Sleep(), t)
}

// Draw draws the menu over the frame drawn so far, and its hints.
func (m *Menu) Draw(u *ui.Context) {
	shown := float32(m.shown.At(u.Now))
	if shown <= 0 {
		return
	}
	m.refresh(u.Now)
	h := padTop + u.LineHeight(title) + 20 + float32(len(m.visible)*widgets.SettingHeight) + padTop
	p := widgets.BeginPanel(u, h, shown)
	box := p.Box
	y := box.Y + padTop
	u.Text("Quick menu", title, box.X+40, y, theme.Paper)
	y += u.LineHeight(title) + 20
	for n, i := range m.visible {
		row := gfx.Rect{X: box.X + padSide, Y: y, W: box.W - 2*padSide, H: widgets.SettingHeight}
		widgets.DrawSetting(u, m.rows[i], row, float32(m.k[i].At(u.Now)), n == len(m.visible)-1)
		y += widgets.SettingHeight
	}
	p.End(u)
	if m.open {
		// The menu's hints replace the screen's, which would show through.
		u.Fill(gfx.Rect{Y: theme.ScreenH - widgets.HintsHeight, W: theme.ScreenW, H: widgets.HintsHeight}, theme.Ground)
		widgets.HintBar(u, m.hints[m.focus])
	}
}

// refresh writes the sleep row's value again when the timer, or the whole
// seconds of its time left, have changed since it was last written.
func (m *Menu) refresh(now time.Time) {
	s := m.act.Sleep()
	if m.rows[rowSleep].Value != "" && s == m.sleep {
		if due, ok := screens.SleepDue(s, m.at); !ok || now.Before(due) {
			return
		}
	}
	m.sleep, m.at = s, now
	m.rows[rowSleep].Value = screens.SleepValue(s, now)
	m.rows[rowSleep].Active = s.Mode != player.SleepOff
}

// live reports whether a station plays.
func (m *Menu) live() bool { return m.act.Live != nil && m.act.Live() }

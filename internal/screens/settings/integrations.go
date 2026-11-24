package settings

import (
	"strconv"
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Integration is a switch on the Integrations page: the settings key that
// holds it, and the row that shows it.
type Integration struct {
	Key, Name, Icon, About string
}

// integrationsPage is the Integrations page: a row for each integration, its
// name over a line about it, and A switching it on or off.
type integrationsPage struct {
	focus int
	k     []anim.Value
	rows  []widgets.Setting
	hints []widgets.Hint
}

func newIntegrationsPage(s *Screen) *integrationsPage {
	p := &integrationsPage{k: make([]anim.Value, len(s.opts.Integrations)),
		hints: []widgets.Hint{{Key: "A", Label: "Switch"}, {Key: "B", Label: "Back"}}}
	p.k[0].Set(1)
	p.read(s)
	return p
}

// read writes the rows from the switches.
func (p *integrationsPage) read(s *Screen) {
	p.rows = p.rows[:0]
	for _, in := range s.opts.Integrations {
		p.rows = append(p.rows, widgets.Setting{Icon: in.Icon, Label: in.Name, Detail: in.About, Value: onOff(s.on(in.Key))})
	}
}

// handle moves between the rows and switches the focused one with A.
func (p *integrationsPage) handle(s *Screen, ev input.Event, now time.Time) bool {
	if ev.Kind == input.Release {
		return false
	}
	switch ev.Button {
	case input.Up, input.Down:
		next := p.focus + 1
		if ev.Button == input.Up {
			next = p.focus - 1
		}
		if next < 0 || next >= len(p.rows) {
			return false
		}
		p.k[p.focus].To(0, now, anim.Focus, anim.EaseOutCubic)
		p.focus = next
		p.k[next].To(1, now, anim.Focus, anim.EaseOutCubic)
		return true
	case input.A:
		if ev.Kind != input.Press {
			return false
		}
		in := s.opts.Integrations[p.focus]
		on := !s.on(in.Key)
		s.opts.Prefs.SetBool(in.Key, on)
		s.readPrefs() // first: the rows read the switches it keeps
		p.read(s)
		if s.opts.Switched != nil {
			s.opts.Switched(in.Key, on)
		}
		return true
	}
	return false
}

func (p *integrationsPage) animating(now time.Time) bool {
	for i := range p.k {
		if !p.k[i].Done(now) {
			return true
		}
	}
	return false
}

func (p *integrationsPage) draw(s *Screen, u *ui.Context) {
	widgets.StatusBar(u)
	header(u, "Integrations")
	y := listTop(u)
	for i, r := range p.rows {
		box := gfx.Rect{X: side - rowInset, Y: y, W: theme.ScreenW - 2*(side-rowInset), H: widgets.SettingDetailHeight}
		widgets.DrawSetting(u, r, box, float32(p.k[i].At(u.Now)), i == len(p.rows)-1)
		y += box.H
	}
	widgets.HintBar(u, p.hints)
}

// on reports whether the integration whose switch is key is on, as the
// switches were last read.
func (s *Screen) on(key string) bool {
	if v, ok := s.switches[key]; ok {
		return v
	}
	return s.opts.Prefs.Bool(key)
}

// switchedOn is the Integrations row's value: "2 of 2 on".
func (s *Screen) switchedOn() string {
	n := 0
	for _, in := range s.opts.Integrations {
		if s.on(in.Key) {
			n++
		}
	}
	return strconv.Itoa(n) + " of " + strconv.Itoa(len(s.opts.Integrations)) + " on"
}

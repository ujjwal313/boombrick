// Package settings is the Settings screen and its Storage page, opened from
// the quick menu. It shows only the settings whose feature exists.
package settings

import (
	"time"

	"github.com/ujjwal313/boombrick/internal/platform/input"
	"github.com/ujjwal313/boombrick/internal/player"
	"github.com/ujjwal313/boombrick/internal/screens"
	keys "github.com/ujjwal313/boombrick/internal/settings"
	"github.com/ujjwal313/boombrick/internal/space"
	"github.com/ujjwal313/boombrick/internal/spotify"
	"github.com/ujjwal313/boombrick/internal/theme"
	"github.com/ujjwal313/boombrick/internal/ui"
	"github.com/ujjwal313/boombrick/internal/ui/anim"
	"github.com/ujjwal313/boombrick/internal/ui/gfx"
	"github.com/ujjwal313/boombrick/internal/ui/text"
	"github.com/ujjwal313/boombrick/internal/ui/widgets"
)

// Prefs is the settings store. *settings.Settings is one.
type Prefs interface {
	String(key string) string
	Bool(key string) bool
	Set(key, value string)
	SetBool(key string, v bool)
}

// Player is what Settings changes in the player. *player.Player is one.
type Player interface {
	Sleep() player.Sleep
	SetSleep(m player.SleepMode)
	SetLevelling(on bool)
}

// Space measures and clears the caches for the Storage page. *space.Meter is
// one.
type Space interface {
	Measure()
	Sizes() (space.Sizes, bool)
	Clear()
	Cleared() (int64, bool)
}

// Account is the Spotify account as Settings shows it. *spotify.Library is
// one.
type Account interface {
	Status() (spotify.Status, error)
	Logout()
}

// Options configure the screen.
type Options struct {
	Prefs   Prefs
	Player  Player
	Card    func() (total, free, music int64) // the SD card's space; nil or zeros when unknown
	Space   Space                             // nil: no Storage page
	Toast   func(msg string)
	Version string // the build's version: "0.4.0", or "dev"
	Device  string // "TrimUI Brick"

	Downloader   Downloader // nil: no Downloader card
	ToolReleases Releases   // yt-dlp's releases; nil: the card never says one is available
	Releases     Releases   // Boombrick's; nil: nor does the Boombrick card
	Updater      Updater    // nil: no Update Boombrick pill, as on a desktop
	Quit         func()     // the Update ready dialog's Quit now
	Spotify      Account    // nil: no Streaming quality, nor the Accounts card
	Copies       bool       // importing exists: Prefer downloaded copies is shown
}

// What focus moves between, top to bottom: the rows, then the cards' pills.
const (
	rowStream = iota
	rowQuality
	rowPrefer
	rowStorage
	rowSleep
	rowLevelling
	rowCount
	pillTool    = rowCount     // the Downloader card's pill
	pillApp     = rowCount + 1 // the Boombrick card's
	pillAccount = rowCount + 2 // the Spotify account's
	itemCount   = rowCount + 3
)

// Layout: a full screen, padding 40, in design pixels.
const (
	side     = 40
	rowInset = 20 // a row's own padding brings its text in to the side
	headTop  = widgets.StatusHeight + 12
	panelPad = 20
	radio    = 24
	optGap   = 8
)

var (
	labelText  = text.Style{Face: text.Figtree, Size: 28}
	aboutText  = text.Style{Face: text.Figtree, Size: 22}
	optionText = text.Style{Face: text.Figtree, Size: 28, Tabular: true}
	detailText = text.Style{Face: text.Figtree, Size: 20}
	footText   = text.Style{Face: text.Figtree, Size: 20, Tabular: true}
)

// choice is one option of a setting with options.
type choice struct {
	label, detail, value string
}

// qualities are Download quality's options.
var qualities = []choice{
	{"Best", "Opus · about 72 MB per hour", "best"},
	{"Smaller", "Opus · about 32 MB per hour", "smaller"},
}

// streams are Streaming quality's options.
var streams = []choice{
	{"96 kbps", "about 43 MB per hour", "96"},
	{"160 kbps", "about 72 MB per hour", "160"},
	{"320 kbps", "about 144 MB per hour", "320"},
}

// Screen is Settings.
type Screen struct {
	opts    Options
	storage *storagePage // non-nil while the Storage page is shown
	dialog  *confirm     // Update ready, while it is open
	focus   int
	k       [itemCount]anim.Value
	open    int // the row expanded into its options, or -1
	scroll  anim.Value
	rows    [rowCount]widgets.Setting
	stream  int // Streaming quality's choice, as last read
	quality int // and Download quality's
	sleeps  []choice
	sleep   player.Sleep // the timer as its row last showed it
	sleptAt time.Time    // and when
	card    [3]int64     // the SD card's numbers the storage row was written for
	footer  string
	tool    card
	toolFor toolState // what the Downloader card was written for
	app     card
	appFor  appState // and the Boombrick card
	staging bool     // an update was being staged when the card was written
	account card
	acctFor spotify.Status // what the Spotify card was written for
	hints   map[hintKey][]widgets.Hint
}

type hintKey struct {
	item int
	open bool
}

// New returns the screen.
func New(opts Options) *Screen {
	s := &Screen{opts: opts, open: -1, hints: map[hintKey][]widgets.Hint{}}
	s.rows = [rowCount]widgets.Setting{
		rowStream:    {Icon: "stream", Label: "Streaming quality"},
		rowQuality:   {Icon: "download", Label: "Download quality"},
		rowPrefer:    {Icon: "circlecheck", Label: "Prefer downloaded copies"},
		rowStorage:   {Icon: "sd", Label: "Storage", Chevron: true},
		rowSleep:     {Icon: "moon", Label: "Sleep timer"},
		rowLevelling: {Icon: "sliders", Label: "Volume levelling"},
	}
	for _, m := range player.SleepModes {
		s.sleeps = append(s.sleeps, choice{label: screens.SleepLabel(m)})
	}
	s.reset()
	return s
}

// Opened starts the screen afresh each time it opens, and asks whether the
// downloader and Boombrick have newer releases.
func (s *Screen) Opened() {
	s.reset()
	for _, r := range []Releases{s.opts.ToolReleases, s.opts.Releases} {
		if r != nil {
			r.Check()
		}
	}
}

// reset puts the screen on the main page, focus on the first row shown,
// nothing expanded.
func (s *Screen) reset() {
	s.storage, s.open, s.focus = nil, -1, 0
	for !s.shown(s.focus) {
		s.focus++
	}
	for i := range s.k {
		s.k[i].Set(0)
	}
	s.k[s.focus].Set(1)
	s.scroll.Set(0)
	s.readPrefs()
}

// readPrefs reads the settings kept in Prefs into the rows that show them.
// Only this screen changes them, so they are read when it opens and when it
// changes one rather than every frame.
func (s *Screen) readPrefs() {
	s.stream, s.quality = s.streamIndex(), s.qualityIndex()
	s.rows[rowStream].Value = streams[s.stream].label
	s.rows[rowQuality].Value = qualities[s.quality].label
	s.rows[rowLevelling].Value = onOff(s.opts.Prefs.Bool(keys.VolumeLevelling))
	s.rows[rowPrefer].Value = onOff(s.opts.Prefs.Bool(keys.PreferDownloaded))
}

// shown reports whether an item is shown: Storage only with something to
// measure, and a card's pill only when the card offers one.
func (s *Screen) shown(item int) bool {
	switch item {
	case rowStream:
		return s.opts.Spotify != nil
	case rowPrefer:
		return s.opts.Copies
	case rowStorage:
		return s.opts.Space != nil
	case pillTool:
		return s.opts.Downloader != nil
	case pillApp:
		return s.app.pill != ""
	case pillAccount:
		return s.account.pill != ""
	}
	return true
}

// ShowsDownloads says the Downloader card follows the download queue, for an
// update's progress.
func (s *Screen) ShowsDownloads() bool { return s.opts.Downloader != nil }

// Handle moves between the rows and pills, changes them with A and the
// D-pad, and passes to the Storage page or the dialog while one is shown.
func (s *Screen) Handle(ev input.Event, now time.Time) bool {
	if s.storage != nil {
		return s.storage.handle(s, ev, now)
	}
	if s.dialog != nil {
		if s.dialog.handle(ev, now) && s.dialog.confirmed && s.opts.Quit != nil {
			s.opts.Quit()
		}
		return true
	}
	if ev.Kind == input.Release {
		return false
	}
	switch ev.Button {
	case input.Up:
		return s.move(-1, now)
	case input.Down:
		return s.move(1, now)
	case input.Left, input.Right:
		if s.open < 0 {
			return false
		}
		step := 1
		if ev.Button == input.Left {
			step = -1
		}
		return s.choose(step)
	}
	if ev.Kind == input.Press && ev.Button == input.A {
		return s.act(now)
	}
	return false
}

// move moves focus to the next item shown, folding an expanded row.
func (s *Screen) move(step int, now time.Time) bool {
	next := s.focus + step
	for next >= 0 && next < itemCount && !s.shown(next) {
		next += step
	}
	if next < 0 || next >= itemCount {
		return false
	}
	s.open = -1
	s.k[s.focus].To(0, now, anim.Focus, anim.EaseOutCubic)
	s.focus = next
	s.k[next].To(1, now, anim.Focus, anim.EaseOutCubic)
	return true
}

// act is A on the focused item: it expands or folds a setting with options,
// opens Storage, switches levelling, or presses a card's pill.
func (s *Screen) act(now time.Time) bool {
	switch s.focus {
	case rowStream, rowQuality, rowSleep:
		if s.open == s.focus {
			s.open = -1
		} else {
			s.open = s.focus
		}
	case rowStorage:
		s.storage = newStoragePage(s.opts.Space, now)
	case rowLevelling:
		on := !s.opts.Prefs.Bool(keys.VolumeLevelling)
		s.opts.Prefs.SetBool(keys.VolumeLevelling, on)
		s.opts.Player.SetLevelling(on)
		s.readPrefs()
	case rowPrefer:
		s.opts.Prefs.SetBool(keys.PreferDownloaded, !s.opts.Prefs.Bool(keys.PreferDownloaded))
		s.readPrefs()
	case pillTool:
		if s.toolFor.d.Updating {
			return false
		}
		s.opts.Downloader.UpdateDownloader()
	case pillApp:
		r, ok := s.opts.Releases.Latest()
		if !ok || s.appFor.st.Staging {
			return false
		}
		s.opts.Updater.Stage(r)
	case pillAccount:
		s.opts.Spotify.Logout()
		s.opts.Toast("Signed out of Spotify")
	}
	return true
}

// choose moves the expanded setting's choice by step, applying it at once.
func (s *Screen) choose(step int) bool {
	switch s.open {
	case rowStream:
		i := s.stream + step
		if i < 0 || i >= len(streams) {
			return false
		}
		s.opts.Prefs.Set(keys.StreamingKbps, streams[i].value)
		s.readPrefs()
	case rowQuality:
		i := s.quality + step
		if i < 0 || i >= len(qualities) {
			return false
		}
		s.opts.Prefs.Set(keys.DownloadQuality, qualities[i].value)
		s.readPrefs()
	case rowSleep:
		i := s.sleepIndex() + step
		if i < 0 || i >= len(player.SleepModes) {
			return false
		}
		s.opts.Player.SetSleep(player.SleepModes[i])
	default:
		return false
	}
	return true
}

func (s *Screen) qualityIndex() int {
	if s.opts.Prefs.String(keys.DownloadQuality) == "smaller" {
		return 1
	}
	return 0
}

func (s *Screen) streamIndex() int {
	kbps := s.opts.Prefs.String(keys.StreamingKbps)
	for i, c := range streams {
		if c.value == kbps {
			return i
		}
	}
	return 1
}

func (s *Screen) sleepIndex() int {
	m := s.opts.Player.Sleep().Mode
	for i, x := range player.SleepModes {
		if x == m {
			return i
		}
	}
	return 0
}

// Back folds an expanded setting, closes a dialog (Later, for Update ready),
// or leaves the Storage page, before B closes Settings.
func (s *Screen) Back() bool {
	switch {
	case s.storage != nil:
		return s.storage.back(s, time.Now())
	case s.dialog != nil:
		s.dialog.close(time.Now())
		return true
	case s.open >= 0:
		s.open = -1
		return true
	}
	return false
}

// Claims keeps Start, Select and Menu while a dialog is open, so nothing
// opens over it.
func (s *Screen) Claims(b input.Button) bool {
	open := s.dialog != nil || s.storage != nil && s.storage.dialog != nil
	return open && (b == input.Start || b == input.Select || b == input.Menu)
}

// Animating reports whether focus, the scroll, a dialog or the Storage page
// is moving.
func (s *Screen) Animating(now time.Time) bool {
	if s.storage != nil {
		return s.storage.animating(now)
	}
	if !s.scroll.Done(now) || s.dialog != nil && s.dialog.animating(now) {
		return true
	}
	for i := range s.k {
		if !s.k[i].Done(now) {
			return true
		}
	}
	return false
}

// Due is when the sleep timer's time left next changes after t, while its row
// is shown.
func (s *Screen) Due(t time.Time) (time.Time, bool) {
	if s.storage != nil {
		return time.Time{}, false
	}
	return screens.SleepDue(s.opts.Player.Sleep(), t)
}

// Draw renders the screen; Settings' focus is gold, whatever the accent.
func (s *Screen) Draw(u *ui.Context) {
	acc := u.Accent
	u.Accent = theme.Fallback()
	defer func() { u.Accent = acc }()
	widgets.DrawBackground(u, widgets.FlatGround)
	if s.storage != nil {
		s.storage.draw(s, u)
		return
	}
	s.refresh(u.Now)
	widgets.StatusBar(u)
	header(u, "Settings")
	widgets.LogoMark(u, 24, theme.ScreenW-side-36, headTop+8, theme.Gold)

	top := listTop(u)
	view := gfx.Rect{X: 0, Y: top, W: theme.ScreenW, H: screens.ContentBot - top}
	// y and focus are down the list from its top, whatever the scroll; at is
	// where that top is drawn.
	at := top - float32(s.scroll.At(u.Now))
	var y, focusTop, focusBot float32
	u.PushClip(view)
	for i := range s.rows {
		if !s.shown(i) {
			continue
		}
		box := gfx.Rect{X: side - rowInset, Y: at + y, W: theme.ScreenW - 2*(side-rowInset), H: widgets.SettingHeight}
		if s.open == i {
			box.H = s.panelHeight(u, i)
			s.drawPanel(u, i, box)
		} else {
			widgets.DrawSetting(u, s.rows[i], box, float32(s.k[i].At(u.Now)), false)
		}
		if i == s.focus {
			focusTop, focusBot = y, y+box.H
		}
		y += box.H
	}
	// The cards; focus on a pill keeps its whole card in view.
	for _, c := range []struct {
		card *card
		pill int
	}{{&s.tool, pillTool}, {&s.app, pillApp}} {
		if c.card.title == "" {
			continue
		}
		y += 16
		box := gfx.Rect{X: side, Y: at + y, W: theme.ScreenW - 2*side, H: c.card.height(u)}
		c.card.draw(u, box, float32(s.k[c.pill].At(u.Now)))
		if c.pill == s.focus {
			focusTop, focusBot = y, y+box.H
		}
		y += box.H
	}
	// The Spotify account, under a heading of its own.
	if s.account.title != "" {
		y += 28
		u.Text("Accounts", text.Heading, side, at+y, theme.Paper)
		y += u.LineHeight(text.Heading) + 4
		box := gfx.Rect{X: side, Y: at + y, W: theme.ScreenW - 2*side, H: s.account.height(u)}
		s.account.draw(u, box, float32(s.k[pillAccount].At(u.Now)))
		if s.focus == pillAccount {
			focusTop, focusBot = y, y+box.H
		}
		y += box.H
	}
	y += 28
	u.Text(s.footer, footText, side, at+y, theme.Ink3)
	if s.last() {
		focusBot = y + u.LineHeight(footText) // and the last item the footer
	}
	u.PopClip()
	s.follow(focusTop, focusBot, view.H, u.Now)
	if s.dialog != nil {
		if s.dialog.gone(u.Now) {
			s.dialog = nil
		} else {
			s.dialog.draw(u)
			widgets.HintBar(u, s.dialog.hints())
			return
		}
	}
	widgets.HintBar(u, s.hint())
}

// last reports whether focus is on the last item shown.
func (s *Screen) last() bool {
	for i := s.focus + 1; i < itemCount; i++ {
		if s.shown(i) {
			return false
		}
	}
	return true
}

// onOff is a switch's value.
func onOff(on bool) string {
	if on {
		return "On"
	}
	return "Off"
}

// header draws a full screen's title, as the Queue's.
func header(u *ui.Context, title string) {
	u.Text(title, text.Title, side, headTop, theme.Paper)
}

// listTop is where the rows start, under the header.
func listTop(u *ui.Context) float32 { return headTop + u.LineHeight(text.Title) + 12 }

// follow scrolls the list, view tall, so that the focused item, from top to
// bot down it, is wholly in view, moving as little as it can. An item taller
// than the view keeps its top in view. The scroll is the same for the same
// item every frame, so it comes to rest.
func (s *Screen) follow(top, bot, view float32, now time.Time) {
	to := max(s.scroll.Target(), float64(bot)-float64(view))
	to = min(to, float64(top))
	s.scroll.To(to, now, anim.Focus, anim.EaseOutCubic)
}

// refresh writes the rows' values again when what they show has changed.
// Those of the settings in Prefs are written as the screen changes them.
func (s *Screen) refresh(now time.Time) {
	if s.opts.Card != nil {
		total, free, music := s.opts.Card()
		if c := [3]int64{total, free, music}; c != s.card || s.rows[rowStorage].Value == "" {
			s.card = c
			s.rows[rowStorage].Value = "—"
			if total > 0 {
				s.rows[rowStorage].Value = screens.Size(free) + " free"
			}
			s.footer = "Boombrick " + s.opts.Version + " · " + s.opts.Device
			if total > 0 {
				s.footer += " · SD card " + screens.Size(total)
			}
		}
	} else if s.footer == "" {
		s.footer = "Boombrick " + s.opts.Version + " · " + s.opts.Device
	}
	sl := s.opts.Player.Sleep()
	if due, ok := screens.SleepDue(sl, s.sleptAt); sl != s.sleep || s.rows[rowSleep].Value == "" || ok && !now.Before(due) {
		s.sleep, s.sleptAt = sl, now
		s.rows[rowSleep].Value = screens.SleepValue(sl, now)
		s.rows[rowSleep].Active = sl.Mode != player.SleepOff
	}
	s.refreshCards(now)
}

// refreshCards writes the cards again when what they show has changed, and
// opens Update ready when an update has just been staged.
func (s *Screen) refreshCards(now time.Time) {
	if s.opts.Downloader != nil {
		st := toolState{d: s.opts.Downloader.Downloader(), latest: latest(s.opts.ToolReleases)}
		st.days = daysSince(st.d.Installed, now)
		if st != s.toolFor || s.tool.title == "" {
			s.toolFor = st
			s.tool.setTool(st.d, st.latest, now)
		}
	}
	st := appState{latest: latest(s.opts.Releases)}
	if s.opts.Updater != nil {
		st.st = s.opts.Updater.Status()
	}
	if st != s.appFor || s.app.title == "" {
		s.appFor = st
		s.app.setApp(s.opts.Version, st.latest, st.st, s.opts.Updater != nil)
	}
	if s.staging && !st.st.Staging && st.st.Staged != "" {
		s.dialog = newConfirm(widgets.Dialog{Icon: "download", IconColor: theme.GoldSoft, Title: "Update ready",
			Body:  "It is installed the next time you open Boombrick.",
			Pills: []widgets.DialogPill{{Label: "Quit now"}, {Label: "Later"}}}, now)
	}
	s.staging = st.st.Staging
	if s.opts.Spotify != nil {
		if a, _ := s.opts.Spotify.Status(); a != s.acctFor || s.account.title == "" {
			s.acctFor = a
			s.account.setSpotify(a)
		}
	}
	if !s.shown(s.focus) {
		s.move(-1, now) // the pill went with the update it offered
	}
}

// latest is the version of r's latest release, "" while it isn't known.
func latest(r Releases) string {
	if r == nil {
		return ""
	}
	rel, ok := r.Latest()
	if !ok {
		return ""
	}
	return rel.Version()
}

// options are the choices row offers, the chosen one, the line about them and
// how many to a line.
func (s *Screen) options(row int) (choices []choice, chosen int, about string, cols int) {
	switch row {
	case rowStream:
		return streams, s.stream, "Spotify, over Wi-Fi or hotspot", 3
	case rowQuality:
		return qualities, s.quality, "YouTube Music downloads", 2
	}
	return s.sleeps, s.sleepIndex(), "Fades out, then pauses", 4
}

// optionHeight is the height of one option of choices.
func optionHeight(u *ui.Context, choices []choice) float32 {
	h := u.LineHeight(optionText)
	if choices[0].detail != "" {
		h += u.LineHeight(detailText)
	}
	return h
}

// panelHeight is the height of row expanded into its options.
func (s *Screen) panelHeight(u *ui.Context, row int) float32 {
	choices, _, _, cols := s.options(row)
	lines := (len(choices) + cols - 1) / cols
	return panelPad + u.LineHeight(labelText) + u.LineHeight(aboutText) + 14 +
		float32(lines)*optionHeight(u, choices) + float32(lines-1)*optGap + panelPad
}

// drawPanel draws row expanded in box: its label, a line about it, and its
// options in a grid, the chosen one's radio filled.
func (s *Screen) drawPanel(u *ui.Context, row int, box gfx.Rect) {
	choices, chosen, about, cols := s.options(row)
	widgets.SettingFocus(u, box, 1)
	x, y := box.X+rowInset, box.Y+panelPad
	u.Text(s.rows[row].Label, labelText, x, y, theme.Paper)
	y += u.LineHeight(labelText)
	u.Text(about, aboutText, x, y, theme.Ink3)
	y += u.LineHeight(aboutText) + 14
	cellH := optionHeight(u, choices)
	for i, c := range choices {
		// Each line shares the width out among its own options, so a short
		// last line gives long labels room.
		line, first := i/cols, i/cols*cols
		cellW := (box.W - 2*rowInset) / float32(min(cols, len(choices)-first))
		cx, cy := x+float32(i-first)*cellW, y+float32(line)*(cellH+optGap)
		ry := cy + (u.LineHeight(optionText)-radio)/2
		ring, ink := theme.PaperA(0.45), theme.Ink2
		if i == chosen {
			ring, ink = theme.Gold, theme.Paper
		}
		widgets.Radio(u, cx, ry, radio, ring, i == chosen)
		u.Text(c.label, optionText, cx+radio+12, cy, ink)
		if c.detail != "" {
			u.Text(c.detail, detailText, cx+radio+12, cy+u.LineHeight(optionText), theme.Ink2)
		}
	}
}

// hint is the hint bar for the focused item.
func (s *Screen) hint() []widgets.Hint {
	k := hintKey{item: s.focus, open: s.open == s.focus}
	if h, ok := s.hints[k]; ok {
		return h
	}
	var h []widgets.Hint
	switch {
	case k.open:
		h = []widgets.Hint{{Key: "◀ ▶", Label: "Choose"}, {Key: "A", Label: "Done"}}
	case k.item == rowStorage:
		h = []widgets.Hint{{Key: "A", Label: "Open"}, {Key: "B", Label: "Back"}}
	case k.item == rowLevelling, k.item == rowPrefer:
		h = []widgets.Hint{{Key: "A", Label: "Switch"}, {Key: "B", Label: "Back"}}
	case k.item == pillTool, k.item == pillApp:
		h = []widgets.Hint{{Key: "A", Label: "Update"}, {Key: "B", Label: "Back"}}
	case k.item == pillAccount:
		h = []widgets.Hint{{Key: "A", Label: "Sign out"}, {Key: "B", Label: "Back"}}
	default:
		h = []widgets.Hint{{Key: "A", Label: "Change"}, {Key: "B", Label: "Back"}}
	}
	s.hints[k] = h
	return h
}

// Package tv holds the tview front end. It reads the state that the core
// keeps, and it sends the key presses back as commands. The core does not
// know that this package exists.
package tv

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"playah/internal/directory"
	"playah/internal/radio"
)

// redrawInterval limits the draw rate. mpv sends the cache duration and
// the level many times each second, and a draw for each one wastes CPU.
// The value also sets the speed of the level meter.
const redrawInterval = 150 * time.Millisecond

// topHeight is the height of the top section in screen cells. The level
// meter sets it, and the info panel beside it stands just as tall.
const topHeight = meterHeight

// volumeStep is the change that one key press makes.
const volumeStep = 5

// The names of the four pages. The main page holds the widgets. The add
// page holds the popup window for a new URL, the find page searches the
// station directory, and the help page holds the list of the keys.
const (
	pageMain = "main"
	pageAdd  = "add"
	pageFind = "find"
	pageHelp = "help"
)

// The size of the popup window for a new URL, in screen cells.
const (
	addWidth  = 72
	addHeight = 3
)

// The columns of the recent table: the number of the key that plays the
// stream, the station name, the rate of the stream and its URL.
const (
	colKey = iota
	colName
	colRate
	colURL
)

// nameWidth caps the station name in the recent table, so a long name
// cannot push the URL off the screen.
const nameWidth = 28

// helpWidth is the width of the help popup. Its height follows the number
// of keys, and it holds the border, the blank row and the legend of the
// play states.
const helpWidth = 56

// keysHint stands at the foot of the main page, as the finder has its own
// line of keys. It names the keys that the user needs most, and the help
// key for the rest. The line fits a terminal of 78 columns.
const keysHint = " [yellow]Enter[-] [gray]play[-]  [yellow]s[-] [gray]stop[-]  [yellow]p[-] [gray]pause[-]" +
	"  [yellow]a[-] [gray]add[-]  [yellow]f[-] [gray]find[-]  [yellow]+ -[-] [gray]volume[-]" +
	"  [yellow]?[-] [gray]all keys[-]  [yellow]q[-] [gray]quit[-]"

// binding is one key and what it does. The help popup shows the list.
type binding struct{ keys, what string }

// bindings holds the keys that the front end takes.
var bindings = []binding{
	{"\u2191 \u2193  j k", "move in the recent list"},
	{"Enter", "play the stream under the cursor"},
	{"Space", "play, or stop a stream that plays"},
	{"a", "add a stream"},
	{"f", "find a station in the directory"},
	{"p", "pause or continue"},
	{"s", "stop"},
	{"+ -", "change the volume by 5%"},
	{"l", "show or hide the level meter"},
	{"1-9", "play a recent stream"},
	{"d Del", "remove the stream from the list"},
	{"?", "show or hide this help"},
	{"q", "quit"},
}

// view holds the widgets and the core app.
type view struct {
	app *tview.Application
	a   *radio.App

	pages  *tview.Pages
	input  *tview.InputField
	info   *tview.TextView
	meter  *tview.TextView
	top    *tview.Flex // the info panel, with the level meter at its right
	recent *tview.Table
	help   *tview.TextView
	find   *finder

	// body is the main page. At its foot stands the line of keys, and
	// over it the message bar, which takes no row while no message waits.
	body    *tview.Flex
	message *tview.TextView
	keysBar *tview.TextView

	// The four fields hold the text of the last draw. render compares
	// against them, so an mpv update that changes nothing on screen costs
	// no draw.
	lastInfo    string
	lastMeter   string
	lastMessage string
	lastRecent  string

	// updates carries work from other goroutines into the main loop, and
	// the pump runs it there. done closes when the user interface stops,
	// so a goroutine with work left knows that nobody takes it.
	//
	// tview has QueueUpdate for this, but it blocks until the main loop
	// runs the function, and after the end of the loop it blocks forever.
	updates chan func()
	done    chan struct{}
}

// Run starts the tview user interface. It matches radio.Frontend.
func Run(a *radio.App) error { return runWith(a, nil, nil) }

// runWith starts the user interface on a given screen. Run supplies nil
// for both hooks, and tview then opens the terminal.
//
// A test supplies a simulation screen, and a ready function that receives
// the view before the main loop starts. The test then queues its screen
// reads on the goroutine that draws.
func runWith(a *radio.App, screen tcell.Screen, ready func(*view)) error {
	app := tview.NewApplication()
	if screen != nil {
		app.SetScreen(screen)
	}

	// Bracketed paste gives the whole text in one event. Without it, the
	// terminal sends one key event for each letter, and tview draws the
	// screen for each one. A pasted URL then fills in slowly.
	app.EnablePaste(true)
	v := newView(app, a)

	// A signal must stop the user interface, not the process. The caller
	// then closes mpv, which runs in idle mode and does not stop alone.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	go func() {
		select {
		case <-signals:
			app.Stop()
		case <-v.done:
			// signal.Stop does not close the channel, so without this
			// case the goroutine would wait here for good.
		}
	}()

	defer close(v.done)
	go v.pump(v.done)

	if ready != nil {
		ready(v)
	}

	if a.StartURL != "" {
		v.play(a.StartURL)
		v.selectURL(a.StartURL)
	}

	return app.SetRoot(v.pages, true).SetFocus(v.recent).Run()
}

// newView builds the widget tree.
func newView(app *tview.Application, a *radio.App) *view {
	v := &view{app: app, a: a, updates: make(chan func()), done: make(chan struct{})}

	v.input = tview.NewInputField().
		SetLabel(" URL  ").
		SetFieldWidth(0)
	v.input.SetBorder(true).SetTitle(" New stream ")
	v.input.SetDoneFunc(func(key tcell.Key) {
		url := v.input.GetText()
		v.closeAdd()
		if key == tcell.KeyEnter {
			v.play(url)
			v.selectURL(url)
		}
	})

	// A row of the info panel must stay one row. A long track title that
	// wrapped would push the rows under it out of the panel, and the
	// panel stands as tall as the level meter.
	v.info = tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	v.info.SetBorder(true).SetTitle(" Now playing ")

	v.meter = tview.NewTextView().SetDynamicColors(true)
	v.meter.SetBorder(true).SetTitle(" Level ")

	// The table names each stream, gives its rate and its URL. It reads
	// the rows of every stream, and not of the rows on the screen alone,
	// so the columns keep their width while the user moves in the table.
	v.recent = tview.NewTable().SetSelectable(true, false).SetEvaluateAllRows(true)
	v.recent.SetBorder(true).SetTitle(" Recent ").SetBorderPadding(0, 0, 1, 1)

	// The row under the cursor stands in reverse video of the colors of
	// the terminal. tview swaps the two colors of each cell instead, which
	// hides the station name: the name stands in the color of the
	// terminal, and the swap paints it in the color of the ground that it
	// stands on.
	v.recent.SetSelectedStyle(tcell.StyleDefault.Reverse(true))
	v.recent.SetSelectedFunc(func(row, _ int) {
		if url := v.urlAt(row); url != "" {
			v.play(url)
		}
	})

	v.message = tview.NewTextView().SetDynamicColors(true)
	v.keysBar = tview.NewTextView().SetDynamicColors(true).SetText(keysHint)

	v.help = tview.NewTextView().SetDynamicColors(true).SetText(helpText())
	v.help.SetBorder(true).SetTitle(" Keys ")

	v.find = newFinder(v)

	v.pages = tview.NewPages().
		AddPage(pageMain, v.root(), true, true).
		AddPage(pageAdd, popup(v.input, addWidth, addHeight), true, false).
		AddPage(pageFind, v.find.root, true, false).
		AddPage(pageHelp, popup(v.help, helpWidth, len(bindings)+4), true, false)

	v.keys()
	v.fillRecent()
	_ = v.render()
	return v
}

// root returns the layout of the main page. The top section holds the
// info panel, with the level meter at its right while the user shows it.
func (v *view) root() tview.Primitive {
	v.top = tview.NewFlex().AddItem(v.info, 0, 1, false)
	v.placeMeter()

	v.body = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(v.top, topHeight, 0, false).
		AddItem(v.recent, 0, 1, true).
		AddItem(v.message, 0, 0, false).
		AddItem(v.keysBar, 1, 0, false)
	return v.body
}

// placeMeter puts the level meter at the right of the info panel, or takes
// it away, as the config says. The info panel then takes the whole width.
func (v *view) placeMeter() {
	v.top.RemoveItem(v.meter)
	if !v.a.Cfg.HideMeter {
		v.top.AddItem(v.meter, meterWidth, 0, false)
	}
}

// toggleMeter shows or hides the level meter.
func (v *view) toggleMeter() {
	_ = v.a.ToggleMeter()
	v.placeMeter()
	_ = v.render()
}

// popup puts one widget of a fixed size in the middle of the screen. The
// empty boxes around it let the main page show through.
func popup(p tview.Primitive, width, height int) tview.Primitive {
	column := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(p, height, 0, true).
		AddItem(nil, 0, 1, false)

	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(column, width, 0, true).
		AddItem(nil, 0, 1, false)
}

// helpText lays the key bindings out in two columns, and it names the
// mark of each play state under them, as a legend for the mark in the
// status row of the info panel.
func helpText() string {
	var b strings.Builder
	for _, k := range bindings {
		fmt.Fprintf(&b, " [yellow]%-11s[-][gray]%s[-]\n", k.keys, k.what)
	}
	b.WriteString("\n")
	for i, m := range statusMarks {
		gap := "  "
		if i == 0 {
			gap = " "
		}
		fmt.Fprintf(&b, "%s[%s]%s[-] [gray]%s[-]", gap, statusColor, m.icon, strings.ToLower(m.status.String()))
	}
	return b.String()
}

// openAdd shows the popup window with an empty field.
func (v *view) openAdd() {
	v.pages.HidePage(pageHelp)
	v.input.SetText("")
	v.pages.ShowPage(pageAdd)
	v.app.SetFocus(v.input)
}

// closeAdd hides the popup window and gives the focus back to the list.
func (v *view) closeAdd() {
	v.pages.HidePage(pageAdd)
	v.app.SetFocus(v.recent)
}

// openFind shows the station finder. A build or a test without a station
// directory says so in the message bar instead.
func (v *view) openFind() {
	if v.a.Directory == nil {
		v.a.State.Notice = "playah has no station directory"
		_ = v.render()
		return
	}
	v.find.open()
}

// closeFind hides the station finder and gives the focus back to the list.
func (v *view) closeFind() {
	v.pages.HidePage(pageFind)
	v.app.SetFocus(v.recent)
}

// finding tells that the station finder stands in front. The finder takes
// the letter keys for its search field.
func (v *view) finding() bool {
	name, _ := v.pages.GetFrontPage()
	return name == pageFind
}

// toggleHelp shows the list of the keys, or hides it again.
func (v *view) toggleHelp() {
	if name, _ := v.pages.GetFrontPage(); name == pageHelp {
		v.closeHelp()
		return
	}
	v.pages.ShowPage(pageHelp)
	v.app.SetFocus(v.help)
}

// closeHelp hides the list of the keys.
func (v *view) closeHelp() {
	v.pages.HidePage(pageHelp)
	v.app.SetFocus(v.recent)
}

// play starts a stream and refreshes the widgets. The core writes any
// fault into the state, so the render call shows it.
func (v *view) play(url string) {
	_ = v.a.Play(url)
	v.fillRecent()
	_ = v.render()
}

// playStation plays a station from the directory and refreshes the
// widgets, as play does for a URL.
func (v *view) playStation(st directory.Station) {
	_ = v.a.PlayStation(st)
	v.fillRecent()
	v.selectURL(st.URL)
	_ = v.render()
}

// selectURL puts the cursor on the row of a stream, when the table holds it.
// A stream that the user adds or picks from the finder lands at the top, and
// the cursor goes with it, so the space bar gives that stream back after a
// stop.
func (v *view) selectURL(url string) {
	url = strings.TrimSpace(url)
	for i, r := range v.a.Cfg.Recent {
		if r.URL == url {
			v.recent.Select(i, 0)
			return
		}
	}
}

// forgetCurrent takes the stream under the cursor out of the recent
// table. The cursor stays on the same row, which now holds the stream
// that stood under it, or on the last row when the stream stood last.
func (v *view) forgetCurrent() {
	row, _ := v.recent.GetSelection()
	url := v.urlAt(row)
	if url == "" {
		return
	}
	_ = v.a.Forget(url)
	v.fillRecent()
	if count := v.recent.GetRowCount(); count > 0 {
		v.recent.Select(min(row, count-1), 0)
	}
	_ = v.render()
}

// playOrStop stops a stream that plays, and starts one when none does. The
// cursor stays on the stream that the user played, even when the play moved
// that stream to the top, so the key gives that one back after a stop.
func (v *view) playOrStop() {
	if v.a.State.Loaded {
		_ = v.a.Stop()
		_ = v.render()
		return
	}
	v.play(v.currentURL())
}

// currentURL returns the stream under the cursor in the recent table. It
// falls back on the stream that the state holds, for a table that is empty.
func (v *view) currentURL() string {
	row, _ := v.recent.GetSelection()
	if url := v.urlAt(row); url != "" {
		return url
	}
	return v.a.State.URL
}

// urlAt returns the URL of one row of the recent table. The rows follow
// the recent list of the config, row for row.
func (v *view) urlAt(row int) string {
	if row < 0 || row >= len(v.a.Cfg.Recent) {
		return ""
	}
	return v.a.Cfg.Recent[row].URL
}

// pump reads the player events and applies them in the update queue.
// Only the tview main loop touches the state, so the core needs no lock.
//
// A draw follows only when the text changes. mpv reports the cache and the
// bitrate many times each second, and each draw clears the screen and
// moves the terminal cursor. A draw for an unchanged screen therefore made
// the cursor flicker.
func (v *view) pump(done <-chan struct{}) {
	ticker := time.NewTicker(redrawInterval)
	defer ticker.Stop()

	var pending []radio.Event
	for {
		select {
		case <-done:
			return
		case work := <-v.updates:
			// The work changes widgets, so a draw follows it.
			v.app.QueueUpdateDraw(work)
		case ev, ok := <-v.a.Player.Events:
			if !ok {
				return
			}
			pending = append(pending, ev)
		case <-ticker.C:
			if len(pending) == 0 {
				continue
			}
			batch := pending
			pending = nil

			// QueueUpdate blocks until the main loop runs the function,
			// so changed is safe to read after the call. Draw is itself a
			// queued update, so a call inside the function would block.
			changed := false
			v.app.QueueUpdate(func() {
				for _, ev := range batch {
					v.a.Handle(ev)
				}
				changed = v.render()
			})
			if changed {
				v.app.Draw()
			}
		}
	}
}

// fillRecent rebuilds the table of the streams that the user played
// before. Each row names the station and gives the rate that the stream
// reported, and a dash stands for a value that no play gave yet.
//
// The cursor stays on the same stream, because a name that arrives while
// the user moves in the table must not move it, and a play moves the stream
// to the top of the list. Each row carries its URL as its reference, as the
// text of the cell is escaped. A stream that left the list gives its index
// back, so the cursor stays on the row that moved up into its place.
func (v *view) fillRecent() {
	row, _ := v.recent.GetSelection()
	var cursor any
	if cell := v.recent.GetCell(row, colURL); cell != nil {
		cursor = cell.GetReference()
	}
	v.recent.Clear()
	target := row
	for i, r := range v.a.Cfg.Recent {
		key := ""
		if i < 9 {
			key = fmt.Sprintf("(%d)", i+1)
		}
		if cursor != nil && cursor == any(r.URL) {
			target = i
		}
		v.recent.SetCell(i, colKey, rowCell(key, tcell.ColorGray))
		v.recent.SetCell(i, colName, rowCell(tview.Escape(r.NameText()), tcell.ColorDefault).
			SetMaxWidth(nameWidth))
		v.recent.SetCell(i, colRate, rowCell(r.BitrateText(), tcell.ColorGray))
		v.recent.SetCell(i, colURL, rowCell(tview.Escape(r.URL), tcell.ColorGray).
			SetExpansion(1).SetReference(r.URL))
	}
	if count := v.recent.GetRowCount(); count > 0 {
		v.recent.Select(min(target, count-1), 0)
	}
	v.lastRecent = recentText(v.a.Cfg.Recent)
}

// rowCell builds one cell of the recent table. The station name stands in
// the color of the terminal, and the rest of the row in gray, as the
// values of the panels above do.
func rowCell(text string, color tcell.Color) *tview.TableCell {
	return tview.NewTableCell(text).SetTextColor(color)
}

// recentText writes one line for each row of the recent table. render
// compares against it, so the table is rebuilt only when a row changed,
// which happens when a stream reports its station or its rate.
func recentText(items []radio.Recent) string {
	var b strings.Builder
	for _, r := range items {
		fmt.Fprintf(&b, "%s\t%s\t%s\n", r.NameText(), r.BitrateText(), r.URL)
	}
	return b.String()
}

// render writes the current state into the info panel, the level meter,
// the recent table and the message bar. It reports whether any of them
// changed. The info panel holds the stream, then a blank row, then the
// play state, the volume and the seconds in the cache.
func (v *view) render() bool {
	s := v.a.State
	audio := strings.Join([]string{
		dash(s.Codec), s.BitrateText(), s.SampleRateText(), dash(s.Channels),
	}, "  ")

	var b strings.Builder
	row := func(name, value string) {
		fmt.Fprintf(&b, " [gray]%-9s[-]%s\n", name, value)
	}
	row("Track", tview.Escape(dash(s.Title)))
	row("Station", tview.Escape(dash(s.Station)))
	row("Genre", tview.Escape(dash(s.Genre)))
	row("Audio", tview.Escape(audio))
	b.WriteString("\n")
	row("Status", statusText(s.Status()))
	row("Volume", volumeText(s.Volume))
	row("Buffer", bufferText(s.Cache))

	changed := false
	if text := b.String(); text != v.lastInfo {
		v.lastInfo = text
		v.info.SetText(text)
		changed = true
	}
	if text := meterPanel(s.Levels); text != v.lastMeter {
		v.lastMeter = text
		v.meter.SetText(text)
		changed = true
	}
	if text := recentText(v.a.Cfg.Recent); text != v.lastRecent {
		v.fillRecent()
		changed = true
	}
	if text := messageText(s); text != v.lastMessage {
		v.lastMessage = text
		v.message.SetText(text)
		height := 0
		if text != "" {
			height = 1
		}
		v.body.ResizeItem(v.message, height, 0)
		changed = true
	}
	return changed
}

// messageText writes the message bar. The bar holds the last message, for
// example the fault of a stream that failed, or the word that mpv left
// when it stopped. A stream fault stands in red, and a note that is not
// about the stream, such as a failed config save, in yellow. With no
// message the text stays empty, and the bar gives its row back to the
// recent table.
func messageText(s radio.State) string {
	if s.Err != "" {
		return fmt.Sprintf(" [red]%s[-]", tview.Escape(s.Err))
	}
	if s.Notice != "" {
		return fmt.Sprintf(" [yellow]%s[-]", tview.Escape(s.Notice))
	}
	return ""
}

// volumeText writes the value of the volume row.
func volumeText(percent float64) string {
	return fmt.Sprintf("%.0f%%", percent)
}

// bufferText writes the value of the buffer row: the seconds in the cache,
// or a dash before mpv reports the first value.
func bufferText(seconds float64) string {
	if seconds <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f s", seconds)
}

// dash shows a dash for a value that mpv did not send yet.
func dash(text string) string {
	if strings.TrimSpace(text) == "" {
		return "-"
	}
	return text
}

// keys installs the global key bindings.
func (v *view) keys() {
	v.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyCtrlQ:
			v.app.Stop()
			return nil
		case tcell.KeyCtrlP:
			_ = v.a.TogglePause()
			return nil
		case tcell.KeyCtrlS:
			_ = v.a.Stop()
			_ = v.render()
			return nil
		case tcell.KeyEsc:
			v.closeAdd()
			v.closeHelp()
			v.closeFind()
			return nil
		case tcell.KeyDelete:
			// The finder and the popup window take Delete for their
			// fields, so the key removes a row only from the table.
			if v.recent.HasFocus() {
				v.forgetCurrent()
				return nil
			}
		case tcell.KeyUp, tcell.KeyDown:
			// Ctrl with an arrow key changes the volume from any widget.
			if event.Modifiers()&tcell.ModCtrl != 0 {
				step := volumeStep
				if event.Key() == tcell.KeyDown {
					step = -volumeStep
				}
				v.changeVolume(step)
				return nil
			}
		case tcell.KeyRune:
			// The letter keys work only when the popup window and the
			// finder are closed. The fields must receive every letter
			// that the user types.
			if v.input.HasFocus() || v.finding() {
				return event
			}
			switch event.Rune() {
			case 'a':
				v.openAdd()
				return nil
			case 'f':
				v.openFind()
				return nil
			case 'q':
				v.app.Stop()
				return nil
			case 'p':
				_ = v.a.TogglePause()
				return nil
			case 's':
				_ = v.a.Stop()
				_ = v.render()
				return nil
			case ' ':
				v.playOrStop()
				return nil
			case '?':
				v.toggleHelp()
				return nil
			case '1', '2', '3', '4', '5', '6', '7', '8', '9':
				// The table holds no keys of its own, as the list before
				// it did, so the number keys stand here.
				if url := v.urlAt(int(event.Rune() - '1')); url != "" {
					v.play(url)
				}
				return nil
			case 'j':
				// The list already moves on the arrow keys. j and k
				// become an arrow key, so one handler keeps the movement.
				return tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone)
			case 'k':
				return tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone)
			case 'd':
				// The key removes a row, so it acts only on the table that
				// the user sees, and not behind the help popup.
				if v.recent.HasFocus() {
					v.forgetCurrent()
				}
				return nil
			case 'l':
				v.toggleMeter()
				return nil
			case '+', '=':
				v.changeVolume(volumeStep)
				return nil
			case '-', '_':
				v.changeVolume(-volumeStep)
				return nil
			}
		}
		return event
	})
}

func (v *view) changeVolume(step int) {
	_ = v.a.ChangeVolume(step)
	_ = v.render()
}

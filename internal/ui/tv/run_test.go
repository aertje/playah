package tv

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"playah/internal/radio"
)

// radioError builds the event that the player sends for a failed stream.
func radioError(text string) radio.Event {
	return radio.Event{Kind: radio.EventError, Text: text}
}

// TestRenderShowsStreamData checks that the mpv events reach the screen.
func TestRenderShowsStreamData(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.waitFor(t, "Status")

	h.send("idle-active", false)
	h.send("audio-codec-name", "mp3")
	h.send("audio-bitrate", 128000.0)
	h.send("metadata", map[string]any{
		"icy-title": "Artist - Song",
		"icy-name":  "Groove Salad",
	})

	h.waitFor(t, "Artist - Song")
	h.waitFor(t, "Groove Salad")
	h.waitFor(t, "128 kbps")
	h.waitForStatus(t, radio.StatusPlaying)

	h.app.Player.Events <- radioError("the stream failed")
	h.waitFor(t, "the stream failed")
}

// TestAddPopupPlaysTheURL presses "a" in the recent list, types a URL and
// presses Enter. The front end must close the popup window and put the URL
// at the top of the recent list.
func TestAddPopupPlaysTheURL(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.waitFor(t, "Recent")

	h.screen.InjectKey(tcell.KeyRune, 'a', tcell.ModNone)
	h.waitFor(t, "New stream")

	url := "https://example.com/stream.mp3"
	for _, r := range url {
		h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	h.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	// The typed URL shows in the field while the popup is open, so the
	// URL on the screen does not prove that Enter ran. The popup going
	// away does.
	h.waitForGone(t, "New stream")
	h.waitFor(t, url)
	if got := h.app.Cfg.Recent; len(got) == 0 || got[0].URL != url {
		t.Errorf("the recent list is %v, want %q at the top", got, url)
	}
}

// addURL types one URL in the popup window and presses Enter.
func (h *harness) addURL(t *testing.T, url string) {
	t.Helper()

	h.screen.InjectKey(tcell.KeyRune, 'a', tcell.ModNone)
	h.waitFor(t, "New stream")
	for _, r := range url {
		h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	h.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)

	// The field shows the typed URL too, so wait for the popup to close
	// before the caller reads the table.
	h.waitForGone(t, "New stream")
	h.waitFor(t, url)
}

// TestStationNameRenamesTheRecentRow plays a stream and sends the ICY name
// and the rate that a radio station reports. The row of the recent table
// must then hold both beside the URL, and Enter must still play that URL.
// A value that no play gave yet must stand as a dash.
func TestStationNameRenamesTheRecentRow(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	url := "https://example.com/groovesalad.mp3"
	h.addURL(t, url)
	h.waitForRecentRow(t, 0, "-", "-")

	// mpv names a stream by the last part of its URL while the station
	// sends no name. The table must keep the dash for such a name.
	h.send("media-title", "groovesalad")
	h.waitFor(t, "Station  groovesalad")
	if name, _, _ := h.recentRow(0); name != "-" {
		t.Errorf("recent row 0 names the station %q, want a dash", name)
	}

	h.send("metadata", map[string]any{"icy-name": "Groove Salad"})
	h.send("audio-bitrate", 128000.0)
	h.waitForRecentRow(t, 0, "Groove Salad", "128 kbps")

	if _, _, got := h.recentRow(0); got != url {
		t.Errorf("the row carries the URL %q, want %q", got, url)
	}
	if got := h.recentName(0); got != "Groove Salad" {
		t.Errorf("the config holds the name %q, want the station name", got)
	}

	// Enter must play the URL that the item carries, not its label.
	h.screen.InjectKey(tcell.KeyRune, 's', tcell.ModNone)
	h.waitForLoaded(t, false)
	h.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	h.waitForLoaded(t, true)
	if got := h.playing(); got != url {
		t.Errorf("Enter played %q, want %q", got, url)
	}
}

// TestTheRowUnderTheCursorReadsInReverse checks the highlight of the
// recent table. Every cell of that row must stand in reverse video, so the
// station name keeps the color of the terminal and stays readable. The
// rows under it must keep the plain style.
func TestTheRowUnderTheCursorReadsInReverse(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.addURL(t, "https://example.com/one.mp3")
	h.addURL(t, "https://example.com/two.mp3")
	h.send("metadata", map[string]any{"icy-name": "Groove Salad"})
	h.waitForRecentRow(t, 0, "Groove Salad", "-")

	for i, style := range h.rowStyle(0) {
		if _, _, attr := style.Decompose(); attr&tcell.AttrReverse == 0 {
			t.Fatalf("cell %d of the row under the cursor reads plain, want reverse video", i)
		}
	}
	for i, style := range h.rowStyle(1) {
		if _, _, attr := style.Decompose(); attr&tcell.AttrReverse != 0 {
			t.Fatalf("cell %d of the second row reads in reverse video, want plain", i)
		}
	}
}

// TestNumberKeyPlaysTheRecentStream presses a number key, which plays the
// stream of that row. The table holds no keys of its own, so the front end
// carries them.
func TestNumberKeyPlaysTheRecentStream(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	first := "https://example.com/one-128.mp3"
	second := "https://example.com/two-320.mp3"
	h.addURL(t, first)
	h.addURL(t, second)

	// The stream that played last stands at the top of the table.
	h.screen.InjectKey(tcell.KeyRune, '2', tcell.ModNone)
	h.waitForPlaying(t, first)

	// A number without a row must change nothing.
	h.screen.InjectKey(tcell.KeyRune, '9', tcell.ModNone)
	if got := h.playing(); got != first {
		t.Errorf("the empty row played %q, want %q", got, first)
	}
}

// TestVimKeysMoveTheList checks that j goes down and k goes up.
func TestVimKeysMoveTheList(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.addURL(t, "https://example.com/one.mp3")
	h.addURL(t, "https://example.com/two.mp3")

	if got := h.selected(); got != 0 {
		t.Fatalf("the list starts on item %d, want 0", got)
	}
	h.screen.InjectKey(tcell.KeyRune, 'j', tcell.ModNone)
	if got := h.waitForItem(t, 1); got != 1 {
		t.Errorf("j gave item %d, want 1", got)
	}
	h.screen.InjectKey(tcell.KeyRune, 'k', tcell.ModNone)
	if got := h.waitForItem(t, 0); got != 0 {
		t.Errorf("k gave item %d, want 0", got)
	}
}

// TestLetterKeysGoToThePopup checks that the popup window keeps the letters
// that also work as commands. The letters must not stop the front end.
func TestLetterKeysGoToThePopup(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.screen.InjectKey(tcell.KeyRune, 'a', tcell.ModNone)
	h.waitFor(t, "New stream")
	for _, r := range "qps" {
		h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	h.waitFor(t, "qps")

	select {
	case err := <-h.done:
		t.Fatalf("a letter in the popup window stopped the front end (err: %v)", err)
	case <-time.After(200 * time.Millisecond):
	}
	h.screen.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)
	h.waitForGone(t, "New stream")
}

// TestStatusBarHoldsTheMessage checks the foot of the screen. The line of
// keys stands there at all times, and the message bar takes the row over
// it only while a message waits, such as the word that mpv leaves when it
// stops.
func TestStatusBarHoldsTheMessage(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.waitFor(t, "Recent")
	if got := h.bottomRow(); !strings.Contains(got, "? all keys") {
		t.Errorf("the last row reads %q, want the line of keys", got)
	}
	if got := h.messageRow(); !strings.HasPrefix(got, "╚") {
		t.Errorf("the row over the keys reads %q, want the border of the table", got)
	}

	h.app.Player.Events <- radio.Event{Kind: radio.EventExit}
	h.waitForMessage(t, "mpv stopped")
	if got := h.bottomRow(); !strings.Contains(got, "? all keys") {
		t.Errorf("the last row reads %q, want the line of keys under the message", got)
	}
}

// TestStatusBarShowsAStreamFault checks that a fault of the player also
// reaches the message bar.
func TestStatusBarShowsAStreamFault(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.app.Player.Events <- radioError("the stream failed")
	h.waitForMessage(t, "the stream failed")
}

// TestHelpPopupListsTheKeys presses "?" and looks for the key list. A
// second press must close the popup window again.
func TestHelpPopupListsTheKeys(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.waitFor(t, "Recent")
	h.screen.InjectKey(tcell.KeyRune, '?', tcell.ModNone)
	h.waitFor(t, "Keys")
	h.waitFor(t, "play, or stop a stream that plays")

	h.screen.InjectKey(tcell.KeyRune, '?', tcell.ModNone)
	h.waitForGone(t, "play, or stop a stream that plays")
}

// TestEscapeClosesTheHelpPopup checks the other way out of the popup.
func TestEscapeClosesTheHelpPopup(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.screen.InjectKey(tcell.KeyRune, '?', tcell.ModNone)
	h.waitFor(t, "Keys")
	h.screen.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)
	h.waitForGone(t, "show or hide this help")
}

// TestArrowKeysMoveTheList checks that the arrows move the list, as j and
// k do.
func TestArrowKeysMoveTheList(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.addURL(t, "https://example.com/one.mp3")
	h.addURL(t, "https://example.com/two.mp3")

	h.screen.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	if got := h.waitForItem(t, 1); got != 1 {
		t.Errorf("the down arrow gave item %d, want 1", got)
	}
	h.screen.InjectKey(tcell.KeyUp, 0, tcell.ModNone)
	if got := h.waitForItem(t, 0); got != 0 {
		t.Errorf("the up arrow gave item %d, want 0", got)
	}
}

// TestSpaceStopsAndPlays checks the space bar. It stops a stream that
// plays, and it starts the stream under the cursor again.
func TestSpaceStopsAndPlays(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	url := "https://example.com/one.mp3"
	h.addURL(t, url)
	h.waitForLoaded(t, true)

	h.screen.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	h.waitForLoaded(t, false)

	h.screen.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	h.waitForLoaded(t, true)
	if got := h.playing(); got != url {
		t.Errorf("the space bar played %q, want %q", got, url)
	}
}

// TestStatusMarksFollowTheState checks the marks in the status row. The
// row shows a glyph, not a word, so each state must reach the screen.
func TestStatusMarksFollowTheState(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	// The panel stands beside the level meter, with its own border.
	h.waitFor(t, "Status")
	h.waitForStatus(t, radio.StatusIdle)

	h.send("idle-active", false)
	h.waitForStatus(t, radio.StatusPlaying)

	h.send("core-idle", true)
	h.waitForStatus(t, radio.StatusBuffering)

	h.send("pause", true)
	h.waitForStatus(t, radio.StatusPaused)

	h.app.Player.Events <- radioError("the stream failed")
	h.waitForStatus(t, radio.StatusError)
}

// TestHelpPopupNamesTheStatusMarks checks the legend under the keys. The
// status row shows the mark alone, so the popup must give it a word.
func TestHelpPopupNamesTheStatusMarks(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.screen.InjectKey(tcell.KeyRune, '?', tcell.ModNone)
	h.waitFor(t, "Keys")
	for _, m := range statusMarks {
		h.waitFor(t, m.icon+" "+strings.ToLower(m.status.String()))
	}
}

// TestMeterToggles presses l twice. The level meter goes away and comes back,
// and the config file keeps the choice for the next start.
func TestMeterToggles(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.waitFor(t, " Level ")

	h.screen.InjectKey(tcell.KeyRune, 'l', tcell.ModNone)
	h.waitForGone(t, " Level ")
	if !loadConfig(t).HideMeter {
		t.Error("the config file does not keep the hidden meter")
	}

	h.screen.InjectKey(tcell.KeyRune, 'l', tcell.ModNone)
	h.waitFor(t, " Level ")
	if loadConfig(t).HideMeter {
		t.Error("the config file still hides the meter")
	}
}

// TestMeterStartsHidden checks that a config that hides the level meter
// gives the info panel the whole width from the first draw.
func TestMeterStartsHidden(t *testing.T) {
	h := startWith(t, func(a *radio.App) { a.Cfg.HideMeter = true })
	defer h.stop(t)

	h.waitFor(t, "Now playing")
	if strings.Contains(h.text(), " Level ") {
		t.Errorf("the screen shows the hidden meter:\n%s", h.text())
	}
}

// TestDeleteForgetsTheRecentStream removes streams from the recent table
// with d and with Delete. The cursor stays on its row, the config file
// forgets the stream, and d does nothing behind the help popup.
func TestDeleteForgetsTheRecentStream(t *testing.T) {
	h := startWith(t, func(a *radio.App) {
		for _, url := range []string{"https://example.com/c", "https://example.com/b", "https://example.com/a"} {
			a.Cfg.Remember(url)
		}
	})
	defer h.stop(t)

	h.waitFor(t, "https://example.com/c")

	// The help popup hides the table, so d must leave the table alone.
	h.screen.InjectKey(tcell.KeyRune, '?', tcell.ModNone)
	h.waitFor(t, "remove the stream from the list")
	h.screen.InjectKey(tcell.KeyRune, 'd', tcell.ModNone)
	h.screen.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)
	h.waitForGone(t, "remove the stream from the list")
	h.waitFor(t, "https://example.com/a")

	h.screen.InjectKey(tcell.KeyDown, 0, tcell.ModNone)
	h.screen.InjectKey(tcell.KeyRune, 'd', tcell.ModNone)
	h.waitForGone(t, "https://example.com/b")
	if _, _, url := h.recentRow(1); url != "https://example.com/c" {
		t.Errorf("row 1 holds %q, want the stream that stood under the removed one", url)
	}
	if got := h.selected(); got != 1 {
		t.Errorf("the cursor stands on row %d, want 1", got)
	}
	for _, r := range loadConfig(t).Recent {
		if r.URL == "https://example.com/b" {
			t.Error("the config file still holds the removed stream")
		}
	}

	// The last row goes, and the cursor moves up onto the row left.
	h.screen.InjectKey(tcell.KeyDelete, 0, tcell.ModNone)
	h.waitForGone(t, "https://example.com/c")
	if got := h.selected(); got != 0 {
		t.Errorf("the cursor stands on row %d, want 0", got)
	}
}

// cursorURL reads the URL of the stream under the cursor, on the goroutine
// that owns the config.
func (h *harness) cursorURL() string {
	var out string
	h.queue(func() {
		row, _ := h.view.recent.GetSelection()
		out = h.view.urlAt(row)
	})
	return out
}

// remembered fills the recent list in the order of the URLs, the first URL
// at the top. A test passes it to startWith.
func remembered(urls ...string) func(*radio.App) {
	return func(a *radio.App) {
		for i := len(urls) - 1; i >= 0; i-- {
			a.Cfg.Remember(urls[i])
		}
	}
}

// TestStartURLPlaysWithoutAutoPlay checks that the front end plays the
// start stream whatever the config says. main decides whether auto_play
// applies, and it leaves the start stream empty when it does not.
func TestStartURLPlaysWithoutAutoPlay(t *testing.T) {
	url := "https://example.com/start.mp3"
	h := startWith(t, func(a *radio.App) {
		a.Cfg.AutoPlay = false
		a.StartURL = url
	})
	defer h.stop(t)

	h.waitForPlaying(t, url)
	h.waitForLoaded(t, true)
}

// TestNoticeDoesNotTurnTheStatusToError checks that a note that is not
// about the stream reaches the message bar, and that the status row keeps
// the state of the stream that plays.
func TestNoticeDoesNotTurnTheStatusToError(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	// No mpv runs here, so a Play would fail and set a stream fault. The
	// event alone puts the core in the playing state.
	h.send("idle-active", false)
	h.waitForStatus(t, radio.StatusPlaying)

	h.typeText("f")
	h.waitForMessage(t, "playah has no station directory")
	if got := h.infoText(); !strings.Contains(got, statusText(radio.StatusPlaying)) ||
		strings.Contains(got, statusText(radio.StatusError)) {
		t.Errorf("the info panel holds\n%s\nwant the status row of a stream that plays", got)
	}
}

// TestMessageTextColors checks the colors of the message bar: red for a
// fault of the stream, yellow for a note, and the fault wins.
func TestMessageTextColors(t *testing.T) {
	if got := messageText(radio.State{Err: "bad"}); got != " [red]bad[-]" {
		t.Errorf("a stream fault reads %q, want red", got)
	}
	if got := messageText(radio.State{Notice: "note"}); got != " [yellow]note[-]" {
		t.Errorf("a notice reads %q, want yellow", got)
	}
	if got := messageText(radio.State{Err: "bad", Notice: "note"}); got != " [red]bad[-]" {
		t.Errorf("both set read %q, want the fault in red", got)
	}
	if got := messageText(radio.State{}); got != "" {
		t.Errorf("no message reads %q, want empty", got)
	}
}

// TestSpaceResumesTheStreamThatPlayed plays the stream of the fourth row.
// The play moves it to the top, and the cursor must go with it, so that
// Space and Space again start the same stream and not the one that now
// stands where the cursor stood.
func TestSpaceResumesTheStreamThatPlayed(t *testing.T) {
	urls := []string{"https://example.com/a", "https://example.com/b", "https://example.com/c", "https://example.com/d"}
	h := startWith(t, remembered(urls...))
	defer h.stop(t)

	h.waitFor(t, urls[3])
	for range 3 {
		h.screen.InjectKey(tcell.KeyRune, 'j', tcell.ModNone)
	}
	h.waitForItem(t, 3)
	h.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	h.waitForPlaying(t, urls[3])

	if got := h.waitForItem(t, 0); got != 0 {
		t.Fatalf("the cursor stands on row %d, want 0", got)
	}
	if got := h.cursorURL(); got != urls[3] {
		t.Fatalf("the cursor stands on %q, want %q", got, urls[3])
	}

	h.screen.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	h.waitForLoaded(t, false)
	h.screen.InjectKey(tcell.KeyRune, ' ', tcell.ModNone)
	h.waitForLoaded(t, true)
	if got := h.playing(); got != urls[3] {
		t.Errorf("Space resumed %q, want %q", got, urls[3])
	}
}

// TestNumberKeyLeavesTheCursorOnItsStream plays a stream with a number key
// while the cursor stands on another one. The play moves the played stream
// to the top, and the cursor must stay on the other stream. A station name
// that arrives for the played stream rebuilds the table, and it must not
// move the cursor either.
func TestNumberKeyLeavesTheCursorOnItsStream(t *testing.T) {
	urls := []string{"https://example.com/a", "https://example.com/b", "https://example.com/c"}
	h := startWith(t, remembered(urls...))
	defer h.stop(t)

	h.waitFor(t, urls[2])
	h.screen.InjectKey(tcell.KeyRune, '3', tcell.ModNone)
	h.waitForPlaying(t, urls[2])
	h.waitFor(t, urls[2])

	if got := h.cursorURL(); got != urls[0] {
		t.Fatalf("the cursor stands on %q, want %q", got, urls[0])
	}
	if got := h.selected(); got != 1 {
		t.Errorf("the cursor stands on row %d, want 1", got)
	}

	h.send("metadata", map[string]any{"icy-name": "Station C"})
	h.waitForRecentRow(t, 0, "Station C", "-")
	if got := h.cursorURL(); got != urls[0] {
		t.Errorf("a station name moved the cursor to %q, want %q", got, urls[0])
	}
}

// TestBracketedURLPlays puts a URL with a tag in the recent table. The
// table escapes the URL, so the screen shows it as typed, and Enter must
// play the URL from the config and not the escaped text of the cell.
func TestBracketedURLPlays(t *testing.T) {
	url := "http://host/[red]x"
	h := startWith(t, remembered(url))
	defer h.stop(t)

	h.waitFor(t, url)
	h.screen.InjectKey(tcell.KeyEnter, 0, tcell.ModNone)
	h.waitForPlaying(t, url)
}

// TestTheSignalGoroutineEndsWithTheFrontEnd checks that the goroutine that
// waits for a signal does not outlive the user interface.
func TestTheSignalGoroutineEndsWithTheFrontEnd(t *testing.T) {
	h := start(t)
	h.stop(t)

	deadline := time.Now().Add(waitTimeout)
	for strings.Contains(stacks(), "tv.runWith.func1") {
		if time.Now().After(deadline) {
			t.Fatalf("the signal goroutine still runs\n%s", stacks())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

package tv

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"playah/internal/radio"
)

// waitTimeout bounds every wait in the harness. The value is generous on
// purpose: "go test ./..." runs this package beside the smoke test of the
// radio package, which plays a real stream through mpv, and a race build
// on a loaded machine needs the room.
const waitTimeout = 15 * time.Second

// stacks gives the stack of every goroutine. A wait that runs out means
// that the front end stopped answering, and only the stacks say where it
// waits: the tview main loop, the pump or a queued update. The tests have
// run out twice on a loaded machine without leaving a trace of it.
func stacks() string {
	buf := make([]byte, 1<<20)
	return string(buf[:runtime.Stack(buf, true)])
}

// harness runs the front end on a simulation screen and counts the draws.
// It needs no mpv, because the test pushes the events that mpv would send.
type harness struct {
	app    *radio.App
	view   *view
	tvapp  *tview.Application
	screen tcell.SimulationScreen
	draws  *atomic.Int64
	done   chan error
	t      *testing.T
}

// queue runs f on the goroutine that draws, and it waits for f to finish.
// QueueUpdate alone blocks for good when the main loop has stopped, which
// would turn a front end that quit early into a test that hangs until the
// timeout of the whole run. This fails the test with the stacks instead.
// It must run on the goroutine of the test.
func (h *harness) queue(f func()) {
	h.t.Helper()
	ran := make(chan struct{})
	go h.tvapp.QueueUpdate(func() {
		f()
		close(ran)
	})
	select {
	case <-ran:
	case <-h.view.done:
		select {
		case <-ran:
		default:
			h.t.Fatalf("the front end stopped before it ran a queued update\n%s", stacks())
		}
	case <-time.After(waitTimeout):
		h.t.Fatalf("the front end did not run a queued update\n%s", stacks())
	}
}

// start runs the front end in a goroutine and waits for the first draw.
//
// The test must not touch the screen before that draw. tview calls
// SetScreen, and SetScreen calls simscreen.Init, which writes the screen
// size without a lock. The wait on the draw counter puts every later read
// after that write.
func start(t *testing.T) *harness {
	t.Helper()
	return startWith(t, nil)
}

// startWith runs the front end like start, after setup changed the core.
// A test sets the station directory there, before the main loop reads it.
func startWith(t *testing.T, setup func(*radio.App)) *harness {
	t.Helper()

	// The front end writes the config file when it plays a stream, and
	// when a stream names its station. The test therefore points the
	// config path at a temporary directory: macOS reads HOME, and Linux
	// reads XDG_CONFIG_HOME.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))

	player := &radio.Player{Events: make(chan radio.Event, 256)}
	app := radio.NewApp(player, radio.Config{Volume: 70}, "")
	if setup != nil {
		setup(app)
	}
	screen := tcell.NewSimulationScreen("UTF-8")

	var draws atomic.Int64
	viewCh := make(chan *view, 1)
	done := make(chan error, 1)
	go func() {
		done <- runWith(app, screen, func(v *view) {
			v.app.SetAfterDrawFunc(func(tcell.Screen) { draws.Add(1) })
			viewCh <- v
		})
	}()

	// runWith returns before it calls ready only when it fails early, and
	// the test must not wait for a view then.
	var v *view
	select {
	case v = <-viewCh:
	case err := <-done:
		t.Fatalf("the front end stopped before it started (err: %v)\n%s", err, stacks())
	case <-time.After(waitTimeout):
		t.Fatalf("the front end did not start\n%s", stacks())
	}
	h := &harness{app: app, view: v, tvapp: v.app, screen: screen, draws: &draws, done: done, t: t}

	deadline := time.Now().Add(waitTimeout)
	for h.draws.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the front end did not draw the first screen\n%s", stacks())
		}
		time.Sleep(10 * time.Millisecond)
	}
	return h
}

// send pushes one property change from mpv.
func (h *harness) send(name string, data any) {
	h.app.Player.Events <- radio.Event{Kind: radio.EventProperty, Name: name, Data: data}
}

// stop quits the front end with the quit key. Escape goes first, because
// the finder takes the letter for its search field.
func (h *harness) stop(t *testing.T) {
	t.Helper()
	h.screen.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)
	h.screen.InjectKey(tcell.KeyRune, 'q', tcell.ModNone)
	select {
	case err := <-h.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(waitTimeout):
		t.Fatalf("the quit key did not stop the front end\n%s", stacks())
	}
}

// settle waits until the draw count stops to rise, then it returns that
// count. A front end that keeps drawing past the wait fails the test.
func (h *harness) settle(t *testing.T) int64 {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		before := h.draws.Load()
		time.Sleep(500 * time.Millisecond)
		if after := h.draws.Load(); after == before {
			return after
		}
		if time.Now().After(deadline) {
			t.Fatalf("the front end kept drawing for %v\n%s", waitTimeout, stacks())
		}
	}
}

// waitFor reads the simulation screen until the text appears.
func (h *harness) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		if strings.Contains(h.text(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the screen never showed %q. It shows:\n%s\n%s", want, h.text(), stacks())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// text reads the screen on the goroutine that draws it. GetContents gives
// back the live cell buffer, so a read from the test goroutine would race
// with a draw. QueueUpdate blocks until the main loop runs the function.
func (h *harness) text() string {
	var out string
	h.queue(func() { out = h.screenText() })
	return out
}

// screenText joins the screen cells into lines.
func (h *harness) screenText() string {
	cells, width, height := h.screen.GetContents()
	var b strings.Builder
	for y := range height {
		for x := range width {
			runes := cells[y*width+x].Runes
			if len(runes) == 0 {
				b.WriteRune(' ')
				continue
			}
			b.WriteRune(runes[0])
		}
		b.WriteRune('\n')
	}
	return b.String()
}

// rowStyle reads the style of the screen cells of one row of the recent
// table, from the first cell of the row to the last. It runs on the
// goroutine that draws, because GetContents gives back the live cell
// buffer.
func (h *harness) rowStyle(row int) []tcell.Style {
	var out []tcell.Style
	h.queue(func() {
		cells, width, _ := h.screen.GetContents()
		// The table stands under the top section and the border row of
		// its own panel, and it holds one column of padding inside each
		// side of the border.
		y := topHeight + 1 + row
		for x := 2; x < width-2; x++ {
			out = append(out, cells[y*width+x].Style)
		}
	})
	return out
}

// meterBars reads the fill height of each bar in the level meter. It runs
// on the goroutine that draws, because that goroutine owns the widget.
func (h *harness) meterBars() []int {
	var text string
	h.queue(func() { text = h.view.lastMeter })

	rows := splitPanel(text)[meterPad:]
	bars := make([]int, 0, meterChannels)
	for _, x := range barStarts(meterChannels) {
		bars = append(bars, filledRows(rows, x))
	}
	return bars
}

// waitForBars reads the level meter until the fill heights match.
func (h *harness) waitForBars(t *testing.T, want []int) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		got := h.meterBars()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the bars fill %v rows, want %v\n%s", got, want, stacks())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// selected reads the index of the highlighted row in the recent table. It
// runs on the goroutine that draws, because that goroutine owns the widget.
func (h *harness) selected() int {
	var out int
	h.queue(func() { out, _ = h.view.recent.GetSelection() })
	return out
}

// recentRow reads the cells of one row of the recent table: the station
// name, the rate and the URL. It runs on the goroutine that draws, because
// that goroutine owns the widget.
func (h *harness) recentRow(i int) (string, string, string) {
	var name, rate, url string
	h.queue(func() {
		if i < 0 || i >= h.view.recent.GetRowCount() {
			return
		}
		name = h.view.recent.GetCell(i, colName).Text
		rate = h.view.recent.GetCell(i, colRate).Text
		url = h.view.recent.GetCell(i, colURL).Text
	})
	return name, rate, url
}

// recentName reads the station name that the config holds for one item of
// the recent list. It runs on the goroutine that owns the config.
func (h *harness) recentName(i int) string {
	var out string
	h.queue(func() {
		if i >= 0 && i < len(h.app.Cfg.Recent) {
			out = h.app.Cfg.Recent[i].Name
		}
	})
	return out
}

// waitForRecentRow reads the recent table until one row holds the name and
// the rate.
func (h *harness) waitForRecentRow(t *testing.T, i int, name, rate string) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		gotName, gotRate, _ := h.recentRow(i)
		if gotName == name && gotRate == rate {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("recent row %d reads %q %q, want %q %q\n%s", i, gotName, gotRate, name, rate, stacks())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForItem reads the table selection until it reaches the wanted row.
func (h *harness) waitForItem(t *testing.T, want int) int {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		got := h.selected()
		if got == want {
			return got
		}
		if time.Now().After(deadline) {
			t.Logf("the cursor never reached row %d\n%s", want, stacks())
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForGone reads the simulation screen until the text goes away.
func (h *harness) waitForGone(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		if !strings.Contains(h.text(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the screen still shows %q. It shows:\n%s\n%s", want, h.text(), stacks())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// bottomRow reads the last row of the screen, where the line of keys
// stands.
func (h *harness) bottomRow() string {
	lines := strings.Split(strings.TrimRight(h.text(), "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// messageRow reads the row over the line of keys, where the message bar
// stands while a message waits.
func (h *harness) messageRow() string {
	lines := strings.Split(strings.TrimRight(h.text(), "\n"), "\n")
	return strings.TrimSpace(lines[len(lines)-2])
}

// waitForMessage reads the message bar until it holds the text.
func (h *harness) waitForMessage(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		got := h.messageRow()
		if strings.Contains(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the message bar reads %q, want %q in it\n%s", got, want, stacks())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// loaded reads whether the core holds a stream. It runs on the goroutine
// that owns the state.
func (h *harness) loaded() bool {
	var out bool
	h.queue(func() { out = h.app.State.Loaded })
	return out
}

// waitForLoaded reads the core until it holds a stream, or holds none.
func (h *harness) waitForLoaded(t *testing.T, want bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for h.loaded() != want {
		if time.Now().After(deadline) {
			t.Fatalf("the core has Loaded = %v, want %v\n%s", !want, want, stacks())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// playing reads the URL that the core holds, on the goroutine that owns it.
func (h *harness) playing() string {
	var out string
	h.queue(func() { out = h.app.State.URL })
	return out
}

// waitForPlaying reads the core until it holds the URL, on the goroutine
// that owns the state.
func (h *harness) waitForPlaying(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		got := h.playing()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the core plays %q, want %q\n%s", got, want, stacks())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// infoText reads the text of the info panel. It runs on the goroutine
// that draws, because that goroutine owns the widget.
func (h *harness) infoText() string {
	var out string
	h.queue(func() { out = h.view.lastInfo })
	return out
}

// waitForStatus reads the info panel until its status row holds the mark
// of the play state.
func (h *harness) waitForStatus(t *testing.T, st radio.Status) {
	t.Helper()
	want := statusText(st)
	deadline := time.Now().Add(waitTimeout)
	for {
		got := h.infoText()
		if strings.Contains(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the info panel holds\n%s\nwant a status row of %q\n%s", got, want, stacks())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// loadConfig reads the config file that the front end wrote, and it fails
// the test when the file does not load.
func loadConfig(t *testing.T) radio.Config {
	t.Helper()
	cfg, err := radio.LoadConfig()
	if err != nil {
		t.Fatalf("the config file does not load: %v", err)
	}
	return cfg
}

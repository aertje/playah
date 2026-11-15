package tv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"playah/internal/directory"
)

// findDelay is the pause in the typing that starts a search. A search for
// each letter would send a request for each one, and the list would jump
// while the user types.
const findDelay = 300 * time.Millisecond

// findTimeout bounds one request to the station directory. The directory
// tries its other servers within that time when the first one fails.
const findTimeout = 15 * time.Second

// browseWidth is the width of the pane with the browse categories, its
// border included. The longest name, "Language", fits with its padding.
const browseWidth = 14

// The width of the columns in the station table, in screen cells. A long
// station name or a long list of tags must not push the other columns off
// the screen.
const (
	findNameWidth = 34
	findTagsWidth = 40
)

// findHint stands at the foot of the finder. It names the keys of the
// panes, which differ from the keys of the main page.
const findHint = " [yellow]Tab[-] [gray]next[-]  [yellow]j k[-] [gray]move[-]" +
	"  [yellow]Enter[-] [gray]play or open[-]  [yellow]Backspace[-] [gray]back[-]  [yellow]Esc[-] [gray]close[-]"

// category is one entry of the browse pane. Popular lists the stations
// themselves. The other entries first list the values to browse by, and
// a value then lists its stations.
type category struct {
	name   string
	kind   directory.Kind
	browse bool
}

// categories holds the entries of the browse pane, in the order that it
// shows them.
var categories = []category{
	{name: "Popular"},
	{name: "Genre", kind: directory.Genre, browse: true},
	{name: "Country", kind: directory.Country, browse: true},
	{name: "Language", kind: directory.Language, browse: true},
}

// finder is the page that searches and browses the station directory. The
// focus stands in the search field or in one of the two panes, and Tab
// moves it. The text of the field filters what the result pane shows.
//
// Only the tview main loop touches the finder. A request runs in its own
// goroutine, and it sends its result back through the pump of the view.
type finder struct {
	v *view

	root    *tview.Flex
	search  *tview.InputField
	browse  *tview.Table
	results *tview.Table

	// cat is the entry of the browse pane, and facet the value that the
	// user opened inside it. facet stays nil for Popular, and for a
	// category that lists its values.
	cat   int
	facet *directory.Facet

	// facets holds the values of each category after the first load. The
	// lists change little, so the finder loads each one once.
	facets map[directory.Kind][]directory.Facet

	// The rows of the result pane. It lists either values to browse by or
	// stations, and listing says which.
	listing  bool
	shown    []directory.Facet
	stations []directory.Station

	// reselect names the value to select when its list shows again, so
	// the way back leaves the cursor on the value that the user opened.
	reselect string

	// gen counts the requests. A result that arrives after a newer
	// request started belongs to text that the user already changed, so
	// the finder drops it.
	gen     int
	timer   *time.Timer
	cancel  context.CancelFunc
	loading bool

	// opened tells that the finder loaded its first list.
	opened bool

	// query is the text of the field at the last search. The field
	// reports each change, and a change that the finder made itself
	// must not start a search of its own.
	query string
}

// newFinder builds the widgets of the finder page.
func newFinder(v *view) *finder {
	f := &finder{v: v, facets: map[directory.Kind][]directory.Facet{}}

	// The field keeps the colors of the terminal, as the rest of the
	// screen does. The tview default paints it blue.
	f.search = tview.NewInputField().
		SetLabel(" Search  ").
		SetFieldWidth(0).
		SetFieldStyle(tcell.StyleDefault)
	f.search.SetBorder(true).SetTitle(" Find a station ")
	f.search.SetChangedFunc(f.typed)

	f.browse = tview.NewTable().SetSelectable(true, false)
	f.browse.SetBorder(true).SetTitle(" Browse ").SetBorderPadding(0, 0, 1, 1)
	markFocus(f.browse)
	for i, c := range categories {
		f.browse.SetCell(i, 0, rowCell(c.name, tcell.ColorDefault).SetExpansion(1))
	}
	f.browse.SetSelectionChangedFunc(func(row, _ int) { f.chooseCategory(row) })
	f.browse.SetSelectedFunc(func(int, int) { f.v.app.SetFocus(f.results) })

	f.results = tview.NewTable().SetSelectable(true, false)
	f.results.SetBorder(true).SetBorderPadding(0, 0, 1, 1)
	markFocus(f.results)
	f.results.SetSelectedFunc(func(row, _ int) { f.pick(row) })

	hint := tview.NewTextView().SetDynamicColors(true).SetText(findHint)

	panes := tview.NewFlex().
		AddItem(f.browse, browseWidth, 0, false).
		AddItem(f.results, 0, 1, true)

	f.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(f.search, 3, 0, false).
		AddItem(panes, 0, 1, true).
		AddItem(hint, 1, 0, false)
	f.root.SetInputCapture(f.capture)

	f.setTitle()
	return f
}

// markFocus makes the selected row of a pane say whether the pane holds
// the focus. Both panes keep a selected row, and the arrow keys move the
// one in reverse video. The other one stands underlined.
func markFocus(t *tview.Table) {
	t.SetSelectedStyle(tcell.StyleDefault.Underline(true))
	t.SetFocusFunc(func() { t.SetSelectedStyle(tcell.StyleDefault.Reverse(true)) })
	t.SetBlurFunc(func() { t.SetSelectedStyle(tcell.StyleDefault.Underline(true)) })
}

// open shows the finder. The first open loads the popular stations, so
// the list holds something to pick before the user types.
func (f *finder) open() {
	f.v.pages.HidePage(pageHelp)
	f.v.pages.ShowPage(pageFind)
	f.v.app.SetFocus(f.results)
	if !f.opened {
		f.opened = true
		f.refresh(0)
	}
}

// capture takes the keys of the finder before the widget with the focus
// sees them. Tab moves the focus between the search field and the two
// panes, and the keys act where the focus stands: the field takes the
// letters, and a pane moves on the vim keys.
func (f *finder) capture(event *tcell.EventKey) *tcell.EventKey {
	switch event.Key() {
	case tcell.KeyTab:
		f.cycle(1)
		return nil
	case tcell.KeyBacktab:
		f.cycle(-1)
		return nil
	}

	if f.search.HasFocus() {
		switch event.Key() {
		case tcell.KeyEnter, tcell.KeyDown:
			f.v.app.SetFocus(f.results)
			return nil
		}
		return event
	}

	switch event.Key() {
	case tcell.KeyLeft:
		f.v.app.SetFocus(f.browse)
		return nil
	case tcell.KeyRight:
		f.v.app.SetFocus(f.results)
		return nil
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		f.back()
		return nil
	case tcell.KeyRune:
		// The table moves on j, k, g and G by itself. The finder adds h
		// and l for the panes, as in vim.
		switch event.Rune() {
		case 'h':
			f.v.app.SetFocus(f.browse)
			return nil
		case 'l':
			f.v.app.SetFocus(f.results)
			return nil
		}
	}
	return event
}

// cycle moves the focus to the next widget, or to the one before for a
// negative step: the search field, the browse pane and the result pane.
func (f *finder) cycle(step int) {
	order := []tview.Primitive{f.search, f.browse, f.results}
	at := 0
	for i, p := range order {
		if p.HasFocus() {
			at = i
		}
	}
	f.v.app.SetFocus(order[(at+step+len(order))%len(order)])
}

// typed starts a new search after a pause, when the text of the field
// changed. Each change calls it, also the one that clears the field.
func (f *finder) typed(text string) {
	if text == f.query {
		return
	}
	f.query = text
	f.refresh(findDelay)
}

// clearSearch empties the field without a search of its own. The caller
// refreshes the result pane for the new place anyway.
func (f *finder) clearSearch() {
	f.query = ""
	f.search.SetText("")
}

// chooseCategory shows one entry of the browse pane. The pane calls it
// for each move of its cursor, so the result pane follows the cursor.
func (f *finder) chooseCategory(row int) {
	if row == f.cat || row < 0 || row >= len(categories) {
		return
	}
	f.cat = row
	f.facet = nil
	f.reselect = ""
	f.clearSearch()
	f.refresh(0)
}

// pick acts on one row of the result pane. A value opens its stations,
// and a station plays and closes the finder.
func (f *finder) pick(row int) {
	if f.listing {
		if row < 0 || row >= len(f.shown) {
			return
		}
		facet := f.shown[row]
		f.facet = &facet
		f.clearSearch()
		f.refresh(0)
		return
	}
	if row < 0 || row >= len(f.stations) {
		return
	}
	st := f.stations[row]
	f.v.closeFind()
	f.v.playStation(st)
}

// back goes up one level: from the stations of a value to the list of the
// values, and from there to the browse pane.
func (f *finder) back() {
	if f.facet == nil {
		f.v.app.SetFocus(f.browse)
		return
	}
	f.reselect = f.facet.Code
	f.facet = nil
	f.clearSearch()
	f.refresh(0)
	f.v.app.SetFocus(f.results)
}

// refresh fills the result pane for the current category, value and
// search text. A list of values filters on the screen, and a list of
// stations asks the directory after the given pause.
func (f *finder) refresh(delay time.Duration) {
	c := categories[f.cat]
	f.setTitle()
	if c.browse && f.facet == nil {
		if list, ok := f.facets[c.kind]; ok {
			f.stop()
			f.showFacets(list)
			return
		}
		f.load(0, func(ctx context.Context, dir *directory.Client) (func(), error) {
			list, err := dir.Facets(ctx, c.kind)
			return func() {
				f.facets[c.kind] = list
				f.showFacets(list)
			}, err
		})
		return
	}

	q := directory.Query{Name: strings.TrimSpace(f.search.GetText()), Facet: f.facet}
	f.load(delay, func(ctx context.Context, dir *directory.Client) (func(), error) {
		list, err := dir.Stations(ctx, q)
		return func() { f.showStations(list) }, err
	})
}

// load runs one request after the pause, in its own goroutine. The work
// returns the function that shows its result, and the pump runs that
// function in the main loop. A newer request, or the end of the user
// interface, makes the result go nowhere.
//
// The rows of the last list stay on the screen while the request runs, so
// the list does not blink at each letter. The title of the pane tells that
// a request runs.
func (f *finder) load(delay time.Duration, work func(context.Context, *directory.Client) (func(), error)) {
	f.stop()
	f.gen++
	gen := f.gen
	ctx, cancel := context.WithTimeout(context.Background(), findTimeout)
	f.cancel = cancel
	f.loading = true
	if f.results.GetRowCount() == 0 || f.isMessage() {
		f.message("Loading...", tcell.ColorGray)
	}
	f.setTitle()

	dir := f.v.a.Directory
	done := f.v.done
	updates := f.v.updates
	f.timer = time.AfterFunc(delay, func() {
		defer cancel()
		show, err := work(ctx, dir)
		apply := func() {
			if gen != f.gen {
				return
			}
			f.loading = false
			f.cancel = nil
			f.setTitle()
			if err != nil {
				f.message(err.Error(), tcell.ColorRed)
				return
			}
			show()
		}
		// A request that the finder cancelled has nothing to show.
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		select {
		case updates <- apply:
		case <-done:
		}
	})
}

// stop cancels the request that waits or runs. Its result then goes
// nowhere, because the count of the requests moves on.
func (f *finder) stop() {
	if f.timer != nil {
		f.timer.Stop()
	}
	if f.cancel != nil {
		f.cancel()
		f.cancel = nil
	}
	f.gen++
	if f.loading {
		f.loading = false
		f.setTitle()
	}
}

// setTitle names what the result pane shows: the category, and the value
// inside it. Dots follow the name while a request runs.
func (f *finder) setTitle() {
	title := categories[f.cat].name
	if f.facet != nil {
		title += " › " + f.facet.Name
	}
	if f.loading {
		title += " ..."
	}
	f.results.SetTitle(" " + tview.Escape(title) + " ")
}

// showFacets lists the values of a category that hold the search text.
func (f *finder) showFacets(list []directory.Facet) {
	text := strings.ToLower(strings.TrimSpace(f.search.GetText()))
	f.shown = f.shown[:0]
	for _, facet := range list {
		if strings.Contains(strings.ToLower(facet.Name), text) {
			f.shown = append(f.shown, facet)
		}
	}
	f.listing = true
	f.stations = nil

	if len(f.shown) == 0 {
		f.message("Nothing matches", tcell.ColorGray)
		f.listing = true
		return
	}

	f.results.Clear()
	selected := 0
	for i, facet := range f.shown {
		f.results.SetCell(i, 0, rowCell(tview.Escape(facet.Name), tcell.ColorDefault).
			SetMaxWidth(findNameWidth).SetExpansion(1))
		f.results.SetCell(i, 1, rowCell(stationCount(facet.Count), tcell.ColorGray).
			SetAlign(tview.AlignRight))
		if facet.Code == f.reselect {
			selected = i
		}
	}
	f.reselect = ""
	f.results.Select(selected, 0)
	if selected == 0 {
		f.results.ScrollToBeginning()
	}
}

// stationCount names the number of stations that one value holds.
func stationCount(n int) string {
	if n == 1 {
		return "1 station"
	}
	return fmt.Sprintf("%d stations", n)
}

// showStations lists the stations that the directory gave back. Each row
// names the station and gives its rate, its country and its tags.
func (f *finder) showStations(list []directory.Station) {
	f.listing = false
	f.shown = nil
	f.stations = list

	if len(list) == 0 {
		f.message("No stations found", tcell.ColorGray)
		return
	}

	f.results.Clear()
	for i, st := range list {
		rate := "-"
		if st.Bitrate > 0 {
			rate = fmt.Sprintf("%d kbps", st.Bitrate)
		}
		f.results.SetCell(i, 0, rowCell(tview.Escape(st.Name), tcell.ColorDefault).SetMaxWidth(findNameWidth))
		f.results.SetCell(i, 1, rowCell(rate, tcell.ColorGray).SetAlign(tview.AlignRight))
		f.results.SetCell(i, 2, rowCell(dash(st.Country), tcell.ColorGray))
		f.results.SetCell(i, 3, rowCell(tview.Escape(strings.Join(st.Tags, ", ")), tcell.ColorGray).
			SetMaxWidth(findTagsWidth).SetExpansion(1))
	}
	f.results.Select(0, 0)
	f.results.ScrollToBeginning()
}

// message fills the result pane with one line that the user cannot
// select: a wait, an empty result or a fault.
func (f *finder) message(text string, color tcell.Color) {
	f.shown = nil
	f.stations = nil
	f.results.Clear()
	f.results.SetCell(0, 0, rowCell(tview.Escape(text), color).SetSelectable(false).SetExpansion(1))
}

// isMessage tells that the result pane holds a message and no rows.
func (f *finder) isMessage() bool {
	return len(f.shown) == 0 && len(f.stations) == 0
}

package tv

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"playah/internal/directory"
	"playah/internal/radio"
)

// fakeStation is one station as the directory sends it.
type fakeStation struct {
	UUID    string `json:"stationuuid"`
	Name    string `json:"name"`
	URL     string `json:"url_resolved"`
	Codec   string `json:"codec"`
	Bitrate int    `json:"bitrate"`
	Country string `json:"countrycode"`
	Tags    string `json:"tags"`
}

// The stations of the fake directory. Popular is the list without a
// filter, and the jazz list answers the jazz tag.
var (
	popularStations = []fakeStation{
		{"u-groove", "Groove Salad", "https://example.com/groove", "MP3", 128, "US", "ambient,chill"},
		{"u-drone", "Drone Zone", "https://example.com/drone", "MP3", 128, "US", "ambient"},
	}
	jazzStations = []fakeStation{
		{"u-jazz", "Jazz Corner", "https://example.com/jazz", "AAC", 64, "NL", "jazz"},
	}
)

// fakeDirectory serves the Radio Browser API from memory, and it notes
// the plays that the front end counts.
type fakeDirectory struct {
	mu     sync.Mutex
	clicks []string
	fail   bool
}

// serve starts the fake directory, and it gives the core a client for it.
func (d *fakeDirectory) serve(t *testing.T) func(*radio.App) {
	t.Helper()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	return func(a *radio.App) {
		a.Directory = directory.NewWithServers("playah-test", []string{srv.URL})
	}
}

func (d *fakeDirectory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	fail := d.fail
	d.mu.Unlock()
	if fail {
		http.Error(w, "down", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()
	var out any
	switch {
	case r.URL.Path == "/json/stations/search":
		list := popularStations
		if q.Get("tag") == "jazz" {
			list = jazzStations
		}
		var found []fakeStation
		for _, st := range list {
			if strings.Contains(strings.ToLower(st.Name), strings.ToLower(q.Get("name"))) {
				found = append(found, st)
			}
		}
		out = found
	case r.URL.Path == "/json/tags":
		out = []map[string]any{{"name": "jazz", "stationcount": 1}, {"name": "rock", "stationcount": 7}}
	case r.URL.Path == "/json/countries":
		out = []map[string]any{{"name": "Netherlands", "iso_3166_1": "NL", "stationcount": 1}}
	case r.URL.Path == "/json/languages":
		out = []map[string]any{{"name": "dutch", "iso_639": "nl", "stationcount": 1}}
	case strings.HasPrefix(r.URL.Path, "/json/url/"):
		d.mu.Lock()
		d.clicks = append(d.clicks, strings.TrimPrefix(r.URL.Path, "/json/url/"))
		d.mu.Unlock()
		out = map[string]any{"ok": true}
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// clicked reads the plays that the directory counted.
func (d *fakeDirectory) clicked() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.clicks...)
}

// key presses one special key.
func (h *harness) key(k tcell.Key) {
	h.screen.InjectKey(k, 0, tcell.ModNone)
}

// typeText presses one key for each letter.
func (h *harness) typeText(text string) {
	for _, r := range text {
		h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

// startFind runs the front end with the fake directory and opens the
// finder on the popular stations.
func startFind(t *testing.T) (*harness, *fakeDirectory) {
	t.Helper()
	d := &fakeDirectory{}
	h := startWith(t, d.serve(t))
	h.typeText("f")
	h.waitFor(t, "Find a station")
	h.waitFor(t, "Drone Zone")
	return h, d
}

// TestFindOpensOnPopular checks that the finder lists the popular
// stations at once, with the rate, the country and the tags of each one.
func TestFindOpensOnPopular(t *testing.T) {
	h, _ := startFind(t)
	defer h.stop(t)

	h.waitFor(t, "Groove Salad")
	h.waitFor(t, "128 kbps")
	h.waitFor(t, "ambient, chill")
	h.waitFor(t, " Popular ")
}

// TestFindSearchesAsYouType checks that Tab reaches the search field and
// that the letters filter the stations there. The letters q and s quit
// and stop on the main page, and here they must reach the field instead.
func TestFindSearchesAsYouType(t *testing.T) {
	h, _ := startFind(t)
	defer h.stop(t)

	h.key(tcell.KeyTab)
	h.typeText("qs")
	h.waitFor(t, "No stations found")
	h.key(tcell.KeyBackspace2)
	h.key(tcell.KeyBackspace2)
	h.waitFor(t, "Drone Zone")

	h.typeText("groove")
	h.waitFor(t, "groove")
	h.waitForGone(t, "Drone Zone")
	h.waitFor(t, "Groove Salad")
}

// TestFindMovesOnTheVimKeys checks that j and k move in the result pane
// and type nothing, and that Tab walks from the result pane to the field
// and on to the browse pane.
func TestFindMovesOnTheVimKeys(t *testing.T) {
	h, _ := startFind(t)
	defer h.stop(t)

	h.typeText("j")
	h.waitForFindRow(t, 1)
	h.typeText("k")
	h.waitForFindRow(t, 0)
	if text := h.text(); strings.Contains(text, "Search  j") || strings.Contains(text, "Search  k") {
		t.Errorf("the letters reached the search field:\n%s", text)
	}

	h.key(tcell.KeyTab) // to the search field
	h.typeText("j")
	h.waitFor(t, "Search  j")
	h.key(tcell.KeyBackspace2)
	h.waitFor(t, "Drone Zone")

	h.key(tcell.KeyTab) // to the browse pane
	h.typeText("j")
	h.waitFor(t, " Genre ")
}

// waitForFindRow reads the result pane until its cursor stands on the row.
func (h *harness) waitForFindRow(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		var got int
		h.queue(func() { got, _ = h.view.find.results.GetSelection() })
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the cursor of the result pane stands on row %d, want %d\n%s", got, want, stacks())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestFindBrowsesAGenre walks the tree: the genre list, the stations of
// one genre, and back to the list with the cursor on that genre.
func TestFindBrowsesAGenre(t *testing.T) {
	h, _ := startFind(t)
	defer h.stop(t)

	h.typeText("hj") // to the browse pane, and down to Genre
	h.waitFor(t, " Genre ")
	h.waitFor(t, "7 stations")

	// The search field filters the list of the genres on the screen.
	h.key(tcell.KeyBacktab) // from the browse pane back to the field
	h.typeText("ja")
	h.waitForGone(t, "rock")
	h.key(tcell.KeyBackspace2)
	h.key(tcell.KeyBackspace2)
	h.waitFor(t, "rock")

	h.key(tcell.KeyEnter) // from the field to the result pane
	h.key(tcell.KeyEnter) // open jazz, the first genre
	h.waitFor(t, "Genre › jazz")
	h.waitFor(t, "Jazz Corner")

	h.key(tcell.KeyBackspace2)
	h.waitFor(t, "7 stations")
	h.waitForGone(t, "Genre › jazz")
}

// TestFindPlaysAStation picks a station. The finder closes, the stream
// plays, the recent table names it with its rate at once, and the play
// counts in the directory.
func TestFindPlaysAStation(t *testing.T) {
	h, d := startFind(t)
	defer h.stop(t)

	h.typeText("j")
	h.key(tcell.KeyEnter)
	h.waitForGone(t, "Find a station")
	h.waitForPlaying(t, "https://example.com/drone")
	h.waitForRecentRow(t, 0, "Drone Zone", "128 kbps")

	deadline := time.Now().Add(waitTimeout)
	for len(d.clicked()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("the play did not count in the directory\n%s", stacks())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := d.clicked(); got[0] != "u-drone" {
		t.Errorf("the directory counted %v, want [u-drone]", got)
	}
}

// TestFindClosesOnEscape checks that Escape leaves the finder, and that
// the letter keys work on the main page again.
func TestFindClosesOnEscape(t *testing.T) {
	h, _ := startFind(t)
	defer h.stop(t)

	h.key(tcell.KeyEsc)
	h.waitForGone(t, "Find a station")
	h.typeText("?")
	h.waitFor(t, "find a station in the directory")
}

// TestFindShowsAFault checks that a directory that fails says so in the
// result pane.
func TestFindShowsAFault(t *testing.T) {
	d := &fakeDirectory{fail: true}
	h := startWith(t, d.serve(t))
	defer h.stop(t)

	h.typeText("f")
	h.waitFor(t, "the station directory did not answer")
}

// TestFindWithoutDirectory checks the message for a core that holds no
// station directory.
func TestFindWithoutDirectory(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.typeText("f")
	h.waitFor(t, "playah has no station directory")
}

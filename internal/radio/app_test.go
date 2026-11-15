package radio

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"playah/internal/directory"
)

// property builds the event that the player sends for one mpv property.
func property(name string, data any) Event {
	return Event{Kind: EventProperty, Name: name, Data: data}
}

// TestOnlyTheStationNamesTheRecentItem drives the events that mpv sends
// for a stream that carries no ICY name, and then for one that does. The
// name that mpv builds out of the URL must not reach the recent list, and
// the name that the station sends must reach the config file.
func TestOnlyTheStationNamesTheRecentItem(t *testing.T) {
	useTempConfigDir(t)

	url := "https://example.com/groovesalad-128-mp3"
	a := NewApp(nil, Config{Volume: 80}, url)
	a.State.URL = url
	a.Cfg.Remember(url)

	a.Handle(property("media-title", "groovesalad-128-mp3"))
	if got := a.Cfg.Recent[0].Name; got != "" {
		t.Errorf("the recent list holds %q, want the URL until the station names itself", got)
	}

	a.Handle(property("metadata", map[string]any{"icy-name": "Groove Salad"}))
	if got := a.Cfg.Recent[0].Name; got != "Groove Salad" {
		t.Errorf("the recent list holds %q, want the station name", got)
	}
	if got := mustLoad(t).Recent[0].Name; got != "Groove Salad" {
		t.Errorf("the config file holds %q, want the station name", got)
	}

	a.Handle(property("audio-bitrate", 128123.0))
	a.Handle(property("audio-bitrate", 130400.0))
	if got := a.Cfg.Recent[0].Bitrate; got != 128 {
		t.Errorf("the recent list holds %d kbps, want the first reading", got)
	}
	if got := mustLoad(t).Recent[0].BitrateText(); got != "128 kbps" {
		t.Errorf("the config file holds %q, want the rate of the stream", got)
	}
}

// TestPlayStationNamesTheRecentItem plays a station from the directory.
// The recent list and the config file name it with its rate at once, and
// the pick counts in the directory even though no mpv loads the stream.
func TestPlayStationNamesTheRecentItem(t *testing.T) {
	useTempConfigDir(t)

	clicks := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clicks <- r.URL.Path
	}))
	defer srv.Close()

	a := NewApp(&Player{}, Config{Volume: 80}, "")
	a.Directory = directory.NewWithServers("playah-test", []string{srv.URL})
	st := directory.Station{UUID: "u-1", Name: "Groove Salad", URL: "https://example.com/groove", Bitrate: 128}

	if err := a.PlayStation(st); err == nil {
		t.Fatal("a player without mpv took the stream")
	}
	if got := a.Cfg.Recent[0]; got.URL != st.URL || got.Name != st.Name || got.Bitrate != st.Bitrate {
		t.Errorf("the recent list holds %+v, want the station", got)
	}
	if got := mustLoad(t).Recent[0].Name; got != st.Name {
		t.Errorf("the config file holds %q, want the station name", got)
	}

	select {
	case got := <-clicks:
		if got != "/json/url/u-1" {
			t.Errorf("the directory heard %q, want the click of the station", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the pick did not count in the directory")
	}
}

// fullState holds a value in every field that belongs to one track.
func fullState(url string) State {
	return State{
		URL: url, Loaded: true, Title: "Song", Station: "Name", stationTagged: true,
		Genre: "Ambient", Codec: "mp3", Bitrate: 128000, SampleRate: 44100,
		Channels: "stereo", Cache: 9, Levels: []Level{{RMS: -20, Peak: -3}},
	}
}

// TestPlayClearsEveryTrackField checks that a second stream starts with
// nothing from the first one.
func TestPlayClearsEveryTrackField(t *testing.T) {
	useTempConfigDir(t)
	p, _ := newFake(t, 16)
	a := NewApp(p, Config{Volume: 80}, "")
	a.State = fullState("http://old")

	if err := a.Play("http://new"); err != nil {
		t.Fatal(err)
	}

	want := State{URL: "http://new", Loaded: true}
	got := a.State
	if got.URL != want.URL || !got.Loaded || got.Title != "" || got.Station != "" ||
		got.stationTagged || got.Genre != "" || got.Codec != "" || got.Bitrate != 0 ||
		got.SampleRate != 0 || got.Channels != "" || got.Cache != 0 || got.Levels != nil {
		t.Errorf("state = %+v, want only the URL and Loaded", got)
	}
}

// TestLateEventsOfAnOldStreamAreDropped plays two streams in a row, and
// then delivers the data of the first one. Neither the state nor the
// recent list may take it.
func TestLateEventsOfAnOldStreamAreDropped(t *testing.T) {
	useTempConfigDir(t)
	p, _ := newFake(t, 16)
	a := NewApp(p, Config{Volume: 80}, "")
	if err := a.Play("http://a"); err != nil {
		t.Fatal(err)
	}
	if err := a.Play("http://b"); err != nil {
		t.Fatal(err)
	}

	late := func(name string, data any) Event {
		return Event{Kind: EventProperty, Name: name, Data: data, Stream: 1}
	}
	a.Handle(late("metadata", map[string]any{"icy-name": "Old Name", "icy-genre": "Old Genre", "icy-title": "Old"}))
	a.Handle(late("media-title", "old"))
	a.Handle(late("audio-bitrate", 320000.0))
	a.Handle(late("audio-codec-name", "aac"))
	a.Handle(late("audio-params/samplerate", 48000.0))
	a.Handle(late("audio-params/channels", "mono"))
	a.Handle(late("demuxer-cache-duration", 5.0))
	a.Handle(late(MeterProperty, map[string]any{"lavfi.astats.1.RMS_level": "-10"}))
	a.Handle(Event{Kind: EventError, Text: "old stream failed", Stream: 1})

	s := a.State
	if s.Title != "" || s.Station != "" || s.Genre != "" || s.Codec != "" || s.Bitrate != 0 ||
		s.SampleRate != 0 || s.Channels != "" || s.Cache != 0 || s.Levels != nil || s.Err != "" {
		t.Errorf("a late event reached the state: %+v", s)
	}
	for _, r := range a.Cfg.Recent {
		if r.Name != "" || r.Bitrate != 0 {
			t.Errorf("a late event reached the recent list: %+v", r)
		}
	}

	// The current stream still applies, and so do the global properties
	// and the exit, whatever stream they carry.
	a.Handle(Event{Kind: EventProperty, Name: "audio-bitrate", Data: 128000.0, Stream: 2})
	a.Handle(Event{Kind: EventProperty, Name: "pause", Data: true, Stream: 1})
	if a.State.Bitrate != 128000 || !a.State.Paused {
		t.Errorf("state = %+v, want the current bitrate and the pause", a.State)
	}
	a.Handle(Event{Kind: EventExit, Stream: 1})
	if a.State.Err == "" {
		t.Error("the exit event did not apply")
	}
}

// unsavableConfig makes the config file impossible to write.
func unsavableConfig(t *testing.T) {
	t.Helper()
	path := useTempConfigDir(t)
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), dirPerms); err != nil {
		t.Fatal(err)
	}
	// A file stands where the config directory must be.
	if err := os.WriteFile(filepath.Dir(path), nil, configPerms); err != nil {
		t.Fatal(err)
	}
}

func TestFailedSaveIsANoticeNotAnError(t *testing.T) {
	unsavableConfig(t)
	p, _ := newFake(t, 16)
	a := NewApp(p, Config{Volume: 80, Recent: []Recent{{URL: "http://x"}}}, "")

	if err := a.Play("http://a"); err == nil {
		t.Fatal("Play hid the failed save")
	}
	if a.State.Notice == "" || a.State.Err != "" {
		t.Errorf("notice = %q, err = %q, want a notice only", a.State.Notice, a.State.Err)
	}
	if got := a.State.Status(); got == StatusError {
		t.Error("a failed save made the status an error")
	}

	// Each command clears the notice at its start.
	if err := a.TogglePause(); err != nil {
		t.Fatal(err)
	}
	if a.State.Notice != "" {
		t.Errorf("notice = %q after another command", a.State.Notice)
	}

	if err := a.ToggleMeter(); err == nil || a.State.Notice == "" || a.State.Err != "" {
		t.Errorf("ToggleMeter: err = %v, notice = %q, state err = %q", err, a.State.Notice, a.State.Err)
	}
	if err := a.ChangeVolume(1); err != nil || a.State.Notice != "" {
		t.Errorf("ChangeVolume: err = %v, notice = %q, want it cleared", err, a.State.Notice)
	}
	if err := a.Forget("http://x"); err == nil || a.State.Notice == "" || a.State.Err != "" {
		t.Errorf("Forget: err = %v, notice = %q, state err = %q", err, a.State.Notice, a.State.Err)
	}
	if err := a.Stop(); err != nil || a.State.Notice != "" {
		t.Errorf("Stop: err = %v, notice = %q, want it cleared", err, a.State.Notice)
	}
}

func TestPlayStationSaveFaultIsANotice(t *testing.T) {
	unsavableConfig(t)
	p, _ := newFake(t, 16)
	a := NewApp(p, Config{Volume: 80}, "")
	st := directory.Station{UUID: "u", Name: "Groove", URL: "http://a", Bitrate: 128}

	_ = a.PlayStation(st)
	if a.State.Notice == "" || a.State.Err != "" {
		t.Errorf("notice = %q, err = %q, want a notice only", a.State.Notice, a.State.Err)
	}
}

func TestPlayerFaultStaysAnError(t *testing.T) {
	a := NewApp(&Player{}, Config{Volume: 80}, "")
	if err := a.TogglePause(); err == nil {
		t.Fatal("a player without mpv paused")
	}
	if a.State.Err == "" || a.State.Notice != "" || a.State.Status() != StatusError {
		t.Errorf("state = %+v, want the error", a.State)
	}
	a.Player, _ = newFake(t, 16)
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	if a.State.Err != "" {
		t.Errorf("Stop kept the error %q", a.State.Err)
	}
}

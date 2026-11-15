package radio

import (
	"context"
	"strings"
	"time"

	"playah/internal/directory"
)

// clickTimeout bounds the call that counts a play in the station
// directory. The count only feeds the popularity order of the directory,
// so a slow server must not hold a goroutine for long.
const clickTimeout = 10 * time.Second

// Frontend runs one user interface until the user quits. Each front end
// owns its main loop, its key map and its draw method. The core calls no
// front end, so a front end needs no interface to satisfy.
type Frontend func(*App) error

// App joins the player, the config and the state of the current stream.
//
// One goroutine owns an App. A front end must call the methods from the
// goroutine that owns the screen: tview does this in the draw queue, and
// Bubble Tea does it in Update. The App therefore needs no lock.
type App struct {
	Player *Player
	Cfg    Config
	State  State

	// StartURL is the stream to play at the start, and it is empty for
	// none. The caller decides: a URL from the command line always plays,
	// and the last URL of the config file plays only with auto play on.
	// A front end plays it as it stands.
	StartURL string

	// Directory is the station directory that a front end searches and
	// browses. It stays nil when the build or the test has none, and a
	// front end then offers no station search.
	Directory *directory.Client
}

// NewApp makes an App from a started player and a loaded config.
func NewApp(player *Player, cfg Config, startURL string) *App {
	return &App{
		Player:   player,
		Cfg:      cfg,
		State:    State{Volume: float64(cfg.Volume)},
		StartURL: startURL,
	}
}

// Play loads one stream and remembers it. Each command method writes a
// fault of the player into State.Err, and a fault of the config file into
// State.Notice, so every front end shows the same message. Play clears
// both before it starts. It returns the first fault, and a failed config
// write does not stop the stream.
func (a *App) Play(url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return nil
	}

	a.State.Err = ""
	a.State.Notice = ""
	a.State.URL = url
	// The stream before this one must leave nothing behind.
	a.State.clearTrack()
	a.State.Loaded = true
	a.Cfg.Remember(url)

	if err := a.Player.Load(url); err != nil {
		a.State.Err = err.Error()
		return err
	}
	return a.save()
}

// save writes the config file, and it shows a fault as a notice.
func (a *App) save() error {
	err := a.Save()
	if err != nil {
		a.State.Notice = err.Error()
	}
	return err
}

// PlayStation plays a station from the directory. The directory already
// knows the name and the rate of the stream, so the recent list shows both
// at once, before the stream itself sends them.
//
// The pick also counts in the directory, which orders its stations by
// popularity, even when mpv fails to load the stream: the directory asks
// to hear of each pick. That call runs in its own goroutine, because the
// directory may answer slowly, and its fault does not belong on the
// screen.
func (a *App) PlayStation(st directory.Station) error {
	err := a.Play(st.URL)

	named := a.Cfg.SetName(strings.TrimSpace(st.URL), st.Name)
	rated := a.Cfg.SetBitrate(strings.TrimSpace(st.URL), st.Bitrate)
	if named || rated {
		_ = a.save()
	}

	if a.Directory != nil {
		go func(dir *directory.Client, uuid string) {
			ctx, cancel := context.WithTimeout(context.Background(), clickTimeout)
			defer cancel()
			_ = dir.Click(ctx, uuid)
		}(a.Directory, st.UUID)
	}
	return err
}

// Stop ends playback and clears the track data. The state changes even
// when the command fails, as it does in Play: mpv that takes no command
// plays nothing, and a screen that still reads "Playing" would lie.
func (a *App) Stop() error {
	a.State.Err = ""
	a.State.Notice = ""
	a.State.Loaded = false
	a.State.clearTrack()

	if err := a.Player.Stop(); err != nil {
		a.State.Err = err.Error()
		return err
	}
	return nil
}

// TogglePause switches between play and pause.
func (a *App) TogglePause() error {
	a.State.Notice = ""
	if err := a.Player.TogglePause(); err != nil {
		a.State.Err = err.Error()
		return err
	}
	return nil
}

// ChangeVolume adds a step to the volume and keeps it in range.
func (a *App) ChangeVolume(step int) error {
	a.State.Notice = ""
	value := int(a.State.Volume) + step
	if value < minVolume {
		value = minVolume
	}
	if value > maxVolume {
		value = maxVolume
	}

	a.State.Volume = float64(value)
	a.Cfg.Volume = value
	if err := a.Player.SetVolume(value); err != nil {
		a.State.Err = err.Error()
		return err
	}
	return nil
}

// Forget takes one stream out of the recent list and writes the config
// file. A stream that plays keeps playing: only the list forgets it.
func (a *App) Forget(url string) error {
	a.State.Notice = ""
	if !a.Cfg.Forget(url) {
		return nil
	}
	return a.save()
}

// ToggleMeter shows or hides the level meter, and it writes the choice into
// the config file, so the next start shows the screen the same way.
func (a *App) ToggleMeter() error {
	a.State.Notice = ""
	a.Cfg.HideMeter = !a.Cfg.HideMeter
	return a.save()
}

// Handle applies one player event to the state.
//
// An event of a stream that is not the latest one is dropped, when it
// describes a stream: it would write the old name, rate or genre under
// the new stream. The properties of the player itself always apply.
func (a *App) Handle(ev Event) {
	if streamScoped(ev) && ev.Stream != a.stream() {
		return
	}
	switch ev.Kind {
	case EventProperty:
		a.State.Apply(ev.Name, ev.Data)
		a.rememberStream()
	case EventError:
		a.State.Err = ev.Text
	case EventExit:
		a.State.Err = "mpv stopped. Restart playah"
	}
}

// rememberStream writes the name and the rate of the current stream into
// the recent list, so the list shows both without a play. Only a name that
// the station sent counts: while a stream sends none, mpv names it by the
// last part of its URL, which the list does not keep.
//
// A failed write costs the data of one stream, and the next play writes it
// again, so the fault does not belong on the screen.
func (a *App) rememberStream() {
	changed := a.State.stationTagged && a.Cfg.SetName(a.State.URL, a.State.Station)
	if a.Cfg.SetBitrate(a.State.URL, a.State.Kbps()) {
		changed = true
	}
	if changed {
		_ = a.Save()
	}
}

// Save writes the config file.
func (a *App) Save() error { return a.Cfg.Save() }

// stream returns the number of the latest stream. A test may build an App
// without a player.
func (a *App) stream() int64 {
	if a.Player == nil {
		return 0
	}
	return a.Player.Stream()
}

// streamScoped reports whether the event describes one stream, so a late
// event of an older stream must not apply.
func streamScoped(ev Event) bool {
	switch ev.Kind {
	case EventError:
		return true
	case EventProperty:
		switch ev.Name {
		case "metadata", "media-title", "audio-bitrate", "audio-codec-name",
			"demuxer-cache-duration", MeterProperty:
			return true
		}
		return strings.HasPrefix(ev.Name, "audio-params/")
	}
	return false
}

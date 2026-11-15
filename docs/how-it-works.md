# How playah works

This document is for people who want to understand, change or extend
playah. For installing and using it, see the [README](../README.md).

- [Overview](#overview)
- [Code layout](#code-layout)
- [Talking to mpv](#talking-to-mpv)
- [The level meter](#the-level-meter)
- [Buffering](#buffering)
- [Drawing the screen](#drawing-the-screen)
- [The station finder](#the-station-finder)
- [The config file](#the-config-file)
- [Front ends](#front-ends)
- [Development](#development)
- [Limits](#limits)

## Overview

playah is a thin layer over mpv. It starts one mpv process, sends it
commands over a private IPC channel, and watches a handful of mpv
properties to draw the screen. All the decoding, networking and audio
output is mpv's job.

```
 ┌─────────────┐   keys    ┌────────────┐  JSON over a   ┌─────┐
 │  terminal   │ ────────► │   playah   │  socket pair   │ mpv │ ──► audio
 │  (tview UI) │ ◄──────── │ radio.App  │ ◄────────────► │     │
 └─────────────┘  redraw   └────────────┘                └─────┘
                                 │ HTTP
                                 ▼
                          Radio Browser API
```

## Code layout

The core package imports no user interface library. Each front end is one
function that runs its own main loop.

```
internal/radio/      player.go  state.go  config.go  app.go   standard library only
internal/directory/  directory.go                           the Radio Browser client
internal/ui/tv/      run.go  graph.go  find.go              the tview front end
main.go                                                     picks a front end from a map
.github/workflows/   ci.yml                                 build, vet, test, lint, govulncheck
                     release.yml                            on version tags: release binaries
```

`radio.App` holds the player, the config, the state and the station
directory. It gives a front end these commands: `Play`, `PlayStation`,
`Stop`, `TogglePause`, `ChangeVolume`, `Forget` and `ToggleMeter`.
`Handle` applies one player event, and `Save` writes the config.

A command writes a stream fault into `App.State.Err` and any other fault,
such as a failed config save, into `App.State.Notice`, so every front end
shows the same message. The UI shows a stream fault in red and a notice in
yellow; a notice does not change the play state.

`radio.Status` holds the play state. The core names the state and the
fault, and each front end picks the mark, the word and the color that show
them.

### Threading

`App` has no lock. One goroutine owns it, so a front end must call its
methods from the goroutine that owns the screen. The tview front end reads
the player events in a second goroutine and applies them with
`QueueUpdate`. A Bubble Tea front end would apply them in `Update`.

Each event carries the number of the stream that it belongs to. `Handle`
drops the events of a stream that mpv already replaced, so a late name or
rate cannot land on the new stream.

## Talking to mpv

playah starts mpv in idle mode and hands it one end of a socket pair with
`--input-ipc-client=fd://3`. The two programs talk over it in newline
delimited JSON. There is no socket file on disk, and when playah dies, mpv
sees the channel close and quits by itself.

playah observes these mpv properties and redraws the screen at most 6
times per second:

| Property | Shown as |
| --- | --- |
| `metadata` | Track, Station, Genre (from the `icy-title`, `icy-name` and `icy-genre` tags) |
| `pause`, `core-idle`, `idle-active` | Status |
| `audio-bitrate`, `audio-codec-name`, `audio-params/*` | Audio |
| `demuxer-cache-duration` | Buffer |
| `volume` | Volume |
| `af-metadata/lvl` | Level meter |

A station that sends no ICY tags shows a dash for the track.

### Shutting down

The front end traps `SIGINT`, `SIGTERM` and `SIGHUP`. The signal stops the
user interface, then `main` sends `quit` to mpv and kills it if it has not
stopped within 2 seconds.

## The level meter

mpv does not hand out audio samples over IPC, but it does pass on filter
measurements. playah adds one lavfi filter to the mpv audio chain:

```
@lvl:lavfi=[astats=metadata=1:reset=1:measure_perchannel=Peak_level+RMS_level:measure_overall=none]
```

The `astats` filter writes the peak and RMS level of each channel into the
frame metadata. mpv exposes that as the property `af-metadata/lvl` and
reports a new value about 16 times per second. The meter draws one upright
bar per channel.

playah adds the filter with the `af add` command after it connects. A build
of mpv without `astats` rejects the command, and playah then shows no
level at all. Passing the filter with `--af` on the command line would not
work, because a filter that fails there stops the sound entirely.

A spectrum analyser is not possible this way: it needs the samples
themselves. Drawing one would mean decoding a second copy of the stream,
for example with ffmpeg, and running a transform over it.

## Buffering

playah starts mpv with:

```
--cache=yes --cache-pause-initial=yes --cache-pause-wait=2
```

A station stays silent until the cache holds 2 seconds of sound, and the
status shows buffering until then. mpv uses the same wait when the cache
runs dry in the middle of a stream. The readahead stays at the mpv default,
so the cache keeps filling past the 2 seconds while the sound plays.

These options are on the command line, so they win over `mpv.conf`. Every
other setting in your `mpv.conf` still applies.

## Drawing the screen

The front end only draws when the text on the screen actually changes.
Each draw clears the screen and moves the terminal cursor, and mpv reports
the cache and the level many times per second, so drawing on every event
made the cursor flicker.

The front end also turns on bracketed paste, so the terminal sends a pasted
URL as one event. Without it the terminal sends one key event per letter,
tview redraws for each one, and a pasted URL fills in slowly.

### Screen layout

The screen needs about 78 columns and 17 rows. The level meter takes 15
columns and 10 rows, the info panel gets the rest of that width, and the
recent table takes the rows below.

A row of the info panel never wraps, because a row that grew would push
the rows below it out of the panel. A narrow screen cuts off the end of
the track title and the audio line instead. The recent table gives the
spare width to the URL column and cuts off a URL that does not fit.

## The station finder

The finder uses [Radio Browser](https://www.radio-browser.info), a free,
community maintained directory of some 50,000 stations. The client lives
in `internal/directory`.

- Popular lists the stations people play most. Genre, Country and Language
  list their entries with a station count, and opening one lists its
  stations.
- Typing in the search box searches station names after a short pause,
  within the genre, country or language that is open. On a list of genres,
  countries or languages it filters that list instead.
- Stations that Radio Browser marks as broken are hidden, and so is any
  stream without an http or https URL.
- playah registers each pick with the directory, which ranks its stations
  by that count.
- Each directory server gets 4 seconds to answer before the finder tries
  the next one. When none answers, the error shows in the result pane.

In the panes the keys are vim style and letters type nothing; in the search
box every letter types. The finder opens with the focus in the result pane.

## The config file

playah writes one JSON file, at the location from `os.UserConfigDir`:

- macOS: `~/Library/Application Support/playah/config.json`
- Linux: `$XDG_CONFIG_HOME/playah/config.json`

```json
{
  "last_url": "https://ice1.somafm.com/groovesalad-128-mp3",
  "volume": 80,
  "auto_play": true,
  "recent": [
    {
      "url": "https://ice1.somafm.com/groovesalad-128-mp3",
      "name": "Groove Salad [SomaFM]",
      "bitrate": 128
    },
    { "url": "https://ice2.somafm.com/dronezone-128-mp3" }
  ]
}
```

- `last_url` is the stream that plays on the next start. Removing that
  stream from the list with `d` also stops it playing on the next start.
- `recent` holds the last 12 streams. The cursor in the table follows the
  stream that plays.
- `hide_meter: true` appears when you hide the level meter with `l`, and
  the field is dropped again when you show it.
- `auto_play: false` makes playah open idle. A URL on the command line
  still plays.

playah writes a temporary file and renames it over the old one, so a crash
cannot leave a half written config. A config file that cannot be parsed
stops the start: the error names the file, and playah does not overwrite
it.

### Station names and rates

Each recent stream keeps the name the station sent and the rate mpv read
from the stream, in kbps. The table shows a dash for a value no play has
reported yet, and it always shows the URL.

Only the name from the ICY tags counts. For a station that sends no name,
mpv shows the last part of the URL instead; the info panel displays that,
but the table does not keep it.

The rate drifts by a few kbps while a stream plays, so playah only rewrites
the file when the rate leaves a 5 percent band around the stored value.

A config written by an older playah holds a plain URL for each stream. It
still loads, and those streams pick up their names on the next play.

## Front ends

A front end is a single function:

```go
// Frontend runs one user interface until the user quits.
type Frontend func(*App) error
```

The `-ui` option picks the front end, and the environment variable
`PLAYAH_UI` sets the default. This build has one, `tview`.

To add a front end, write a package under `internal/ui` with a `Run`
function of that type and add one line to the `frontends` map in
`main.go`. The core needs no change. A second front end, such as one in
Bubble Tea, would share no widget interface with the first.

Check that the core still depends on no UI library with:

```sh
go list -deps ./internal/radio | grep -E 'tview|tcell'   # must print nothing
```

## Development

### Build

```sh
go build -o playah .
```

### Tests

```sh
go test ./...                  # no network needed
PLAYAH_SMOKE=1 go test ./...   # adds a smoke test that plays a real stream
go test -race ./...            # run this before changing the player or the event pump
```

- **`internal/radio`** has unit tests for the state, the config and the
  app. The player tests run against a fake mpv on a socket pair. One test
  starts a real mpv and checks that it quits when the channel closes; it
  needs no network and skips when mpv is missing. The smoke test, enabled
  by `PLAYAH_SMOKE=1`, starts mpv at volume 0, loads a SomaFM stream and
  waits for the first ICY title.
- **`internal/ui/tv`** runs the whole front end on a tcell simulation
  screen. The tests push the events mpv would send, read the screen cells
  and quit with `Ctrl-Q`. They need no mpv. They also count draws, so a
  change that brings back the flicker fails the tests.
- **`internal/directory`** tests the Radio Browser client against an
  `httptest` server, and the finder tests use the same kind of fake
  directory. No test calls the real directory.

Every wait in the UI test harness is bounded, and a wait that runs out
prints the stack of every goroutine. A race in the tests of the add popup
was found and fixed. Once, the front end tests stopped answering for longer
than the wait allowed; the hang did not come back under load, and its cause
is not known.

## Limits

- The IPC channel is a unix socket pair, so Windows does not work.
- mpv must accept the stream. playah adds no protocol support of its own.
- The finder needs the network.

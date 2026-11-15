# playah

**Internet radio in your terminal.** playah is a small, keyboard driven
player for radio streams, built on [mpv](https://mpv.io). Paste a stream
URL or search thousands of stations, and playah remembers what you listen
to so the next start picks up where you left off.

- Shows the current track, station, audio format and a live level meter
- Finds stations in the free [Radio Browser](https://www.radio-browser.info)
  directory, by name, genre, country or language
- Keeps your last 12 stations one keypress away
- Vim style keys, with arrow keys too

## Install

You need:

- **mpv** on your `PATH`
  - macOS: `brew install mpv`
  - Debian/Ubuntu: `sudo apt install mpv`
- **Go 1.27** or later, to build playah
- macOS or Linux (Windows is not supported)

Then build it:

```sh
git clone https://github.com/aertje/playah.git
cd playah
go build -o playah .
```

Move the `playah` binary anywhere on your `PATH`, or run it in place as
`./playah`.

## Quick start

```sh
playah
```

The first time, the station list is empty. Either:

- press **`f`** to search for a station and press **`Enter`** to play it, or
- press **`a`**, paste a stream URL and press **`Enter`**.

You can also pass a URL straight away:

```sh
playah https://ice1.somafm.com/groovesalad-128-mp3
```

From then on, plain `playah` starts playing the last station you listened
to. Press **`?`** at any time to see every key, and **`q`** to quit.

## Finding stations

Press **`f`** to open the finder. The left pane lets you browse
**Popular**, **Genre**, **Country** and **Language**; the right pane shows
what you picked. Press **`Enter`** on a station to play it and add it to
your list.

To search, press **`Tab`** to move to the search box and start typing. In
a list of stations it searches by name; in a list of genres, countries or
languages it filters that list.

| Key | In the finder |
| --- | --- |
| `Tab` / `Shift-Tab` | Move between the search box and the two panes |
| `j` `k` or `↓` `↑` | Move down and up |
| `h` `l` or `←` `→` | Jump to the left or right pane |
| `g` / `G` | Jump to the top or bottom |
| `Enter` | Open a genre, country or language, or play a station |
| `Backspace` | Go back one level |
| `Esc` | Close the finder |

Stations that Radio Browser reports as broken are hidden.

## Keys

| Key | Action |
| --- | --- |
| `Space` | Play the selected station, or stop the one playing |
| `Enter` | Play the selected station |
| `1` to `9` | Play one of your recent stations by number |
| `j` `k` or `↓` `↑` | Move through your recent stations |
| `p` | Pause or resume |
| `s` | Stop |
| `+` / `-` | Volume up or down by 5% |
| `a` | Add a station by URL |
| `f` | Find a station |
| `d` or `Delete` | Remove the selected station from your list |
| `l` | Show or hide the level meter |
| `?` | Show or hide all keys |
| `Esc` | Close a popup |
| `q` | Quit |

While the URL popup is open, letters go into the text field. Use
`Ctrl-Up` / `Ctrl-Down` for volume and `Ctrl-P`, `Ctrl-S`, `Ctrl-Q` to
pause, stop or quit from there.

## Settings

playah saves its settings automatically in a small JSON file:

- macOS: `~/Library/Application Support/playah/config.json`
- Linux: `~/.config/playah/config.json` (or `$XDG_CONFIG_HOME/playah/`)

Most of it (your recent stations, volume, whether the meter is hidden) is
managed for you. The one setting you may want to change by hand is
`auto_play`:

```json
{
  "auto_play": false
}
```

With `auto_play` set to `false`, playah opens without playing anything and
waits for you to pick a station. A URL on the command line still plays.

If the file gets corrupted, playah refuses to start and tells you which
file it is, rather than overwriting it. Fix or delete the file to continue.

Your own `mpv.conf` still applies, except for the few cache options that
playah sets itself.

## Tips and troubleshooting

- **Screen size.** playah needs a terminal of about 78 by 17 characters.
  In a narrower window, long titles and URLs are cut off.
- **No level meter?** Your mpv build lacks the `astats` audio filter.
  Everything else still works.
- **Track shows a dash.** Some stations do not send track titles.
- **Finder shows an error.** The finder needs an internet connection to
  reach Radio Browser. Your saved stations still play.
- **A stream will not play.** playah plays whatever mpv can play. Try the
  URL with `mpv <url>` to see mpv's own error.

## Under the hood

Curious how playah talks to mpv, draws the level meter, or how the code is
laid out? See [How playah works](docs/how-it-works.md). It also covers
building, testing and adding a new front end.

## License

[MIT](LICENSE)

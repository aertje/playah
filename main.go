// Command playah is a small terminal front end for mpv. It plays internet
// radio streams, and it shows the ICY metadata that the station sends.
//
// The core package internal/radio holds the player, the state and the
// config. It imports no user interface library. Each front end is one
// radio.Frontend function that runs its own main loop.
package main

import (
	"flag"
	"fmt"
	"maps"
	"os"
	"runtime/debug"
	"slices"
	"strings"

	"playah/internal/directory"
	"playah/internal/radio"
	"playah/internal/ui/tv"
)

// frontends holds the user interfaces that this build includes. A new
// front end needs one line here and one package under internal/ui.
var frontends = map[string]radio.Frontend{
	"tview": tv.Run,
}

const defaultFrontend = "tview"

// userAgent names playah and its version to the station directory. The
// directory asks every client to send a name that says what calls it.
func userAgent() string { return "playah/" + version() }

// version returns the module version that the build recorded, or "dev"
// for a build without one, such as go run or go build in a checkout.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "dev"
	}
	return info.Main.Version
}

// startURL picks the stream to play at the start. A URL on the command
// line always plays. The last stream of the config file plays only when
// the config asks for auto play. Otherwise nothing plays.
func startURL(arg string, cfg radio.Config) string {
	if arg = strings.TrimSpace(arg); arg != "" {
		return arg
	}
	if cfg.AutoPlay {
		return cfg.LastURL
	}
	return ""
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "playah:", err)
		os.Exit(1)
	}
}

func run() error {
	names := slices.Sorted(maps.Keys(frontends))

	fallback := os.Getenv("PLAYAH_UI")
	if fallback == "" {
		fallback = defaultFrontend
	}
	uiName := flag.String("ui", fallback, "the user interface: "+strings.Join(names, ", "))
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: playah [-ui name] [stream URL]")
		flag.PrintDefaults()
	}
	flag.Parse()

	frontend, ok := frontends[*uiName]
	if !ok {
		return fmt.Errorf("unknown user interface %q. This build has: %s",
			*uiName, strings.Join(names, ", "))
	}

	cfg, err := radio.LoadConfig()
	if err != nil {
		return err
	}
	player, err := radio.StartPlayer(cfg.Volume)
	if err != nil {
		return err
	}
	defer player.Close()

	app := radio.NewApp(player, cfg, startURL(flag.Arg(0), cfg))
	app.Directory = directory.New(userAgent())
	runErr := frontend(app)
	if err := app.Save(); err != nil {
		fmt.Fprintln(os.Stderr, "playah: the config did not save:", err)
	}
	return runErr
}

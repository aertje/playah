package radio

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	appName     = "playah"
	maxRecent   = 12
	defaultVol  = 80
	minVolume   = 0
	maxVolume   = 130
	configPerms = 0o600
	dirPerms    = 0o700
)

// Recent is one stream in the recent list. The name is the station name
// that the stream reported, and the bitrate is the rate that mpv read from
// it. Both stand empty until the stream played once.
type Recent struct {
	URL     string `json:"url"`
	Name    string `json:"name,omitempty"`
	Bitrate int    `json:"bitrate,omitempty"` // kbps
}

// NameText returns the station name, or a dash while the station has not
// named itself yet.
func (r Recent) NameText() string {
	if r.Name == "" {
		return "-"
	}
	return r.Name
}

// BitrateText returns the rate of the stream, or a dash while no stream
// played long enough to report one.
func (r Recent) BitrateText() string {
	if r.Bitrate <= 0 {
		return "-"
	}
	return strconv.Itoa(r.Bitrate) + " kbps"
}

// UnmarshalJSON reads one entry. A config that an older playah wrote holds
// a plain URL for each entry, so a string reads as an entry without a name.
func (r *Recent) UnmarshalJSON(data []byte) error {
	var url string
	if err := json.Unmarshal(data, &url); err == nil {
		*r = Recent{URL: url}
		return nil
	}
	// The alias drops the methods, so Unmarshal does not call this one again.
	type entry Recent
	var out entry
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*r = Recent(out)
	return nil
}

// Config holds the state that survives a restart.
type Config struct {
	LastURL  string   `json:"last_url"`
	Volume   int      `json:"volume"`
	AutoPlay bool     `json:"auto_play"`
	Recent   []Recent `json:"recent"`

	// HideMeter hides the level meter. The field names the hidden state, so
	// a config that an older playah wrote shows the meter, as it did.
	HideMeter bool `json:"hide_meter,omitempty"`
}

// configPath returns the config file location for this user.
// On macOS this is ~/Library/Application Support/playah/config.json.
// On Linux it is $XDG_CONFIG_HOME/playah/config.json.
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appName, "config.json"), nil
}

// LoadConfig reads the config file. A missing file gives the defaults. A
// file that cannot be read or parsed gives an error, because the defaults
// would replace the file at the next save and lose what the user wrote.
func LoadConfig() (Config, error) {
	cfg := Config{Volume: defaultVol, AutoPlay: true}

	path, err := configPath()
	if err != nil {
		// Without a config directory there is no file to lose.
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("the config file %s cannot be read: %w. Fix or remove it", path, err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("the config file %s is not valid: %w. Fix or remove it", path, err)
	}
	if cfg.Volume < minVolume || cfg.Volume > maxVolume {
		cfg.Volume = defaultVol
	}
	for i, r := range cfg.Recent {
		if fromURL(r.URL, r.Name) {
			cfg.Recent[i].Name = ""
		}
	}
	return cfg, nil
}

// fromURL reports whether mpv built the name out of the URL. mpv names a
// stream by the last part of its URL while the station sends no name of
// its own, and an earlier playah wrote such a name into the config. The
// list shows the whole URL for those streams again.
func fromURL(url, name string) bool {
	if name == "" {
		return false
	}
	tail := url
	if i := strings.IndexAny(tail, "?#"); i >= 0 {
		tail = tail[:i]
	}
	tail = path.Base(tail)
	return strings.EqualFold(name, tail) ||
		strings.EqualFold(name, strings.TrimSuffix(tail, path.Ext(tail)))
}

// Save writes the config file. It writes a temporary file in the same
// directory first, then it renames that file. A crash therefore cannot
// leave a partial config, and the unique name keeps two writers apart.
func (c Config) Save() (err error) {
	path, err := configPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerms); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	f, err := os.CreateTemp(dir, "config-*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	if err = f.Chmod(configPerms); err != nil {
		return err
	}
	if _, err = f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// Remember puts one URL at the top of the recent list. A stream that the
// list already holds keeps the station name that it reported before.
func (c *Config) Remember(url string) {
	if url == "" {
		return
	}
	c.LastURL = url
	out := []Recent{c.entry(url)}
	for _, old := range c.Recent {
		if old.URL != url && old.URL != "" {
			out = append(out, old)
		}
		if len(out) == maxRecent {
			break
		}
	}
	c.Recent = out
}

// Forget takes one URL out of the recent list. It reports whether the
// list held the URL. A URL that was the last one played no longer plays
// on the next start either, because the user asked to be rid of it.
func (c *Config) Forget(url string) bool {
	for i, r := range c.Recent {
		if r.URL != url {
			continue
		}
		c.Recent = append(c.Recent[:i], c.Recent[i+1:]...)
		if c.LastURL == url {
			c.LastURL = ""
		}
		return true
	}
	return false
}

// entry returns the item that the list holds for one URL, or a fresh item
// for a URL that the list does not hold.
func (c *Config) entry(url string) Recent {
	for _, r := range c.Recent {
		if r.URL == url {
			return r
		}
	}
	return Recent{URL: url}
}

// SetName writes the station name of one URL. It reports whether the name
// changed, so the caller writes the config file only for a new name.
func (c *Config) SetName(url, name string) bool {
	if url == "" || name == "" {
		return false
	}
	for i, r := range c.Recent {
		if r.URL != url {
			continue
		}
		if r.Name == name {
			return false
		}
		c.Recent[i].Name = name
		return true
	}
	return false
}

// rateBand is the room that a stored bitrate holds, in percent. mpv reads
// the rate from the stream many times each second, and the reading wobbles
// by a few kbps while the decoder runs. A write for each wobble would
// rewrite the config file all through the play, so only a rate that leaves
// the band counts as a new rate: a station that changed its stream.
const rateBand = 5

// SetBitrate writes the rate of one URL in kbps. It reports whether the
// rate changed, so the caller writes the config file only for a new rate.
func (c *Config) SetBitrate(url string, kbps int) bool {
	if url == "" || kbps <= 0 {
		return false
	}
	for i, r := range c.Recent {
		if r.URL != url {
			continue
		}
		if diff := kbps - r.Bitrate; r.Bitrate > 0 && abs(diff)*100 <= r.Bitrate*rateBand {
			return false
		}
		c.Recent[i].Bitrate = kbps
		return true
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

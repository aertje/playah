package radio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRememberMovesURLToTheTop(t *testing.T) {
	c := Config{Recent: []Recent{{URL: "a"}, {URL: "b"}, {URL: "c"}}}
	c.Remember("c")

	if c.LastURL != "c" {
		t.Errorf("last URL = %q", c.LastURL)
	}
	want := []string{"c", "a", "b"}
	if len(c.Recent) != 3 {
		t.Fatalf("recent = %v", c.Recent)
	}
	for i, url := range want {
		if c.Recent[i].URL != url {
			t.Fatalf("recent = %v, want %v", c.Recent, want)
		}
	}
}

func TestSetNameNamesTheStream(t *testing.T) {
	c := Config{Recent: []Recent{{URL: "a"}, {URL: "b"}}}

	if !c.SetName("a", "Groove Salad") {
		t.Fatal("the first name did not change the list")
	}
	if c.SetName("a", "Groove Salad") {
		t.Error("the same name changed the list again")
	}
	if c.SetName("c", "Drone Zone") {
		t.Error("a URL that the list does not hold changed the list")
	}
	if c.SetName("a", "") {
		t.Error("an empty name changed the list")
	}

	// The stream keeps its name when it moves back to the top.
	c.Remember("b")
	c.Remember("a")
	if got := c.Recent[0]; got.URL != "a" || got.Name != "Groove Salad" {
		t.Errorf("the top item is %+v, want the name back", got)
	}
	if got := c.Recent[0].NameText(); got != "Groove Salad" {
		t.Errorf("name = %q, want the station name", got)
	}
	if got := c.Recent[1].NameText(); got != "-" {
		t.Errorf("name = %q, want a dash for a stream without a name", got)
	}
}

// TestSetBitrateKeepsTheWobble checks the rate that mpv reads from the
// stream. The reading moves by a few kbps while the decoder runs, and only
// a rate that leaves the band may rewrite the config file.
func TestSetBitrateKeepsTheWobble(t *testing.T) {
	c := Config{Recent: []Recent{{URL: "a"}}}

	if !c.SetBitrate("a", 128) {
		t.Fatal("the first rate did not change the list")
	}
	if got := c.Recent[0].BitrateText(); got != "128 kbps" {
		t.Errorf("rate = %q", got)
	}
	if c.SetBitrate("a", 131) || c.SetBitrate("a", 124) {
		t.Error("a wobble of the reading changed the list")
	}
	if got := c.Recent[0].Bitrate; got != 128 {
		t.Errorf("rate = %d, want the first reading", got)
	}
	if !c.SetBitrate("a", 320) {
		t.Error("a stream of another rate did not change the list")
	}
	if c.SetBitrate("a", 0) || c.SetBitrate("b", 128) {
		t.Error("an unknown rate or URL changed the list")
	}

	// The stream keeps its rate when it moves back to the top.
	c.Remember("a")
	if got := c.Recent[0].Bitrate; got != 320 {
		t.Errorf("rate = %d, want the rate back", got)
	}
	if got := (Recent{URL: "b"}).BitrateText(); got != "-" {
		t.Errorf("rate = %q, want a dash for a stream that never played", got)
	}
}

// TestOldConfigReadsTheRecentList checks the config that an older playah
// wrote, which held a plain URL for each item of the recent list.
func TestOldConfigReadsTheRecentList(t *testing.T) {
	path := useTempConfigDir(t)
	if err := os.MkdirAll(filepath.Dir(path), dirPerms); err != nil {
		t.Fatal(err)
	}
	old := `{"last_url":"a","volume":60,"auto_play":true,"recent":["a","b"]}`
	if err := os.WriteFile(path, []byte(old), configPerms); err != nil {
		t.Fatal(err)
	}

	cfg := mustLoad(t)
	if cfg.Volume != 60 || cfg.LastURL != "a" {
		t.Fatalf("the old config did not load: %+v", cfg)
	}
	if len(cfg.Recent) != 2 || cfg.Recent[0].URL != "a" || cfg.Recent[1].URL != "b" {
		t.Errorf("recent = %+v, want the two URLs", cfg.Recent)
	}
	if cfg.Recent[0].Name != "" {
		t.Errorf("recent = %+v, want no name yet", cfg.Recent)
	}
}

func TestNamesSurviveSaveAndLoad(t *testing.T) {
	useTempConfigDir(t)

	in := Config{Volume: 55, AutoPlay: true}
	in.Remember("https://example.com/stream")
	in.SetName("https://example.com/stream", "Groove Salad")
	if err := in.Save(); err != nil {
		t.Fatal(err)
	}

	out := mustLoad(t)
	if len(out.Recent) != 1 || out.Recent[0].Name != "Groove Salad" {
		t.Errorf("recent = %+v, want the station name", out.Recent)
	}
}

// TestLoadDropsTheNameFromTheURL checks the repair of a config that an
// earlier playah wrote, which kept the name that mpv builds out of the URL
// for a stream that sends no name of its own.
func TestLoadDropsTheNameFromTheURL(t *testing.T) {
	useTempConfigDir(t)

	in := Config{Volume: 80, AutoPlay: true, Recent: []Recent{
		{URL: "https://example.com/groovesalad-128-mp3", Name: "groovesalad-128-mp3"},
		{URL: "https://example.com/dronezone.mp3?x=1", Name: "dronezone"},
		{URL: "https://example.com/groovesalad-128-mp3", Name: "Groove Salad"},
	}}
	if err := in.Save(); err != nil {
		t.Fatal(err)
	}

	out := mustLoad(t)
	if out.Recent[0].Name != "" || out.Recent[1].Name != "" {
		t.Errorf("recent = %+v, want no name from the URL", out.Recent[:2])
	}
	if out.Recent[2].Name != "Groove Salad" {
		t.Errorf("recent = %+v, want the station name kept", out.Recent[2])
	}
}

func TestRememberKeepsTheLimit(t *testing.T) {
	var c Config
	for i := range maxRecent + 5 {
		c.Remember(string(rune('a' + i)))
	}
	if len(c.Recent) > maxRecent {
		t.Errorf("recent holds %d items, want %d at most", len(c.Recent), maxRecent)
	}
}

// useTempConfigDir points the config functions at a temporary directory.
// macOS reads HOME, and Linux reads XDG_CONFIG_HOME, so the test sets both.
func useTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config"))

	path, err := configPath()
	if err != nil {
		t.Skip("this platform has no user config directory")
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("the test would write to the real config path %s", path)
	}
	return path
}

func TestSaveAndLoad(t *testing.T) {
	useTempConfigDir(t)

	in := Config{LastURL: "https://example.com/stream", Volume: 55, AutoPlay: true}
	in.Remember("https://example.com/stream")
	if err := in.Save(); err != nil {
		t.Fatal(err)
	}

	out := mustLoad(t)
	if out.LastURL != in.LastURL || out.Volume != 55 || !out.AutoPlay {
		t.Errorf("loaded %+v, want %+v", out, in)
	}
}

// mustLoad loads the config of the test, which must be valid.
func mustLoad(t *testing.T) Config {
	t.Helper()
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMissingConfigGivesDefaults(t *testing.T) {
	useTempConfigDir(t)

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Volume != defaultVol || !cfg.AutoPlay {
		t.Errorf("defaults did not apply: %+v", cfg)
	}
}

// TestBadConfigIsAnErrorAndStaysOnDisk checks that a config that does not
// parse is not replaced by the defaults, and that the error names the file.
func TestBadConfigIsAnErrorAndStaysOnDisk(t *testing.T) {
	path := useTempConfigDir(t)
	if err := os.MkdirAll(filepath.Dir(path), dirPerms); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{broken"), configPerms); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig()
	if err == nil {
		t.Fatal("a broken config loaded")
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "Fix or remove") {
		t.Errorf("the error %q must name the file and tell the fix", err)
	}
	if got, _ := os.ReadFile(path); string(got) != "{broken" {
		t.Errorf("the file changed to %q", got)
	}
}

func TestUnreadableConfigIsAnError(t *testing.T) {
	path := useTempConfigDir(t)
	// A directory in place of the file makes the read fail.
	if err := os.MkdirAll(path, dirPerms); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("error = %v, want one that names %s", err, path)
	}
}

func TestSaveLeavesNoTempFileAndKeepsTheMode(t *testing.T) {
	path := useTempConfigDir(t)
	cfg := Config{Volume: 40}
	for range 2 {
		if err := cfg.Save(); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Errorf("the config directory holds %v, want only config.json", entries)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != configPerms {
		t.Errorf("mode = %v, want %v", info.Mode().Perm(), os.FileMode(configPerms))
	}
}

func TestFailedSaveRemovesTheTempFile(t *testing.T) {
	path := useTempConfigDir(t)
	// A directory that holds a file stands in the way of the rename.
	if err := os.MkdirAll(filepath.Join(path, "keep"), dirPerms); err != nil {
		t.Fatal(err)
	}
	if err := (Config{Volume: 40}).Save(); err == nil {
		t.Fatal("the save over a directory worked")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("the failed save left %s", e.Name())
		}
	}
}

// TestForgetDropsTheStream takes one stream out of the recent list. The
// others keep their order, and the last URL goes with the stream, so the
// next start does not play it.
func TestForgetDropsTheStream(t *testing.T) {
	var c Config
	for _, url := range []string{"c", "b", "a"} {
		c.Remember(url)
	}

	if !c.Forget("a") {
		t.Fatal("Forget did not find a URL that the list holds")
	}
	var got []string
	for _, r := range c.Recent {
		got = append(got, r.URL)
	}
	if strings.Join(got, " ") != "b c" {
		t.Errorf("the list holds %v, want [b c]", got)
	}
	if c.LastURL != "" {
		t.Errorf("the last URL is %q, want none after the stream is gone", c.LastURL)
	}

	if c.Forget("x") {
		t.Error("Forget found a URL that the list does not hold")
	}
	c.LastURL = "b"
	c.Forget("c")
	if c.LastURL != "b" {
		t.Errorf("the last URL is %q, want b, which stays in the list", c.LastURL)
	}
}

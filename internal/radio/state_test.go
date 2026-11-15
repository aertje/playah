package radio

import "testing"

func TestApplyMetadata(t *testing.T) {
	var s State
	s.Apply("idle-active", false)
	s.Apply("metadata", map[string]any{
		"icy-title": "Artist - Song",
		"icy-name":  "Groove Salad",
		"icy-genre": "Ambient",
	})
	s.Apply("audio-bitrate", 128000.0)
	s.Apply("audio-params/samplerate", 44100.0)
	s.Apply("audio-codec-name", "mp3")

	if s.Title != "Artist - Song" {
		t.Errorf("title = %q", s.Title)
	}
	if s.Station != "Groove Salad" || s.Genre != "Ambient" {
		t.Errorf("station = %q genre = %q", s.Station, s.Genre)
	}
	if got := s.BitrateText(); got != "128 kbps" {
		t.Errorf("bitrate = %q", got)
	}
	if got := s.SampleRateText(); got != "44.1 kHz" {
		t.Errorf("sample rate = %q", got)
	}

	if got := s.Status(); got != StatusPlaying {
		t.Errorf("status = %v, want Playing", got)
	}
	s.Apply("core-idle", true)
	if got := s.Status(); got != StatusBuffering {
		t.Errorf("status = %v, want Buffering", got)
	}
	s.Apply("pause", true)
	if got := s.Status(); got != StatusPaused {
		t.Errorf("status = %v, want Paused", got)
	}
}

func TestIdleClearsTrack(t *testing.T) {
	var s State
	s.Apply("idle-active", false)
	s.Apply("metadata", map[string]any{"icy-title": "Song"})
	s.Apply("idle-active", true)

	if s.Title != "" || s.Loaded {
		t.Errorf("the track did not clear: %+v", s)
	}
	if got := s.Status(); got != StatusIdle {
		t.Errorf("status = %v, want Idle", got)
	}
}

func TestSeparateArtistTag(t *testing.T) {
	var s State
	s.Apply("idle-active", false)
	s.Apply("metadata", map[string]any{"title": "Song", "artist": "Artist"})
	if s.Title != "Artist - Song" {
		t.Errorf("title = %q", s.Title)
	}
}

// TestMediaTitleDoesNotTagTheStation checks the name that mpv builds out
// of the URL for a stream that sends no name of its own. The info panel
// shows it, and the recent list must not keep it.
func TestMediaTitleDoesNotTagTheStation(t *testing.T) {
	var s State
	s.Apply("idle-active", false)
	s.Apply("media-title", "groovesalad-128-mp3")

	if s.Station != "groovesalad-128-mp3" {
		t.Errorf("station = %q, want the media title", s.Station)
	}
	if s.stationTagged {
		t.Error("the media title counted as a name from the station")
	}

	s.Apply("metadata", map[string]any{"icy-name": "Groove Salad"})
	if s.Station != "Groove Salad" || !s.stationTagged {
		t.Errorf("station = %q tagged = %v, want the ICY name", s.Station, s.stationTagged)
	}

	s.Apply("idle-active", true)
	if s.stationTagged {
		t.Error("the idle stream kept the tag of the stream before it")
	}
}

func TestUnknownValuesShowDash(t *testing.T) {
	var s State
	if s.BitrateText() != "-" || s.SampleRateText() != "-" || s.CacheText() != "-" {
		t.Error("an empty state must show a dash")
	}
}

package radio

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// SilenceDB is the level that playah shows for silence. astats reports
// minus infinity, and a bar graph needs a number.
const SilenceDB = -96.0

// Level holds the level of one audio channel in dBFS. Zero decibel is the
// loudest sample that the audio format holds.
type Level struct {
	RMS  float64
	Peak float64
}

// State holds the last value that mpv reported for each property.
type State struct {
	URL        string
	Loaded     bool
	Paused     bool
	CoreIdle   bool
	IdleActive bool
	Title      string // icy-title, or the track title
	Station    string // icy-name, or the media title
	Genre      string
	Codec      string
	Bitrate    float64 // bits per second
	SampleRate float64
	Channels   string
	Cache      float64 // seconds in the demuxer cache
	Volume     float64
	Levels     []Level // one level for each audio channel

	// Err holds the fault of the player or of the stream. It makes the
	// status an error.
	Err string

	// Notice holds a fault that is not about the stream, such as a config
	// file that did not save. The sound goes on, so the status stays.
	Notice string

	// stationTagged tells that the station named itself in the stream
	// metadata. mpv also names a stream by the last part of its URL,
	// which is no name for the recent list to keep.
	stationTagged bool
}

// Apply stores one property change from mpv.
func (s *State) Apply(name string, data any) {
	switch name {
	case "pause":
		s.Paused = toBool(data)
		if s.Paused {
			// mpv sends no level while it pauses. A meter that keeps
			// the last value tells the user that the sound plays.
			s.Levels = nil
		}
	case "core-idle":
		s.CoreIdle = toBool(data)
	case "idle-active":
		s.IdleActive = toBool(data)
		if s.IdleActive {
			s.clearTrack()
		} else {
			s.Loaded = true
		}
	case "metadata":
		s.applyMetadata(data)
	case "media-title":
		if s.Station == "" {
			s.Station = toString(data)
		}
	case "audio-bitrate":
		s.Bitrate = toFloat(data)
	case "audio-codec-name":
		s.Codec = toString(data)
	case "audio-params/samplerate":
		s.SampleRate = toFloat(data)
	case "audio-params/channels":
		s.Channels = toString(data)
	case "demuxer-cache-duration":
		s.Cache = toFloat(data)
	case "volume":
		s.Volume = toFloat(data)
	case MeterProperty:
		s.applyLevels(data)
	}
}

// applyMetadata reads the ICY tags that a radio stream sends.
func (s *State) applyMetadata(data any) {
	tags, ok := data.(map[string]any)
	if !ok {
		return
	}
	get := func(keys ...string) string {
		for _, want := range keys {
			for key, value := range tags {
				if strings.EqualFold(key, want) {
					if text := toString(value); text != "" {
						return text
					}
				}
			}
		}
		return ""
	}

	if title := get("icy-title", "title"); title != "" {
		s.Title = title
	}
	if artist := get("artist"); artist != "" && s.Title != "" && !strings.Contains(s.Title, artist) {
		s.Title = artist + " - " + s.Title
	}
	if name := get("icy-name", "name"); name != "" {
		s.Station = name
		s.stationTagged = true
	}
	if genre := get("icy-genre", "genre"); genre != "" {
		s.Genre = genre
	}
}

// applyLevels reads the astats metadata. Each key holds the channel
// number, for example lavfi.astats.2.RMS_level for the right channel.
func (s *State) applyLevels(data any) {
	tags, ok := data.(map[string]any)
	if !ok || len(tags) == 0 {
		s.Levels = nil
		return
	}

	found := make(map[int]Level, 2)
	for key, value := range tags {
		channel, field, ok := splitLevelKey(key)
		if !ok {
			continue
		}
		level := found[channel]
		switch field {
		case "RMS_level":
			level.RMS = toDB(value)
		case "Peak_level":
			level.Peak = toDB(value)
		default:
			continue
		}
		found[channel] = level
	}
	if len(found) == 0 {
		s.Levels = nil
		return
	}

	numbers := make([]int, 0, len(found))
	for number := range found {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)

	levels := make([]Level, 0, len(numbers))
	for _, number := range numbers {
		levels = append(levels, found[number])
	}
	s.Levels = levels
}

// splitLevelKey reads the channel number and the field name from one
// astats key. It reports false for a key of another shape.
func splitLevelKey(key string) (int, string, bool) {
	parts := strings.Split(key, ".")
	if len(parts) != 4 || parts[0] != "lavfi" || parts[1] != "astats" {
		return 0, "", false
	}
	channel, err := strconv.Atoi(parts[2])
	if err != nil || channel < 1 {
		return 0, "", false
	}
	return channel, parts[3], true
}

// toDB reads one astats value in dBFS. Silence gives minus infinity, and
// a channel without sound gives nan. Both become SilenceDB.
func toDB(v any) float64 {
	text, ok := v.(string)
	if !ok {
		return SilenceDB
	}
	db, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil || math.IsNaN(db) || db < SilenceDB {
		return SilenceDB
	}
	if math.IsInf(db, 1) {
		return 0
	}
	return db
}

func (s *State) clearTrack() {
	s.Title = ""
	s.Station = ""
	s.stationTagged = false
	s.Genre = ""
	s.Codec = ""
	s.Bitrate = 0
	s.SampleRate = 0
	s.Channels = ""
	s.Cache = 0
	s.Levels = nil
	s.Loaded = false
}

// Status is the play state of the stream. The core names the state, and
// each front end picks how to draw it: a word, a glyph or a color.
type Status int

const (
	StatusIdle Status = iota
	StatusError
	StatusPaused
	StatusBuffering
	StatusPlaying
)

// String gives the English word for the state.
func (st Status) String() string {
	switch st {
	case StatusError:
		return "Error"
	case StatusPaused:
		return "Paused"
	case StatusBuffering:
		return "Buffering"
	case StatusPlaying:
		return "Playing"
	default:
		return "Idle"
	}
}

// Status returns the play state of the current stream.
func (s *State) Status() Status {
	switch {
	case s.Err != "":
		return StatusError
	case !s.Loaded || s.IdleActive:
		return StatusIdle
	case s.Paused:
		return StatusPaused
	case s.CoreIdle:
		return StatusBuffering
	default:
		return StatusPlaying
	}
}

// Format helpers. Each one returns a dash when the value is not known yet.

func (s *State) BitrateText() string {
	if s.Bitrate <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d kbps", s.Kbps())
}

// Kbps returns the bitrate in kbps, and zero while mpv reported none.
func (s *State) Kbps() int {
	if s.Bitrate <= 0 {
		return 0
	}
	return int(math.Round(s.Bitrate / 1000))
}

func (s *State) SampleRateText() string {
	if s.SampleRate <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.1f kHz", s.SampleRate/1000)
}

func (s *State) CacheText() string {
	if s.Cache <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f s", s.Cache)
}

func toBool(v any) bool {
	b, _ := v.(bool)
	return b
}

func toFloat(v any) float64 {
	f, _ := v.(float64)
	return f
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%.0f", t)
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

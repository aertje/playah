package radio

import "testing"

func TestApplyLevelsReadsBothChannels(t *testing.T) {
	var s State
	s.Apply(MeterProperty, map[string]any{
		"lavfi.astats.2.RMS_level":  "-24.4",
		"lavfi.astats.1.RMS_level":  "-14.8",
		"lavfi.astats.1.Peak_level": "-8.3",
		"lavfi.astats.2.Peak_level": "-15.9",
	})

	if len(s.Levels) != 2 {
		t.Fatalf("the state holds %d channels, want 2", len(s.Levels))
	}
	if s.Levels[0].RMS != -14.8 || s.Levels[0].Peak != -8.3 {
		t.Errorf("the left channel is %+v, want the values of channel 1", s.Levels[0])
	}
	if s.Levels[1].RMS != -24.4 {
		t.Errorf("the right channel is %+v, want the values of channel 2", s.Levels[1])
	}
}

// TestApplyLevelsTakesSilence covers the values that astats sends for a
// channel without sound.
func TestApplyLevelsTakesSilence(t *testing.T) {
	var s State
	s.Apply(MeterProperty, map[string]any{
		"lavfi.astats.1.RMS_level":  "-inf",
		"lavfi.astats.1.Peak_level": "nan",
	})

	if len(s.Levels) != 1 {
		t.Fatalf("the state holds %d channels, want 1", len(s.Levels))
	}
	if s.Levels[0].RMS != SilenceDB || s.Levels[0].Peak != SilenceDB {
		t.Errorf("silence gives %+v, want two times %v", s.Levels[0], SilenceDB)
	}
}

// TestApplyLevelsDropsOtherKeys checks that a key of another shape does
// not make a channel.
func TestApplyLevelsDropsOtherKeys(t *testing.T) {
	var s State
	s.Apply(MeterProperty, map[string]any{
		"lavfi.astats.Overall.RMS_level": "-12.0",
		"lavfi.r128.M":                   "-23.0",
	})
	if s.Levels != nil {
		t.Errorf("the state holds %+v, want no channel", s.Levels)
	}
}

// TestPauseClearsTheLevels checks that a paused stream shows no bar.
func TestPauseClearsTheLevels(t *testing.T) {
	var s State
	s.Apply(MeterProperty, map[string]any{"lavfi.astats.1.RMS_level": "-14.8"})
	s.Apply("pause", true)

	if s.Levels != nil {
		t.Errorf("the state holds %+v after a pause, want no channel", s.Levels)
	}
}

// TestNullMetadataClearsTheLevels covers the first value that mpv sends.
// mpv reports null before the audio chain runs.
func TestNullMetadataClearsTheLevels(t *testing.T) {
	var s State
	s.Apply(MeterProperty, map[string]any{"lavfi.astats.1.RMS_level": "-14.8"})
	s.Apply(MeterProperty, nil)

	if s.Levels != nil {
		t.Errorf("the state holds %+v for a null value, want no channel", s.Levels)
	}
}

package tv

import (
	"strings"
	"testing"

	"playah/internal/radio"
)

// plain removes the tview color tags, so a test can count the letters.
func plain(text string) string {
	var b strings.Builder
	skip := false
	for _, r := range text {
		switch {
		case r == '[':
			skip = true
		case r == ']' && skip:
			skip = false
		case !skip:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// splitPanel gives the rows of a meter panel without the color tags.
func splitPanel(text string) [][]rune {
	lines := strings.Split(plain(text), "\n")
	rows := make([][]rune, len(lines))
	for i, line := range lines {
		rows[i] = []rune(line)
	}
	return rows
}

// panelRows draws the meter for these levels and splits it into rows.
// It drops the padding over the bars, so row 0 is the top of the bars.
func panelRows(levels ...radio.Level) [][]rune {
	return splitPanel(meterPanel(levels))[meterPad:]
}

// filledRows counts the rows of one bar that hold a fill. The bar stands
// at column x, and it grows from the foot of the panel.
func filledRows(rows [][]rune, x int) int {
	n := 0
	for _, row := range rows[:meterRows] {
		if r := row[x]; r != '░' && r != ' ' && r != '━' {
			n++
		}
	}
	return n
}

// TestMeterPanelKeepsItsShape checks that every state of the meter gives
// the same block of text. A row that grows would push the panel border.
func TestMeterPanelKeepsItsShape(t *testing.T) {
	cases := map[string][]radio.Level{
		"no level":  nil,
		"mono":      {{RMS: -12, Peak: -6}},
		"stereo":    {{RMS: -12, Peak: -6}, {RMS: -40, Peak: -30}},
		"five one":  {{}, {}, {}, {}, {}, {}},
		"the floor": {{RMS: radio.SilenceDB, Peak: radio.SilenceDB}},
	}

	for name, levels := range cases {
		t.Run(name, func(t *testing.T) {
			rows := splitPanel(meterPanel(levels))
			if want := meterHeight - 2; len(rows) != want {
				t.Fatalf("the panel has %d rows, want %d", len(rows), want)
			}
			for i, row := range rows {
				if len(row) != meterInnerWidth {
					t.Errorf("row %d has %d cells, want %d", i, len(row), meterInnerWidth)
				}
			}
		})
	}
}

func TestMeterBarFillsWithTheLevel(t *testing.T) {
	cases := []struct {
		name string
		rms  float64
		rows int
	}{
		{"silence", radio.SilenceDB, 0},
		{"the floor", meterFloor, 0},
		{"the top", 0, meterRows},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := panelRows(radio.Level{RMS: c.rms, Peak: c.rms})
			x := barStarts(1)[0]
			if got := filledRows(rows, x); got != c.rows {
				t.Errorf("a level of %.1f dB fills %d rows, want %d", c.rms, got, c.rows)
			}
		})
	}

	// Half of the range fills half of the bar. A half row counts as a
	// row, so the count lands on one of the two rows in the middle.
	t.Run("the middle", func(t *testing.T) {
		rows := panelRows(radio.Level{RMS: meterFloor / 2, Peak: meterFloor / 2})
		got := filledRows(rows, barStarts(1)[0])
		if got < meterRows/2 || got > meterRows/2+1 {
			t.Errorf("half of the range fills %d rows, want about %d", got, meterRows/2)
		}
	})
}

// TestMeterBarMovesInWholeRows checks that a level that reaches into a
// row lights the whole row, in the shade of its zone. The bar carries no
// part row at its end, so nothing runs along it over the shades.
func TestMeterBarMovesInWholeRows(t *testing.T) {
	step := -meterFloor / meterRows
	rows := panelRows(radio.Level{RMS: meterFloor + step/2, Peak: radio.SilenceDB})
	x := barStarts(1)[0]

	if got := rows[meterRows-1][x]; got != shadeHalf {
		t.Errorf("half of a row gives %q at the foot of the bar, want %q", got, shadeHalf)
	}
	if got := filledRows(rows, x); got != 1 {
		t.Errorf("half of a row fills %d rows, want 1", got)
	}
}

// TestMeterBarShowsThePeak checks the marker that stands above the fill.
func TestMeterBarShowsThePeak(t *testing.T) {
	rows := panelRows(radio.Level{RMS: -40, Peak: -6})
	x := barStarts(1)[0]

	marker := -1
	for i, row := range rows[:meterRows] {
		if row[x] == '━' {
			marker = i
		}
	}
	if marker < 0 {
		t.Fatal("the bar holds no peak marker")
	}
	if fill := meterRows - filledRows(rows, x); marker >= fill {
		t.Errorf("the peak marker stands on row %d, want above the fill at row %d", marker, fill)
	}
}

// TestMeterBarShadesTheZones checks the ink of a bar. A row takes the
// shade of the level at its foot, so a bar that stands full reads as the
// scale: half ink under meterQuiet, three quarters up to meterLoud, and
// whole cells in the headroom above it.
func TestMeterBarShadesTheZones(t *testing.T) {
	rows := panelRows(radio.Level{RMS: 0, Peak: radio.SilenceDB})
	x := barStarts(1)[0]

	var bar []rune
	for _, row := range rows[:meterRows] {
		bar = append(bar, row[x])
	}
	if want := "██▓▓▒▒"; string(bar) != want {
		t.Errorf("a full bar reads %q from the top, want %q", string(bar), want)
	}

	// The shade says where the row stands, not how loud the sound is, so
	// a quiet bar keeps the half shade of the rows that it reaches.
	quiet := panelRows(radio.Level{RMS: meterQuiet - 6, Peak: radio.SilenceDB})
	if got := quiet[meterRows-1][x]; got != shadeHalf {
		t.Errorf("a quiet bar has %q at its foot, want %q", got, shadeHalf)
	}
}

// TestMeterPanelPadsTheTop checks that a full bar leaves the rows over
// it blank, so it stops short of the panel border.
func TestMeterPanelPadsTheTop(t *testing.T) {
	full := radio.Level{RMS: 0, Peak: 0}
	rows := splitPanel(meterPanel([]radio.Level{full, full}))
	for i := range meterPad {
		if got := strings.TrimSpace(string(rows[i])); got != "" {
			t.Errorf("padding row %d reads %q, want it blank", i, got)
		}
	}
}

// TestMeterPanelNamesTheChannels checks the row under the bars. The name
// stands in the middle of its bar.
func TestMeterPanelNamesTheChannels(t *testing.T) {
	stereo := panelRows(radio.Level{RMS: -12}, radio.Level{RMS: -12})
	for i, x := range barStarts(meterChannels) {
		want := []rune(channelName(i, meterChannels))[0]
		if got := stereo[meterRows][x+meterBarWidth/2]; got != want {
			t.Errorf("channel %d is named %q, want %q", i, got, want)
		}
	}

	mono := panelRows(radio.Level{RMS: -12})
	if got := strings.TrimSpace(string(mono[meterRows])); got != "M" {
		t.Errorf("one channel is named %q, want %q", got, "M")
	}
}

// TestMeterPanelStartsEmpty checks the panel that playah shows before mpv
// sends the first level. It holds both bars, and neither one has a fill.
func TestMeterPanelStartsEmpty(t *testing.T) {
	empty := panelRows()
	if got := strings.TrimSpace(string(empty[meterRows])); got != "L     R" {
		t.Errorf("the empty panel is named %q, want %q", got, "L     R")
	}
	for i, x := range barStarts(meterChannels) {
		if got := filledRows(empty, x); got != 0 {
			t.Errorf("bar %d fills %d rows, want 0", i, got)
		}
	}
}

// TestStatusTextNamesTheState checks the value of the status row. It
// holds the mark of the state and the name beside it.
func TestStatusTextNamesTheState(t *testing.T) {
	for _, m := range statusMarks {
		if got, want := statusText(m.status), m.icon+" "+m.status.String(); got != want {
			t.Errorf("the status row for %v reads %q, want %q", m.status, got, want)
		}
	}
}

// TestVolumeTextNamesThePercent checks the value of the volume row. mpv
// takes a volume over 100%, which amplifies the sound, and the row shows
// that number as well.
func TestVolumeTextNamesThePercent(t *testing.T) {
	cases := map[float64]string{0: "0%", 70: "70%", 130: "130%"}
	for percent, want := range cases {
		if got := volumeText(percent); got != want {
			t.Errorf("a volume of %.0f%% reads %q, want %q", percent, got, want)
		}
	}
}

// TestBufferTextNamesTheSeconds checks the value of the buffer row. It
// holds the seconds, and a dash before mpv reports the first cache value.
func TestBufferTextNamesTheSeconds(t *testing.T) {
	cases := map[float64]string{0: "-", 1.4: "1 s", 16: "16 s"}
	for seconds, want := range cases {
		if got := bufferText(seconds); got != want {
			t.Errorf("a buffer of %.1f s reads %q, want %q", seconds, got, want)
		}
	}
}

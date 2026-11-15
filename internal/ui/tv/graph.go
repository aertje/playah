package tv

import (
	"math"
	"strings"

	"playah/internal/radio"
)

// The level meter draws one upright bar for each channel. The bar shows
// this range, and it stands this many screen rows tall, which puts 6 dB
// in one row: a bar lights whole rows, so that is the step it moves in.
// The row count also sets the height of the info panel beside it.
const (
	meterFloor = -36.0 // the level in dBFS at the foot of a bar
	meterRows  = 6     // the number of screen rows in one bar
)

// The shape of the meter panel in screen cells. The two bars stand two
// cells in from the panel border, with a gap between them.
const (
	meterInnerWidth = 13 // the width inside the border
	meterBarWidth   = 3
	meterBarMargin  = 2 // the cells between the border and the bar beside it
)

// meterWidth is the width of the whole panel, its border included.
const meterWidth = meterInnerWidth + 2

// meterPad is the number of blank rows over the bars. A full bar then
// stops short of the panel border instead of running into it.
const meterPad = 1

// meterHeight is the height of the whole panel. Over the bars stands the
// padding, under them the name row, and around them the border.
const meterHeight = meterPad + meterRows + 3

// meterChannels is the number of channels that the meter shows. A stream
// with more channels gets the first two.
const meterChannels = 2

// The zones of the level meter in dBFS. The sound is thin under
// meterQuiet, and the range above meterLoud is the headroom that a
// stream at a healthy level reaches. Both stand on a row boundary,
// because a row of the bar holds 6 dB.
const (
	meterQuiet = -24.0
	meterLoud  = -12.0
)

// The shades that a full cell of a fill writes in. A cell takes the
// shade of the zone that it stands in, so the ink of a bar grows with it:
// half a cell of ink in the range that warns, three quarters in the range
// that is fair, and a whole cell above that. The shades stand in the
// color of the terminal, so the ink alone carries the warning.
const (
	shadeHalf = '▒'
	shadeFair = '▓'
	shadeFull = '█'
)

// barRest is the cell past the end of a fill, in every bar. It stands in
// the light shade, under the half shade of the faintest fill, and in
// gray, so the room that is left reads as the rest of the bar and never
// as a fill.
var barRest = cell{r: '░', color: "gray"}

// defaultColor is the tview tag for the color that the terminal itself
// writes in. A panel that carries no warning takes it, so playah stays
// readable on a light terminal as well as on a dark one.
const defaultColor = "-"

// cell is one position of a graph: the letter and its color name.
type cell struct {
	r     rune
	color string
}

// markup joins the cells into text with tview color tags. It opens one
// tag for each run of the same color, so the text stays short.
func markup(cells []cell) string {
	var b strings.Builder
	open := ""
	for _, c := range cells {
		if c.color != open {
			b.WriteString("[" + c.color + "]")
			open = c.color
		}
		b.WriteRune(c.r)
	}
	if open != "" {
		b.WriteString("[-]")
	}
	return b.String()
}

// meterPanel draws the level meter as the lines of the panel: the
// padding, the bars, and the channel name under each one. mpv sends no level while the
// stream is idle or paused, and the bars are then empty.
func meterPanel(levels []radio.Level) string {
	if len(levels) > meterChannels {
		levels = levels[:meterChannels]
	}

	// Empty bars keep the panel in shape before mpv sends the first
	// level, and for an mpv build that has no astats filter.
	count := len(levels)
	if count == 0 {
		count = meterChannels
	}

	grid := newGrid(meterPad+meterRows+1, meterInnerWidth)
	bars, names := grid[meterPad:meterPad+meterRows], grid[meterPad+meterRows]

	silent := radio.Level{RMS: radio.SilenceDB, Peak: radio.SilenceDB}
	for i, x := range barStarts(count) {
		level := silent
		if i < len(levels) {
			level = levels[i]
		}
		drawBar(bars, x, level)
		paint(names, x+meterBarWidth/2, channelName(i, count), "gray")
	}

	return panelText(grid)
}

// panelText joins the rows of a panel into the text of one widget.
func panelText(grid [][]cell) string {
	lines := make([]string, len(grid))
	for i, row := range grid {
		lines[i] = markup(row)
	}
	return strings.Join(lines, "\n")
}

// shadeFor gives the shade of one cell of a bar from the value at its
// foot: half ink under low, three quarters up to fair, and a whole cell
// above that. A zone therefore starts on the cell that reaches its limit.
func shadeFor(value, low, fair float64) rune {
	switch {
	case value >= fair:
		return shadeFull
	case value >= low:
		return shadeFair
	default:
		return shadeHalf
	}
}

// meterShade gives the shade of row i of a level bar, counted from the
// foot, where the row stands for the level at its foot.
func meterShade(i int) rune {
	return shadeFor(meterFloor-float64(i)*meterFloor/meterRows, meterQuiet, meterLoud)
}

// barCell gives cell i of a bar, counted from its foot. A cell under the fill stands whole,
// in the shade of its zone, and the cells past it stand as rest: a bar
// moves in whole cells and carries nothing at its end, so what it says is
// how much of it is left and how dark that part stands.
func barCell(fill, i int, shade rune) cell {
	if i < fill {
		return cell{r: shade, color: defaultColor}
	}
	return barRest
}

// fillCells maps a value to the number of cells that a bar of this many
// cells lights. A value over zero lights the cell that it reaches, so the
// bar stands empty at zero alone, and the value that fills the bar lights
// every cell, as does a larger one.
func fillCells(value, full float64, cells int) int {
	if value <= 0 {
		return 0
	}
	return min(int(math.Ceil(value/full*float64(cells))), cells)
}

// drawBar fills one bar from the foot up. The RMS level sets the fill,
// and the peak level puts a marker in the row that it reaches, where that
// row stands over the fill.
func drawBar(rows [][]cell, x int, l radio.Level) {
	height := len(rows)
	fill := meterFill(l.RMS, height)
	peak := meterFill(l.Peak, height)

	// i counts the rows from the foot of the bar, and the grid holds the
	// top row first.
	for i := range height {
		c := barCell(fill, i, meterShade(i))
		if i == peak-1 && c.r == barRest.r {
			c = cell{r: '━', color: defaultColor}
		}
		row := rows[height-1-i]
		for w := range meterBarWidth {
			row[x+w] = c
		}
	}
}

// barStarts gives the first cell of each bar inside the panel. Two bars
// stand apart with a gap between them. A single bar stands in the middle.
func barStarts(count int) []int {
	if count == 1 {
		return []int{(meterInnerWidth - meterBarWidth) / 2}
	}
	return []int{meterBarMargin, meterInnerWidth - meterBarWidth - meterBarMargin}
}

// newGrid gives the rows of one panel, all of them empty.
func newGrid(rows, width int) [][]cell {
	grid := make([][]cell, rows)
	for i := range grid {
		grid[i] = blankRow(width)
	}
	return grid
}

// blankRow gives one row of empty cells in the default color.
func blankRow(width int) []cell {
	row := make([]cell, width)
	for i := range row {
		row[i] = cell{r: ' ', color: defaultColor}
	}
	return row
}

// paint writes text into one row, one cell for each letter. It drops the
// letters that fall outside the panel.
func paint(row []cell, x int, text, color string) {
	for _, r := range text {
		if x >= 0 && x < len(row) {
			row[x] = cell{r: r, color: color}
		}
		x++
	}
}

// meterFill maps one level in dBFS to a number of rows. A level at the
// floor or under it leaves the bar empty, and one over 0 dBFS fills it.
func meterFill(db float64, rows int) int {
	if db <= meterFloor {
		return 0
	}
	if db > 0 {
		db = 0
	}
	return fillCells(db-meterFloor, -meterFloor, rows)
}

// channelName gives the short name of one channel.
func channelName(i, count int) string {
	if count == 1 {
		return "M"
	}
	if i == 0 {
		return "L"
	}
	return "R"
}

// statusColor is the color of the play mark. The shape carries the state,
// and the message bar at the foot of the screen carries the words of a
// fault, so the mark needs no color of its own.
const statusColor = defaultColor

// statusMark is how the front end draws one play state: the mark that
// stands in the status row of the info panel, and the same mark in the
// legend of the help popup. The marks are Geometric Shapes, and the pause
// mark is the double bar of General Punctuation. Nearly every font draws
// both sets, and each mark takes one cell. Two half blocks made a pause
// mark that stood too heavy beside the others.
type statusMark struct {
	status radio.Status
	icon   string
}

// statusMarks holds one mark for each play state, in the order that the
// legend in the help popup shows them. A solid mark means that the sound
// runs, and a light one that it waits.
var statusMarks = []statusMark{
	{radio.StatusPlaying, "▶"},
	{radio.StatusBuffering, "▷"},
	{radio.StatusPaused, "‖"},
	{radio.StatusIdle, "■"},
	{radio.StatusError, "✕"},
}

// markFor gives the mark of one play state.
func markFor(st radio.Status) statusMark {
	for _, m := range statusMarks {
		if m.status == st {
			return m
		}
	}
	// The core adds no state without a mark, so this stands for a build
	// that forgot one.
	return statusMark{status: st, icon: "?"}
}

// statusText writes the value of the status row: the mark of the play
// state and its name.
func statusText(st radio.Status) string {
	return markFor(st).icon + " " + st.String()
}

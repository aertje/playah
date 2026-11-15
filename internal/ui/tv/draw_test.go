package tv

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"playah/internal/radio"
)

// TestNoDrawWhenTheTextDoesNotChange floods the front end with the cache
// events that mpv sends many times each second. Nothing on the screen
// changes, so nothing must draw. Each draw clears the screen and moves
// the terminal cursor, which makes the cursor flicker.
func TestNoDrawWhenTheTextDoesNotChange(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.send("idle-active", false)
	h.send("demuxer-cache-duration", 16.1)
	base := h.settle(t)

	// The same value 60 times over 1.5 seconds. That is 10 ticker periods.
	for i := range 60 {
		h.send("demuxer-cache-duration", 16.1)
		if i%12 == 11 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	time.Sleep(600 * time.Millisecond)

	if extra := h.draws.Load() - base; extra > 0 {
		t.Errorf("60 equal cache events gave %d draws, want none", extra)
	}
}

// TestNoDrawForCacheJitter sends cache values that differ below the shown
// precision. The panel text stays the same, so only the buffer graph may
// draw.
func TestNoDrawForCacheJitter(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.send("idle-active", false)
	h.send("demuxer-cache-duration", 16.0)
	base := h.settle(t)

	for i := range 40 {
		h.send("demuxer-cache-duration", 16.0+float64(i%5)*0.04)
		if i%8 == 7 {
			time.Sleep(300 * time.Millisecond)
		}
	}
	time.Sleep(600 * time.Millisecond)

	if extra := h.draws.Load() - base; extra > 0 {
		t.Errorf("cache jitter gave %d draws, want none", extra)
	}
}

// TestDrawWhenTheTrackChanges makes sure that the comparison does not stop
// a real update.
func TestDrawWhenTheTrackChanges(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	base := h.settle(t)
	h.send("metadata", map[string]any{"icy-title": "Artist - Song"})
	h.waitFor(t, "Artist - Song")

	if extra := h.draws.Load() - base; extra < 1 {
		t.Errorf("a new track gave %d draws, want 1 or more", extra)
	}
}

// TestPasteArrivesInOneDraw pastes a URL. Bracketed paste gives the whole
// text in one event, so the front end draws once and not once for each
// letter.
func TestPasteArrivesInOneDraw(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	url := "https://ice1.somafm.com/groovesalad-128-mp3"
	h.screen.InjectKey(tcell.KeyRune, 'a', tcell.ModNone)
	h.waitFor(t, "New stream")
	base := h.settle(t)

	_ = h.screen.PostEvent(tcell.NewEventPaste(true))
	for _, r := range url {
		h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
	_ = h.screen.PostEvent(tcell.NewEventPaste(false))

	h.waitFor(t, url)
	extra := h.draws.Load() - base
	h.screen.InjectKey(tcell.KeyEsc, 0, tcell.ModNone)
	t.Logf("a paste of %d letters gave %d draws", len(url), extra)
	if extra > 2 {
		t.Errorf("a paste of %d letters gave %d draws, want 2 or fewer", len(url), extra)
	}
}

// TestExitEventShowsAMessage checks the path for a dead mpv process.
func TestExitEventShowsAMessage(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.app.Player.Events <- radio.Event{Kind: radio.EventExit}
	h.waitFor(t, "mpv stopped")
}

// TestLevelMeterDrawsTheChannels sends the metadata that the astats
// filter gives. The meter must show one bar for the left channel and one
// bar for the right channel, and the louder channel must stand taller.
func TestLevelMeterDrawsTheChannels(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.send("idle-active", false)
	h.send(radio.MeterProperty, map[string]any{
		"lavfi.astats.1.RMS_level":  "-6.0",
		"lavfi.astats.1.Peak_level": "-2.0",
		"lavfi.astats.2.RMS_level":  "-30.0",
		"lavfi.astats.2.Peak_level": "-24.0",
	})
	h.waitForBars(t, []int{5, 1})
}

// TestPauseClearsTheLevelMeter checks that the meter empties when mpv
// stops the sound. A meter that keeps the last bar tells the user that
// the stream still plays.
func TestPauseClearsTheLevelMeter(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.send("idle-active", false)
	h.send(radio.MeterProperty, map[string]any{
		"lavfi.astats.1.RMS_level": "-6.0",
		"lavfi.astats.2.RMS_level": "-6.0",
	})
	h.waitForBars(t, []int{5, 5})

	h.send("pause", true)
	h.waitForStatus(t, radio.StatusPaused)
	h.waitForBars(t, []int{0, 0})
}

// TestBufferShowsTheSeconds checks the buffer row of the info panel. It follows the cache
// duration that mpv reports.
func TestBufferShowsTheSeconds(t *testing.T) {
	h := start(t)
	defer h.stop(t)

	h.send("idle-active", false)
	h.send("demuxer-cache-duration", 12.0)
	h.waitFor(t, "12 s")

	h.send("demuxer-cache-duration", 1.0)
	h.waitFor(t, "1 s")
}

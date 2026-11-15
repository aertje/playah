package radio

import (
	"os"
	"testing"
	"time"
)

// TestSmokeStream plays a real stream. It needs mpv and a network
// connection. It runs only when PLAYAH_SMOKE=1 is set.
func TestSmokeStream(t *testing.T) {
	if os.Getenv("PLAYAH_SMOKE") != "1" {
		t.Skip("set PLAYAH_SMOKE=1 to play a real stream (needs mpv and a network connection)")
	}

	p, err := StartPlayer(0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if err := p.Load("https://ice1.somafm.com/groovesalad-128-mp3"); err != nil {
		t.Fatal(err)
	}

	var s State
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev := <-p.Events:
			switch ev.Kind {
			case EventProperty:
				s.Apply(ev.Name, ev.Data)
			case EventError:
				t.Log("error event:", ev.Text)
			}
			if s.Title != "" && s.Bitrate > 0 {
				t.Logf("status=%s track=%q station=%q genre=%q codec=%s %s %s %s cache=%s",
					s.Status(), s.Title, s.Station, s.Genre, s.Codec,
					s.BitrateText(), s.SampleRateText(), s.Channels, s.CacheText())
				return
			}
		case <-deadline:
			t.Fatalf("no metadata arrived. state=%+v", s)
		}
	}
}

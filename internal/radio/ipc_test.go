package radio

import (
	"os/exec"
	"testing"
	"time"
)

// TestMpvQuitsWhenTheChannelCloses starts a real mpv, and it closes the
// connection without a quit command. That is what a crash of playah does,
// and mpv must not stay behind. The test needs mpv but no network.
func TestMpvQuitsWhenTheChannelCloses(t *testing.T) {
	if _, err := exec.LookPath("mpv"); err != nil {
		t.Skip("mpv is not installed")
	}
	p, err := StartPlayer(0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	_ = p.conn.Close()
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		t.Fatal("mpv still runs after the channel closed")
	}
}

package radio

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// guard is how long a test waits for an event that must come. It is a
// failure bound, never a way to wait for something.
const guard = 10 * time.Second

// fakeMPV is the mpv side of a connection. It reads the commands of the
// player and writes the lines that mpv would send.
type fakeMPV struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

// request is one command that the player sent.
type request struct {
	ID      int64 `json:"request_id"`
	Command []any `json:"command"`
}

// newFake makes a player around one end of a socket pair, and a fake mpv
// around the other. The reader of the player runs when the test says so.
func newFake(t *testing.T, buffer int) (*Player, *fakeMPV) {
	t.Helper()
	mine, child, err := socketPair()
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := net.FileConn(child)
	_ = child.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = theirs.Close(); _ = mine.Close() })
	return newPlayer(mine, buffer), &fakeMPV{t: t, conn: theirs, r: bufio.NewReader(theirs)}
}

// run starts the reader of the player, and it returns a channel that
// closes when the reader ends.
func run(p *Player) <-chan struct{} {
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		p.readLoop()
	}()
	return ended
}

// next reads the next command from the player.
func (f *fakeMPV) next() request {
	f.t.Helper()
	_ = f.conn.SetReadDeadline(time.Now().Add(guard))
	line, err := f.r.ReadBytes('\n')
	if err != nil {
		f.t.Fatalf("the fake mpv read no command: %v", err)
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		f.t.Fatal(err)
	}
	return req
}

// load reads the two commands of Player.Load and returns the id of the
// loadfile request.
func (f *fakeMPV) load() int64 {
	f.t.Helper()
	pause := f.next()
	if len(pause.Command) == 0 || pause.Command[0] != "set_property" {
		f.t.Fatalf("the first command of a load is %v, want set_property", pause.Command)
	}
	req := f.next()
	if len(req.Command) == 0 || req.Command[0] != "loadfile" {
		f.t.Fatalf("the second command of a load is %v, want loadfile", req.Command)
	}
	return req.ID
}

// line writes one line to the player.
func (f *fakeMPV) line(format string, args ...any) {
	f.t.Helper()
	if _, err := fmt.Fprintf(f.conn, format+"\n", args...); err != nil {
		f.t.Fatal(err)
	}
}

// loadReply answers a loadfile request.
func (f *fakeMPV) loadReply(id, entry int64) {
	f.t.Helper()
	f.line(`{"request_id":%d,"error":"success","data":{"playlist_entry_id":%d}}`, id, entry)
}

// bitrate sends a stream scoped property. Its event shows the stream that
// the player believes mpv is on.
func (f *fakeMPV) bitrate() {
	f.t.Helper()
	f.line(`{"event":"property-change","name":"audio-bitrate","data":128000}`)
}

// event waits for the next event of the player.
func event(t *testing.T, p *Player) Event {
	t.Helper()
	select {
	case ev := <-p.Events:
		return ev
	case <-time.After(guard):
		t.Fatal("no event arrived")
		return Event{}
	}
}

// streamOfNextBitrate asks the fake for a bitrate event, and returns the
// stream that the event carries.
func streamOfNextBitrate(t *testing.T, p *Player, f *fakeMPV) int64 {
	t.Helper()
	f.bitrate()
	ev := event(t, p)
	if ev.Kind != EventProperty || ev.Name != "audio-bitrate" {
		t.Fatalf("event = %+v, want the bitrate", ev)
	}
	return ev.Stream
}

func TestStreamFollowsLoadReplyAndStartFile(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	if got := streamOfNextBitrate(t, p, f); got != 0 {
		t.Errorf("before any load the stream is %d, want 0", got)
	}

	if err := p.Load("http://a"); err != nil {
		t.Fatal(err)
	}
	if got := p.Stream(); got != 1 {
		t.Fatalf("Stream = %d after the first load, want 1", got)
	}
	f.loadReply(f.load(), 7)

	// mpv still plays the old stream until start-file arrives.
	if got := streamOfNextBitrate(t, p, f); got != 0 {
		t.Errorf("before start-file the event is of stream %d, want 0", got)
	}
	f.line(`{"event":"start-file","playlist_entry_id":7}`)
	if got := streamOfNextBitrate(t, p, f); got != 1 {
		t.Errorf("after start-file the event is of stream %d, want 1", got)
	}
}

func TestTwoQuickLoadsKeepTheirOwnStreams(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	for _, url := range []string{"http://a", "http://b"} {
		if err := p.Load(url); err != nil {
			t.Fatal(err)
		}
	}
	f.loadReply(f.load(), 7)
	f.loadReply(f.load(), 8)
	if got := p.Stream(); got != 2 {
		t.Fatalf("Stream = %d, want 2", got)
	}

	f.line(`{"event":"start-file","playlist_entry_id":7}`)
	if got := streamOfNextBitrate(t, p, f); got != 1 {
		t.Errorf("a late event of the first load is of stream %d, want 1", got)
	}
	f.line(`{"event":"start-file","playlist_entry_id":8}`)
	if got := streamOfNextBitrate(t, p, f); got != 2 {
		t.Errorf("an event of the second load is of stream %d, want 2", got)
	}
}

func TestALoadThatNeverStartsDoesNotTakeTheNextStart(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	for _, url := range []string{"http://a", "http://b"} {
		if err := p.Load(url); err != nil {
			t.Fatal(err)
		}
	}
	f.loadReply(f.load(), 7)
	f.loadReply(f.load(), 8)

	// mpv replaced the first load before it started.
	f.line(`{"event":"start-file","playlist_entry_id":8}`)
	if got := streamOfNextBitrate(t, p, f); got != 2 {
		t.Errorf("the event is of stream %d, want 2", got)
	}
}

// TestStartFileWithoutAnEntryUsesThePendingStream covers an mpv that sends
// no playlist entry id.
func TestStartFileWithoutAnEntryUsesThePendingStream(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	if err := p.Load("http://a"); err != nil {
		t.Fatal(err)
	}
	f.line(`{"request_id":%d,"error":"success"}`, f.load())
	f.line(`{"event":"start-file"}`)
	if got := streamOfNextBitrate(t, p, f); got != 1 {
		t.Errorf("the event is of stream %d, want 1", got)
	}
}

func TestRejectedLoadKeepsTheStreamAndSurfacesItsError(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	if err := p.Load("http://a"); err != nil {
		t.Fatal(err)
	}
	f.loadReply(f.load(), 7)
	f.line(`{"event":"start-file","playlist_entry_id":7}`)

	if err := p.Load("http://b"); err != nil {
		t.Fatal(err)
	}
	f.line(`{"request_id":%d,"error":"invalid parameter"}`, f.load())
	ev := event(t, p)
	if ev.Kind != EventError || ev.Text != "mpv: invalid parameter" || ev.Stream != 2 {
		t.Errorf("event = %+v, want the load error of stream 2", ev)
	}

	// The rejected load must not give its number to a start that follows.
	f.line(`{"event":"start-file","playlist_entry_id":99}`)
	if got := streamOfNextBitrate(t, p, f); got != 1 {
		t.Errorf("the event is of stream %d, want 1", got)
	}
}

func TestFailedStreamEventCarriesItsStream(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	if err := p.Load("http://a"); err != nil {
		t.Fatal(err)
	}
	f.loadReply(f.load(), 7)
	f.line(`{"event":"start-file","playlist_entry_id":7}`)
	f.line(`{"event":"end-file","reason":"error"}`)

	ev := event(t, p)
	if ev.Kind != EventError || ev.Stream != 1 {
		t.Errorf("event = %+v, want the stream error of stream 1", ev)
	}
}

func TestLoadThatCannotBeSentDoesNotMoveTheStream(t *testing.T) {
	p := &Player{}
	if err := p.Load("http://a"); err == nil {
		t.Fatal("a player without a connection loaded a stream")
	}
	if got := p.Stream(); got != 0 {
		t.Errorf("Stream = %d, want 0", got)
	}

	// A write to a closed far end fails, and the stream number it took
	// goes back.
	q, f := newFake(t, 1)
	_ = f.conn.Close()
	if err := q.write(false, true, "loadfile", "http://a", "replace"); err == nil {
		t.Fatal("a write to a closed connection worked")
	}
	if got := q.Stream(); got != 0 {
		t.Errorf("Stream = %d after a failed send, want 0", got)
	}
	if len(q.loads) != 0 {
		t.Errorf("%d load requests wait for a reply", len(q.loads))
	}
}

func TestQuietErrorIsHiddenAndOtherErrorsShow(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	if err := p.writeCommand(true, "af", "add", "x"); err != nil {
		t.Fatal(err)
	}
	quiet := f.next()
	f.line(`{"request_id":%d,"error":"option not found"}`, quiet.ID)

	if err := p.TogglePause(); err != nil {
		t.Fatal(err)
	}
	loud := f.next()
	f.line(`{"request_id":%d,"error":"property unavailable"}`, loud.ID)

	ev := event(t, p)
	if ev.Kind != EventError || ev.Text != "mpv: property unavailable" {
		t.Errorf("event = %+v, want the error of the pause command", ev)
	}
}

func TestOverLongLineIsSkipped(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	huge := strings.Repeat("x", maxLine+10)
	f.line(`{"event":"property-change","name":"metadata","data":"%s"}`, huge)
	f.line(`{"event":"property-change","name":"pause","data":true}`)

	ev := event(t, p)
	if ev.Name != "pause" || ev.Data != true {
		t.Errorf("event = %+v, want the pause that follows the long line", ev)
	}
}

func TestEventsAreNotDroppedWhenTheChannelIsFull(t *testing.T) {
	p, _ := newFake(t, 1)
	p.Events <- Event{Kind: EventProperty, Name: "volume", Data: 1.0}

	sent := make(chan struct{})
	go func() {
		defer close(sent)
		p.send(Event{Kind: EventExit})
	}()

	if first := event(t, p); first.Name != "volume" {
		t.Fatalf("first event = %+v", first)
	}
	if second := event(t, p); second.Kind != EventExit {
		t.Errorf("second event = %+v, want the exit that waited for room", second)
	}
	<-sent
}

func TestMeterEventsAreDroppedWhenTheChannelIsFull(t *testing.T) {
	p, _ := newFake(t, 1)
	p.Events <- Event{Kind: EventProperty, Name: "volume"}

	// send returns at once, because it drops the meter event.
	p.send(Event{Kind: EventProperty, Name: MeterProperty})

	if got := len(p.Events); got != 1 {
		t.Fatalf("the channel holds %d events, want only the first", got)
	}
	if ev := <-p.Events; ev.Name != "volume" {
		t.Errorf("event = %+v, want the one that was there first", ev)
	}
}

func TestCloseFreesAReaderThatWaitsToDeliver(t *testing.T) {
	p, f := newFake(t, 1)
	ended := run(p)

	// The first event fills the channel, and the reader blocks on the
	// second one, because nobody reads Events.
	f.line(`{"event":"property-change","name":"pause","data":true}`)
	f.line(`{"event":"property-change","name":"pause","data":false}`)

	p.Close()
	p.Close()

	select {
	case <-ended:
	case <-time.After(guard):
		t.Fatal("the reader still waits after Close")
	}
}

func TestBrokenConnectionIsReported(t *testing.T) {
	p, f := newFake(t, 16)
	run(p)

	_ = f.conn.Close()
	ev := event(t, p)
	if ev.Kind != EventError || ev.Text != brokenText {
		t.Errorf("event = %+v, want the broken connection message", ev)
	}
}

func TestCloseIsNotABrokenConnection(t *testing.T) {
	p, f := newFake(t, 16)
	ended := run(p)

	p.Close()
	_ = f.conn.Close()
	<-ended
	if got := len(p.Events); got != 0 {
		t.Errorf("Close left %d events", got)
	}
}

// TestExitOfMpvIsReportedOnce checks that the exit message of waitLoop
// and the message of a broken connection do not both reach the screen.
func TestExitOfMpvIsReportedOnce(t *testing.T) {
	p, f := newFake(t, 16)
	p.exited = make(chan struct{})
	close(p.exited)
	ended := run(p)

	_ = f.conn.Close()
	<-ended
	if got := len(p.Events); got != 0 {
		t.Fatalf("the reader reported the end of mpv itself, with %d events", got)
	}
	if !p.markReported() || p.markReported() {
		t.Error("markReported must be true once, then false")
	}
}

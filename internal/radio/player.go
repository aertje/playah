package radio

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Event kinds that the player sends to the user interface.
const (
	EventProperty = "property"
	EventError    = "error"
	EventExit     = "exit"
)

// meterLabel names the level filter in the mpv audio chain.
const meterLabel = "lvl"

// prebufferSeconds is how much sound mpv must hold before it starts to
// play. mpv holds the stream paused until the cache reaches this amount,
// so a slow start does not break up the first seconds of the sound.
const prebufferSeconds = 2

// MeterProperty is the mpv property that holds the level of each audio
// channel. The astats filter fills it about 16 times each second.
const MeterProperty = "af-metadata/" + meterLabel

// meterFilter measures the peak level and the RMS level of each channel.
// The measure options keep the message small, because mpv sends the full
// property value on each change.
const meterFilter = "@" + meterLabel + ":lavfi=[astats=metadata=1:reset=1:" +
	"measure_perchannel=Peak_level+RMS_level:measure_overall=none]"

// Event is one message from mpv.
type Event struct {
	Kind string // EventProperty, EventError or EventExit
	Name string // property name, for EventProperty
	Data any    // property value, for EventProperty
	Text string // message, for EventError

	// Stream names the stream that the event belongs to, by the number
	// that Player.Load gave it. A property event and the end of a failed
	// stream carry the stream that mpv was on when it sent them. The error
	// reply to a command carries the latest stream. App.Handle drops a
	// stream event that is not from the latest stream.
	Stream int64
}

// Player runs one mpv process and talks to it over the JSON IPC socket.
type Player struct {
	cmd    *exec.Cmd
	conn   net.Conn
	Events chan Event

	// done closes first when Close starts. It frees a reader that waits
	// to deliver an event while nobody reads Events.
	done chan struct{}

	// exited closes when the mpv process ends. Only waitLoop calls
	// cmd.Wait, because two calls at the same time are a data race.
	exited chan struct{}

	// stream counts the loadfile commands that the player sent. Load
	// moves it, and App reads it from the goroutine of the screen.
	stream atomic.Int64

	mu     sync.Mutex
	nextID int64
	closed bool

	// reported tells that the user interface heard of the end of mpv.
	// The reader and the waiter both see that end, and one message is
	// enough.
	reported bool

	// quietID is the request id of a command whose error the user must
	// not see. The level filter uses it, because it is optional.
	quietID int64

	// loads maps the request id of each loadfile command that waits for
	// its reply to the stream number of that command.
	loads map[int64]int64
}

// properties lists the mpv properties that the user interface shows.
// mpv sends the current value as soon as it accepts the observe command.
var properties = []string{
	"pause",
	"core-idle",
	"idle-active",
	"metadata",
	"media-title",
	"audio-bitrate",
	"audio-codec-name",
	"audio-params/samplerate",
	"audio-params/channels",
	"demuxer-cache-duration",
	"volume",
	MeterProperty,
}

const (
	// eventBuffer is the room of the Events channel.
	eventBuffer = 64

	// writeTimeout bounds one write to mpv. A write that stalls would
	// hold the lock of the player, and Close would wait for it.
	writeTimeout = 2 * time.Second

	// maxLine is the longest line from mpv that the reader keeps. A longer
	// line is skipped, because one odd message must not end the reader.
	maxLine = 1024 * 1024

	// brokenGrace is how long the reader waits for the exit of mpv before
	// it calls a read error a broken connection. A dead mpv closes the
	// socket first, and waitLoop then names the true cause.
	brokenGrace = time.Second
)

// brokenText is the message for a read error while mpv still runs.
const brokenText = "the connection to mpv broke. Restart playah"

// StartPlayer starts mpv in idle mode and connects to it.
//
// mpv gets one end of a socket pair as its IPC channel, so no socket file
// exists for another process to find. mpv quits when that channel closes,
// which also happens when playah dies without a chance to clean up.
func StartPlayer(volume int) (*Player, error) {
	if _, err := exec.LookPath("mpv"); err != nil {
		return nil, errors.New("mpv is not on the PATH. Install mpv first")
	}

	conn, child, err := socketPair()
	if err != nil {
		return nil, fmt.Errorf("the IPC channel to mpv did not open: %w", err)
	}

	cmd := exec.Command("mpv",
		"--idle=yes",
		"--no-video",
		"--no-terminal",
		"--pause=no",
		"--cache=yes",
		"--cache-pause-initial=yes",
		fmt.Sprintf("--cache-pause-wait=%d", prebufferSeconds),
		// The first extra file is file descriptor 3 in the child.
		"--input-ipc-client=fd://3",
		fmt.Sprintf("--volume=%d", volume),
	)
	cmd.ExtraFiles = []*os.File{child}
	err = cmd.Start()
	// The child holds its own copy now. Without this close, mpv would
	// never see the end of the channel when playah dies.
	_ = child.Close()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("mpv did not start: %w", err)
	}

	p := newPlayer(conn, eventBuffer)
	p.cmd = cmd
	p.exited = make(chan struct{})
	go p.readLoop()
	go p.waitLoop()

	for i, name := range properties {
		if err := p.command("observe_property", i+1, name); err != nil {
			p.Close()
			return nil, err
		}
	}

	// The level filter is optional. An mpv build without the astats
	// filter rejects the command, and playah then shows no meter. The
	// player hides that fault, because the sound plays without the
	// filter. A command line option cannot do this: a filter that fails
	// there stops the audio.
	_ = p.writeCommand(true, "af", "add", meterFilter)

	return p, nil
}

// socketPair makes a connected pair of unix sockets. The first is the
// connection of the player, and the second goes to mpv.
func socketPair() (net.Conn, *os.File, error) {
	// The lock keeps a process that another goroutine starts from
	// inheriting the descriptors before they are marked close-on-exec.
	syscall.ForkLock.RLock()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err == nil {
		syscall.CloseOnExec(fds[0])
		syscall.CloseOnExec(fds[1])
	}
	syscall.ForkLock.RUnlock()
	if err != nil {
		return nil, nil, err
	}

	mine := os.NewFile(uintptr(fds[0]), "mpv-ipc-player")
	child := os.NewFile(uintptr(fds[1]), "mpv-ipc-mpv")
	// FileConn duplicates the descriptor, so the file closes after it.
	conn, err := net.FileConn(mine)
	_ = mine.Close()
	if err != nil {
		_ = child.Close()
		return nil, nil, err
	}
	return conn, child, nil
}

// newPlayer makes a player around a connection to mpv. It starts no
// process and no goroutine.
func newPlayer(conn net.Conn, buffer int) *Player {
	return &Player{
		conn:   conn,
		Events: make(chan Event, buffer),
		done:   make(chan struct{}),
	}
}

// Load starts a new stream and replaces the current one. Each Load that
// reaches mpv gives the stream the next number, which Stream reports.
func (p *Player) Load(url string) error {
	if err := p.command("set_property", "pause", false); err != nil {
		return err
	}
	return p.write(false, true, "loadfile", url, "replace")
}

// Stream returns the number of the latest stream that Load sent to mpv.
// It stays zero until the first Load.
func (p *Player) Stream() int64 { return p.stream.Load() }

// Stop ends playback and returns mpv to the idle state.
func (p *Player) Stop() error { return p.command("stop") }

// TogglePause switches between play and pause.
func (p *Player) TogglePause() error { return p.command("cycle", "pause") }

// SetVolume sets the mpv volume in percent.
func (p *Player) SetVolume(v int) error { return p.command("set_property", "volume", v) }

// Close shuts mpv down and releases the connection. It is safe to call
// more than once.
func (p *Player) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	if p.done != nil {
		close(p.done)
	}
	p.mu.Unlock()

	_ = p.writeCommand(false, "quit")
	if p.cmd != nil && p.exited != nil {
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			// mpv ignored the quit command.
			_ = p.cmd.Process.Kill()
			select {
			case <-p.exited:
			case <-time.After(2 * time.Second):
			}
		}
	}
	if p.conn != nil {
		_ = p.conn.Close()
	}
}

// command sends one mpv command and drops the reply, except for errors.
func (p *Player) command(args ...any) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return errors.New("the player is closed")
	}
	p.mu.Unlock()
	return p.writeCommand(false, args...)
}

// writeCommand sends one mpv command. A quiet command keeps its error
// reply away from the user interface.
func (p *Player) writeCommand(quiet bool, args ...any) error {
	return p.write(quiet, false, args...)
}

// write sends one mpv command. A load command also takes the next stream
// number, and the reader maps the reply back to it.
func (p *Player) write(quiet, load bool, args ...any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// A player without a connection cannot send a command. The user
	// interface shows the message, and the process continues.
	if p.conn == nil {
		return errors.New("the player has no connection to mpv")
	}

	id := p.nextID + 1
	payload, err := json.Marshal(map[string]any{
		"command":    args,
		"request_id": id,
	})
	if err != nil {
		return err
	}

	p.nextID = id
	if quiet {
		p.quietID = id
	}
	if load {
		if p.loads == nil {
			p.loads = make(map[int64]int64)
		}
		p.loads[id] = p.stream.Add(1)
	}

	_ = p.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	if _, err := p.conn.Write(append(payload, '\n')); err != nil {
		if load {
			// The command did not go out, so the stream does not exist.
			delete(p.loads, id)
			p.stream.Add(-1)
		}
		return err
	}
	return nil
}

// ipcMessage covers both the event messages and the command replies.
type ipcMessage struct {
	Event   string          `json:"event"`
	ID      int64           `json:"request_id"`
	Name    string          `json:"name"`
	Data    json.RawMessage `json:"data"`
	Reason  string          `json:"reason"`
	Error   string          `json:"error"`
	EntryID int64           `json:"playlist_entry_id"` // start-file
}

// errLineTooLong marks a line that readLine skipped.
var errLineTooLong = errors.New("the line from mpv is too long")

// readLine reads one line without its newline. A line longer than maxLine
// comes back as errLineTooLong after the reader has consumed all of it.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, more, err := r.ReadLine()
		if err != nil {
			return nil, err
		}
		if !tooLong {
			if len(line)+len(chunk) > maxLine {
				tooLong = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if more {
			continue
		}
		if tooLong {
			return nil, errLineTooLong
		}
		return line, nil
	}
}

// streamTracker follows which stream mpv is on. mpv answers a loadfile
// command at once, and it sends start-file when the stream really starts.
// The reply therefore tells the number of the stream that starts next.
type streamTracker struct {
	current int64           // the stream that mpv is on
	pending int64           // the stream of the last accepted loadfile
	entries map[int64]int64 // playlist entry id to stream number
}

// accepted notes the reply to a loadfile command. A reply that holds the
// playlist entry id lets start-file name its stream exactly, even when
// two loads came in quick succession.
func (t *streamTracker) accepted(stream, entry int64) {
	t.pending = stream
	if entry == 0 {
		return
	}
	if t.entries == nil {
		t.entries = make(map[int64]int64)
	}
	t.entries[entry] = stream
}

// started adopts the stream that begins now.
func (t *streamTracker) started(entry int64) {
	stream, ok := t.entries[entry]
	switch {
	case ok && entry != 0:
		delete(t.entries, entry)
		// An older load that never started is gone for good.
		for id, s := range t.entries {
			if s < stream {
				delete(t.entries, id)
			}
		}
	case t.pending != 0:
		stream = t.pending
	default:
		return
	}
	t.current = stream
	if t.pending == stream {
		t.pending = 0
	}
}

// failed drops the stream of a rejected loadfile, so a start-file that
// follows does not take its number.
func (t *streamTracker) failed(stream int64) {
	if t.pending == stream {
		t.pending = 0
	}
}

// readLoop reads the newline delimited JSON that mpv sends.
func (p *Player) readLoop() {
	reader := bufio.NewReaderSize(p.conn, 64*1024)
	var tracker streamTracker

	for {
		line, err := readLine(reader)
		if errors.Is(err, errLineTooLong) {
			continue
		}
		if err != nil {
			p.readFailed()
			return
		}

		var msg ipcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		switch {
		case msg.Event == "property-change":
			var value any
			if len(msg.Data) > 0 {
				_ = json.Unmarshal(msg.Data, &value)
			}
			p.send(Event{Kind: EventProperty, Name: msg.Name, Data: value, Stream: tracker.current})

		case msg.Event == "start-file":
			tracker.started(msg.EntryID)

		case msg.Event == "end-file" && msg.Reason == "error":
			p.send(Event{Kind: EventError, Text: "the stream failed. Check the URL", Stream: tracker.current})

		case msg.Event == "":
			p.reply(&tracker, msg)
		}
	}
}

// reply handles the answer to one command.
func (p *Player) reply(tracker *streamTracker, msg ipcMessage) {
	stream, isLoad := p.takeLoad(msg.ID)
	failed := msg.Error != "" && msg.Error != "success"

	if isLoad {
		if failed {
			tracker.failed(stream)
		} else {
			var result struct {
				Entry int64 `json:"playlist_entry_id"`
			}
			_ = json.Unmarshal(msg.Data, &result)
			tracker.accepted(stream, result.Entry)
		}
	}
	if !failed || p.isQuiet(msg.ID) {
		return
	}

	// A rejected load belongs to its own stream. Any other command
	// belongs to the latest one.
	if !isLoad {
		stream = p.Stream()
	}
	p.send(Event{Kind: EventError, Text: "mpv: " + msg.Error, Stream: stream})
}

// readFailed reports a read error. A read error after Close is the normal
// end of the reader.
func (p *Player) readFailed() {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return
	}

	// A dead mpv closes the socket before the waiter sees the exit, and
	// the exit message names the cause better than a broken connection.
	if p.exited != nil {
		select {
		case <-p.exited:
			return
		case <-time.After(brokenGrace):
		case <-p.done:
			return
		}
	}
	if p.markReported() {
		p.send(Event{Kind: EventError, Text: brokenText, Stream: p.Stream()})
	}
}

// markReported reports whether the caller is the first to tell the user
// that mpv is gone.
func (p *Player) markReported() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reported {
		return false
	}
	p.reported = true
	return true
}

// waitLoop reaps the mpv process and reports its end. It is the only
// caller of cmd.Wait.
func (p *Player) waitLoop() {
	_ = p.cmd.Wait()
	close(p.exited)

	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if !closed && p.markReported() {
		p.send(Event{Kind: EventExit})
	}
}

// takeLoad returns the stream of a loadfile request, and it forgets the
// request.
func (p *Player) takeLoad(id int64) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	stream, ok := p.loads[id]
	delete(p.loads, id)
	return stream, ok
}

// isQuiet reports whether the reader must drop the error of a reply.
func (p *Player) isQuiet(id int64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return id != 0 && id == p.quietID
}

// send delivers an event. It waits for room in the channel, because a
// lost pause or exit event would leave the screen wrong for good, and it
// gives up when Close starts. The level meter is the exception: a newer
// level follows within a fraction of a second, so a full channel drops it
// instead of holding the reader.
func (p *Player) send(e Event) {
	if e.Kind == EventProperty && e.Name == MeterProperty {
		select {
		case p.Events <- e:
		default:
		}
		return
	}
	select {
	case p.Events <- e:
	case <-p.done:
	}
}

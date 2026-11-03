package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/LeeEirc/tclientlib"
)

type senderQueueEvent struct {
	data    string
	command tclientlib.Command
	finish  bool
}

// The first operation waits before reading its data, so tests can fill the
// queue and reuse input buffers while the network writer is still blocked.
// Only the sender worker records events; assertions read them after it stops.
type senderQueueClient struct {
	started      chan struct{}
	release      chan struct{}
	finishCalled chan struct{}
	first        sync.Once
	events       []senderQueueEvent
	writeErr     error
	commandErr   error
	finishErr    error
	zeroWrite    bool
}

func (c *senderQueueClient) wait() {
	c.first.Do(func() { close(c.started); <-c.release })
}

func (c *senderQueueClient) Write(p []byte) (int, error) {
	c.wait()
	last := len(c.events) - 1
	if last >= 0 && c.events[last].command == 0 && !c.events[last].finish {
		c.events[last].data += string(p)
	} else {
		c.events = append(c.events, senderQueueEvent{data: string(p)})
	}
	if c.writeErr != nil || c.zeroWrite {
		return 0, c.writeErr
	}
	return len(p), nil
}

func (c *senderQueueClient) SendCommand(command tclientlib.Command) error {
	c.wait()
	c.events = append(c.events, senderQueueEvent{command: command})
	return c.commandErr
}

func (c *senderQueueClient) TerminalMode() tclientlib.TerminalMode {
	return tclientlib.TerminalMode{}
}

func (c *senderQueueClient) finish() error {
	c.events = append(c.events, senderQueueEvent{finish: true})
	close(c.finishCalled)
	return c.finishErr
}

func newSenderQueueTest(t *testing.T, client *senderQueueClient) (*inputSender, context.CancelFunc, func()) {
	t.Helper()
	client.started, client.release, client.finishCalled = make(chan struct{}), make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	sender := newInputSender(ctx, client, client.finish)
	var once sync.Once
	release := func() { once.Do(func() { close(client.release) }) }
	t.Cleanup(func() {
		cancel()
		release()
		select {
		case <-sender.stopped:
		case <-time.After(2 * time.Second):
			t.Error("sender did not stop during cleanup")
		}
	})
	return sender, cancel, release
}

func waitSenderQueueStarted(t *testing.T, client *senderQueueClient) {
	t.Helper()
	select {
	case <-client.started:
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not start its first operation")
	}
}

func waitSenderQueueStopped(t *testing.T, sender *inputSender) {
	t.Helper()
	select {
	case <-sender.stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not stop")
	}
}

func senderQueueResult(t *testing.T, sender *inputSender) error {
	t.Helper()
	select {
	case err := <-sender.result:
		waitSenderQueueStopped(t, sender)
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("sender did not report its result")
		return nil
	}
}

func queueInput(t *testing.T, sender *inputSender, data []byte) {
	t.Helper()
	if n, err := sender.Write(data); n != len(data) || err != nil {
		t.Fatalf("queue input: accepted %d of %d bytes, error %v", n, len(data), err)
	}
}

func TestSenderQueuePreservesDataCommandAndFinishOrder(t *testing.T) {
	client := new(senderQueueClient)
	sender, _, release := newSenderQueueTest(t, client)
	queueInput(t, sender, []byte("first"))
	waitSenderQueueStarted(t, client)
	// A multi-chunk Write may be divided or coalesced internally, but its data
	// must stay before the command that follows it.
	chunked := bytes.Repeat([]byte("a"), maxInputChunk+7)
	queueInput(t, sender, chunked)
	if err := sender.SendCommand(tclientlib.CommandNoOperation); err != nil {
		t.Fatal(err)
	}
	queueInput(t, sender, []byte("middle"))
	if err := sender.SendCommand(tclientlib.CommandAreYouThere); err != nil {
		t.Fatal(err)
	}
	queueInput(t, sender, []byte("last"))
	sender.finishInput()
	select {
	case <-client.finishCalled:
		t.Fatal("finish ran before pending input was sent")
	default:
	}
	if n, err := sender.Write([]byte("after EOF")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("input after EOF: %d, %v", n, err)
	}
	release()
	if err := senderQueueResult(t, sender); err != nil {
		t.Fatal(err)
	}
	want := []senderQueueEvent{
		{data: "first" + string(chunked)},
		{command: tclientlib.CommandNoOperation},
		{data: "middle"},
		{command: tclientlib.CommandAreYouThere},
		{data: "last"},
		{finish: true},
	}
	if !reflect.DeepEqual(client.events, want) {
		t.Fatalf("send order = %#v, want %#v", client.events, want)
	}
}

func TestSenderQueueLimitIncludesInflightData(t *testing.T) {
	client := new(senderQueueClient)
	sender, _, _ := newSenderQueueTest(t, client)
	queueInput(t, sender, make([]byte, maxInputChunk))
	waitSenderQueueStarted(t, client)
	remaining := maxPendingInputBytes - maxInputChunk
	n, err := sender.Write(make([]byte, remaining+17))
	if n != remaining || !errors.Is(err, errInputBacklog) {
		t.Fatalf("overflow: accepted %d bytes, error %v; want %d and errInputBacklog", n, err, remaining)
	}
	if n, err := sender.Write([]byte("x")); n != 0 || !errors.Is(err, errInputBacklog) {
		t.Fatalf("full queue accepted input: %d, %v", n, err)
	}
	if err := sender.SendCommand(tclientlib.CommandNoOperation); !errors.Is(err, errInputBacklog) {
		t.Fatalf("full queue accepted command: %v", err)
	}
}

func TestSenderQueueCommandsUseByteBudget(t *testing.T) {
	client := new(senderQueueClient)
	sender, _, _ := newSenderQueueTest(t, client)
	queueInput(t, sender, make([]byte, maxInputChunk))
	waitSenderQueueStarted(t, client)
	queueInput(t, sender, make([]byte, maxPendingInputBytes-maxInputChunk-3))
	if err := sender.SendCommand(tclientlib.CommandNoOperation); err != nil {
		t.Fatalf("command should fit with three bytes available: %v", err)
	}
	if err := sender.SendCommand(tclientlib.CommandAreYouThere); !errors.Is(err, errInputBacklog) {
		t.Fatalf("two-byte command fit into one remaining byte: %v", err)
	}
	queueInput(t, sender, []byte("x"))
	if n, err := sender.Write([]byte("y")); n != 0 || !errors.Is(err, errInputBacklog) {
		t.Fatalf("command bytes were not counted: %d, %v", n, err)
	}
}

func TestSenderQueueCopiesInputBeforeReturning(t *testing.T) {
	client := new(senderQueueClient)
	sender, _, release := newSenderQueueTest(t, client)
	first := []byte("inflight")
	queueInput(t, sender, first)
	waitSenderQueueStarted(t, client)
	queued := make([]byte, len("queued"), 64)
	copy(queued, "queued")
	queueInput(t, sender, queued)
	suffix := []byte("suffix")
	queueInput(t, sender, suffix)
	for _, data := range [][]byte{first, queued, suffix} {
		for i := range data {
			data[i] = 'X'
		}
	}
	sender.finishInput()
	release()
	if err := senderQueueResult(t, sender); err != nil {
		t.Fatal(err)
	}
	want := []senderQueueEvent{{data: "inflightqueuedsuffix"}, {finish: true}}
	if !reflect.DeepEqual(client.events, want) {
		t.Fatalf("reusing caller buffers changed queued input: %#v", client.events)
	}
}

func TestSenderQueueCancellationDoesNotStartPendingOperations(t *testing.T) {
	client := new(senderQueueClient)
	sender, cancel, release := newSenderQueueTest(t, client)
	queueInput(t, sender, []byte("inflight"))
	waitSenderQueueStarted(t, client)
	if err := sender.SendCommand(tclientlib.CommandNoOperation); err != nil {
		t.Fatal(err)
	}
	queueInput(t, sender, []byte("must not start"))
	sender.finishInput()
	cancel()
	if n, err := sender.Write([]byte("after cancel")); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("write after cancellation: %d, %v", n, err)
	}
	release()
	waitSenderQueueStopped(t, sender)
	want := []senderQueueEvent{{data: "inflight"}}
	if !reflect.DeepEqual(client.events, want) {
		t.Fatalf("canceled sender started pending work: %#v", client.events)
	}
}

func TestSenderQueuePropagatesFailures(t *testing.T) {
	wantErr := errors.New("send failed")
	for _, test := range []struct {
		name         string
		client       *senderQueueClient
		startCommand bool
		want         error
	}{
		{name: "write", client: &senderQueueClient{writeErr: wantErr}, want: wantErr},
		{name: "command", client: &senderQueueClient{commandErr: wantErr}, startCommand: true, want: wantErr},
		{name: "finish", client: &senderQueueClient{finishErr: wantErr}, want: wantErr},
		{name: "zero-write", client: &senderQueueClient{zeroWrite: true}, want: io.ErrShortWrite},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := test.client
			sender, _, release := newSenderQueueTest(t, client)
			if test.startCommand {
				if err := sender.SendCommand(tclientlib.CommandNoOperation); err != nil {
					t.Fatal(err)
				}
			} else {
				queueInput(t, sender, []byte("first"))
			}
			waitSenderQueueStarted(t, client)
			queueInput(t, sender, []byte("later"))
			sender.finishInput()
			release()
			if err := senderQueueResult(t, sender); !errors.Is(err, test.want) {
				t.Fatalf("sender result = %v, want %v", err, test.want)
			}
			if n, err := sender.Write([]byte("after failure")); n != 0 || !errors.Is(err, test.want) {
				t.Fatalf("write after failure: %d, %v", n, err)
			}
			if err := sender.SendCommand(tclientlib.CommandAreYouThere); !errors.Is(err, test.want) {
				t.Fatalf("command after failure: %v", err)
			}
			if test.name != "finish" && len(client.events) != 1 {
				t.Fatalf("sender continued after failure: %#v", client.events)
			}
		})
	}
}

func TestSenderQueueSaturationLeavesLocalQuitAvailable(t *testing.T) {
	client := new(senderQueueClient)
	sender, _, _ := newSenderQueueTest(t, client)
	queueInput(t, sender, make([]byte, maxPendingInputBytes))
	waitSenderQueueStarted(t, client)
	var local bytes.Buffer
	terminal := terminalInput{client: sender, local: &local, ending: []byte("\r\n"), echo: "on"}
	if err := terminal.feed([]byte("x")); !errors.Is(err, errInputBacklog) {
		t.Fatalf("saturated input: %v", err)
	}
	if local.Len() != 0 {
		t.Fatalf("rejected character was echoed: %q", local.String())
	}
	if err := terminal.feed([]byte("\x1dquit\r")); !errors.Is(err, errQuit) {
		t.Fatalf("saturated sender prevented local quit: %v", err)
	}
}

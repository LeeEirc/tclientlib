package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LeeEirc/tclientlib"
)

type observedWriteConn struct {
	net.Conn
	writeStarted chan struct{}
	startOnce    sync.Once
}

func (c *observedWriteConn) Write(p []byte) (int, error) {
	c.startOnce.Do(func() { close(c.writeStarted) })
	return c.Conn.Write(p)
}

func TestInputEOFStartsDrainDeadlineBeforeSendCompletes(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	// The peer deliberately never reads. In the terminal parsing path, EOF
	// must still reach the control loop while application output is blocked.
	client, err := tclientlib.NewClientConn(left, &tclientlib.ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terminal := &terminalInput{client: client, local: io.Discard, ending: []byte("\r\n"), echo: "off"}
	input := testInput(t, []byte("pending input"))
	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), options{drainTimeout: 50 * time.Millisecond}, input, io.Discard, left, client, terminal, func() error { return nil }, func() {})
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked pending input unexpectedly completed successfully")
		}
	case <-time.After(time.Second):
		left.Close()
		<-done
		t.Fatal("stdin EOF could not start its deadline while network Write was blocked")
	}
}

func TestTerminalInputOverflowFailsSessionExplicitly(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	client, err := tclientlib.NewClientConn(left, &tclientlib.ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	// The writer is blocked, so the entire accepted budget remains pending,
	// including whatever chunk the sender already took from the queue.
	input := testInput(t, bytes.Repeat([]byte{'x'}, maxPendingInputBytes+1))
	terminal := &terminalInput{client: client, local: io.Discard, ending: []byte("\r\n"), echo: "off"}
	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), options{drainTimeout: time.Second}, input, io.Discard, left, client, terminal, func() error { return nil }, func() {})
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errInputBacklog) || !strings.Contains(err.Error(), "queued input may not have reached the peer") {
			t.Fatalf("overflow did not report incomplete delivery: %v", err)
		}
	case <-time.After(3 * time.Second):
		left.Close()
		<-done
		t.Fatal("terminal queue saturation blocked instead of failing explicitly")
	}
}

func TestTerminalEOFDrainsDataAndCommandsBeforeFlushAndCloseWrite(t *testing.T) {
	wire := make(chan []byte, 1)
	host, port := testServer(t, func(conn net.Conn) {
		data, err := io.ReadAll(conn)
		if err != nil {
			t.Errorf("server read: %v", err)
		}
		wire <- data
		_, _ = conn.Write([]byte("final output"))
	})
	conn, err := net.Dial("tcp", net.JoinHostPort(host, port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client, err := tclientlib.NewClientConn(conn, &tclientlib.ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terminal := &terminalInput{client: client, local: io.Discard, ending: []byte("\r\n"), echo: "off"}
	input := testInput(t, []byte("one\r\x1dsend nop\rcontinue\rtwo\r"))
	var output bytes.Buffer
	// Model an encoder's final buffered bytes. The server cannot return its
	// final output until both these bytes and TCP CloseWrite have occurred.
	flush := func() error { return writeFull(client, []byte("[flush]")) }
	o := options{drainTimeout: time.Second, closeWrite: true}
	if err := runSession(context.Background(), o, input, &output, conn, client, terminal, flush, func() {}); err != nil {
		t.Fatal(err)
	}
	if got, want := <-wire, []byte("one\r\n\xff\xf1two\r\n[flush]"); !bytes.Equal(got, want) {
		t.Fatalf("wire order got %q, want %q", got, want)
	}
	if output.String() != "final output" {
		t.Fatalf("final output was lost: %q", output.String())
	}
}

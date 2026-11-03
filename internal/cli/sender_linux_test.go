//go:build linux

package cli

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/LeeEirc/tclientlib"
)

func TestBlockedInputWriteDoesNotBlockLocalQuit(t *testing.T) {
	master, slave := resizeTestPTY(t, 80, 24)
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	conn := &observedWriteConn{Conn: left, writeStarted: make(chan struct{})}
	client, err := tclientlib.NewClientConn(conn, &tclientlib.ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terminal := &terminalInput{client: client, local: io.Discard, ending: []byte("\r\n"), echo: "off"}
	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), options{drainTimeout: time.Second}, slave, io.Discard, conn, client, terminal, func() error { return nil }, func() {})
	}()
	if _, err := master.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.writeStarted:
	case <-time.After(time.Second):
		conn.Close()
		<-done
		t.Fatal("input did not reach network Write")
	}
	// This is a distinct stdin read, made only after Write has begun. The
	// unresponsive peer never reads, so only closing the session releases it.
	if _, err := master.Write([]byte("\x1dquit\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("local quit: %v", err)
		}
	case <-time.After(time.Second):
		conn.Close()
		<-done
		t.Fatal("blocked input Write prevented local quit from being read")
	}
}

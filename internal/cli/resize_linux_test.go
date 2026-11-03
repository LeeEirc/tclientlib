//go:build linux

package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/LeeEirc/tclientlib"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

func resizeTestPTY(t *testing.T, width, height uint16) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("PTY unavailable: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	number, err := unix.IoctlGetInt(int(master.Fd()), unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slave.Close() })
	if err := unix.IoctlSetWinsize(int(slave.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Col: width, Row: height}); err != nil {
		t.Fatal(err)
	}
	state, err := term.MakeRaw(int(slave.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { term.Restore(int(slave.Fd()), state) })
	return master, slave
}

// resizeTestConn first requests NAWS. Its initial negotiation reply succeeds,
// then a standalone resize write can be held until Close. That reproduces a
// socket under backpressure without depending on TCP buffer sizes or sleeps.
type resizeTestConn struct {
	firstRead     bool
	blockResize   bool
	resizeBlocked chan struct{}
	remoteEOF     chan struct{}
	closed        chan struct{}
	writes        chan []byte
	blockOnce     sync.Once
	closeOnce     sync.Once
}

func newResizeTestConn(block bool) *resizeTestConn {
	return &resizeTestConn{
		blockResize: block, resizeBlocked: make(chan struct{}), remoteEOF: make(chan struct{}),
		closed: make(chan struct{}), writes: make(chan []byte, 16),
	}
}

func (c *resizeTestConn) Read(p []byte) (int, error) {
	if !c.firstRead {
		c.firstRead = true
		return copy(p, []byte{255, 253, 31}), nil // DO NAWS
	}
	select {
	case <-c.remoteEOF:
		return 0, io.EOF
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *resizeTestConn) Write(p []byte) (int, error) {
	if c.blockResize && bytes.HasPrefix(p, []byte{255, 250, 31}) {
		c.blockOnce.Do(func() { close(c.resizeBlocked) })
		<-c.closed
		return 0, net.ErrClosed
	}
	select {
	case c.writes <- append([]byte(nil), p...):
	case <-c.closed:
		return 0, net.ErrClosed
	}
	return len(p), nil
}

func (c *resizeTestConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *resizeTestConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *resizeTestConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *resizeTestConn) SetDeadline(time.Time) error      { return nil }
func (c *resizeTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *resizeTestConn) SetWriteDeadline(time.Time) error { return nil }

func TestBlockedResizeDoesNotBlockSessionExit(t *testing.T) {
	for _, exit := range []string{"local quit", "remote EOF"} {
		t.Run(exit, func(t *testing.T) {
			master, slave := resizeTestPTY(t, 110, 35)
			conn := newResizeTestConn(true)
			defer conn.Close()
			client, err := tclientlib.NewClientConn(conn, &tclientlib.ClientConfig{Width: 80, Height: 24})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			terminal := &terminalInput{client: client, local: io.Discard, ending: []byte("\r\n"), echo: "off"}
			done := make(chan error, 1)
			go func() {
				done <- runSession(context.Background(), options{drainTimeout: time.Second}, slave, io.Discard, conn, client, terminal, func() error { return nil }, func() {})
			}()
			// Wait for the blocking NAWS write itself, rather than assuming the
			// signal subscription or the negotiation completed after a delay.
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
		waitResize:
			for {
				select {
				case <-conn.resizeBlocked:
					break waitResize
				case <-ticker.C:
					_ = unix.Kill(os.Getpid(), unix.SIGWINCH)
				case <-deadline.C:
					conn.Close()
					<-done
					t.Fatal("resize never reached the blocked network write")
				}
			}
			if exit == "local quit" {
				if _, err := master.Write([]byte("\x1dquit\r")); err != nil {
					t.Fatal(err)
				}
			} else {
				close(conn.remoteEOF)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("%s: %v", exit, err)
				}
			case <-time.After(time.Second):
				conn.Close()
				<-done
				t.Fatalf("blocked resize prevented %s from ending the session", exit)
			}
		})
	}
}

func TestSessionRefreshesSizeWithoutAnotherSignal(t *testing.T) {
	_, slave := resizeTestPTY(t, 109, 37)
	conn := newResizeTestConn(false)
	defer conn.Close()
	// Model a size captured before login: the terminal already has a newer
	// size when the interactive session starts, and no new SIGWINCH arrives.
	client, err := tclientlib.NewClientConn(conn, &tclientlib.ClientConfig{Width: 80, Height: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	terminal := &terminalInput{client: client, local: io.Discard, ending: []byte("\r\n"), echo: "off"}
	done := make(chan error, 1)
	go func() {
		done <- runSession(context.Background(), options{drainTimeout: time.Second}, slave, io.Discard, conn, client, terminal, func() error { return nil }, func() {})
	}()
	want := []byte{255, 250, 31, 0, 109, 0, 37, 255, 240}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case packet := <-conn.writes:
			if bytes.Contains(packet, want) {
				close(conn.remoteEOF)
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				return
			}
		case <-timer.C:
			close(conn.remoteEOF)
			<-done
			t.Fatal("session kept the size captured before login until another SIGWINCH")
		}
	}
}

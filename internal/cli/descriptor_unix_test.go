//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func descriptorTestFlags(t *testing.T, file *os.File) int {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var controlErr error
	if err := raw.Control(func(fd uintptr) {
		flags, controlErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
	}); err != nil {
		t.Fatal(err)
	}
	if controlErr != nil {
		t.Fatal(controlErr)
	}
	return flags
}

func descriptorTestPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close(); writer.Close() })
	return reader, writer
}

func TestTerminalOutputsPreserveManagedPipeFlags(t *testing.T) {
	for _, alias := range []string{"same file", "duplicated descriptor"} {
		t.Run(alias, func(t *testing.T) {
			_, stdout := descriptorTestPipe(t)
			stderr := stdout
			if alias == "duplicated descriptor" {
				raw, err := stdout.SyscallConn()
				if err != nil {
					t.Fatal(err)
				}
				var duplicate int
				var duplicateErr error
				if err := raw.Control(func(fd uintptr) { duplicate, duplicateErr = unix.Dup(int(fd)) }); err != nil {
					t.Fatal(err)
				}
				if duplicateErr != nil {
					t.Fatal(duplicateErr)
				}
				stderr = os.NewFile(uintptr(duplicate), "stderr alias")
				defer stderr.Close()
			}
			before := descriptorTestFlags(t, stdout)
			if before&unix.O_NONBLOCK == 0 {
				t.Fatal("Go pipe was not nonblocking before output setup")
			}
			_, _, restore, err := terminalOutputs(context.Background(), stdout, stderr)
			if err != nil {
				t.Fatal(err)
			}
			if got := descriptorTestFlags(t, stdout); got != before {
				_ = restore()
				t.Fatalf("output setup changed pipe flags: %#x, want %#x", got, before)
			}
			if err := restore(); err != nil {
				t.Fatal(err)
			}
			for _, file := range []*os.File{stdout, stderr} {
				if got := descriptorTestFlags(t, file); got != before {
					t.Fatalf("output restore changed pipe flags: %#x, want %#x", got, before)
				}
			}
		})
	}
}

func TestOutputSetupFailureDoesNotChangeEarlierDescriptor(t *testing.T) {
	_, stdout := descriptorTestPipe(t)
	_, stderr := descriptorTestPipe(t)
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}
	before := descriptorTestFlags(t, stdout)
	if _, _, _, err := terminalOutputs(context.Background(), stdout, stderr); err == nil {
		t.Fatal("closed output descriptor was accepted")
	}
	if got := descriptorTestFlags(t, stdout); got != before {
		t.Fatalf("failed setup changed stdout flags: %#x, want %#x", got, before)
	}
}

func TestRunConnectFailurePreservesInputFlags(t *testing.T) {
	input, _ := descriptorTestPipe(t)
	before := descriptorTestFlags(t, input)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	if code := Run([]string{"--timeout", "1s", host, port}, input, io.Discard, &diagnostics); code != 1 {
		t.Fatalf("connection failure exit = %d, diagnostics=%q", code, diagnostics.String())
	}
	if got := descriptorTestFlags(t, input); got != before {
		t.Fatalf("connection failure changed stdin flags: %#x, want %#x", got, before)
	}
}

func TestReadInputPreservesManagedPipeFlags(t *testing.T) {
	for _, operation := range []string{"read", "cancel before read", "cancel while idle"} {
		t.Run(operation, func(t *testing.T) {
			input, writer := descriptorTestPipe(t)
			before := descriptorTestFlags(t, input)
			ctx, cancel := context.WithCancel(context.Background())
			if operation == "cancel while idle" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
			}
			defer cancel()
			switch operation {
			case "read":
				if _, err := writer.Write([]byte("x")); err != nil {
					t.Fatal(err)
				}
			case "cancel before read":
				cancel()
			}
			var data [1]byte
			n, err := readInput(ctx, input, data[:])
			if operation == "read" {
				if n != 1 || err != nil || data[0] != 'x' {
					t.Fatalf("read = %d %v %q", n, err, data)
				}
			} else if !errors.Is(err, ctx.Err()) || err == nil {
				t.Fatalf("canceled read = %d, %v; context=%v", n, err, ctx.Err())
			}
			if got := descriptorTestFlags(t, input); got != before {
				t.Fatalf("input operation changed pipe flags: %#x, want %#x", got, before)
			}
		})
	}
}

func TestTerminalProbesPreserveManagedPipeFlags(t *testing.T) {
	input, _ := descriptorTestPipe(t)
	before := descriptorTestFlags(t, input)
	if terminal, err := fileIsTerminal(input); err != nil || terminal {
		t.Fatalf("pipe terminal probe = %t, %v", terminal, err)
	}
	if _, _, err := fileTerminalSize(input); err == nil {
		t.Fatal("pipe unexpectedly has a terminal size")
	}
	if _, err := fileMakeRaw(input); err == nil {
		t.Fatal("pipe unexpectedly accepted raw terminal mode")
	}
	if got := descriptorTestFlags(t, input); got != before {
		t.Fatalf("terminal probes changed pipe flags: %#x, want %#x", got, before)
	}
}

func TestDescriptorCallbackDoesNotUseClosedFile(t *testing.T) {
	input, _ := descriptorTestPipe(t)
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	called := false
	err := withFileDescriptor(input, func(uintptr) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("closed descriptor callback called=%t, error=%v", called, err)
	}
}

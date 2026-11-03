package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsPipeSequence uint64

var windowsPipeModes = []struct {
	name string
	open func(*testing.T) (*os.File, *os.File)
}{
	{"synchronous", synchronousTestPipe},
	{"overlapped", overlappedTestPipe},
	{"os-pipe", func(t *testing.T) (*os.File, *os.File) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { r.Close(); w.Close() })
		return r, w
	}},
}

func synchronousTestPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	var r, w windows.Handle
	if err := windows.CreatePipe(&r, &w, nil, 4096); err != nil {
		t.Fatal(err)
	}
	reader, writer := os.NewFile(uintptr(r), "pipe-reader"), os.NewFile(uintptr(w), "pipe-writer")
	t.Cleanup(func() { reader.Close(); writer.Close() })
	return reader, writer
}

func overlappedTestPipe(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	name := fmt.Sprintf(`\\.\pipe\tclient-test-%d-%d`, os.Getpid(), atomic.AddUint64(&windowsPipeSequence, 1))
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	r, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	reader := os.NewFile(uintptr(r), "overlapped-reader")
	t.Cleanup(func() { reader.Close() })
	w, err := windows.CreateFile(path, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer := os.NewFile(uintptr(w), "overlapped-writer")
	t.Cleanup(func() { writer.Close() })
	// The client connected before ConnectNamedPipe, so no asynchronous connect
	// remains outstanding (ERROR_PIPE_CONNECTED is the documented success case).
	if err := windows.ConnectNamedPipe(r, &windows.Overlapped{}); err != nil && err != windows.ERROR_PIPE_CONNECTED {
		t.Fatal(err)
	}
	return reader, writer
}

var peekNamedPipe = windows.NewLazySystemDLL("kernel32.dll").NewProc("PeekNamedPipe")

func pipeAvailable(t *testing.T, file *os.File) uint32 {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var available uint32
	var peekErr error
	err = raw.Control(func(fd uintptr) {
		ok, _, callErr := peekNamedPipe.Call(fd, 0, 0, 0, uintptr(unsafe.Pointer(&available)), 0)
		if ok == 0 {
			peekErr = callErr
		}
	})
	if err != nil || peekErr != nil {
		t.Fatalf("peek pipe: %v, %v", err, peekErr)
	}
	return available
}

func waitPipeData(t *testing.T, reader *os.File) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for pipeAvailable(t, reader) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("output never reached the pipe")
		}
		time.Sleep(time.Millisecond)
	}
}

func checkPipeReusable(t *testing.T, reader, writer *os.File) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// A canceled write may already have copied a prefix into the pipe. Drain it
	// before checking that a later session can use the original handles.
	buffer := make([]byte, 4096)
	for available := pipeAvailable(t, reader); available > 0; available = pipeAvailable(t, reader) {
		if available < uint32(len(buffer)) {
			buffer = buffer[:available]
		}
		if _, err := readInput(ctx, reader, buffer); err != nil {
			t.Fatal(err)
		}
	}
	output, _, restore, err := terminalOutputs(ctx, writer, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	marker := []byte("original handles still work")
	if n, err := output.Write(marker); err != nil || n != len(marker) {
		t.Fatalf("reuse output: %d, %v", n, err)
	}
	got := make([]byte, len(marker))
	if n, err := readInput(ctx, reader, got); err != nil || n != len(marker) || !bytes.Equal(got, marker) {
		t.Fatalf("reuse input: %d, %v, %q", n, err, got)
	}
}

type windowsIOResult struct {
	n   int
	err error
}

func TestWindowsPipeOutputCancellation(t *testing.T) {
	for _, mode := range windowsPipeModes {
		t.Run(mode.name, func(t *testing.T) {
			reader, writer := mode.open(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			output, _, restore, err := terminalOutputs(ctx, writer, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restore() })
			done, stopped := make(chan windowsIOResult, 1), make(chan struct{})
			payload := make([]byte, 8<<20)
			go func() {
				defer close(stopped)
				n, err := output.Write(payload)
				done <- windowsIOResult{n, err}
			}()
			t.Cleanup(func() { cancel(); reader.Close(); <-stopped })
			waitPipeData(t, reader)
			select {
			case result := <-done:
				t.Fatalf("write did not block: %+v", result)
			default:
			}
			cancel()
			select {
			case result := <-done:
				if !errors.Is(result.err, context.Canceled) || result.n < 0 || result.n >= len(payload) {
					t.Fatalf("canceled write: %+v", result)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not stop pipe output")
			}
			if err := restore(); err != nil {
				t.Fatal(err)
			}
			checkPipeReusable(t, reader, writer)
		})
	}
}

func TestWindowsPipeInputCancellation(t *testing.T) {
	for _, mode := range windowsPipeModes {
		t.Run(mode.name, func(t *testing.T) {
			reader, writer := mode.open(t)
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			done, stopped := make(chan windowsIOResult, 1), make(chan struct{})
			go func() {
				defer close(stopped)
				n, err := readInput(ctx, reader, make([]byte, 1))
				done <- windowsIOResult{n, err}
			}()
			t.Cleanup(func() { cancel(); writer.Close(); <-stopped })
			select {
			case result := <-done:
				if result.n != 0 || !errors.Is(result.err, context.DeadlineExceeded) {
					t.Fatalf("canceled read: %+v", result)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not stop pipe input")
			}
			stopInput(reader)
			checkPipeReusable(t, reader, writer)
		})
	}
}

func TestWindowsPipeCancellationBeforeSyscall(t *testing.T) {
	for _, mode := range windowsPipeModes {
		t.Run(mode.name, func(t *testing.T) {
			reader, writer := mode.open(t)
			handle, err := duplicatePipe(reader)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { windows.CloseHandle(handle) })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releaseOperation := func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}
			done := make(chan windowsIOResult, 1)
			go func() {
				defer close(stopped)
				var buffer [1]byte
				n, err := pipeIO(ctx, handle, func(overlapped *windows.Overlapped, done *uint32) error {
					close(entered)
					<-release
					return windows.ReadFile(handle, buffer[:], done, overlapped)
				})
				done <- windowsIOResult{n, err}
			}()
			t.Cleanup(func() { cancel(); releaseOperation(); writer.Close(); <-stopped })
			select {
			case <-entered:
			case result := <-done:
				t.Fatalf("pipe operation did not start: %+v", result)
			case <-time.After(2 * time.Second):
				t.Fatal("pipe operation did not start")
			}
			cancel()
			// Let the cancellation watcher run while there is no pending I/O.
			// It must retry once ReadFile starts, rather than lose cancellation.
			time.Sleep(30 * time.Millisecond)
			releaseOperation()
			select {
			case result := <-done:
				if result.n != 0 || !errors.Is(result.err, context.Canceled) {
					t.Fatalf("early cancellation: %+v", result)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation before ReadFile was lost")
			}
			checkPipeReusable(t, reader, writer)
		})
	}
}

func TestWindowsPipeEOF(t *testing.T) {
	for _, mode := range windowsPipeModes {
		t.Run(mode.name, func(t *testing.T) {
			reader, writer := mode.open(t)
			writer.Close()
			if n, err := readInput(context.Background(), reader, make([]byte, 1)); n != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf("pipe EOF: %d, %v", n, err)
			}
		})
	}
}

func TestWindowsPipeAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	n, err := pipeIO(ctx, windows.InvalidHandle, func(*windows.Overlapped, *uint32) error {
		t.Fatal("operation started after cancellation")
		return nil
	})
	if n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("already canceled operation: %d, %v", n, err)
	}
}

func TestWindowsRegularFileOutputUsesOriginalFile(t *testing.T) {
	file := testInput(t, nil)
	output, diagnostics, restore, err := terminalOutputs(context.Background(), file, file)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if output != file || diagnostics != file {
		t.Fatal("non-pipe file was replaced")
	}
	stopInput(file)
	if _, err := io.WriteString(output, "你好\n"); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsDrainDeadlineCancelsBlockedOutput(t *testing.T) {
	for _, mode := range windowsPipeModes {
		t.Run(mode.name, func(t *testing.T) {
			reader, writer := mode.open(t)
			input, inputWriter := synchronousTestPipe(t)
			host, port := testServer(t, func(conn net.Conn) {
				_, _ = conn.Write(bytes.Repeat([]byte("x"), 8<<20))
			})
			o, err := parseOptions([]string{"--drain-timeout", "50ms", host, port}, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done, stopped := make(chan error, 1), make(chan struct{})
			go func() {
				defer close(stopped)
				done <- run(ctx, o, input, writer, io.Discard)
			}()
			t.Cleanup(func() { cancel(); reader.Close(); inputWriter.Close(); <-stopped })
			waitPipeData(t, reader)
			inputWriter.Close()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("blocked output did not reach the drain deadline")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("drain deadline did not stop blocked output")
			}
			checkPipeReusable(t, reader, writer)
		})
	}
}

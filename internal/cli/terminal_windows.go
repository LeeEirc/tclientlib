package cli

import (
	"context"
	"io"
	"os"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

func supportsTerminal() bool { return false }

func interruptSignals() []os.Signal { return []os.Signal{os.Interrupt} }

func watchResize() (<-chan os.Signal, func()) { return nil, func() {} }

// Keep the caller's handle open. Closing a synchronous Windows pipe does not
// reliably interrupt a pending ReadFile or WriteFile; cancel the operation on
// its issuing thread instead. Overlapped pipe handles need CancelIoEx as well.
func stopInput(*os.File) {}

func readInput(ctx context.Context, input *os.File, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	handle, err := duplicatePipe(input)
	if err != nil {
		return 0, err
	}
	if handle == windows.InvalidHandle {
		return input.Read(p)
	}
	defer windows.CloseHandle(handle)
	n, err := pipeIO(ctx, handle, func(overlapped *windows.Overlapped, done *uint32) error {
		return windows.ReadFile(handle, p, done, overlapped)
	})
	runtime.KeepAlive(p)
	if err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_HANDLE_EOF || n == 0 && err == nil {
		err = io.EOF
	}
	return n, err
}

func terminalOutputs(ctx context.Context, stdout, stderr io.Writer) (io.Writer, io.Writer, func() error, error) {
	writers := []io.Writer{stdout, stderr}
	var handles []windows.Handle
	restore := func() error {
		var first error
		for _, handle := range handles {
			if err := windows.CloseHandle(handle); err != nil && first == nil {
				first = err
			}
		}
		handles = nil
		return first
	}
	for i, writer := range writers {
		file, ok := writer.(*os.File)
		if !ok {
			continue
		}
		handle, err := duplicatePipe(file)
		if err != nil {
			_ = restore()
			return nil, nil, nil, err
		}
		if handle != windows.InvalidHandle {
			handles = append(handles, handle)
			writers[i] = &pipeWriter{ctx: ctx, handle: handle}
		}
	}
	return writers[0], writers[1], restore, nil
}

// Use RawConn to preserve the original handle's runtime polling association.
// Disk files and consoles retain os.File's append and Unicode handling.
func duplicatePipe(file *os.File) (windows.Handle, error) {
	handle := windows.InvalidHandle
	raw, err := file.SyscallConn()
	if err != nil {
		return handle, err
	}
	var handleErr error
	err = raw.Control(func(fd uintptr) {
		var kind uint32
		kind, handleErr = windows.GetFileType(windows.Handle(fd))
		if handleErr == nil && kind == windows.FILE_TYPE_PIPE {
			process := windows.CurrentProcess()
			handleErr = windows.DuplicateHandle(process, windows.Handle(fd), process, &handle, 0, false, windows.DUPLICATE_SAME_ACCESS)
		}
	})
	if err == nil {
		err = handleErr
	}
	return handle, err
}

type pipeWriter struct {
	ctx    context.Context
	handle windows.Handle
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n, err := pipeIO(w.ctx, w.handle, func(overlapped *windows.Overlapped, done *uint32) error {
			return windows.WriteFile(w.handle, p, done, overlapped)
		})
		runtime.KeepAlive(p)
		total += n
		p = p[n:]
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

var cancelSynchronousIO = windows.NewLazySystemDLL("kernel32.dll").NewProc("CancelSynchronousIo")

// A supplied OVERLAPPED works with both synchronous inherited pipes and pipes
// opened for overlapped I/O. Its event's low bit suppresses completion packets:
// the original pipe may belong to Go's IOCP, which does not own this OVERLAPPED.
func pipeIO(ctx context.Context, handle windows.Handle, operation func(*windows.Overlapped, *uint32) error) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	thread, err := windows.OpenThread(windows.THREAD_TERMINATE, false, windows.GetCurrentThreadId())
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(thread)
	event, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(event)
	overlapped := &windows.Overlapped{HEvent: event | 1}
	finished, watcherStopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcherStopped)
		select {
		case <-finished:
			return
		case <-ctx.Done():
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			_ = windows.CancelIoEx(handle, overlapped)
			_, _, _ = cancelSynchronousIO.Call(uintptr(thread))
			// Cancellation may arrive before ReadFile/WriteFile starts. Retry
			// until completion so that an initial ERROR_NOT_FOUND cannot hang.
			select {
			case <-finished:
				return
			case <-ticker.C:
			}
		}
	}()
	var done uint32
	err = operation(overlapped, &done)
	if err == nil || err == windows.ERROR_IO_PENDING {
		// An overlapped handle's immediate-success byte count must also come
		// from OVERLAPPED. Synchronous handles populate it before returning.
		err = windows.GetOverlappedResult(handle, overlapped, &done, true)
	}
	close(finished)
	// Join before reusing the thread or freeing any handle/buffer. Otherwise a
	// late CancelSynchronousIo could cancel an unrelated operation on it.
	<-watcherStopped
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	return int(done), err
}

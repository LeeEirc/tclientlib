//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package cli

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

func supportsTerminal() bool { return true }

func interruptSignals() []os.Signal {
	// Go otherwise exits immediately on a broken stdout/stderr pipe, bypassing
	// terminal restoration. Catch SIGPIPE so the write error follows cleanup.
	return []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGPIPE}
}

func watchResize() (<-chan os.Signal, func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	return ch, func() { signal.Stop(ch) }
}

func readInput(ctx context.Context, input *os.File, p []byte) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if len(p) == 0 {
			return 0, nil
		}
		var n int
		var ready bool
		err := withFileDescriptor(input, func(fd uintptr) error {
			poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if _, err := unix.Poll(poll, 100); err != nil {
				return err
			}
			if poll[0].Revents == 0 {
				return nil
			}
			if poll[0].Revents&unix.POLLNVAL != 0 {
				return os.ErrClosed
			}
			ready = true
			var readErr error
			n, readErr = unix.Read(int(fd), p)
			return readErr
		})
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil || ready {
			return n, err
		}
	}
}

// Poll checks cancellation without closing or changing the caller's stdin.
func stopInput(*os.File) {}

type descriptorFlags struct {
	file  *os.File
	flags int
}

// A stdout pipe can remain open while its reader stops consuming data. Plain
// File.Write then cannot be interrupted by closing the Telnet socket. Use
// nonblocking writes plus bounded poll so signals and session cancellation also
// interrupt local output. Snapshot both descriptors before changing either:
// stdout and stderr may refer to the same open-file description.
func terminalOutputs(ctx context.Context, stdout, stderr io.Writer) (io.Writer, io.Writer, func() error, error) {
	writers := []io.Writer{stdout, stderr}
	var saved []descriptorFlags
	seen := make(map[uintptr]bool)
	for i, writer := range writers {
		file, ok := writer.(*os.File)
		if !ok {
			continue
		}
		if err := withFileDescriptor(file, func(fd uintptr) error {
			if !seen[fd] {
				flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
				if err != nil {
					return err
				}
				saved = append(saved, descriptorFlags{file: file, flags: flags})
				seen[fd] = true
			}
			return nil
		}); err != nil {
			return nil, nil, nil, err
		}
		writers[i] = &pollWriter{ctx: ctx, file: file}
	}
	restore := func() error {
		var first error
		for i := len(saved) - 1; i >= 0; i-- {
			entry := saved[i]
			err := withFileDescriptor(entry.file, func(fd uintptr) error {
				_, err := unix.FcntlInt(fd, unix.F_SETFL, entry.flags)
				return err
			})
			if err != nil && first == nil {
				first = err
			}
		}
		return first
	}
	for _, entry := range saved {
		err := withFileDescriptor(entry.file, func(fd uintptr) error {
			_, err := unix.FcntlInt(fd, unix.F_SETFL, entry.flags|unix.O_NONBLOCK)
			return err
		})
		if err != nil {
			_ = restore()
			return nil, nil, nil, err
		}
	}
	return writers[0], writers[1], restore, nil
}

type pollWriter struct {
	ctx  context.Context
	file *os.File
}

func (w *pollWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		if err := w.ctx.Err(); err != nil {
			return total, err
		}
		var n int
		err := withFileDescriptor(w.file, func(fd uintptr) error {
			var writeErr error
			n, writeErr = unix.Write(int(fd), p)
			return writeErr
		})
		if n > 0 {
			total += n
			p = p[n:]
		}
		if err == unix.EINTR {
			continue
		}
		if err != nil && err != unix.EAGAIN && err != unix.EWOULDBLOCK {
			return total, err
		}
		if err == nil {
			if n == 0 {
				return total, io.ErrShortWrite
			}
			continue
		}
		err = withFileDescriptor(w.file, func(fd uintptr) error {
			_, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLOUT}}, 100)
			return err
		})
		if err != nil && err != unix.EINTR {
			return total, err
		}
	}
	return total, nil
}

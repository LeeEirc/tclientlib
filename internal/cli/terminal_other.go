//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package cli

import (
	"context"
	"io"
	"os"
	"time"
)

func supportsTerminal() bool { return false }

func interruptSignals() []os.Signal { return []os.Signal{os.Interrupt} }

func watchResize() (<-chan os.Signal, func()) { return nil, func() {} }

// Other platforms use the runtime's descriptor cancellation on Close. Unix
// uses poll instead, allowing it to leave the caller's stdin descriptor open.
func readInput(ctx context.Context, input *os.File, p []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return input.Read(p)
}

func stopInput(input *os.File) {
	_ = input.SetReadDeadline(time.Now())
	_ = input.Close()
}

func terminalOutputs(_ context.Context, stdout, stderr io.Writer) (io.Writer, io.Writer, func() error, error) {
	return stdout, stderr, func() error { return nil }, nil
}

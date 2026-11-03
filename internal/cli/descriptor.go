package cli

import (
	"os"

	"golang.org/x/term"
)

// File.Fd can switch a runtime-managed Unix descriptor to blocking mode.
// Control leaves that state unchanged and keeps the descriptor valid until the
// operation returns, even if another goroutine closes the file concurrently.
// The descriptor must not be retained for later I/O outside this callback.
func withFileDescriptor(file *os.File, operation func(uintptr) error) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	var operationErr error
	if err := raw.Control(func(fd uintptr) { operationErr = operation(fd) }); err != nil {
		return err
	}
	return operationErr
}

func fileIsTerminal(file *os.File) (terminal bool, err error) {
	err = withFileDescriptor(file, func(fd uintptr) error {
		terminal = term.IsTerminal(int(fd))
		return nil
	})
	return terminal, err
}

func fileTerminalSize(file *os.File) (width, height int, err error) {
	err = withFileDescriptor(file, func(fd uintptr) error {
		var sizeErr error
		width, height, sizeErr = term.GetSize(int(fd))
		return sizeErr
	})
	return width, height, err
}

func fileMakeRaw(file *os.File) (state *term.State, err error) {
	err = withFileDescriptor(file, func(fd uintptr) error {
		var rawErr error
		state, rawErr = term.MakeRaw(int(fd))
		return rawErr
	})
	return state, err
}

func fileRestoreTerminal(file *os.File, state *term.State) error {
	return withFileDescriptor(file, func(fd uintptr) error { return term.Restore(int(fd), state) })
}

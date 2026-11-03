package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"reflect"
	"sync"
	"time"

	"github.com/LeeEirc/tclientlib"
	"golang.org/x/text/transform"
)

// Run runs tclient and returns an exit code after restoring the local terminal.
// Codes are 0 for normal completion, 1 for connection or I/O errors, 2 for
// invalid arguments, and 3 for automatic-login failures. It does not exit the
// process, so example commands can share the same implementation.
func Run(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	o, err := parseOptions(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "tclient:", err)
		return 2
	}
	isTTY, err := fileIsTerminal(stdin)
	if err != nil {
		fmt.Fprintln(stderr, "tclient: inspect stdin:", err)
		return 1
	}
	if o.askPass && !isTTY {
		fmt.Fprintln(stderr, "tclient: --ask-pass requires a terminal on stdin")
		return 2
	}
	if isTTY && !supportsTerminal() {
		fmt.Fprintln(stderr, "tclient: interactive terminals are currently supported on Unix; redirected input remains available")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), interruptSignals()...)
	defer stop()
	err = run(ctx, o, stdin, stdout, stderr)
	if err == nil {
		return 0
	}
	// A full diagnostic pipe must not undo cancellation after the terminal has
	// already been restored. Reporting uses the platform's cancellable outputs.
	if ctx.Err() == nil {
		diagnosticCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		_, diagnostics, restore, outputErr := terminalOutputs(diagnosticCtx, io.Discard, stderr)
		if outputErr == nil {
			fmt.Fprintln(diagnostics, "tclient:", err)
			_ = restore()
		}
		cancel()
	}
	if errors.Is(err, tclientlib.ErrLoginFailed) || errors.Is(err, tclientlib.ErrPasswordChangeRequired) || errors.Is(err, tclientlib.ErrLoginTimeout) {
		return 3
	}
	return 1
}

func run(ctx context.Context, o options, stdin *os.File, stdout, stderr io.Writer) (retErr error) {
	// File streams are independently cancellable and must not share a lock:
	// a full stdout pipe must not prevent a local quit command on stderr.
	sharedOutput := sameMemoryWriter(stdout, stderr)
	outputCtx, cancelOutput := context.WithCancel(ctx)
	defer cancelOutput()
	stdout, stderr, restoreOutput, err := terminalOutputs(outputCtx, stdout, stderr)
	if err != nil {
		return fmt.Errorf("prepare output: %w", err)
	}
	defer func() {
		if err := restoreOutput(); retErr == nil {
			retErr = err
		}
	}()
	outputMu, localMu := new(sync.Mutex), new(sync.Mutex)
	if sharedOutput {
		localMu = outputMu
	}
	stdout = &lockedWriter{mu: outputMu, writer: stdout}
	stderr = &lockedWriter{mu: localMu, writer: stderr}
	isTTY, err := fileIsTerminal(stdin)
	if err != nil {
		return fmt.Errorf("inspect stdin: %w", err)
	}
	password := ""
	passwordCR := false
	if o.askPass {
		var err error
		password, passwordCR, err = readPassword(ctx, stdin, stderr)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		if password == "" && o.user == "" {
			return fmt.Errorf("automatic login requires a username or a nonempty password")
		}
	}
	var trace io.Writer
	if o.traceFile != "" {
		file, err := openTrace(o.traceFile)
		if err != nil {
			return fmt.Errorf("create trace: %w", err)
		}
		defer file.Close()
		trace = file
		if _, err := fmt.Fprintln(stderr, "Warning: trace may record manually entered passwords, commands, and remote output:", o.traceFile); err != nil {
			return err
		}
	}
	conn, err := (&net.Dialer{Timeout: o.timeout}).DialContext(ctx, "tcp", o.address)
	if err != nil {
		return err
	}
	defer conn.Close()
	// Automatic login can be blocked in Read/Write. Closing the connection on
	// cancellation interrupts both directions, including before the CLI is raw.
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-watchDone:
		}
	}()
	lineEnding, ending, _ := o.ending()
	config := &tclientlib.ClientConfig{
		Timeout: o.timeout, TerminalType: o.terminal, LineEnding: lineEnding, Trace: trace,
	}
	if o.autoLogin {
		config.User, config.Password = o.user, password
	}
	if isTTY {
		if width, height, err := fileTerminalSize(stdin); err == nil {
			config.Width, config.Height = width, height
		}
	}
	flush := func() error { return nil }
	if encoding, _ := o.encoding(); encoding != nil {
		config.WrapStream = func(reader io.Reader, writer io.Writer) (io.Reader, io.Writer) {
			encoded := &closingWriter{writer: transform.NewWriter(writer, encoding.NewEncoder())}
			flush = encoded.Close
			return transform.NewReader(reader, encoding.NewDecoder()), encoded
		}
	}
	client, err := tclientlib.NewClientConn(conn, config)
	if err != nil {
		return err
	}
	defer client.Close()
	var terminal *terminalInput
	if isTTY {
		state, err := fileMakeRaw(stdin)
		if err != nil {
			return fmt.Errorf("enter terminal raw mode: %w", err)
		}
		defer func() {
			if err := fileRestoreTerminal(stdin, state); retErr == nil {
				retErr = err
			}
		}()
		terminal = &terminalInput{client: client, local: stderr, ending: ending, echo: o.echo, afterCR: passwordCR}
	}
	return runSession(ctx, o, stdin, stdout, conn, client, terminal, flush, cancelOutput)
}

func runSession(ctx context.Context, o options, stdin *os.File, output io.Writer, conn net.Conn, client *tclientlib.Client, terminal *terminalInput, flush func() error, cancelOutput context.CancelFunc) error {
	sessionCtx, cancel := context.WithCancel(ctx)
	finishInput := func() error {
		if err := flush(); err != nil {
			return fmt.Errorf("flush input: %w", err)
		}
		if o.closeWrite {
			halfCloser, ok := conn.(interface{ CloseWrite() error })
			if !ok {
				return fmt.Errorf("connection does not support closing the input direction")
			}
			if err := halfCloser.CloseWrite(); err != nil {
				return fmt.Errorf("close input direction: %w", err)
			}
		}
		return nil
	}
	var sender *inputSender
	var sendResult <-chan error
	if terminal != nil {
		sender = newInputSender(sessionCtx, client, finishInput)
		terminal.client = sender
		sendResult = sender.result
	}
	var sizes <-chan terminalSize
	if terminal != nil {
		var stop func()
		sizes, stop = watchTerminalSize(sessionCtx, stdin)
		defer stop()
	}
	resizeErrors, resizeStopped := startResizeWorker(sessionCtx, sizes, client.WindowChange)
	sent := make(chan error, 1)
	received := make(chan error, 1)
	inputStopped := make(chan struct{})
	outputStopped := make(chan struct{})
	go func() {
		defer close(inputStopped)
		sent <- copyInput(sessionCtx, stdin, client, terminal)
	}()
	go func() {
		defer close(outputStopped)
		_, err := io.Copy(output, client)
		received <- err
	}()
	defer func() {
		cancel()
		cancelOutput()
		// Close the socket before waiting or flushing a charset writer: either
		// input or negotiation may currently be blocked writing to the peer.
		_ = conn.Close()
		stopInput(stdin)
		if sender != nil {
			<-sender.stopped
		}
		<-resizeStopped
		<-inputStopped
		<-outputStopped
	}()
	var timer *time.Timer
	var drained <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-received:
			if err != nil {
				return fmt.Errorf("read remote output: %w", err)
			}
			return nil
		case err := <-sent:
			if errors.Is(err, errQuit) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("send input: %w", err)
			}
			// Keep the socket's write side available for negotiation while
			// draining. Some endpoints require TCP EOF to produce output; the
			// explicit close-write option trades later Telnet replies for that.
			deadline := time.Now().Add(o.drainTimeout)
			if err := conn.SetDeadline(deadline); err != nil {
				return fmt.Errorf("set EOF drain deadline: %w", err)
			}
			sent = nil
			timer = time.NewTimer(time.Until(deadline))
			drained = timer.C
			if sender != nil {
				sender.finishInput()
			} else if err := finishInput(); err != nil {
				return err
			}
		case err := <-sendResult:
			if err != nil {
				return fmt.Errorf("send input: %w", err)
			}
			sendResult = nil
		case <-drained:
			return fmt.Errorf("remote output did not finish within %s after stdin EOF", o.drainTimeout)
		case err := <-resizeErrors:
			return fmt.Errorf("resize terminal: %w", err)
		}
	}
}

func openTrace(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
}

type lockedWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func sameMemoryWriter(a, b io.Writer) bool {
	if _, ok := a.(*os.File); ok {
		return false
	}
	typ := reflect.TypeOf(a)
	return typ != nil && typ == reflect.TypeOf(b) && typ.Comparable() && a == b
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

// closingWriter lets stdin EOF flush a transform.Writer before TCP CloseWrite;
// the library's later Client.Close must not flush the transformer a second time.
type closingWriter struct {
	mu     sync.Mutex
	writer *transform.Writer
	closed bool
	err    error
}

func (w *closingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	return w.writer.Write(p)
}

func (w *closingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.closed {
		w.closed = true
		w.err = w.writer.Close()
	}
	return w.err
}

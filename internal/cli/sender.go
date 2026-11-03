package cli

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/LeeEirc/tclientlib"
)

const (
	maxPendingInputBytes = 256 * 1024
	maxInputChunk        = 4096
)

var errInputBacklog = errors.New("pending terminal input exceeded 256 KiB; closing connection (queued input may not have reached the peer)")

type sendOperation struct {
	data    []byte
	command tclientlib.Command
}

func (op sendOperation) size() int {
	if op.command != 0 {
		return 2 // IAC and the standalone command.
	}
	return len(op.data)
}

// inputSender accepts terminal input without waiting for network writes. A
// single worker preserves the order of application data and Telnet commands.
// Its byte budget includes the current write as well as queued operations.
// Saturation is an explicit session error: never block local quit parsing or
// silently drop characters and continue with an incomplete command stream.
type inputSender struct {
	ctx    context.Context
	client interactiveClient
	finish func() error

	mu           sync.Mutex
	queue        []sendOperation
	pendingBytes int
	sealed       bool
	failure      error
	ready        chan struct{}
	result       chan error
	stopped      chan struct{}
}

func newInputSender(ctx context.Context, client interactiveClient, finish func() error) *inputSender {
	s := &inputSender{
		ctx: ctx, client: client, finish: finish,
		ready: make(chan struct{}, 1), result: make(chan error, 1), stopped: make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *inputSender) TerminalMode() tclientlib.TerminalMode { return s.client.TerminalMode() }

func (s *inputSender) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > maxInputChunk {
			n = maxInputChunk
		}
		if err := s.enqueue(sendOperation{data: p[:n]}); err != nil {
			return total, err
		}
		total += n
		p = p[n:]
	}
	return total, nil
}

func (s *inputSender) SendCommand(command tclientlib.Command) error {
	return s.enqueue(sendOperation{command: command})
}

func (s *inputSender) enqueue(op sendOperation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.failure != nil {
		return s.failure
	}
	if s.sealed {
		return io.ErrClosedPipe
	}
	if op.size() > maxPendingInputBytes-s.pendingBytes {
		return errInputBacklog
	}
	// Adjacent keystrokes can share a pending data chunk. Never append to the
	// in-flight operation, or coalesce across a Telnet command boundary.
	last := len(s.queue) - 1
	if op.command == 0 && last >= 0 && s.queue[last].command == 0 && len(s.queue[last].data)+len(op.data) <= maxInputChunk {
		s.queue[last].data = append(s.queue[last].data, op.data...)
	} else {
		op.data = append([]byte(nil), op.data...)
		s.queue = append(s.queue, op)
	}
	s.pendingBytes += op.size()
	s.wake()
	return nil
}

func (s *inputSender) wake() {
	select {
	case s.ready <- struct{}{}:
	default:
	}
}

// Called only after the input reader reports EOF and the main loop sets the
// drain deadline. The worker flushes/half-closes after every accepted operation.
func (s *inputSender) finishInput() {
	s.mu.Lock()
	s.sealed = true
	s.mu.Unlock()
	s.wake()
}

func (s *inputSender) run() {
	defer close(s.stopped)
	for {
		if s.ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if len(s.queue) == 0 {
			sealed := s.sealed
			s.mu.Unlock()
			if sealed {
				if s.ctx.Err() == nil {
					s.complete(s.finish())
				}
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-s.ready:
			}
			continue
		}
		op := s.queue[0]
		s.queue[0] = sendOperation{}
		s.queue = s.queue[1:]
		if len(s.queue) == 0 {
			s.queue = nil
		}
		s.mu.Unlock()
		// Quit cancels pending work. Closing the connection releases any write
		// already in flight; do not start another queued operation afterward.
		if s.ctx.Err() != nil {
			return
		}
		var err error
		if op.command != 0 {
			err = s.client.SendCommand(op.command)
		} else {
			err = writeFull(s.client, op.data)
		}
		if err != nil {
			s.complete(err)
			return
		}
		s.mu.Lock()
		s.pendingBytes -= op.size()
		s.mu.Unlock()
	}
}

func (s *inputSender) complete(err error) {
	s.mu.Lock()
	s.failure = err
	s.mu.Unlock()
	s.result <- err
}

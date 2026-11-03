package cli

import (
	"context"
	"os"
)

type terminalSize struct {
	height int
	width  int
}

// watchTerminalSize subscribes before taking its first snapshot. Login may
// have taken long enough for the size captured in ClientConfig to be stale;
// refreshing here also closes the gap before the signal subscription began.
func watchTerminalSize(ctx context.Context, file *os.File) (<-chan terminalSize, func()) {
	signals, stopSignals := watchResize()
	watchCtx, cancel := context.WithCancel(ctx)
	updates := make(chan terminalSize, 1)
	stopped := make(chan struct{})
	refresh := func() {
		width, height, err := fileTerminalSize(file)
		if err == nil && width > 0 && height > 0 {
			queueLatestSize(updates, terminalSize{height: height, width: width})
		}
	}
	refresh()
	go func() {
		defer close(stopped)
		defer close(updates)
		defer stopSignals()
		for {
			select {
			case <-watchCtx.Done():
				return
			case _, ok := <-signals:
				if !ok {
					return
				}
				refresh()
			}
		}
	}()
	return updates, func() {
		cancel()
		<-stopped
	}
}

// Only the size watcher publishes to this one-element queue. A stalled network
// write retains the most recent pending size instead of building a backlog.
func queueLatestSize(updates chan terminalSize, size terminalSize) {
	select {
	case updates <- size:
		return
	default:
	}
	select {
	case <-updates:
	default:
	}
	select {
	case updates <- size:
	default:
	}
}

// A single worker serializes NAWS writes without occupying the session's
// control loop. Cancellation stops new work; closing the connection releases
// an in-flight write (or a wait for the transport's write mutex).
func startResizeWorker(ctx context.Context, updates <-chan terminalSize, send func(height, width int) error) (<-chan error, <-chan struct{}) {
	stopped := make(chan struct{})
	if updates == nil {
		close(stopped)
		return nil, stopped
	}
	failures := make(chan error, 1)
	go func() {
		defer close(stopped)
		for {
			select {
			case <-ctx.Done():
				return
			case size, ok := <-updates:
				if !ok || ctx.Err() != nil {
					return
				}
				if err := send(size.height, size.width); err != nil {
					select {
					case failures <- err:
					case <-ctx.Done():
					}
					return
				}
			}
		}
	}()
	return failures, stopped
}

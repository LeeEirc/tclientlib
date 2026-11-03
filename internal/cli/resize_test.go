package cli

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResizeWorkerCoalescesPendingSizes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan terminalSize, 1)
	started := make(chan terminalSize, 2)
	release := make(chan struct{})
	var calls []terminalSize
	send := func(height, width int) error {
		size := terminalSize{height: height, width: width}
		calls = append(calls, size)
		started <- size
		if len(calls) == 1 {
			<-release
		}
		return nil
	}
	failures, stopped := startResizeWorker(ctx, updates, send)
	first := terminalSize{height: 24, width: 80}
	queueLatestSize(updates, first)
	select {
	case size := <-started:
		if size != first {
			t.Fatalf("first size = %+v", size)
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("first resize did not start")
	}
	// A blocked write must not block the watcher or accumulate every obsolete
	// size. Once it finishes, the next write must contain the final dimensions.
	for i := 1; i <= 100; i++ {
		queueLatestSize(updates, terminalSize{height: 24 + i, width: 80 + i})
	}
	close(release)
	wantLast := terminalSize{height: 124, width: 180}
	select {
	case size := <-started:
		if size != wantLast {
			t.Fatalf("pending size = %+v, want %+v", size, wantLast)
		}
	case <-time.After(time.Second):
		t.Fatal("last resize did not start")
	}
	cancel()
	<-stopped
	if len(calls) != 2 {
		t.Fatalf("sent %d resizes, want first and latest only", len(calls))
	}
	select {
	case err := <-failures:
		t.Fatalf("unexpected resize error: %v", err)
	default:
	}
}

func TestResizeWorkerReportsWriteError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan terminalSize, 1)
	updates <- terminalSize{height: 24, width: 80}
	want := errors.New("resize write failed")
	failures, stopped := startResizeWorker(ctx, updates, func(int, int) error { return want })
	select {
	case err := <-failures:
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("resize error was not delivered")
	}
	<-stopped
}

func TestResizeWorkerDoesNotSendQueuedSizeAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan terminalSize, 1)
	updates <- terminalSize{height: 24, width: 80}
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	_, stopped := startResizeWorker(ctx, updates, func(int, int) error {
		calls++
		if calls == 1 {
			close(started)
			<-release
		}
		return nil
	})
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("resize did not start")
	}
	queueLatestSize(updates, terminalSize{height: 35, width: 110})
	cancel()
	close(release)
	<-stopped
	if calls != 1 {
		t.Fatalf("sent a queued resize after cancellation: %d calls", calls)
	}
}

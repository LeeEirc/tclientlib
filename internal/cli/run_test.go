package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/LeeEirc/tclientlib"
	"golang.org/x/text/encoding/simplifiedchinese"
)

func TestOptionsRejectAmbiguousCredentials(t *testing.T) {
	for _, args := range [][]string{
		{"--user", "admin", "localhost"}, {"--ask-pass", "localhost"},
		{"--auto-login", "localhost"}, {"--password", "secret", "localhost"},
		{"--line-ending", "bogus", "localhost"}, {"--charset", "bogus", "localhost"},
		{"--echo", "bogus", "localhost"}, {"--timeout", "0", "localhost"},
		{"--drain-timeout", "-1s", "localhost"}, {"localhost", "65536"},
	} {
		if _, err := parseOptions(args, io.Discard); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	for _, host := range []string{"::1", "[::1]"} {
		o, err := parseOptions([]string{host, "2323"}, io.Discard)
		if err != nil || o.address != "[::1]:2323" {
			t.Fatalf("host %q: %s, %v", host, o.address, err)
		}
	}
}

func testInput(t *testing.T, data []byte) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { file.Close() })
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return file
}

func testServer(t *testing.T, serve func(net.Conn)) (host, port string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		serve(conn)
	}()
	t.Cleanup(func() {
		listener.Close()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("test server did not stop")
		}
	})
	host, port, err = net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func TestPipedInputPreservesBytesAndDrainsFinalOutput(t *testing.T) {
	payload := []byte("show\r\n\x1dhelp\r\x00\xff")
	wire := make(chan []byte, 1)
	host, port := testServer(t, func(conn net.Conn) {
		data, err := io.ReadAll(conn)
		if err != nil {
			t.Errorf("read stdin: %v", err)
		}
		wire <- data
		_, _ = conn.Write([]byte("final output\r\n"))
	})
	var output, diagnostics bytes.Buffer
	code := Run([]string{"--close-write", "--line-ending", "lf", host, port}, testInput(t, payload), &output, &diagnostics)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
	if got, want := <-wire, append(append([]byte(nil), payload...), 255); !bytes.Equal(got, want) {
		t.Fatalf("wire got %q, want %q", got, want)
	}
	if got, want := output.String(), "final output\r\n"; got != want {
		t.Fatalf("output got %q, want %q", got, want)
	}
}

func TestCharsetRoundTripAndFlushAtEOF(t *testing.T) {
	text := "你好\r\n"
	encoded, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	wire := make(chan []byte, 1)
	host, port := testServer(t, func(conn net.Conn) {
		data, _ := io.ReadAll(conn)
		wire <- data
		_, _ = conn.Write(encoded)
	})
	var output, diagnostics bytes.Buffer
	if code := Run([]string{"--close-write", "--charset", "gbk", host, port}, testInput(t, []byte(text)), &output, &diagnostics); code != 0 {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
	if got := <-wire; !bytes.Equal(got, encoded) {
		t.Fatalf("wire got %x, want %x", got, encoded)
	}
	if got := output.String(); got != text {
		t.Fatalf("output got %q, want %q", got, text)
	}
}

func TestRemoteEOFCancelsIdleInput(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	host, port := testServer(t, func(conn net.Conn) { _, _ = conn.Write([]byte("bye")) })
	var output, diagnostics bytes.Buffer
	start := time.Now()
	if code := Run([]string{host, port}, input, &output, &diagnostics); code != 0 {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
	if time.Since(start) > time.Second || output.String() != "bye" {
		t.Fatalf("slow or incomplete remote EOF: %s, %q", time.Since(start), output.String())
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		// Ending a session must not leave a goroutine holding the user's stdin.
		if _, err := writer.Write([]byte{'x'}); err != nil {
			t.Fatal(err)
		}
		var one [1]byte
		if n, err := input.Read(one[:]); err != nil || n != 1 || one[0] != 'x' {
			t.Fatalf("stdin unusable after remote EOF: %d %v %q", n, err, one)
		}
	}
}

func TestEOFDrainHasDeadline(t *testing.T) {
	finish := make(chan struct{})
	defer close(finish)
	host, port := testServer(t, func(conn net.Conn) {
		var first [1]byte
		_, _ = conn.Read(first[:])
		<-finish
	})
	var diagnostics bytes.Buffer
	start := time.Now()
	code := Run([]string{"--drain-timeout", "40ms", host, port}, testInput(t, []byte("x")), io.Discard, &diagnostics)
	if code != 1 || time.Since(start) > time.Second {
		t.Fatalf("drain did not time out: code=%d duration=%s diagnostic=%s", code, time.Since(start), diagnostics.String())
	}
}

func TestDefaultEOFStillAnswersNegotiation(t *testing.T) {
	flushed := make(chan struct{})
	response := make(chan []byte, 1)
	host, port := testServer(t, func(conn net.Conn) {
		<-flushed
		_, _ = conn.Write([]byte{255, 251, 1}) // WILL ECHO, after stdin is drained.
		var reply [3]byte
		n, _ := io.ReadFull(conn, reply[:])
		response <- reply[:n]
		_, _ = conn.Write([]byte("final output"))
	})
	o, err := parseOptions([]string{host, port}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", o.address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client, err := tclientlib.NewClientConn(conn, &tclientlib.ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var output bytes.Buffer
	flush := func() error { close(flushed); return nil }
	if err := runSession(context.Background(), o, testInput(t, nil), &output, conn, client, nil, flush, func() {}); err != nil {
		t.Fatal(err)
	}
	if got := <-response; !bytes.Equal(got, []byte{255, 253, 1}) {
		t.Fatalf("negotiation after EOF: %x", got)
	}
	if got := output.String(); got != "final output" {
		t.Fatalf("output after negotiation = %q", got)
	}
}

func TestOutputFailureIsReported(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	host, port := testServer(t, func(conn net.Conn) { _, _ = conn.Write([]byte("hello")) })
	var diagnostics bytes.Buffer
	code := Run([]string{host, port}, input, brokenWriter{errors.New("disk full")}, &diagnostics)
	if code != 1 || !strings.Contains(diagnostics.String(), "disk full") {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
}

func TestAutomaticFailureExitCode(t *testing.T) {
	host, port := testServer(t, func(conn net.Conn) { _, _ = conn.Write([]byte("Login incorrect\r\n")) })
	var diagnostics bytes.Buffer
	code := Run([]string{"--auto-login", "--user", "admin", host, port}, testInput(t, nil), io.Discard, &diagnostics)
	if code != 3 {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
}

func TestRunCancellationUnblocksIdleInputAndRemote(t *testing.T) {
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	connected := make(chan struct{})
	host, port := testServer(t, func(conn net.Conn) {
		close(connected)
		_, _ = io.Copy(io.Discard, conn)
	})
	o, _ := parseOptions([]string{host, port}, io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, o, input, io.Discard, io.Discard) }()
	<-connected
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation was not reported")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop the session")
	}
}

func TestTraceIsPrivateAndNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.trace")
	file, err := openTrace(path)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(file, "keep")
	file.Close()
	if file, err := openTrace(path); err == nil {
		file.Close()
		t.Fatal("existing trace was overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("trace contents: %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("trace permissions = %o", info.Mode().Perm())
	}
}

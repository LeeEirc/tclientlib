package tclientlib

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func assertNoTracePayload(t *testing.T, output string, values ...string) {
	t.Helper()
	for _, value := range values {
		if strings.Contains(output, value) || strings.Contains(output, fmt.Sprintf("% x", []byte(value))) {
			t.Fatalf("trace contains protected payload %q:\n%s", value, output)
		}
	}
}

func TestLoginTraceRedactsSubnegotiationParameters(t *testing.T) {
	var trace bytes.Buffer
	wire := []byte{iac, do, newEnvironment, iac, sb, newEnvironment, 1, iac, se}
	wire = append(wire, (optionPacket{command: sb, option: newEnvironment, parameters: []byte("\x00received-secret")}).bytes()...)
	wire = append(wire, []byte("Login:\r\nPassword:\r\nrouter#")...)
	conn := &streamTestConn{chunks: [][]byte{wire}}
	client, err := NewClientConn(conn, &ClientConfig{User: "private-user", Password: "private-pass", Trace: &trace, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	assertNoTracePayload(t, trace.String(), "private-user", "private-pass", "received-secret")
	for _, want := range []string{"event recv SB NEW_ENVIRON", "packet send SB NEW_ENVIRON", "login success", "<redacted>"} {
		if !strings.Contains(trace.String(), want) {
			t.Fatalf("missing %q:\n%s", want, trace.String())
		}
	}
	trace.Reset()
	if _, err := client.Write([]byte("show")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(trace.String(), "73 68 6f 77") {
		t.Fatalf("post-login trace not restored: %s", trace.String())
	}
}

type traceFlushWriter struct{ io.Writer }

func (w traceFlushWriter) Close() error {
	_, err := w.Write([]byte("cleanup-secret"))
	return err
}

func TestFailedLoginTraceStaysRedactedThroughWrapperClose(t *testing.T) {
	var trace bytes.Buffer
	conn := &streamTestConn{chunks: [][]byte{[]byte("Password:\r\nLogin incorrect\r\n")}}
	_, err := NewClientConn(conn, &ClientConfig{
		Password: "private-pass", Trace: &trace, Timeout: time.Second,
		WrapStream: func(r io.Reader, w io.Writer) (io.Reader, io.Writer) {
			_, _ = w.Write([]byte("setup-secret"))
			return r, traceFlushWriter{w}
		},
	})
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("got %v", err)
	}
	if !conn.closed.Load() {
		t.Fatal("connection not closed")
	}
	assertNoTracePayload(t, trace.String(), "private-pass", "setup-secret", "cleanup-secret")
	if !strings.Contains(conn.writes.String(), "cleanup-secret") {
		t.Fatal("close did not exercise buffered write")
	}
}

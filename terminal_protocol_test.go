package tclientlib

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestNVTReceiveEverySplit(t *testing.T) {
	tests := []struct {
		name string
		wire []byte
		want []byte
	}{
		{"line endings", []byte("a\r\x00b\r\nc\x00d\r\re\r"), []byte("a\rb\r\nc\x00d\r\re\r")},
		{"repeated CR NUL", []byte("\r\r\x00\x00\r\x00"), []byte("\r\r\x00\r")},
		{"escaped IAC", []byte{'\r', iac, iac, 0, '\r', 0, iac, iac}, []byte{'\r', iac, 0, '\r', iac}},
		{"command inside CR NUL", []byte{'\r', iac, nop, 0}, []byte{'\r'}},
		{"negotiation inside CR NUL", []byte{'\r', iac, will, echoOption, 0}, []byte{'\r'}},
		{"subnegotiation inside CR NUL", []byte{'\r', iac, sb, terminalType, 0, '\r', 0, iac, se, 0}, []byte{'\r'}},
		{"outbound binary is independent", []byte{iac, do, binaryOption, '\r', 0}, []byte{'\r'}},
		{"binary preserves all data", []byte{iac, will, binaryOption, '\r', 0, '\r', '\n', 0, iac, iac}, []byte{'\r', 0, '\r', '\n', 0, iac}},
		{"mode changes within read", []byte{'\r', 0, iac, will, binaryOption, '\r', 0, iac, wont, binaryOption, '\r', 0}, []byte{'\r', '\r', 0, '\r'}},
		{"enter binary after CR", []byte{'\r', iac, will, binaryOption, 0}, []byte{'\r', 0}},
		{"leave binary after CR", []byte{iac, will, binaryOption, '\r', iac, wont, binaryOption, 0}, []byte{'\r', 0}},
		{"empty binary span resets CR", []byte{'\r', iac, will, binaryOption, iac, wont, binaryOption, 0}, []byte{'\r', 0}},
		{"duplicate WONT preserves CR", []byte{'\r', iac, wont, binaryOption, 0}, []byte{'\r'}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for split := 0; split <= len(tc.wire); split++ {
				t.Run(fmt.Sprint(split), func(t *testing.T) {
					client, _ := protocolTestClient(t, [][]byte{tc.wire[:split], tc.wire[split:]}, "")
					got, err := io.ReadAll(client)
					if err != nil || !bytes.Equal(got, tc.want) {
						t.Fatalf("read = %v, %v; want %v", got, err, tc.want)
					}
				})
			}
			client, _ := protocolTestClient(t, streamChunks(tc.wire, 1), "")
			got, err := io.ReadAll(client)
			if err != nil || !bytes.Equal(got, tc.want) {
				t.Fatalf("byte reads = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestWriteRetainsApplicationLineEndingsInBothModes(t *testing.T) {
	for _, binary := range []bool{false, true} {
		t.Run(fmt.Sprint(binary), func(t *testing.T) {
			var wire []byte
			if binary {
				wire = []byte{iac, do, binaryOption}
			}
			client, conn := protocolTestClient(t, [][]byte{wire}, "")
			if _, err := io.ReadAll(client); err != nil {
				t.Fatal(err)
			}
			conn.writes.Reset()
			input := []byte{'\r', '\n', '\r', 0, '\r', 'x', '\n', iac}
			for _, value := range input {
				if n, err := client.Write([]byte{value}); err != nil || n != 1 {
					t.Fatalf("write = %d, %v", n, err)
				}
			}
			want := append(append([]byte(nil), input...), iac)
			if !bytes.Equal(conn.writes.Bytes(), want) {
				t.Fatalf("wire = %v, want %v", conn.writes.Bytes(), want)
			}
		})
	}
}

func TestTerminalModeNegotiationDirectionsAndDuplicates(t *testing.T) {
	enable := []byte{iac, will, echoOption, iac, do, suppressGAOption, iac, will, suppressGAOption,
		iac, do, binaryOption, iac, will, binaryOption}
	disable := []byte{iac, wont, echoOption, iac, dont, suppressGAOption, iac, wont, suppressGAOption,
		iac, dont, binaryOption, iac, wont, binaryOption}
	first := append(append(append([]byte(nil), enable...), enable...), 'a')
	second := append(append(append([]byte(nil), disable...), disable...), 'b')
	client, conn := protocolTestClient(t, [][]byte{first, second}, "")
	if got := client.TerminalMode(); got != (TerminalMode{}) {
		t.Fatalf("initial mode = %+v", got)
	}
	p := make([]byte, 1)
	if _, err := io.ReadFull(client, p); err != nil || p[0] != 'a' {
		t.Fatalf("first read = %q, %v", p, err)
	}
	wantMode := TerminalMode{RemoteEcho: true, LocalSuppressGA: true, RemoteSuppressGA: true, SendBinary: true, ReceiveBinary: true}
	if got := client.TerminalMode(); got != wantMode {
		t.Fatalf("enabled mode = %+v, want %+v", got, wantMode)
	}
	wantReplies := []byte{iac, do, echoOption, iac, will, suppressGAOption, iac, do, suppressGAOption,
		iac, will, binaryOption, iac, do, binaryOption}
	if !bytes.Equal(conn.writes.Bytes(), wantReplies) {
		t.Fatalf("enable replies = %v, want %v", conn.writes.Bytes(), wantReplies)
	}
	if _, err := io.ReadFull(client, p); err != nil || p[0] != 'b' {
		t.Fatalf("second read = %q, %v", p, err)
	}
	if got := client.TerminalMode(); got != (TerminalMode{}) {
		t.Fatalf("disabled mode = %+v", got)
	}
	wantReplies = append(wantReplies, iac, dont, echoOption, iac, wont, suppressGAOption, iac, dont, suppressGAOption,
		iac, wont, binaryOption, iac, dont, binaryOption)
	if !bytes.Equal(conn.writes.Bytes(), wantReplies) {
		t.Fatalf("all replies = %v, want %v", conn.writes.Bytes(), wantReplies)
	}
}

func TestTerminalModeConcurrentAccess(t *testing.T) {
	cycle := []byte{iac, will, echoOption, iac, do, suppressGAOption, iac, do, binaryOption,
		iac, will, binaryOption, 'x', iac, wont, echoOption, iac, dont, suppressGAOption,
		iac, dont, binaryOption, iac, wont, binaryOption}
	client, _ := protocolTestClient(t, streamChunks(bytes.Repeat(cycle, 100), 3), "")
	var wg sync.WaitGroup
	errors := make(chan error, 3)
	wg.Add(4)
	go func() {
		defer wg.Done()
		_, err := io.ReadAll(client)
		errors <- err
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = client.TerminalMode()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if _, err := client.Write([]byte("x\r\n")); err != nil {
				errors <- err
				return
			}
		}
		errors <- nil
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := client.WindowChange(24+i, 80+i); err != nil {
				errors <- err
				return
			}
		}
		errors <- nil
	}()
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommandOutputDoesNotWaitForBlockedApplicationWrite(t *testing.T) {
	server, peer := net.Pipe()
	defer server.Close()
	deadline := time.Now().Add(2 * time.Second)
	_ = server.SetDeadline(deadline)
	_ = peer.SetDeadline(deadline)
	conn := &protocolWriteStartedConn{Conn: peer, started: make(chan struct{})}
	client, err := NewClientConn(conn, &ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	written := make(chan error, 1)
	go func() { _, err := client.Write([]byte("input")); written <- err }()
	<-conn.started // The peer is not reading, so application Write holds writeMu.
	served := make(chan error, 1)
	go func() { _, err := server.Write([]byte{iac, nop, 'x'}); served <- err }()
	p := make([]byte, 1)
	if _, err := io.ReadFull(client, p); err != nil || p[0] != 'x' {
		t.Fatalf("NOP unnecessarily blocked output: %q, %v", p, err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(server, make([]byte, len("input"))); err != nil {
		t.Fatal(err)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

type protocolWriteStartedConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *protocolWriteStartedConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func TestConcurrentNAWSKeepsWireModeAndLatestSize(t *testing.T) {
	client, conn := protocolTestClient(t, nil, "")
	transport := client.transport
	if err := transport.handleEvent(telnetEvent{typ: negotiationEvent, command: do, option: windowSize}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan error, 3)
	var wg sync.WaitGroup
	for worker := 0; worker < 2; worker++ {
		worker := worker
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < 200; i++ {
				if err := client.WindowChange(24+worker*200+i, 80+worker*200+i); err != nil {
					errors <- err
					return
				}
				runtime.Gosched()
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 200; i++ {
			for _, command := range []byte{dont, do} {
				if err := transport.handleEvent(telnetEvent{typ: negotiationEvent, command: command, option: windowSize}); err != nil {
					errors <- err
					return
				}
				runtime.Gosched()
			}
		}
	}()
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	data, events, err := newDecoder(defaultMaxSubnegotiation).feed(conn.writes.Bytes())
	if err != nil || len(data) != 0 {
		t.Fatalf("unexpected application bytes or decode error: %v, %v", data, err)
	}
	enabled := false
	width, height := 0, 0
	for _, event := range events {
		switch event.typ {
		case negotiationEvent:
			if event.option != windowSize || event.command != will && event.command != wont {
				t.Fatalf("unexpected negotiation: %+v", event)
			}
			enabled = event.command == will
		case subnegotiationEvent:
			if !enabled || event.option != windowSize || len(event.parameters) != 4 {
				t.Fatalf("NAWS sent outside negotiated mode: %+v", event)
			}
			width = int(binary.BigEndian.Uint16(event.parameters[:2]))
			height = int(binary.BigEndian.Uint16(event.parameters[2:]))
		default:
			t.Fatalf("unexpected event: %+v", event)
		}
	}
	if !enabled || width != transport.config.Width || height != transport.config.Height {
		t.Fatalf("final wire state=%t %dx%d; latest size=%dx%d", enabled, width, height, transport.config.Width, transport.config.Height)
	}
}

func protocolTestClient(t *testing.T, chunks [][]byte, user string) (*Client, *streamTestConn) {
	t.Helper()
	conn := &streamTestConn{chunks: chunks}
	config, err := prepareConfig(&ClientConfig{User: user})
	if err != nil {
		t.Fatal(err)
	}
	// Bypass automatic login to exercise USER negotiation independently of
	// prompts; the same transport is used by NewClientConn.
	transport := newTransport(conn, config.transportConfig())
	client := &Client{conn: conn, transport: transport, reader: transport, writer: transport}
	t.Cleanup(func() { _ = client.Close() })
	return client, conn
}

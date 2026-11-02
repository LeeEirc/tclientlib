package tclientlib

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClientReadHandlesFragmentedNegotiation(t *testing.T) {
	server, conn := net.Pipe()
	client, err := NewClientConn(conn, &ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		if _, err := server.Write(append([]byte("hello"), iac)); err != nil {
			serverErr <- err
			return
		}
		if _, err := server.Write([]byte{will, echoOption}); err != nil {
			serverErr <- err
			return
		}
		reply := make([]byte, 3)
		if _, err := io.ReadFull(server, reply); err != nil {
			serverErr <- err
			return
		}
		if !bytes.Equal(reply, []byte{iac, do, echoOption}) {
			serverErr <- errors.New("unexpected ECHO reply")
			return
		}
		_, err := server.Write([]byte("world"))
		serverErr <- err
	}()

	var output bytes.Buffer
	buf := make([]byte, 2)
	for {
		n, readErr := client.Read(buf)
		output.Write(buf[:n])
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				t.Fatal(readErr)
			}
			break
		}
	}
	if got := output.String(); got != "helloworld" {
		t.Fatalf("got %q", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestWriteEscapesIAC(t *testing.T) {
	server, conn := net.Pipe()
	client, err := NewClientConn(conn, &ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		_, err := client.Write([]byte{'a', iac, 'b'})
		done <- err
	}()

	got := make([]byte, 4)
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatal(err)
	}
	want := []byte{'a', iac, iac, 'b'}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSendCommandBypassesApplicationStream(t *testing.T) {
	server, conn := net.Pipe()
	client, err := NewClientConn(conn, &ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() { done <- client.SendCommand(CommandBreak) }()
	got := make([]byte, 2)
	if _, err := io.ReadFull(server, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{iac, brk}) {
		t.Fatalf("got %v", got)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWindowChangeMatchesSSHArgumentOrder(t *testing.T) {
	server, conn := net.Pipe()
	client, err := NewClientConn(conn, &ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if err := client.WindowChange(511, 255); err != nil {
		t.Fatal(err)
	}

	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		if _, err := server.Write([]byte{iac, do, windowSize}); err != nil {
			serverErr <- err
			return
		}
		want := append([]byte{iac, will, windowSize}, optionPacket{
			command:    sb,
			option:     windowSize,
			parameters: []byte{0, 255, 1, 255}, // width=255, height=511
		}.bytes()...)
		got := make([]byte, len(want))
		if _, err := io.ReadFull(server, got); err != nil {
			serverErr <- err
			return
		}
		if !bytes.Equal(got, want) {
			serverErr <- errors.New("unexpected NAWS response")
			return
		}
		_, err := server.Write([]byte("x"))
		serverErr <- err
	}()

	buf := make([]byte, 1)
	if _, err := io.ReadFull(client, buf); err != nil {
		t.Fatal(err)
	}
	if buf[0] != 'x' {
		t.Fatalf("got %q", buf)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestLoginHandlesFragmentedPrompts(t *testing.T) {
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		for _, part := range []string{"Log", "in:"} {
			if _, err := server.Write([]byte(part)); err != nil {
				serverErr <- err
				return
			}
		}
		user := make([]byte, len("admin")+2)
		if _, err := io.ReadFull(server, user); err != nil {
			serverErr <- err
			return
		}
		if string(user) != "admin\r\x00" {
			serverErr <- errors.New("unexpected username")
			return
		}
		if _, err := server.Write([]byte("Pass")); err != nil {
			serverErr <- err
			return
		}
		if _, err := server.Write([]byte("word:")); err != nil {
			serverErr <- err
			return
		}
		password := make([]byte, len("secret")+2)
		if _, err := io.ReadFull(server, password); err != nil {
			serverErr <- err
			return
		}
		if string(password) != "secret\r\x00" {
			serverErr <- errors.New("unexpected password")
			return
		}
		_, err := server.Write([]byte("\r\nrouter#"))
		serverErr <- err
	}()

	client, err := NewClientConn(conn, &ClientConfig{
		User: "admin", Password: "secret", Timeout: time.Second,
		LineEnding: LineEndingCRNUL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestPasswordOnlyLogin(t *testing.T) {
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		if _, err := server.Write([]byte("Password:")); err != nil {
			serverErr <- err
			return
		}
		password := make([]byte, len("secret")+2)
		if _, err := io.ReadFull(server, password); err != nil {
			serverErr <- err
			return
		}
		if string(password) != "secret\r\x00" {
			serverErr <- errors.New("unexpected password")
			return
		}
		_, err := server.Write([]byte("\r\nswitch>"))
		serverErr <- err
	}()

	client, err := NewClientConn(conn, &ClientConfig{
		Password: "secret", Timeout: time.Second, LineEnding: LineEndingCRNUL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestLoginTimeout(t *testing.T) {
	server, conn := net.Pipe()
	defer server.Close()
	_, err := NewClientConn(conn, &ClientConfig{Password: "secret", Timeout: 20 * time.Millisecond})
	if !errors.Is(err, ErrLoginTimeout) {
		t.Fatalf("got %v, want ErrLoginTimeout", err)
	}
}

func TestWrapStreamIsTheSingleLoginAndSessionPath(t *testing.T) {
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		if _, err := server.Write([]byte("LOGIN:")); err != nil {
			serverErr <- err
			return
		}
		user := make([]byte, len("ADMIN")+2)
		if _, err := io.ReadFull(server, user); err != nil {
			serverErr <- err
			return
		}
		if string(user) != "ADMIN\r\x00" {
			serverErr <- errors.New("wrapper did not encode username")
			return
		}
		if _, err := server.Write([]byte("\r\nDEVICE#")); err != nil {
			serverErr <- err
			return
		}
		input := make([]byte, 4)
		if _, err := io.ReadFull(server, input); err != nil {
			serverErr <- err
			return
		}
		if string(input) != "SHOW" {
			serverErr <- errors.New("wrapper did not encode session input")
			return
		}
		serverErr <- nil
	}()

	client, err := NewClientConn(conn, &ClientConfig{
		User: "admin", Timeout: time.Second, LineEnding: LineEndingCRNUL,
		WrapStream: func(reader io.Reader, writer io.Writer) (io.Reader, io.Writer) {
			return lowerReader{reader}, upperWriter{writer}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("show")); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

type lowerReader struct{ io.Reader }

func (r lowerReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	copy(p[:n], strings.ToLower(string(p[:n])))
	return n, err
}

type upperWriter struct{ io.Writer }

func (w upperWriter) Write(p []byte) (int, error) {
	return w.Writer.Write([]byte(strings.ToUpper(string(p))))
}

func TestUnsupportedRemoteOptionIsRejected(t *testing.T) {
	server, conn := net.Pipe()
	client, err := NewClientConn(conn, &ClientConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		const authenticationOption byte = 37
		if _, err := server.Write([]byte{iac, will, authenticationOption}); err != nil {
			serverErr <- err
			return
		}
		got := make([]byte, 3)
		if _, err := io.ReadFull(server, got); err != nil {
			serverErr <- err
			return
		}
		if !bytes.Equal(got, []byte{iac, dont, authenticationOption}) {
			serverErr <- errors.New("unsupported option was not rejected")
			return
		}
		_, err := server.Write([]byte("ok"))
		serverErr <- err
	}()

	got := make([]byte, 2)
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "ok" {
		t.Fatalf("got %q", got)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

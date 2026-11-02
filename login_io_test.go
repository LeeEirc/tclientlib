package tclientlib

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestAutomaticLineEndingVariants(t *testing.T) {
	cases := []struct {
		name   string
		ending LineEnding
		want   []byte
	}{
		{name: "default-crlf", ending: LineEndingCRLF, want: []byte{'\r', '\n'}},
		{name: "crnul", ending: LineEndingCRNUL, want: []byte{'\r', 0}},
		{name: "cr", ending: LineEndingCR, want: []byte{'\r'}},
		{name: "lf", ending: LineEndingLF, want: []byte{'\n'}},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			config, err := prepareConfig(&ClientConfig{
				User: "admin", Password: "secret", LineEnding: testCase.ending,
			})
			if err != nil {
				t.Fatal(err)
			}
			machine := newLoginMachine(config)
			var output bytes.Buffer

			result, err := machine.handleLine(&output, []byte("Login:"), nil)
			if err != nil {
				t.Fatal(err)
			}
			if result != loginCredentialSent {
				t.Fatalf("username result=%v", result)
			}
			wantCredential := append([]byte("admin"), testCase.want...)
			if !bytes.Equal(output.Bytes(), wantCredential) {
				t.Fatalf("username output=%v, want=%v", output.Bytes(), wantCredential)
			}

			output.Reset()
			result, err = machine.handleLine(&output, []byte("Press ENTER to continue"), nil)
			if err != nil {
				t.Fatal(err)
			}
			if result != loginContinueSent {
				t.Fatalf("continue result=%v", result)
			}
			if !bytes.Equal(output.Bytes(), testCase.want) {
				t.Fatalf("continue output=%v, want=%v", output.Bytes(), testCase.want)
			}
		})
	}
}

func TestInvalidLineEndingIsRejected(t *testing.T) {
	_, err := prepareConfig(&ClientConfig{LineEnding: LineEnding(255)})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("got %v, want ErrInvalidConfig", err)
	}
}

func TestPostLoginBufferedDataIsPreserved(t *testing.T) {
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(time.Second))
		if err := writeFull(server, []byte("Password:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("secret\r\n")); err != nil {
			serverErr <- err
			return
		}
		serverErr <- writeFull(server, []byte("router#\r\nALARM: fan speed low\r\n"))
	}()

	client, err := NewClientConn(conn, &ClientConfig{
		Password: "secret", Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}

	want := []byte("\r\nALARM: fan speed low\r\n")
	got := make([]byte, len(want))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

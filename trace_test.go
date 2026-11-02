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

func TestTraceRecordsWireAndNegotiation(t *testing.T) {
	server, conn := net.Pipe()
	var trace bytes.Buffer
	client, err := NewClientConn(conn, &ClientConfig{Trace: &trace})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		if _, err := server.Write([]byte{iac, will, echoOption, 'o', 'k'}); err != nil {
			serverErr <- err
			return
		}
		reply := make([]byte, 3)
		if _, err := io.ReadFull(server, reply); err != nil {
			serverErr <- err
			return
		}
		if !bytes.Equal(reply, []byte{iac, do, echoOption}) {
			serverErr <- errors.New("unexpected negotiation reply")
			return
		}
		serverErr <- nil
	}()

	data := make([]byte, 2)
	if _, err := io.ReadFull(client, data); err != nil {
		t.Fatal(err)
	}
	if string(data) != "ok" {
		t.Fatalf("got %q", data)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}

	output := trace.String()
	for _, expected := range []string{
		"wire recv",
		"event recv WILL ECHO",
		"packet send DO ECHO",
		"wire send",
		"application recv",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("trace does not contain %q:\n%s", expected, output)
		}
	}
}

func TestTraceRecordsLoginState(t *testing.T) {
	server, conn := net.Pipe()
	var trace bytes.Buffer
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
		Password: "secret",
		Timeout:  time.Second,
		Trace:    &trace,
		LineEnding: LineEndingCRNUL,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}

	output := trace.String()
	for _, expected := range []string{
		"login start user=false password=true",
		"login password prompt matched",
		"login success",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("trace does not contain %q:\n%s", expected, output)
		}
	}
}

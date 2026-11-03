package tclientlib

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestLoginStatisticsAreIgnoredForEverySplit(t *testing.T) {
	payload := []byte("\r\nLast login failed: 3 times\r\n" +
		"Failed login attempts since last successful login: 5\r\n" +
		"登录成功次数: 10，登录失败次数: 2\r\n" +
		"router#")

	for split := 0; split <= len(payload); split++ {
		t.Run(fmt.Sprintf("split-%d", split), func(t *testing.T) {
			err, serverErr := runStandardLogin(t, payload[:split], payload[split:])
			if err != nil {
				t.Fatalf("split %d: login failed: %v", split, err)
			}
			if serverErr != nil {
				t.Fatalf("split %d: server failed: %v", split, serverErr)
			}
		})
	}
}

func TestExplicitAuthenticationFailure(t *testing.T) {
	err, _ := runStandardLogin(t, []byte("\r\nLogin incorrect\r\n"))
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("got %v, want ErrLoginFailed", err)
	}
}

func TestPasswordChangeRequired(t *testing.T) {
	cases := []string{
		"WARNING: Your password has expired.",
		"Current UNIX password:",
		"New password:",
		"Retype new password:",
		"密码已过期",
		"必须修改密码",
	}
	for _, message := range cases {
		t.Run(message, func(t *testing.T) {
			err, _ := runStandardLogin(t, []byte("\r\n"+message))
			if !errors.Is(err, ErrPasswordChangeRequired) {
				t.Fatalf("got %v, want ErrPasswordChangeRequired", err)
			}
		})
	}
}

func TestNewPasswordPromptDoesNotReceiveManagedPassword(t *testing.T) {
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(time.Second))
		if err := writeFull(server, []byte("Login:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("admin\r\x00")); err != nil {
			serverErr <- err
			return
		}
		if err := writeFull(server, []byte("Password:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("secret\r\x00")); err != nil {
			serverErr <- err
			return
		}
		if err := writeFull(server, []byte("New password:")); err != nil {
			serverErr <- err
			return
		}

		buffer := make([]byte, 64)
		n, readErr := server.Read(buffer)
		if n > 0 {
			serverErr <- errors.New("managed password was sent to the new-password prompt")
			return
		}
		if readErr == nil {
			serverErr <- errors.New("expected client connection to close")
			return
		}
		serverErr <- nil
	}()

	_, err := NewClientConn(conn, &ClientConfig{
		User: "admin", Password: "secret", Timeout: 250 * time.Millisecond,
		LineEnding: LineEndingCRNUL,
	})
	if !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("got %v, want ErrPasswordChangeRequired", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestPasswordExpiryWarningIsOnlyABanner(t *testing.T) {
	err, serverErr := runStandardLogin(t,
		[]byte("\r\nWarning: your password will expire in 7 days\r\nrouter#"),
	)
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}
	if serverErr != nil {
		t.Fatal(serverErr)
	}
}

func TestSuccessTextWithoutShellPromptDoesNotAuthenticate(t *testing.T) {
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(time.Second))
		if err := writeFull(server, []byte("Password:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("secret\r\x00")); err != nil {
			serverErr <- err
			return
		}
		if err := writeFull(server, []byte("\r\nLogin successful.\r\n")); err != nil {
			serverErr <- err
			return
		}
		buffer := make([]byte, 1)
		_, err := server.Read(buffer)
		serverErr <- err
	}()

	_, err := NewClientConn(conn, &ClientConfig{
		Password: "secret", Timeout: 30 * time.Millisecond, LineEnding: LineEndingCRNUL,
	})
	if !errors.Is(err, ErrLoginTimeout) {
		t.Fatalf("got %v, want ErrLoginTimeout", err)
	}
	if serverErr := <-serverErr; serverErr == nil {
		t.Fatal("server expected the timed-out connection to close")
	}
}

func TestLoginPromptNormalization(t *testing.T) {
	cases := map[string][]byte{
		"ansi":      []byte("\x1b[31mPassword:\x1b[0m"),
		"nul":       []byte("Pass\x00word:"),
		"backspace": []byte("Passworx\bd:"),
	}
	for name, prompt := range cases {
		t.Run(name, func(t *testing.T) {
			for split := 0; split <= len(prompt); split++ {
				server, conn := net.Pipe()
				serverErr := make(chan error, 1)
				go func() {
					defer server.Close()
					_ = server.SetDeadline(time.Now().Add(time.Second))
					promptWrite := make(chan error, 1)
					go func() {
						for _, part := range [][]byte{prompt[:split], prompt[split:]} {
							if len(part) == 0 {
								continue
							}
							if err := writeFull(server, part); err != nil {
								promptWrite <- err
								return
							}
						}
						promptWrite <- nil
					}()
					if err := expectBytes(server, []byte("secret\r\x00")); err != nil {
						serverErr <- err
						return
					}
					if err := <-promptWrite; err != nil {
						serverErr <- err
						return
					}
					if err := writeFull(server, []byte("switch>")); err != nil {
						serverErr <- err
						return
					}
					buffer := make([]byte, 1)
					_, _ = server.Read(buffer)
					serverErr <- nil
				}()

				client, err := NewClientConn(conn, &ClientConfig{
					Password: "secret", Timeout: 250 * time.Millisecond,
					LineEnding: LineEndingCRNUL,
				})
				if client != nil {
					_ = client.Close()
				}
				if err != nil {
					t.Fatalf("split %d: %v", split, err)
				}
				if err := <-serverErr; err != nil {
					t.Fatalf("split %d: %v", split, err)
				}
			}
		})
	}
}

func TestCustomPromptReplacesDefaultAndMatchesWholeLine(t *testing.T) {
	config, err := prepareConfig(&ClientConfig{
		User: "admin", UsernamePrompt: regexp.MustCompile(`login:`),
		LineEnding: LineEndingCRNUL,
	})
	if err != nil {
		t.Fatal(err)
	}
	machine := newLoginMachine(config)
	var output bytes.Buffer

	result, err := machine.handleLine(&output, []byte("Last login:"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != loginPending || output.Len() != 0 {
		t.Fatalf("banner matched as username prompt: result=%v output=%q", result, output.Bytes())
	}

	result, err = machine.handleLine(&output, []byte("login:"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != loginCredentialSent || output.String() != "admin\r\x00" {
		t.Fatalf("actual prompt not handled: result=%v output=%q", result, output.Bytes())
	}
}

func TestCustomShellPromptReplacesDefault(t *testing.T) {
	config, err := prepareConfig(&ClientConfig{
		Password:    "secret",
		ShellPrompt: regexp.MustCompile(`READY>`),
	})
	if err != nil {
		t.Fatal(err)
	}
	machine := newLoginMachine(config)
	machine.phase = loginAwaitResult

	result, err := machine.handleLine(io.Discard, []byte("router#"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != loginPending {
		t.Fatalf("default shell prompt remained active: %v", result)
	}
	result, err = machine.handleLine(io.Discard, []byte("READY>"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != loginSuccess {
		t.Fatalf("custom shell prompt did not match: %v", result)
	}
}

func TestTraceRedactsBidirectionalLoginData(t *testing.T) {
	var trace bytes.Buffer
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(time.Second))
		if err := writeFull(server, []byte("Login:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("admin\r\x00")); err != nil {
			serverErr <- err
			return
		}
		if err := writeFull(server, []byte("admin\r\nPassword:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("secret\r\x00")); err != nil {
			serverErr <- err
			return
		}
		serverErr <- writeFull(server, []byte("secret\r\nrouter#"))
	}()

	client, err := NewClientConn(conn, &ClientConfig{
		User: "admin", Password: "secret", Timeout: time.Second, Trace: &trace,
		LineEnding: LineEndingCRNUL,
	})
	if client != nil {
		_ = client.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}

	text := trace.String()
	if !strings.Contains(text, "<redacted>") {
		t.Fatalf("trace did not contain a redaction marker:\n%s", text)
	}
	for _, secretHex := range []string{"61 64 6d 69 6e", "73 65 63 72 65 74"} {
		if strings.Contains(text, secretHex) {
			t.Fatalf("trace leaked credential bytes %q:\n%s", secretHex, text)
		}
	}
}

func runStandardLogin(t *testing.T, resultParts ...[]byte) (error, error) {
	t.Helper()
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(time.Second))
		if err := writeFull(server, []byte("Login:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("admin\r\x00")); err != nil {
			serverErr <- err
			return
		}
		if err := writeFull(server, []byte("Password:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("secret\r\x00")); err != nil {
			serverErr <- err
			return
		}
		for _, part := range resultParts {
			if len(part) == 0 {
				continue
			}
			if err := writeFull(server, part); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()

	client, err := NewClientConn(conn, &ClientConfig{
		User: "admin", Password: "secret", Timeout: 250 * time.Millisecond,
		LineEnding: LineEndingCRNUL,
	})
	if client != nil {
		_ = client.Close()
	}
	return err, <-serverErr
}

func expectBytes(reader io.Reader, want []byte) error {
	got := make([]byte, len(want))
	if _, err := io.ReadFull(reader, got); err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return errors.New("unexpected client data")
	}
	return nil
}

package tclientlib

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"testing"
	"time"
)

func TestLoginEncodingDiagnosticsBeforeUTF8Validation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		config  ClientConfig
		want    error
	}{
		{"latin1-failure", "Login failed for user Andr\xe9.", ClientConfig{}, ErrLoginFailed},
		{"malformed-failure", "Login failed for user Andr\xf0\x28\x8c\x28.", ClientConfig{}, ErrLoginFailed},
		{"latin1-password-expiry", "The password for Andr\xe9 has expired.", ClientConfig{}, ErrPasswordChangeRequired},
		{"custom-failure", "Rejected Andr\xe9>", ClientConfig{FailurePrompt: regexp.MustCompile(`^Rejected .+>$`)}, ErrLoginFailed},
		{"custom-password-expiry", "Rotate Andr\xe9>", ClientConfig{PasswordChangePrompt: regexp.MustCompile(`^Rotate .+>$`)}, ErrPasswordChangeRequired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, ending := range []string{"\r\n", "\n", ""} {
				wire := []byte("Password:\r\n" + tc.message + ending)
				if ending != "" {
					// Discarding a diagnostic must not let this later prompt win.
					wire = append(wire, []byte("router#\r\nTAIL")...)
				}
				for split := 0; split <= len(wire); split++ {
					conn := &streamTestConn{chunks: [][]byte{wire[:split], wire[split:]}, lastError: io.EOF}
					config := tc.config
					config.Password, config.Timeout = "secret", time.Second
					client, err := NewClientConn(conn, &config)
					if client != nil {
						_ = client.Close()
					}
					if client != nil || !errors.Is(err, tc.want) {
						t.Fatalf("ending=%q split=%d: client=%t err=%v, want %v", ending, split, client != nil, err, tc.want)
					}
					if !conn.closed.Load() || conn.writes.String() != "secret\r\n" {
						t.Fatalf("ending=%q split=%d: closed=%t writes=%q", ending, split, conn.closed.Load(), conn.writes.String())
					}
				}
			}
		})
	}
}

func TestLoginEncodingInvalidPromptsCannotTriggerActions(t *testing.T) {
	for _, kind := range []string{"username", "password", "continue", "shell"} {
		t.Run(kind, func(t *testing.T) {
			config, err := prepareConfig(&ClientConfig{User: "admin", Password: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			pattern := regexp.MustCompile(`^.+$`)
			switch kind {
			case "username":
				config.UsernamePrompt = pattern
			case "password":
				config.PasswordPrompt = pattern
			case "continue":
				config.ContinuePrompt = pattern
			case "shell":
				config.ShellPrompt = pattern
			}
			machine := newLoginMachine(config)
			var sent bytes.Buffer
			for _, complete := range []bool{false, true} {
				line := []byte("device\xe9>")
				var result loginResult
				if complete {
					result, err = machine.handleLine(&sent, line, nil)
				} else {
					result, err = machine.handlePartialLine(&sent, line, nil)
				}
				if err != nil || result != loginPending || sent.Len() != 0 ||
					machine.phase != loginAwaitPrompt || machine.continueResponses != 0 {
					t.Fatalf("complete=%t: result=%v err=%v writes=%q phase=%v", complete, result, err, sent.Bytes(), machine.phase)
				}
			}
		})
	}
}

func TestLoginEncodingNonDiagnosticBannerAndUTF8Prompt(t *testing.T) {
	// Encoding errors alone are not authentication errors. The valid prompt
	// contains a multibyte character that can also be split across reads.
	wire := []byte("Device Andr\xe9 ready\r\nPassword for André:\r\n[admin@login ~]$\r\nTAIL")
	for split := 0; split <= len(wire); split++ {
		conn := &streamTestConn{chunks: [][]byte{wire[:split], wire[split:]}, lastError: io.EOF}
		client, err := NewClientConn(conn, &ClientConfig{Password: "secret", Timeout: time.Second})
		if err != nil {
			t.Fatalf("split=%d: %v", split, err)
		}
		tail, readErr := io.ReadAll(client)
		_ = client.Close()
		if readErr != nil || string(tail) != "\r\nTAIL" || conn.writes.String() != "secret\r\n" {
			t.Fatalf("split=%d: tail=%q writes=%q err=%v", split, tail, conn.writes.String(), readErr)
		}
	}
}

func FuzzLoginDiagnosticEncoding(f *testing.F) {
	f.Add(uint8(0), uint16(1), []byte{0xe9})
	f.Add(uint8(1), uint16(4096), []byte{0xc0, 0x80})
	f.Fuzz(func(t *testing.T, choice uint8, size uint16, raw []byte) {
		if len(raw) > 32 {
			raw = raw[:32]
		}
		// Keep the diagnostic structure intact while varying invalid
		// UTF-8 continuation bytes. Exclude IAC and terminal control bytes.
		name := []byte("Andr")
		for _, value := range raw {
			name = append(name, 0x80+value%0x40)
		}
		message := "Login failed for user " + string(name) + "."
		want := ErrLoginFailed
		if choice%2 != 0 {
			message = "The password for " + string(name) + " has expired."
			want = ErrPasswordChangeRequired
		}
		wire := []byte("Password:\r\n" + message + "\r\nrouter#")
		conn := &streamTestConn{chunks: streamChunks(wire, int(size)%4096+1), lastError: io.EOF}
		client, err := NewClientConn(conn, &ClientConfig{Password: "secret", Timeout: time.Second})
		if client != nil {
			_ = client.Close()
		}
		if client != nil || !errors.Is(err, want) || conn.writes.String() != "secret\r\n" {
			t.Fatalf("client=%t err=%v want=%v writes=%q", client != nil, err, want, conn.writes.String())
		}
	})
}

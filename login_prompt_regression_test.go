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

func reviewLoginMachine(t *testing.T, cfg ClientConfig) (*loginMachine, *loginText) {
	t.Helper()
	config, err := prepareConfig(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	return newLoginMachine(config), newLoginText(config.maxLoginBuffer)
}

func TestReviewPartialFailureWaitsForLineEnd(t *testing.T) {
	for _, message := range []string{"Invalid user>", "Wrong passwords>", "用户名或密码错误>"} {
		t.Run(message, func(t *testing.T) {
			machine, text := reviewLoginMachine(t, ClientConfig{Password: "secret"})
			machine.phase = loginAwaitResult
			var sent bytes.Buffer
			for _, part := range []string{message, "\r"} {
				_, result, _, err := machine.consumeChunk(&sent, text, []byte(part), nil)
				if err != nil || result != loginPending {
					t.Fatalf("incomplete %q: result=%v err=%v, want pending", part, result, err)
				}
				if string(text.current()) != message || machine.phase != loginAwaitResult || sent.Len() != 0 {
					t.Fatal("partial diagnostic changed login state, wrote data, or lost its text")
				}
			}
			_, result, _, err := machine.consumeChunk(&sent, text, []byte("\nrouter#"), nil)
			if err != nil || result != loginFailed {
				t.Fatalf("completed failure: result=%v err=%v", result, err)
			}
		})
	}
}

func TestReviewFailureAtEverySplit(t *testing.T) {
	for _, message := range []string{"Invalid user>", "Wrong passwords>", "用户名或密码错误>"} {
		payload := []byte(message + "\r\nrouter#")
		for split := 0; split <= len(payload); split++ {
			machine, text := reviewLoginMachine(t, ClientConfig{Password: "secret"})
			var sent bytes.Buffer
			result := loginPending
			for _, part := range [][]byte{payload[:split], payload[split:]} {
				var err error
				_, result, _, err = machine.consumeChunk(&sent, text, part, nil)
				if err != nil {
					t.Fatal(err)
				}
				if result != loginPending {
					break
				}
			}
			if result != loginFailed || sent.Len() != 0 {
				t.Fatalf("%q split=%d result=%v sent=%q", message, split, result, sent.Bytes())
			}
		}
	}
}

func TestReviewDiagnosticPrefixCanStillBecomeBanner(t *testing.T) {
	for _, prefix := range []string{"Invalid user>", "Wrong passwords>", "用户名或密码错误>"} {
		machine, text := reviewLoginMachine(t, ClientConfig{Password: "secret"})
		_, result, _, err := machine.consumeChunk(io.Discard, text, []byte(prefix), nil)
		if err != nil || result != loginPending {
			t.Fatalf("%q prefix: result=%v err=%v", prefix, result, err)
		}
		_, result, _, err = machine.consumeChunk(io.Discard, text, []byte(" count: 0\r\nrouter#"), nil)
		if err != nil || result != loginSuccess {
			t.Fatalf("%q banner: result=%v err=%v", prefix, result, err)
		}
	}
}

func TestReviewCustomDiagnosticCannotTriggerPromptAction(t *testing.T) {
	for _, passwordChange := range []bool{false, true} {
		cfg := ClientConfig{User: "admin", Password: "secret"}
		pattern := regexp.MustCompile(`REJECT>`)
		cfg.ShellPrompt, cfg.UsernamePrompt, cfg.PasswordPrompt, cfg.ContinuePrompt = pattern, pattern, pattern, pattern
		want := loginFailed
		if passwordChange {
			cfg.PasswordChangePrompt = pattern
			want = loginPasswordChangeRequired
		} else {
			cfg.FailurePrompt = pattern
		}
		machine, text := reviewLoginMachine(t, cfg)
		var sent bytes.Buffer
		_, result, _, err := machine.consumeChunk(&sent, text, []byte("REJECT>"), nil)
		if err != nil || result != loginPending || sent.Len() != 0 || machine.phase != loginAwaitPrompt {
			t.Fatalf("partial diagnostic: result=%v err=%v sent=%q", result, err, sent.Bytes())
		}
		_, result, _, err = machine.consumeChunk(&sent, text, []byte("\n"), nil)
		if err != nil || result != want || sent.Len() != 0 {
			t.Fatalf("complete diagnostic: result=%v want=%v err=%v sent=%q", result, want, err, sent.Bytes())
		}
	}
}

func TestReviewFailureWithoutNewline(t *testing.T) {
	for _, message := range []string{"Invalid user>", "Wrong passwords>", "用户名或密码错误>"} {
		for _, waitForTimeout := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/timeout=%t", message, waitForTimeout), func(t *testing.T) {
				server, conn := net.Pipe()
				defer server.Close()
				defer conn.Close()
				serverDone := make(chan error, 1)
				go func() {
					defer server.Close()
					_ = server.SetDeadline(time.Now().Add(2 * time.Second))
					if err := writeFull(server, []byte(message)); err != nil {
						serverDone <- err
						return
					}
					if waitForTimeout {
						var data [1]byte
						n, _ := server.Read(data[:])
						if n != 0 {
							serverDone <- errors.New("diagnostic caused a credential or continue write")
							return
						}
					}
					serverDone <- nil
				}()
				client, err := NewClientConn(conn, &ClientConfig{Password: "secret", Timeout: 50 * time.Millisecond})
				if client != nil {
					_ = client.Close()
				}
				if !errors.Is(err, ErrLoginFailed) || client != nil {
					t.Errorf("client=%v error=%v, want nil client and ErrLoginFailed", client != nil, err)
				}
				if err := <-serverDone; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestReviewCompleteCustomPromptLimit(t *testing.T) {
	for _, length := range []int{maxPartialPromptBytes, maxPartialPromptBytes + 1, 601, loginReadBufferSize + 1, defaultMaxLoginBuffer} {
		t.Run(fmt.Sprint(length), func(t *testing.T) {
			prompt := strings.Repeat("a", length-1) + ">"
			cfg := ClientConfig{Password: "secret", ShellPrompt: regexp.MustCompile(`^a+>$`)}
			machine, text := reviewLoginMachine(t, cfg)
			_, result, _, err := machine.consumeChunk(io.Discard, text, []byte(prompt), nil)
			wantPartial := loginPending
			if length <= maxPartialPromptBytes {
				wantPartial = loginSuccess
			}
			if err != nil || result != wantPartial {
				t.Fatalf("partial length=%d: result=%v want=%v err=%v", length, result, wantPartial, err)
			}
			for _, ending := range []string{"\n", "\r\n"} {
				machine, text = reviewLoginMachine(t, cfg)
				payload := []byte(prompt + ending + "ALARM")
				consumed, result, replay, err := machine.consumeChunk(io.Discard, text, payload, nil)
				if err != nil || result != loginSuccess {
					t.Fatalf("complete length=%d ending=%q: result=%v err=%v", length, ending, result, err)
				}
				if replay+string(payload[consumed:]) != ending+"ALARM" {
					t.Fatal("lost post-prompt data")
				}
			}
		})
	}
}

func TestReviewBracketPromptStructure(t *testing.T) {
	for _, prompt := range []string{
		"[admin@login ~]$", "[admin@router /var/log/error]#", "[error@router ~]$",
		"[admin@warning ~]$", "[admin@router error]#", "[admin@router /var/log/登录失败]#",
	} {
		for _, ending := range []string{"", "\r\n"} {
			machine, text := reviewLoginMachine(t, ClientConfig{Password: "secret"})
			_, result, _, err := machine.consumeChunk(io.Discard, text, []byte(prompt+ending), nil)
			if err != nil || result != loginSuccess {
				t.Errorf("valid prompt %q ending=%q: result=%v err=%v", prompt, ending, result, err)
			}
		}
	}
	for _, banner := range []string{"[Warning]#", "[ERROR]%", "[Notice]$", "[Login successful]#", "[密码错误]#", "<Unauthorized>", "系统警告 >"} {
		machine, text := reviewLoginMachine(t, ClientConfig{Password: "secret"})
		_, result, _, err := machine.consumeChunk(io.Discard, text, []byte(banner), nil)
		if err != nil || result != loginPending {
			t.Errorf("banner %q: result=%v err=%v", banner, result, err)
		}
	}
}

func TestReviewConstructorPromptsAcrossChunks(t *testing.T) {
	for _, prompt := range []string{strings.Repeat("a", 600) + ">", "[admin@login ~]$", "[admin@router /var/log/error]#"} {
		for _, size := range []int{1, 2, 7, 1024, 4096} {
			cfg := ClientConfig{Password: "secret", Timeout: 2 * time.Second}
			if len(prompt) > maxPartialPromptBytes {
				cfg.ShellPrompt = regexp.MustCompile(`^a+>$`)
			}
			wire := []byte("Password:\r\n" + prompt + "\r\nTAIL")
			conn := &streamTestConn{chunks: streamChunks(wire, size), lastError: io.EOF}
			client, err := NewClientConn(conn, &cfg)
			if err != nil {
				t.Fatalf("prompt length=%d chunk=%d: %v", len(prompt), size, err)
			}
			output, readErr := io.ReadAll(client)
			_ = client.Close()
			if readErr != nil || string(output) != "\r\nTAIL" || conn.writes.String() != "secret\r\n" {
				t.Fatalf("chunk=%d tail=%q writes=%q err=%v", size, output, conn.writes.String(), readErr)
			}
		}
	}
}

func TestReviewCompletePromptStillHonorsLogicalLineLimit(t *testing.T) {
	prompt := strings.Repeat("x", defaultMaxLoginBuffer) + ">"
	data := []byte(prompt + "\r\nx>\r\nTAIL")
	conn := &streamTestConn{chunks: streamChunks(data, 4096)}
	client, err := NewClientConn(conn, &ClientConfig{
		Password: "secret", ShellPrompt: regexp.MustCompile(`^x+>$`), Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	output, err := io.ReadAll(client)
	if err != nil || string(output) != "\r\nTAIL" {
		t.Fatalf("oversized line was accepted or tail was lost: output=%q err=%v", output, err)
	}
}

func FuzzFailurePromptChunking(f *testing.F) {
	f.Add(uint8(0), uint16(1))
	f.Add(uint8(1), uint16(16))
	f.Add(uint8(2), uint16(4096))
	f.Fuzz(func(t *testing.T, choice uint8, size uint16) {
		messages := []string{"Invalid user>", "Wrong passwords>", "用户名或密码错误>"}
		message := messages[int(choice)%len(messages)]
		data := []byte("Password:\r\n" + message + "\r\nrouter#")
		conn := &streamTestConn{chunks: streamChunks(data, int(size)%4096+1), lastError: io.EOF}
		client, err := NewClientConn(conn, &ClientConfig{Password: "secret", Timeout: time.Second})
		if client != nil {
			_ = client.Close()
		}
		if !errors.Is(err, ErrLoginFailed) || client != nil {
			t.Fatalf("message=%q chunk=%d: client=%v err=%v", message, size, client != nil, err)
		}
		if conn.writes.String() != "secret\r\n" {
			t.Fatalf("unexpected writes: %q", conn.writes.String())
		}
	})
}

package tclientlib

import (
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestAdditivePromptsConnectMixedDevices(t *testing.T) {
	// One platform configuration must support both built-in and extra device
	// prompts, including sessions that use a mixture of the two sets.
	cfg := ClientConfig{
		User: "admin", Password: "secret", Timeout: time.Second,
		UsernamePrompt: regexp.MustCompile(`^Account ID:$`),
		PasswordPrompt: regexp.MustCompile(`^Access code:$`),
		ShellPrompt:    regexp.MustCompile(`^SESSION READY$`),
	}
	for choices := 0; choices < 8; choices++ {
		username, password, shell := "Username:", "Password for admin:", "router#"
		if choices&1 != 0 {
			username = "Account ID:"
		}
		if choices&2 != 0 {
			password = "Access code:"
		}
		if choices&4 != 0 {
			shell = "SESSION READY"
		}
		wire := []byte("\x1b[32m" + username + "\x1b[0m\r\n" +
			"\x1b[33m" + password + "\x1b[0m\r\n" +
			"Password expired policy: disabled\r\nLast login failed: 0\r\n" +
			shell + "\r\nTAIL\r\n")
		for _, size := range []int{1, 2, 7, len(wire)} {
			t.Run(fmt.Sprintf("custom-%03b/chunk-%d", choices, size), func(t *testing.T) {
				conn := &streamTestConn{chunks: streamChunks(wire, size), lastError: io.EOF}
				client, err := NewClientConn(conn, &cfg)
				if err != nil {
					t.Fatalf("login: %v; writes=%q", err, conn.writes.String())
				}
				defer client.Close()
				tail, err := io.ReadAll(client)
				if err != nil {
					t.Fatal(err)
				}
				if string(tail) != "\r\nTAIL\r\n" || conn.writes.String() != "admin\r\nsecret\r\n" {
					t.Fatalf("tail=%q writes=%q", tail, conn.writes.String())
				}
			})
		}
	}
}

func TestBareCredentialPromptsWaitForLineEnd(t *testing.T) {
	for _, tc := range []struct {
		prompt   string
		password bool
	}{
		{"Login", false}, {"User", false}, {"Username", false},
		{"User name", false}, {"Name", false}, {"用户名", false}, {"账 号", false},
		{"Password", true}, {"Passwd", true}, {"Passcode", true}, {"密 码", true}, {"口令", true},
	} {
		for _, ending := range []string{"\n", "\r\n"} {
			t.Run(fmt.Sprintf("%s/%q", tc.prompt, ending), func(t *testing.T) {
				machine, text := reviewLoginMachine(t, ClientConfig{
					User: "admin", Password: "secret",
					UsernamePrompt: regexp.MustCompile(`^Account ID:$`),
					PasswordPrompt: regexp.MustCompile(`^Access code:$`),
				})
				var sent bytes.Buffer
				parts := []string{tc.prompt}
				if ending == "\r\n" {
					parts = append(parts, "\r")
				}
				for _, part := range parts {
					_, result, _, err := machine.consumeChunk(&sent, text, []byte(part), nil)
					if err != nil || result != loginPending || sent.Len() != 0 || machine.phase != loginAwaitPrompt || string(text.current()) != tc.prompt {
						t.Fatalf("partial %q: result=%v err=%v writes=%q phase=%v text=%q", part, result, err, sent.String(), machine.phase, text.current())
					}
				}
				_, result, _, err := machine.consumeChunk(&sent, text, []byte("\n"), nil)
				wantWrites, wantPhase := "admin\r\n", loginAwaitPasswordOrResult
				if tc.password {
					wantWrites, wantPhase = "secret\r\n", loginAwaitResult
				}
				if err != nil || result != loginPending || sent.String() != wantWrites || machine.phase != wantPhase {
					t.Fatalf("completed: result=%v err=%v writes=%q phase=%v; want %q phase=%v", result, err, sent.String(), machine.phase, wantWrites, wantPhase)
				}
			})
		}
	}
}

func TestBareCredentialPrefixesDoNotTriggerActions(t *testing.T) {
	for _, tc := range []struct {
		prefix string
		suffix string
		result loginResult
		writes string
		phase  loginPhase
	}{
		{"User", "name:", loginCredentialSent, "admin\r\n", loginAwaitPasswordOrResult},
		{"User", " name:", loginCredentialSent, "admin\r\n", loginAwaitPasswordOrResult},
		{"Password", ": ", loginCredentialSent, "secret\r\n", loginAwaitResult},
		{"User", " name policy: enabled\r\n", loginPending, "", loginAwaitPrompt},
		{"Login", " failed attempts: 0\r\n", loginPending, "", loginAwaitPrompt},
		{"Password", " expired policy: disabled\r\n", loginPending, "", loginAwaitPrompt},
		{"Password", " expired\r\n", loginPasswordChangeRequired, "", loginAwaitPrompt},
	} {
		t.Run(tc.prefix+tc.suffix, func(t *testing.T) {
			machine, text := reviewLoginMachine(t, ClientConfig{User: "admin", Password: "secret"})
			var sent bytes.Buffer
			_, result, _, err := machine.consumeChunk(&sent, text, []byte(tc.prefix), nil)
			if err != nil || result != loginPending || sent.Len() != 0 || string(text.current()) != tc.prefix {
				t.Fatalf("prefix acted on: result=%v err=%v writes=%q text=%q", result, err, sent.String(), text.current())
			}
			_, result, _, err = machine.consumeChunk(&sent, text, []byte(tc.suffix), nil)
			if err != nil || result != tc.result || sent.String() != tc.writes || machine.phase != tc.phase {
				t.Fatalf("result=%v err=%v writes=%q phase=%v; want %v %q phase=%v", result, err, sent.String(), machine.phase, tc.result, tc.writes, tc.phase)
			}
		})
	}
}

func TestExplicitBareCustomPromptsDoNotNeedLineEnd(t *testing.T) {
	for _, password := range []bool{false, true} {
		cfg := ClientConfig{User: "admin", Password: "secret"}
		prompt, wantWrites, wantPhase := "User", "admin\r\n", loginAwaitPasswordOrResult
		if password {
			cfg.PasswordPrompt = regexp.MustCompile(`^Password$`)
			prompt, wantWrites, wantPhase = "Password", "secret\r\n", loginAwaitResult
		} else {
			cfg.UsernamePrompt = regexp.MustCompile(`^User$`)
		}
		t.Run(prompt, func(t *testing.T) {
			machine, text := reviewLoginMachine(t, cfg)
			var sent bytes.Buffer
			_, result, _, err := machine.consumeChunk(&sent, text, []byte(prompt), nil)
			if err != nil || result != loginCredentialSent || sent.String() != wantWrites || machine.phase != wantPhase {
				t.Fatalf("result=%v err=%v writes=%q phase=%v", result, err, sent.String(), machine.phase)
			}
			_, result, _, err = machine.consumeChunk(&sent, text, []byte("\r\n"), nil)
			if err != nil || result != loginPending || sent.String() != wantWrites || machine.phase != wantPhase {
				t.Fatalf("line ending repeated action: result=%v err=%v writes=%q phase=%v", result, err, sent.String(), machine.phase)
			}
		})
	}
}

func TestAdditiveShellKeepsDefaultBoundaries(t *testing.T) {
	for _, line := range []string{
		"[Warning]#", "[ERROR]%", "<Unauthorized>", "Login successful >",
		"Last login: yesterday", "success", "成功", "prefix SESSION READY suffix",
		strings.Repeat("x", maxDefaultPromptBytes) + "#",
	} {
		for _, ending := range []string{"", "\r\n"} {
			t.Run(fmt.Sprintf("%s/%q", line, ending), func(t *testing.T) {
				machine, text := reviewLoginMachine(t, ClientConfig{
					Password: "secret", ShellPrompt: regexp.MustCompile(`^SESSION READY$`),
				})
				machine.phase = loginAwaitResult
				var sent bytes.Buffer
				_, result, _, err := machine.consumeChunk(&sent, text, []byte(line+ending), nil)
				if err != nil || result != loginPending || sent.Len() != 0 {
					t.Fatalf("banner/oversized line acted on: result=%v err=%v writes=%q", result, err, sent.String())
				}
			})
		}
	}
}

func TestAdditivePromptsKeepBuiltinDiagnosticsFirst(t *testing.T) {
	for _, tc := range []struct {
		line             string
		result           loginResult
		immediatePartial bool
	}{
		{"Login incorrect", loginFailed, false},
		{"Invalid user>", loginFailed, false},
		{"Your password has expired", loginPasswordChangeRequired, false},
		{"New password:", loginPasswordChangeRequired, true},
	} {
		t.Run(tc.line, func(t *testing.T) {
			anyText := regexp.MustCompile(`.+`)
			machine, text := reviewLoginMachine(t, ClientConfig{
				User: "admin", Password: "secret",
				UsernamePrompt: anyText, PasswordPrompt: anyText, ShellPrompt: anyText,
			})
			var sent bytes.Buffer
			_, result, _, err := machine.consumeChunk(&sent, text, []byte(tc.line), nil)
			want := loginPending
			if tc.immediatePartial {
				want = tc.result
			}
			if err != nil || result != want || sent.Len() != 0 || machine.phase != loginAwaitPrompt {
				t.Fatalf("partial diagnostic: result=%v err=%v writes=%q phase=%v", result, err, sent.String(), machine.phase)
			}
			if !tc.immediatePartial {
				_, result, _, err = machine.consumeChunk(&sent, text, []byte("\r\nrouter#"), nil)
				if err != nil || result != tc.result || sent.Len() != 0 || machine.phase != loginAwaitPrompt {
					t.Fatalf("completed diagnostic: result=%v err=%v writes=%q phase=%v", result, err, sent.String(), machine.phase)
				}
			}
		})
	}
}

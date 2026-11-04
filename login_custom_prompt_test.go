package tclientlib

import (
	"bytes"
	"io"
	"regexp"
	"testing"
	"time"
)

func TestCustomPromptsUseRegexpMatchSemantics(t *testing.T) {
	for _, kind := range []struct {
		name          string
		set           func(*ClientConfig, *regexp.Regexp)
		defaultPrompt string
		result        loginResult
		writes        string
		phase         loginPhase
		continues     int
	}{
		{"username", func(c *ClientConfig, p *regexp.Regexp) { c.UsernamePrompt = p }, "Login:", loginPending, "admin\r\n", loginAwaitPasswordOrResult, 0},
		{"password", func(c *ClientConfig, p *regexp.Regexp) { c.PasswordPrompt = p }, "Password:", loginPending, "secret\r\n", loginAwaitResult, 0},
		{"shell", func(c *ClientConfig, p *regexp.Regexp) { c.ShellPrompt = p }, "router#", loginSuccess, "", loginAwaitPrompt, 0},
		{"failure", func(c *ClientConfig, p *regexp.Regexp) { c.FailurePrompt = p }, "Access denied", loginFailed, "", loginAwaitPrompt, 0},
		{"password-change", func(c *ClientConfig, p *regexp.Regexp) { c.PasswordChangePrompt = p }, "New password:", loginPasswordChangeRequired, "", loginAwaitPrompt, 0},
		{"continue", func(c *ClientConfig, p *regexp.Regexp) { c.ContinuePrompt = p }, "Press Enter to continue", loginPending, "\r\n", loginAwaitPrompt, 1},
	} {
		t.Run(kind.name, func(t *testing.T) {
			for _, tc := range []struct {
				name    string
				pattern string
				line    string
				matches bool
			}{
				{"substring", `TOKEN`, "prefix TOKEN suffix", true},
				{"case-sensitive-by-default", `TOKEN`, "prefix token suffix", false},
				{"explicit-case-insensitive", `(?i)TOKEN`, "prefix token suffix", true},
				{"scoped-case-insensitive", `(?i:token)`, "prefix ToKeN suffix", true},
				{"anchored-rejects-context", `^TOKEN$`, "prefix TOKEN suffix", false},
				{"anchored-exact", `^TOKEN$`, " TOKEN ", true},
				{"start-anchor-allows-suffix", `^TOKEN`, "TOKEN suffix", true},
				{"start-anchor-rejects-prefix", `^TOKEN`, "prefix TOKEN", false},
				{"end-anchor-allows-prefix", `TOKEN$`, "prefix TOKEN", true},
				{"end-anchor-rejects-suffix", `TOKEN$`, "TOKEN suffix", false},
				{"replaces-default", `TOKEN`, kind.defaultPrompt, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					cfg := ClientConfig{User: "admin", Password: "secret"}
					kind.set(&cfg, regexp.MustCompile(tc.pattern))
					machine, text := reviewLoginMachine(t, cfg)
					var sent bytes.Buffer
					_, result, _, err := machine.consumeChunk(&sent, text, []byte(tc.line+"\r\n"), nil)
					if err != nil {
						t.Fatal(err)
					}
					wantResult, wantWrites, wantPhase, wantContinues := loginPending, "", loginAwaitPrompt, 0
					if tc.matches {
						wantResult, wantWrites, wantPhase, wantContinues = kind.result, kind.writes, kind.phase, kind.continues
					}
					// Nonterminal actions on completed lines continue scanning. Their
					// observable result is the write and state transition, not success.
					if result != wantResult || sent.String() != wantWrites || machine.phase != wantPhase || machine.continueResponses != wantContinues {
						t.Fatalf("result=%v writes=%q phase=%v continues=%d; want %v %q %v %d", result, sent.String(), machine.phase, machine.continueResponses, wantResult, wantWrites, wantPhase, wantContinues)
					}
				})
			}
		})
	}
}

func TestCustomDiagnosticSubstringsKeepPromptPrecedence(t *testing.T) {
	for _, passwordChange := range []bool{false, true} {
		name, want := "failure", loginFailed
		if passwordChange {
			name, want = "password-change", loginPasswordChangeRequired
		}
		t.Run(name, func(t *testing.T) {
			cfg := ClientConfig{User: "admin", Password: "secret"}
			pattern := regexp.MustCompile(`TOKEN`)
			cfg.UsernamePrompt, cfg.PasswordPrompt, cfg.ShellPrompt, cfg.ContinuePrompt = pattern, pattern, pattern, pattern
			if passwordChange {
				cfg.PasswordChangePrompt = pattern
			} else {
				cfg.FailurePrompt = pattern
			}
			machine, text := reviewLoginMachine(t, cfg)
			var sent bytes.Buffer
			const line = "Device reports TOKEN for this account"
			for _, part := range []string{line, "\r"} {
				_, result, _, err := machine.consumeChunk(&sent, text, []byte(part), nil)
				if err != nil || result != loginPending || sent.Len() != 0 || machine.phase != loginAwaitPrompt || machine.continueResponses != 0 || string(text.current()) != line {
					t.Fatalf("partial diagnostic: result=%v err=%v writes=%q phase=%v continues=%d text=%q", result, err, sent.Bytes(), machine.phase, machine.continueResponses, text.current())
				}
			}
			_, result, _, err := machine.consumeChunk(&sent, text, []byte("\nrouter#"), nil)
			if err != nil || result != want || sent.Len() != 0 || machine.phase != loginAwaitPrompt || machine.continueResponses != 0 {
				t.Fatalf("completed diagnostic: result=%v want=%v err=%v writes=%q", result, want, err, sent.Bytes())
			}
		})
	}
}

func TestCustomPasswordChangeSubstringInputStopsCredentialWrites(t *testing.T) {
	pattern := regexp.MustCompile(`ROTATE`)
	machine, text := reviewLoginMachine(t, ClientConfig{
		Password: "secret", PasswordPrompt: pattern, PasswordChangePrompt: pattern,
	})
	var sent bytes.Buffer
	_, result, _, err := machine.consumeChunk(&sent, text, []byte("Device requires ROTATE now:"), nil)
	if err != nil || result != loginPasswordChangeRequired || sent.Len() != 0 {
		t.Fatalf("password-change input: result=%v err=%v writes=%q", result, err, sent.Bytes())
	}
}

func TestCustomPromptMatchesOnlyTheCurrentLogicalLine(t *testing.T) {
	machine, text := reviewLoginMachine(t, ClientConfig{
		Password: "secret", ShellPrompt: regexp.MustCompile(`(?s)TOKEN.*READY`),
	})
	var sent bytes.Buffer
	for _, part := range []string{"prefix TOKEN\r\n", "READY suffix\r\n"} {
		_, result, _, err := machine.consumeChunk(&sent, text, []byte(part), nil)
		if err != nil || result != loginPending || sent.Len() != 0 {
			t.Fatalf("separate lines matched: result=%v err=%v writes=%q", result, err, sent.Bytes())
		}
	}
	_, result, _, err := machine.consumeChunk(&sent, text, []byte("prefix TOKEN is READY suffix"), nil)
	if err != nil || result != loginSuccess || sent.Len() != 0 {
		t.Fatalf("one current line did not match: result=%v err=%v writes=%q", result, err, sent.Bytes())
	}
}

func TestCustomPromptStreamNormalizesLineAndPreservesFollowingData(t *testing.T) {
	wire := []byte("\x1b[32mGateway User identifiex\br: [admin]\x1b[0m\r\n" +
		"\x1b[33mGateway Access code: [required]\x1b[0m\r\n" +
		"\x1b[32mGateway READY> [console]\x1b[0m\r\nTAIL\r\n")
	for _, tc := range []struct {
		name   string
		chunks [][]byte
	}{
		{"one-read", [][]byte{wire}},
		{"split-ansi-sequence", [][]byte{wire[:4], wire[4:]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &streamTestConn{chunks: tc.chunks, lastError: io.EOF}
			client, err := NewClientConn(conn, &ClientConfig{
				User: "admin", Password: "secret", Timeout: time.Second,
				UsernamePrompt: regexp.MustCompile(`User identifier:`),
				PasswordPrompt: regexp.MustCompile(`Access code:`),
				ShellPrompt:    regexp.MustCompile(`READY>`),
			})
			if err != nil {
				t.Fatalf("login: %v", err)
			}
			defer client.Close()
			tail, err := io.ReadAll(client)
			if err != nil {
				t.Fatal(err)
			}
			// The whole accepted prompt line, including its contextual suffix,
			// is consumed. Only the following terminator and session data remain.
			if string(tail) != "\r\nTAIL\r\n" || conn.writes.String() != "admin\r\nsecret\r\n" {
				t.Fatalf("tail=%q writes=%q", tail, conn.writes.String())
			}
		})
	}
}

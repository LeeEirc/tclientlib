package tclientlib

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxContinueResponses  = 3
	maxDefaultPromptBytes = 128
	loginReadBufferSize   = 4096
	maxPartialPromptBytes = 512
)

var (
	defaultUsernamePrompt = regexp.MustCompile(
		`(?i)^\s*(?:(?:(?:please\s+)?(?:enter|input)\s+(?:your\s+)?(?:login|user(?:\s*name)?))|login\s+as|login|user(?:\s*name)?|name|用户名|账\s*号|(?:请\s*)?(?:输入|键入)(?:用户\s*名|账\s*号))\s*[:：]\s*$`,
	)
	defaultPasswordPrompt = regexp.MustCompile(
		`(?i)^\s*(?:(?:(?:please\s+)?(?:enter|input)\s+(?:your\s+)?(?:password|passwd|passcode)(?:\s+for\s+[^:：\r\n]+)?)|(?:password|passwd|passcode)(?:\s+for\s+[^:：\r\n]+)?|(?:login|user)\s+password|口令|(?:[^:：\r\n]*的\s*)?密\s*码|(?:请\s*)?(?:输入|键入)(?:登录\s*)?(?:密\s*码|口令))\s*(?:\([^()\r\n]{1,64}\))?\s*[:：]\s*$`,
	)
	defaultAngleShellPrompt = regexp.MustCompile(
		`^<[\pL\pN_.:@/\\~()\-]{1,126}>$`,
	)
	defaultBracketShellPrompt = regexp.MustCompile(
		`^\[[\pL\pN_.:@/\\~()\-]{1,126}\]$`,
	)
	defaultMarkedBracketShellPrompt = regexp.MustCompile(
		`^\[[^\[\]\r\n]{1,126}\][#$%]$`,
	)
	// An explicit user@host structure disambiguates Unix prompt components
	// from banner labels, even when the host or directory is named "error".
	defaultUnixBracketShellPrompt = regexp.MustCompile(
		`^\[[\pL\pN_.\-]+@[\pL\pN_.:\-]+(?:[ \t]+[^\[\]\r\n]+)?\][#$%]$`,
	)
	defaultTokenShellPrompt = regexp.MustCompile(
		`^(?:[\pL\pN_.:@/\\~()\-]+(?:[ \t]+[\pL\pN_.:@/\\~()\-]+){0,2})?\s*[#$>%]$`,
	)
	defaultFailurePrompt = regexp.MustCompile(
		`(?i)^\s*%?\s*(?:error:\s*)?(?:login\s+(?:incorrect|invalid|failed)(?:\s+for\s+(?:user\s+)?[^,;:.!。]+)?|failed\s+to\s+login(?:\s+for\s+(?:user\s+)?[^,;:.!。]+)?|(?:user\s+)?authentication\s+(?:failed|failure)|login\s+authentication\s+(?:failed|failure)|access\s+denied|permission\s+denied|invalid\s+(?:login|user(?:name)?|password)|incorrect\s+(?:user(?:name)?|password|username\s+or\s+password)|wrong\s+(?:(?:user(?:name)?|login)\s+or\s+)?passwords?|bad\s+(?:passwords?|secrets?)|(?:username|user\s+name)\s+or\s+password\s+(?:error|incorrect|invalid)|登录失败|认证失败|用户名或密码(?:错误|不正确)|访问被拒绝)(?:\s*[.!。])?(?:\s*[,;:，；：]?\s*(?:please\s+)?(?:try\s+again|retry)|\s*[,;:，；：]?\s*请重试)?\s*[>#]?\s*[.!。]?\s*$`,
	)
	defaultPasswordChangePrompt = regexp.MustCompile(
		`(?i)^\s*(?:(?:warning:\s*|error:\s*|%\s*)?(?:(?:(?:the\s+)?password(?:\s+for\s+[^,;:.!。]+)?|your\s+password)\s+(?:has\s+)?expired(?:\s*[,;:]\s*(?:please\s+)?(?:change|update)\s+(?:it|your\s+password)(?:\s+(?:now|immediately))?)?|password\s+change\s+(?:is\s+)?(?:required|needed)|you\s+(?:are\s+required|must)\s+(?:to\s+)?change\s+(?:your\s+)?password(?:\s+(?:now|immediately))?|change\s+(?:your\s+)?password\s+(?:now|immediately)|current\s+(?:unix\s+)?password\s*[:：]?|old\s+password\s*[:：]?|new\s+password\s*[:：]?|(?:retype|re-enter|confirm|verify)\s+(?:your\s+)?(?:new\s+)?password\s*[:：]?|(?:enter|input)\s+(?:your\s+)?(?:current|old|new)\s+password\s*[:：]?|密码(?:已经|已)过期(?:\s*[,，]\s*(?:请)?(?:修改|更新)密码)?|(?:必须|需要)修改密码|当前密码\s*[:：]?|旧密码\s*[:：]?|新密码\s*[:：]?|确认新密码\s*[:：]?|重新输入(?:新)?密码\s*[:：]?))\s*[.!。]?\s*$`,
	)
	defaultContinuePrompt = regexp.MustCompile(
		`(?i)^\s*(?:(?:please\s+)?(?:press|hit)\s+(?:(?:the\s+)?<?(?:enter|return)>?(?:\s+key)?|any\s+key)\s+to\s+(?:continue|proceed|get\s+started|start)|按(?:下)?(?:回车|enter)键?(?:以)?继续|按(?:下)?任意键(?:以)?继续)\s*[.!。…]*\s*$`,
	)
)

type loginGuard struct {
	mu       sync.Mutex
	finished bool
	expired  bool
	timer    *time.Timer
}

func startLoginGuard(conn net.Conn, timeout time.Duration) *loginGuard {
	guard := &loginGuard{}
	guard.timer = time.AfterFunc(timeout, func() {
		guard.mu.Lock()
		defer guard.mu.Unlock()
		if guard.finished {
			return
		}
		guard.expired = true
		_ = conn.Close()
	})
	return guard
}

func (g *loginGuard) stop() {
	g.mu.Lock()
	g.finished = true
	g.mu.Unlock()
	g.timer.Stop()
}

func (g *loginGuard) didExpire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.expired
}

type loginPhase uint8

const (
	loginAwaitPrompt loginPhase = iota
	loginAwaitPasswordOrResult
	loginAwaitResult
)

func (p loginPhase) String() string {
	switch p {
	case loginAwaitPrompt:
		return "await-prompt"
	case loginAwaitPasswordOrResult:
		return "await-password-or-result"
	case loginAwaitResult:
		return "await-result"
	default:
		return "unknown"
	}
}

type loginPatterns struct {
	username       *regexp.Regexp
	password       *regexp.Regexp
	shell          *regexp.Regexp
	failure        *regexp.Regexp
	passwordChange *regexp.Regexp
	continuePrompt *regexp.Regexp
}

func patternsFor(config clientConfig) loginPatterns {
	return loginPatterns{
		username:       selectPattern(config.UsernamePrompt, defaultUsernamePrompt),
		password:       selectPattern(config.PasswordPrompt, defaultPasswordPrompt),
		shell:          config.ShellPrompt,
		failure:        selectPattern(config.FailurePrompt, defaultFailurePrompt),
		passwordChange: selectPattern(config.PasswordChangePrompt, defaultPasswordChangePrompt),
		continuePrompt: selectPattern(config.ContinuePrompt, defaultContinuePrompt),
	}
}

func selectPattern(custom, fallback *regexp.Regexp) *regexp.Regexp {
	if custom != nil {
		return custom
	}
	return fallback
}

type loginMachine struct {
	phase             loginPhase
	config            clientConfig
	patterns          loginPatterns
	continueResponses int
}

func newLoginMachine(config clientConfig) *loginMachine {
	return &loginMachine{
		phase:    loginAwaitPrompt,
		config:   config,
		patterns: patternsFor(config),
	}
}

type loginResult uint8

const (
	loginPending loginResult = iota
	loginCredentialSent
	loginContinueSent
	loginSuccess
	loginFailed
	loginPasswordChangeRequired
)

// Narrative results are only authoritative on a completed logical line. A
// Read boundary is not an end-of-line marker.
func (m *loginMachine) messageResult(line []byte, trace *tracer) loginResult {
	if len(bytes.TrimSpace(line)) == 0 {
		return loginPending
	}
	if fullLineMatch(m.patterns.passwordChange, line) {
		trace.printf("login password-change message matched phase=%s", m.phase)
		return loginPasswordChangeRequired
	}
	if fullLineMatch(m.patterns.failure, line) {
		trace.printf("login failure message matched phase=%s", m.phase)
		return loginFailed
	}
	return loginPending
}

func (m *loginMachine) handleLine(writer io.Writer, line []byte, trace *tracer) (loginResult, error) {
	// Completed lines are bounded by the logical-line limit, not the smaller
	// partial-prompt budget. Custom prompts may legitimately exceed 512 bytes.
	if len(line) > m.config.maxLoginBuffer || !utf8.Valid(line) {
		return loginPending, nil
	}
	if result := m.messageResult(line, trace); result != loginPending {
		return result, nil
	}
	return m.handlePrompt(writer, line, trace)
}

// Interactive prompts normally have no newline. Check them only at the end of
// the currently available chunk, never after every character of a banner.
func (m *loginMachine) handlePartialLine(writer io.Writer, line []byte, trace *tracer) (loginResult, error) {
	if len(line) > maxPartialPromptBytes || !utf8.Valid(line) {
		return loginPending, nil
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return loginPending, nil
	}

	if fullLineMatch(m.patterns.passwordChange, line) {
		// Input prompts can be acted on immediately; narrative messages must
		// wait for a real line ending, EOF, or the overall login timeout.
		if bytes.HasSuffix(line, []byte(":")) || bytes.HasSuffix(line, []byte("：")) {
			trace.printf("login password-change input prompt matched phase=%s", m.phase)
			return loginPasswordChangeRequired, nil
		}
		return loginPending, nil
	}
	// Pending does not mean safe to treat as a prompt. For example, "Invalid
	// user>" also resembles a shell prompt. Keep the entire text and state
	// until it becomes a complete diagnostic or a longer, harmless banner.
	// Apply this before *any* prompt action, including custom prompt rules.
	if fullLineMatch(m.patterns.failure, line) {
		return loginPending, nil
	}
	return m.handlePrompt(writer, line, trace)
}

// handlePrompt runs only after the caller has validated the appropriate line
// limit and given diagnostic messages precedence over interactive actions.
func (m *loginMachine) handlePrompt(writer io.Writer, line []byte, trace *tracer) (loginResult, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return loginPending, nil
	}
	if fullLineMatch(m.patterns.continuePrompt, line) {
		if m.continueResponses >= maxContinueResponses {
			trace.printf("login continue prompt ignored after limit phase=%s", m.phase)
			return loginPending, nil
		}
		trace.printf("login continue prompt matched phase=%s", m.phase)
		if err := writeEnter(writer, m.config.LineEnding); err != nil {
			return loginPending, err
		}
		m.continueResponses++
		return loginContinueSent, nil
	}

	if fullLineMatch(m.patterns.username, line) {
		if m.phase != loginAwaitPrompt || m.config.User == "" {
			trace.printf("login username prompt repeated or credential unavailable phase=%s", m.phase)
			return loginFailed, nil
		}
		trace.printf("login username prompt matched")
		if err := writeCredential(writer, m.config.User, m.config.LineEnding); err != nil {
			return loginPending, err
		}
		m.phase = loginAwaitPasswordOrResult
		return loginCredentialSent, nil
	}

	if fullLineMatch(m.patterns.password, line) {
		if m.phase == loginAwaitResult || m.config.Password == "" {
			trace.printf("login password prompt repeated or credential unavailable phase=%s", m.phase)
			return loginFailed, nil
		}
		trace.printf("login password prompt matched phase=%s", m.phase)
		if err := writeCredential(writer, m.config.Password, m.config.LineEnding); err != nil {
			return loginPending, err
		}
		m.phase = loginAwaitResult
		return loginCredentialSent, nil
	}

	if m.shellPromptMatches(line) {
		trace.printf("login shell prompt matched phase=%s", m.phase)
		return loginSuccess, nil
	}

	return loginPending, nil
}

func (m *loginMachine) shellPromptMatches(line []byte) bool {
	if m.patterns.shell != nil {
		return fullLineMatch(m.patterns.shell, line)
	}
	return defaultShellPromptMatches(line)
}

func defaultShellPromptMatches(line []byte) bool {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || len(line) > maxDefaultPromptBytes {
		return false
	}
	if defaultUnixBracketShellPrompt.Match(line) {
		return true
	}
	if isBannerLikeShellPrompt(string(line)) {
		return false
	}
	if defaultMarkedBracketShellPrompt.Match(line) {
		return true
	}
	if defaultTokenShellPrompt.Match(line) {
		return true
	}

	text := string(line)
	switch {
	case defaultAngleShellPrompt.Match(line):
		return !isBannerLabel(strings.TrimSuffix(strings.TrimPrefix(text, "<"), ">"))
	case defaultBracketShellPrompt.Match(line):
		return !isBannerLabel(strings.TrimSuffix(strings.TrimPrefix(text, "["), "]"))
	default:
		return false
	}
}

func isBannerLabel(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "warning", "notice", "error", "info", "alert", "message", "unauthorized", "denied":
		return true
	default:
		return false
	}
}

func isBannerLikeShellPrompt(value string) bool {
	value = strings.TrimSpace(strings.TrimRight(value, "#$>%"))
	fields := strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	for _, field := range fields {
		switch field {
		case "login", "successful", "failed", "failure", "password", "warning", "notice", "error", "press", "hit", "unauthorized", "denied", "attempt", "attempts", "continue",
			"登录成功", "登录失败", "认证成功", "认证失败", "密码错误", "密码过期", "系统警告", "警告", "错误":
			return true
		}
	}
	return false
}

func fullLineMatch(pattern *regexp.Regexp, line []byte) bool {
	if pattern == nil {
		return false
	}
	line = bytes.TrimSpace(line)
	match := pattern.FindIndex(line)
	return match != nil && match[0] == 0 && match[1] == len(line)
}

// login returns only a line terminator already consumed while confirming a
// prompt. All other bytes after the match stay in the shared buffered reader.
func login(conn net.Conn, reader *bufio.Reader, writer io.Writer, config clientConfig, trace *tracer) (replay string, err error) {
	machine := newLoginMachine(config)
	text := newLoginText(config.maxLoginBuffer)
	trace.printf("login start user=%t password=%t timeout=%s",
		config.User != "", config.Password != "", config.Timeout)
	guard := startLoginGuard(conn, config.Timeout)
	defer func() {
		guard.stop()
		// Do not return a usable Client if the timer won the completion race.
		if guard.didExpire() && err == nil {
			replay = ""
			err = fmt.Errorf("%w after %s", ErrLoginTimeout, config.Timeout)
		}
	}()

	for {
		// Peek(1) fills the 4 KiB buffer without waiting for a full block. Scan
		// what is already available, then discard only the bytes we consumed.
		if _, readErr := reader.Peek(1); readErr != nil {
			// A peer may close (or stop responding) after an error without a
			// newline. Classify that final message, but never send credentials
			// or infer success on a closed/timed-out connection.
			if errors.Is(readErr, io.EOF) || guard.didExpire() {
				if done, resultErr := resolveLoginResult(machine.messageResult(text.current(), trace), trace); done {
					return "", resultErr
				}
			}
			if guard.didExpire() {
				return "", fmt.Errorf("%w after %s", ErrLoginTimeout, config.Timeout)
			}
			return "", readErr
		}
		chunk, _ := reader.Peek(reader.Buffered())
		consumed, result, ending, scanErr := machine.consumeChunk(writer, text, chunk, trace)
		_, _ = reader.Discard(consumed)
		if scanErr != nil {
			if guard.didExpire() {
				return "", fmt.Errorf("%w after %s", ErrLoginTimeout, config.Timeout)
			}
			return "", scanErr
		}
		if done, resultErr := resolveLoginResult(result, trace); done {
			return ending, resultErr
		}
	}
}

func (m *loginMachine) consumeChunk(writer io.Writer, text *loginText, chunk []byte, trace *tracer) (int, loginResult, string, error) {
	for index, value := range chunk {
		line, ending := text.feedByte(value)
		if ending == "" {
			continue
		}
		result, err := m.handleLine(writer, line, trace)
		if err != nil || result == loginSuccess || result == loginFailed || result == loginPasswordChangeRequired {
			return index + 1, result, ending, err
		}
	}

	result, err := m.handlePartialLine(writer, text.current(), trace)
	ending := ""
	if result == loginSuccess && text.pendingCR {
		ending = "\r"
	}
	if isLoginAction(result) {
		text.consumeCurrent()
	}
	return len(chunk), result, ending, err
}

func isLoginAction(result loginResult) bool {
	return result == loginCredentialSent || result == loginContinueSent
}

func resolveLoginResult(result loginResult, trace *tracer) (bool, error) {
	switch result {
	case loginSuccess:
		trace.printf("login success")
		return true, nil
	case loginFailed:
		trace.printf("login failed")
		return true, ErrLoginFailed
	case loginPasswordChangeRequired:
		trace.printf("login password change required")
		return true, ErrPasswordChangeRequired
	case loginPending, loginCredentialSent, loginContinueSent:
		return false, nil
	default:
		return true, fmt.Errorf("%w: unknown login result %d", ErrProtocol, result)
	}
}

func writeCredential(writer io.Writer, value string, ending LineEnding) error {
	terminator := ending.bytes()
	data := make([]byte, 0, len(value)+len(terminator))
	data = append(data, value...)
	data = append(data, terminator...)
	return writeFull(writer, data)
}

func writeEnter(writer io.Writer, ending LineEnding) error {
	return writeFull(writer, ending.bytes())
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

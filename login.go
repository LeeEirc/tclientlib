package tclientlib

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	defaultUsernamePrompt = regexp.MustCompile(`(?i)^(?:login|username|user(?:\s*name)?|name|用户名|账\s*号)\s*[:：]?\s*$`)
	defaultPasswordPrompt = regexp.MustCompile(`(?i)^(?:password(?:\s+for\s+[^:：\r\n]+)?|passwd|passcode|口令|(?:[^:：\r\n]*的\s*)?密\s*码)\s*[:：]?\s*$`)
	defaultShellPrompt = regexp.MustCompile(
		`(?i)^(?:<[^<>\r\n]{1,126}>|\[[^\[\]\r\n]{1,126}\][#$]?|[a-z0-9_.:@/\\~()\-]{0,127}[#$>])\s*$`,
	)
	defaultFailurePrompt = regexp.MustCompile(
		`(?i)^\s*(?:%?\s*login\s+(?:incorrect|invalid|failed)|(?:error:\s*)?(?:failed\s+to\s+login|login\s+failed)|authentication\s+(?:failed|failure)|access\s+denied|permission\s+denied|invalid\s+(?:user(?:name)?|password)|incorrect\s+(?:user(?:name)?|password)|wrong\s+passwords?|bad\s+(?:passwords?|secrets?)|username\s+or\s+password\s+(?:error|incorrect)|登录失败|认证失败|用户名或密码(?:错误|不正确)|访问被拒绝)\s*[.!。]?\s*$`,
	)
	defaultPasswordChangePrompt = regexp.MustCompile(
		`(?i)^\s*(?:(?:warning:\s*)?.*(?:your\s+)?password\s+(?:has\s+)?expired.*|.*password\s+change\s+(?:is\s+)?(?:required|needed).*|.*(?:are\s+required|must)\s+(?:to\s+)?change\s+(?:your\s+)?password.*|.*change\s+(?:your\s+)?password\s+now.*|current\s+(?:unix\s+)?password\s*[:：]?|old\s+password\s*[:：]?|new\s+password\s*[:：]?|retype\s+(?:new\s+)?password\s*[:：]?|confirm\s+(?:new\s+)?password\s*[:：]?|.*密码(?:已经|已)?过期.*|.*(?:必须|需要)修改密码.*|当前密码\s*[:：]?|旧密码\s*[:：]?|新密码\s*[:：]?|确认新密码\s*[:：]?|重新输入(?:新)?密码\s*[:：]?)\s*$`,
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
}

func patternsFor(config clientConfig) loginPatterns {
	return loginPatterns{
		username:       selectPattern(config.UsernamePrompt, defaultUsernamePrompt),
		password:       selectPattern(config.PasswordPrompt, defaultPasswordPrompt),
		shell:          selectPattern(config.ShellPrompt, defaultShellPrompt),
		failure:        selectPattern(config.FailurePrompt, defaultFailurePrompt),
		passwordChange: selectPattern(config.PasswordChangePrompt, defaultPasswordChangePrompt),
	}
}

func selectPattern(custom, fallback *regexp.Regexp) *regexp.Regexp {
	if custom != nil {
		return custom
	}
	return fallback
}

type loginMachine struct {
	phase    loginPhase
	config   clientConfig
	patterns loginPatterns
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
	loginSuccess
	loginFailed
	loginPasswordChangeRequired
)

func (m *loginMachine) handleLine(writer io.Writer, line []byte, trace *tracer) (loginResult, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return loginPending, nil
	}

	if fullLineMatch(m.patterns.passwordChange, line) {
		trace.printf("login password-change prompt matched phase=%s", m.phase)
		return loginPasswordChangeRequired, nil
	}
	if fullLineMatch(m.patterns.failure, line) {
		trace.printf("login failure prompt matched phase=%s", m.phase)
		return loginFailed, nil
	}

	if fullLineMatch(m.patterns.username, line) {
		if m.phase != loginAwaitPrompt || m.config.User == "" {
			trace.printf("login username prompt repeated or credential unavailable phase=%s", m.phase)
			return loginFailed, nil
		}
		trace.printf("login username prompt matched")
		if err := writeCredential(writer, m.config.User); err != nil {
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
		if err := writeCredential(writer, m.config.Password); err != nil {
			return loginPending, err
		}
		m.phase = loginAwaitResult
		return loginCredentialSent, nil
	}

	if fullLineMatch(m.patterns.shell, line) {
		trace.printf("login shell prompt matched phase=%s", m.phase)
		return loginSuccess, nil
	}

	return loginPending, nil
}

func fullLineMatch(pattern *regexp.Regexp, line []byte) bool {
	if pattern == nil {
		return false
	}
	line = bytes.TrimSpace(line)
	match := pattern.FindIndex(line)
	return match != nil && match[0] == 0 && match[1] == len(line)
}

func login(conn net.Conn, reader io.Reader, writer io.Writer, config clientConfig, trace *tracer) error {
	machine := newLoginMachine(config)
	text := newLoginText(config.maxLoginBuffer)
	trace.printf("login start user=%t password=%t timeout=%s",
		config.User != "", config.Password != "", config.Timeout)
	guard := startLoginGuard(conn, config.Timeout)
	defer guard.stop()

	buffer := make([]byte, 4096)
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			lines, current := text.feed(buffer[:n])
			for _, line := range lines {
				result, err := machine.handleLine(writer, line, trace)
				if err != nil {
					trace.printf("login write error: %v", err)
					return err
				}
				if done, resultErr := resolveLoginResult(result, trace); done {
					return resultErr
				}
			}

			if len(bytes.TrimSpace(current)) > 0 {
				result, err := machine.handleLine(writer, current, trace)
				if err != nil {
					trace.printf("login write error: %v", err)
					return err
				}
				if result == loginCredentialSent {
					text.consumeCurrent()
				}
				if done, resultErr := resolveLoginResult(result, trace); done {
					return resultErr
				}
			}
		}
		if readErr != nil {
			if guard.didExpire() {
				trace.printf("login timeout after %s", config.Timeout)
				return fmt.Errorf("%w after %s", ErrLoginTimeout, config.Timeout)
			}
			trace.printf("login read error: %v", readErr)
			return readErr
		}
	}
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
	case loginPending, loginCredentialSent:
		return false, nil
	default:
		return true, fmt.Errorf("%w: unknown login result %d", ErrProtocol, result)
	}
}

func writeCredential(writer io.Writer, value string) error {
	data := make([]byte, 0, len(value)+2)
	data = append(data, value...)
	data = append(data, '\r', 0)
	return writeFull(writer, data)
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

type loginEscapeState uint8

const (
	loginEscapeNone loginEscapeState = iota
	loginEscapeStart
	loginEscapeCSI
	loginEscapeOSC
	loginEscapeOSCEscape
)

type loginText struct {
	line      []byte
	maxLine   int
	pendingCR bool
	escape    loginEscapeState
}

func newLoginText(maxLine int) *loginText {
	return &loginText{maxLine: maxLine}
}

func (t *loginText) feed(input []byte) ([][]byte, []byte) {
	lines := make([][]byte, 0, 2)
	for _, value := range input {
		if t.consumeEscape(value) {
			continue
		}

		if t.pendingCR {
			t.pendingCR = false
			if value == '\n' {
				lines = append(lines, t.finishLine())
				continue
			}
			// A bare carriage return moves the terminal cursor back to column 0.
			t.line = t.line[:0]
		}

		switch value {
		case 0, '\a':
			continue
		case '\x1b':
			t.escape = loginEscapeStart
		case '\r':
			t.pendingCR = true
		case '\n':
			lines = append(lines, t.finishLine())
		case '\b', 0x7f:
			t.removeLastRune()
		case '\t':
			t.appendByte(' ')
		default:
			if value < 0x20 {
				continue
			}
			t.appendByte(value)
		}
	}
	return lines, append([]byte(nil), t.line...)
}

func (t *loginText) consumeEscape(value byte) bool {
	switch t.escape {
	case loginEscapeNone:
		return false
	case loginEscapeStart:
		switch value {
		case '[':
			t.escape = loginEscapeCSI
		case ']':
			t.escape = loginEscapeOSC
		default:
			t.escape = loginEscapeNone
		}
		return true
	case loginEscapeCSI:
		if value >= 0x40 && value <= 0x7e {
			t.escape = loginEscapeNone
		}
		return true
	case loginEscapeOSC:
		switch value {
		case '\a':
			t.escape = loginEscapeNone
		case '\x1b':
			t.escape = loginEscapeOSCEscape
		}
		return true
	case loginEscapeOSCEscape:
		if value == '\\' {
			t.escape = loginEscapeNone
		} else {
			t.escape = loginEscapeOSC
		}
		return true
	default:
		t.escape = loginEscapeNone
		return true
	}
}

func (t *loginText) finishLine() []byte {
	line := append([]byte(nil), t.line...)
	t.line = t.line[:0]
	return line
}

func (t *loginText) consumeCurrent() {
	t.line = t.line[:0]
	t.pendingCR = false
}

func (t *loginText) appendByte(value byte) {
	t.line = append(t.line, value)
	if len(t.line) <= t.maxLine {
		return
	}
	drop := len(t.line) - t.maxLine
	copy(t.line, t.line[drop:])
	t.line = t.line[:t.maxLine]
}

func (t *loginText) removeLastRune() {
	if len(t.line) == 0 {
		return
	}
	_, size := utf8.DecodeLastRune(t.line)
	if size <= 0 {
		size = 1
	}
	t.line = t.line[:len(t.line)-size]
}

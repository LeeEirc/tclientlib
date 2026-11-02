package tclientlib

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	maxContinueResponses = 3
	maxDefaultPromptBytes = 128
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
	if fullLineMatch(m.patterns.continuePrompt, line) {
		if m.continueResponses >= maxContinueResponses {
			trace.printf("login continue prompt ignored after limit phase=%s", m.phase)
			return loginPending, nil
		}
		trace.printf("login continue prompt matched phase=%s", m.phase)
		if err := writeEnter(writer); err != nil {
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
	if defaultMarkedBracketShellPrompt.Match(line) {
		return true
	}
	if defaultTokenShellPrompt.Match(line) && !isBannerLikeShellPrompt(string(line)) {
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
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	for _, field := range fields {
		switch field {
		case "login", "successful", "failed", "failure", "password", "warning", "notice", "error", "press", "hit", "unauthorized", "denied", "attempt", "attempts", "continue":
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
				if isLoginAction(result) {
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

func writeCredential(writer io.Writer, value string) error {
	data := make([]byte, 0, len(value)+2)
	data = append(data, value...)
	data = append(data, '\r', 0)
	return writeFull(writer, data)
}

func writeEnter(writer io.Writer) error {
	return writeFull(writer, []byte{'\r', 0})
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

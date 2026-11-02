package tclientlib

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"regexp"
	"sync"
	"time"
)

var (
	defaultUsernamePrompt = regexp.MustCompile(`(?i)(?:login|username|name|user|用户名|账\s*号)\s*:?\s*$`)
	defaultPasswordPrompt = regexp.MustCompile(`(?i)(?:password|passwd|ssword|密\s*码)\s*:?\s*$`)
	defaultSuccessPrompt  = regexp.MustCompile(`(?im)(?:last\s+login|login\s+time|success|成功)|(?:^|[\r\n])[^\r\n]*[#$>]\s*$`)
	defaultFailurePrompt  = regexp.MustCompile(`(?i)incorrect|failed|username\s*or\s*password\s*error|authentication\s+failure|access\s+denied|失败|错误`)
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

type loginState struct {
	usernameSent bool
	passwordSent bool
}

func login(conn net.Conn, reader io.Reader, writer io.Writer, config clientConfig, trace *tracer) error {
	state := loginState{
		usernameSent: config.User == "",
		passwordSent: config.Password == "",
	}
	trace.printf("login start user=%t password=%t timeout=%s",
		config.User != "", config.Password != "", config.Timeout)
	guard := startLoginGuard(conn, config.Timeout)
	defer guard.stop()

	buffer := make([]byte, 4096)
	received := make([]byte, 0, 4096)
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			received = appendRolling(received, buffer[:n], config.maxLoginBuffer)
			result, err := handleLoginData(writer, received, config, &state, trace)
			if err != nil {
				trace.printf("login write error: %v", err)
				return err
			}
			switch result {
			case loginSuccess:
				trace.printf("login success")
				return nil
			case loginFailed:
				trace.printf("login failed")
				return ErrLoginFailed
			case loginCredentialSent:
				received = received[:0]
			case loginPending:
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

type loginResult uint8

const (
	loginPending loginResult = iota
	loginCredentialSent
	loginSuccess
	loginFailed
)

func handleLoginData(writer io.Writer, data []byte, config clientConfig, state *loginState, trace *tracer) (loginResult, error) {
	usernamePrompt := matchAny(data, defaultUsernamePrompt, config.UsernamePrompt)
	passwordPrompt := matchAny(data, defaultPasswordPrompt, config.PasswordPrompt)

	if !state.usernameSent && usernamePrompt {
		trace.printf("login username prompt matched")
		if err := writeCredential(writer, config.User); err != nil {
			return loginPending, err
		}
		state.usernameSent = true
		return loginCredentialSent, nil
	}
	if !state.passwordSent && passwordPrompt {
		trace.printf("login password prompt matched")
		if err := writeCredential(writer, config.Password); err != nil {
			return loginPending, err
		}
		state.passwordSent = true
		return loginCredentialSent, nil
	}

	if matchAny(data, defaultSuccessPrompt, config.SuccessPrompt) {
		return loginSuccess, nil
	}
	if matchAny(data, defaultFailurePrompt, config.FailurePrompt) {
		return loginFailed, nil
	}
	if state.usernameSent && config.User != "" && usernamePrompt {
		trace.printf("login username prompt repeated")
		return loginFailed, nil
	}
	if state.passwordSent && config.Password != "" && passwordPrompt {
		trace.printf("login password prompt repeated")
		return loginFailed, nil
	}
	return loginPending, nil
}

func writeCredential(writer io.Writer, value string) error {
	data := make([]byte, 0, len(value)+2)
	data = append(data, value...)
	data = append(data, '\r', 0) // NVT CR NUL
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

func appendRolling(dst, src []byte, limit int) []byte {
	if len(src) >= limit {
		return append(dst[:0], src[len(src)-limit:]...)
	}
	if len(dst)+len(src) > limit {
		drop := len(dst) + len(src) - limit
		copy(dst, dst[drop:])
		dst = dst[:len(dst)-drop]
	}
	return append(dst, src...)
}

func matchAny(data []byte, patterns ...*regexp.Regexp) bool {
	data = bytes.TrimSpace(data)
	for _, pattern := range patterns {
		if pattern != nil && pattern.Match(data) {
			return true
		}
	}
	return false
}

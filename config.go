package tclientlib

import (
	"fmt"
	"io"
	"regexp"
	"time"
)

const (
	defaultTimeout           = 15 * time.Second
	defaultWidth             = 80
	defaultHeight            = 24
	defaultTerminalType      = "xterm"
	defaultMaxLoginBuffer    = 64 * 1024
	defaultMaxSubnegotiation = 64 * 1024
)

// ClientConfig configures Dial and NewClientConn. Like ssh.ClientConfig, it is
// copied by the constructor and must be treated as immutable while connecting.
type ClientConfig struct {
	User     string
	Password string
	Timeout  time.Duration

	TerminalType string
	Width        int
	Height       int

	// LineEnding controls the terminator used by automatic username, password,
	// and continue responses. Its zero value is LineEndingCRLF. Client.Write is
	// not rewritten and remains an application-data writer.
	LineEnding LineEnding

	// UsernamePrompt, PasswordPrompt, and ShellPrompt supplement the built-in
	// rules: either the custom expression or the corresponding built-in
	// matcher may recognize a prompt. They use regexp.Match semantics on the
	// current normalized logical line with surrounding whitespace trimmed.
	// Unanchored expressions may match a substring; ^ and $ constrain only
	// the custom expression and do not disable the built-in matcher.
	// ShellPrompt describes the final interactive prompt, not a success
	// message in a login banner.
	UsernamePrompt *regexp.Regexp
	PasswordPrompt *regexp.Regexp
	ShellPrompt    *regexp.Regexp

	// FailurePrompt, PasswordChangePrompt, and ContinuePrompt replace the
	// corresponding built-in expression when non-nil. They use the same
	// regexp.Match semantics and current logical-line scope as the prompts
	// above. ContinuePrompt describes an intermediate prompt that should
	// receive an Enter key before login continues.
	FailurePrompt        *regexp.Regexp
	PasswordChangePrompt *regexp.Regexp
	ContinuePrompt       *regexp.Regexp

	// WrapStream optionally wraps the decoded Telnet application stream before
	// login and before it is exposed through Client.Read and Client.Write. It is
	// intended for charset conversion or similarly transparent stream adapters.
	WrapStream func(reader io.Reader, writer io.Writer) (io.Reader, io.Writer)

	// Trace receives line-oriented diagnostics for raw wire traffic, decoded
	// application data, Telnet negotiation, and login state transitions. It is
	// disabled when nil. Both directions are redacted during initial login, but
	// post-login commands and terminal output may still contain sensitive data.
	Trace io.Writer
}

type clientConfig struct {
	ClientConfig
	maxLoginBuffer    int
	maxSubnegotiation int
}

type transportConfig struct {
	User              string
	TerminalType      string
	Width             int
	Height            int
	Trace             io.Writer
	maxSubnegotiation int
}

func (c clientConfig) transportConfig() transportConfig {
	return transportConfig{
		User:              c.User,
		TerminalType:      c.TerminalType,
		Width:             c.Width,
		Height:            c.Height,
		Trace:             c.Trace,
		maxSubnegotiation: c.maxSubnegotiation,
	}
}

func prepareConfig(config *ClientConfig) (clientConfig, error) {
	if config == nil {
		return clientConfig{}, fmt.Errorf("%w: config is nil", ErrInvalidConfig)
	}

	full := clientConfig{
		ClientConfig:      *config,
		maxLoginBuffer:    defaultMaxLoginBuffer,
		maxSubnegotiation: defaultMaxSubnegotiation,
	}
	if full.Timeout <= 0 {
		full.Timeout = defaultTimeout
	}
	if full.TerminalType == "" {
		full.TerminalType = defaultTerminalType
	}
	if full.Width == 0 {
		full.Width = defaultWidth
	}
	if full.Height == 0 {
		full.Height = defaultHeight
	}
	if !full.LineEnding.valid() {
		return clientConfig{}, fmt.Errorf("%w: line ending %d", ErrInvalidConfig, full.LineEnding)
	}
	if full.Width < 0 || full.Height < 0 {
		return clientConfig{}, fmt.Errorf("%w: %dx%d", ErrInvalidWindowSize, full.Width, full.Height)
	}
	if full.Width > maxWindowSize {
		full.Width = maxWindowSize
	}
	if full.Height > maxWindowSize {
		full.Height = maxWindowSize
	}
	return full, nil
}

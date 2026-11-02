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

	// Prompt expressions replace the built-in expression for that event. They
	// are matched against one normalized logical line and must match the entire
	// line. ShellPrompt describes the final interactive prompt, not a success
	// message in a login banner.
	UsernamePrompt       *regexp.Regexp
	PasswordPrompt       *regexp.Regexp
	ShellPrompt          *regexp.Regexp
	FailurePrompt        *regexp.Regexp
	PasswordChangePrompt *regexp.Regexp

	// WrapStream optionally wraps the decoded Telnet application stream before
	// login and before it is exposed through Client.Read and Client.Write. It is
	// intended for charset conversion or similarly transparent stream adapters.
	WrapStream func(reader io.Reader, writer io.Writer) (io.Reader, io.Writer)

	// Trace receives line-oriented diagnostics for raw wire traffic, decoded
	// application data, Telnet negotiation, and login state transitions. It is
	// disabled when nil. Outgoing login data is redacted, but received terminal
	// output and post-login commands may still contain sensitive information.
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

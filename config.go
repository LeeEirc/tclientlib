package tclientlib

import (
	"regexp"
	"time"
)

type TerminalOptions struct {
	Wide     int
	High     int
	TermType string
}

func defaultTerminalOptions() TerminalOptions {
	return TerminalOptions{
		Wide:     80,
		High:     24,
		TermType: "xterm",
	}
}

type Config struct {
	Username   string
	Password   string
	Timeout    time.Duration
	TTYOptions *TerminalOptions

	UsernamePromptRegex     *regexp.Regexp
	PasswordPromptRegex     *regexp.Regexp
	LoginSuccessPromptRegex *regexp.Regexp

	BuiltinUsernamePromptRegex *regexp.Regexp
	BuiltinPasswordPromptRegex *regexp.Regexp
	BuiltinSuccessPromptRegex  *regexp.Regexp
	BuiltinFailureRegex        *regexp.Regexp
}

func (conf *Config) SetDefaults() {
	if conf.Timeout == 0 || conf.Timeout < defaultTimeout {
		conf.Timeout = defaultTimeout
	}
	t := defaultTerminalOptions()
	opts := conf.TTYOptions
	if opts == nil {
		conf.TTYOptions = &t
	} else {
		if opts.Wide == 0 {
			opts.Wide = t.Wide
		}
		if opts.High == 0 {
			opts.High = t.High
		}
		if opts.TermType == "" {
			opts.TermType = "xterm"
		}
	}
	if conf.BuiltinUsernamePromptRegex == nil {
		conf.BuiltinUsernamePromptRegex = DefaultUsernamePattern
	}
	if conf.BuiltinPasswordPromptRegex == nil {
		conf.BuiltinPasswordPromptRegex = DefaultPasswordPattern
	}
	if conf.BuiltinSuccessPromptRegex == nil {
		conf.BuiltinSuccessPromptRegex = DefaultLoginSuccessPattern
	}
	if conf.BuiltinFailureRegex == nil {
		conf.BuiltinFailureRegex = DefaultLoginFailedPattern
	}

}

type status struct {
	usernameDone bool
	passwordDone bool
}

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

	UsernameRegex     *regexp.Regexp
	PasswordRegex     *regexp.Regexp
	LoginFailureRegex *regexp.Regexp
	LoginSuccessRegex *regexp.Regexp
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
	if conf.UsernameRegex == nil {
		conf.UsernameRegex = usernamePattern
	}

	if conf.PasswordRegex == nil {
		conf.PasswordRegex = passwordPattern
	}

	if conf.LoginSuccessRegex == nil {
		conf.LoginSuccessRegex = defaultLoginSuccessPattern
	}
	if conf.LoginFailureRegex == nil {
		conf.LoginFailureRegex = defaultLoginFailedPattern
	}
}

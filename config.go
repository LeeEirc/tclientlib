package tclientlib

import (
	"regexp"
	"time"
)

type TerminalOptions struct {
	Wide  int
	High  int
	Xterm string
}

func defaultTerminalOptions() TerminalOptions {
	return TerminalOptions{
		Wide:  80,
		High:  24,
		Xterm: "xterm",
	}
}

type ClientConfig struct {
	User         string
	Password     string
	Timeout      time.Duration
	TTYOptions   *TerminalOptions
	CustomString string

	customSuccessPattern *regexp.Regexp
}

func (conf *ClientConfig) SetDefaults() {
	if conf.Timeout == 0 || conf.Timeout < defaultTimeout {
		conf.Timeout = defaultTimeout
	}
	t := defaultTerminalOptions()
	tops := conf.TTYOptions
	if tops == nil {
		conf.TTYOptions = &t
	} else {
		if tops.Wide == 0 {
			tops.Wide = t.Wide
		}
		if tops.High == 0 {
			tops.High = t.High
		}
		if tops.Xterm == "" {
			tops.Xterm = "xterm"
		}
	}
	if conf.CustomString != "" {
		if cusPattern, err := regexp.Compile(conf.CustomString); err == nil {
			conf.customSuccessPattern = cusPattern
		}
	}
}

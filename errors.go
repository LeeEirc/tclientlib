package tclientlib

import "errors"

var (
	ErrInvalidConfig         = errors.New("telnet: invalid client config")
	ErrLoginTimeout          = errors.New("telnet: login timeout")
	ErrLoginFailed           = errors.New("telnet: login failed")
	ErrProtocol              = errors.New("telnet: protocol error")
	ErrSubnegotiationTooLong = errors.New("telnet: subnegotiation too long")
	ErrInvalidWindowSize     = errors.New("telnet: invalid window size")
)

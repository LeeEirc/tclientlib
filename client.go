package tclientlib

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"sync"
	"time"
)

const defaultTimeout = time.Second * 15

type Log func(format string, v ...interface{})

var defaultStdoutF Log = func(format string, v ...interface{}) {
	if !strings.HasSuffix(format, "\r\n") {
		format += "\r\n"
	}
	fmt.Printf(format, v...)
}

type Client struct {
	conf          *Config
	sock          net.Conn
	enableWindows bool
	autoLogin     bool

	mux         sync.Mutex
	loginStatus *status
	LogF        Log
}

func (c *Client) handshake() error {
	if c.autoLogin {
		return c.loginAuthentication()
	}
	return nil
}

func (c *Client) loginAuthentication() error {
	buf := make([]byte, 1024)
	receivedBuf := bytes.NewBuffer(make([]byte, 0, 1024*2))
	defer receivedBuf.Reset()
	for {
		nr, err := c.Read(buf)
		if err != nil {
			return err
		}
		receivedBuf.Write(buf[:nr])
		result := c.handleLoginData(receivedBuf.Bytes())
		switch result {
		case AuthSuccess:
			_, _ = c.Write([]byte("\r\n"))
			return nil
		case AuthFailed:
			return ErrFailedLogin
		case AuthPartial:
			receivedBuf.Reset()
		default:
			continue
		}
	}
}

var ErrFailedLogin = errors.New("failed login")

func (c *Client) handleLoginData(data []byte) AuthStatus {
	if !c.loginStatus.usernameDone {
		usernameRes := []*regexp.Regexp{
			c.conf.BuiltinUsernamePromptRegex,
			c.conf.UsernamePromptRegex,
		}
		for i := range usernameRes {
			if usernameRes[i] != nil && usernameRes[i].Match(data) {
				_, _ = c.sock.Write([]byte(c.conf.Username))
				_, _ = c.sock.Write([]byte{'\r', BINARY})
				c.LogF("Username pattern match: %s", bytes.TrimSpace(data))
				c.loginStatus.usernameDone = true
				return AuthPartial
			}
		}
	}

	if !c.loginStatus.passwordDone {
		passwordRes := []*regexp.Regexp{
			c.conf.BuiltinPasswordPromptRegex,
			c.conf.PasswordPromptRegex,
		}
		for i := range passwordRes {
			if passwordRes[i] != nil && passwordRes[i].Match(data) {
				_, _ = c.sock.Write([]byte(c.conf.Password))
				_, _ = c.sock.Write([]byte{'\r', BINARY})
				c.LogF("Password pattern match: %s", bytes.TrimSpace(data))
				c.loginStatus.passwordDone = true
				return AuthPartial
			}
		}

		return AuthPartial
	}

	successRes := []*regexp.Regexp{
		c.conf.BuiltinSuccessPromptRegex,
		c.conf.LoginSuccessPromptRegex,
	}

	for i := range successRes {
		if successRes[i] != nil && successRes[i].Match(data) {
			c.LogF("Success pattern match: %s", bytes.TrimSpace(data))
			return AuthSuccess
		}
	}

	if c.conf.BuiltinFailureRegex.Match(data) {
		c.LogF("Incorrect pattern match:%s", bytes.TrimSpace(data))
		c.loginStatus.usernameDone = false
		c.loginStatus.passwordDone = false
		return AuthFailed
	}

	c.LogF("No match data: %s", bytes.TrimSpace(data))
	return AUTHUnknown
}

func (c *Client) replyOptionPackets(opts ...OptionPacket) error {
	var buf bytes.Buffer
	for i := range opts {
		buf.Write(opts[i].Bytes())
	}
	_, err := c.sock.Write(buf.Bytes())
	return err
}

func (c *Client) Read(p []byte) (int, error) {
	c.mux.Lock()
	defer c.mux.Unlock()
	innerBuf := make([]byte, len(p))
	var (
		ok     bool
		nr     int
		err    error
		packet OptionPacket
		remain []byte
	)
	// 劫持解析option的包，过滤处理 option packet
loop:
	for {
		nr, err = c.sock.Read(innerBuf)
		if err != nil {
			c.LogF("[Telnet client] read err: %s", err)
			return 0, err
		}
		remain = append(remain, innerBuf[:nr]...)
		for {
			if packet, remain, ok = ReadOptionPacket(remain); ok {
				optPackets := c.handleOptionPacket(packet)
				if err = c.replyOptionPackets(optPackets...); err != nil {
					c.LogF("[Telnet client] reply packets err %s", err)
					return 0, err
				}
				traceLogf("[Telnet client] server: %s ----> client: %s\r\n", packet, optPackets)
				continue
			}
			if packet.OptionCode != 0 || len(remain) == 0 {
				goto loop
			}
			break loop
		}
	}
	return copy(p, remain), err
}

func (c *Client) handleOptionPacket(p OptionPacket) []OptionPacket {
	var (
		replyPacket OptionPacket
	)
	replyPacket.CommandCode = p.CommandCode
	switch p.OptionCode {
	case SB:
		replyPacket.OptionCode = SB
		replyPacket.Parameters = make([]byte, 0)
		if len(p.Parameters) >= 1 {
			// subCommand 0 is , 1 Send , 2 INFO
			// VALUE     1
			// ESC       2
			// USERVAR   3
			switch p.Parameters[0] {
			case 1:
				switch p.CommandCode {
				case OLD_ENVIRON, NEW_ENVIRON:
					if c.conf.Username != "" {
						replyPacket.Parameters = append(replyPacket.Parameters, 0)
						replyPacket.Parameters = append(replyPacket.Parameters, []byte("USER")...)
						replyPacket.Parameters = append(replyPacket.Parameters, 1)
						replyPacket.Parameters = append(replyPacket.Parameters, []byte(c.conf.Username)...)
					}
				case TSPEED:
					replyPacket.Parameters = append(replyPacket.Parameters, 0)
					replyPacket.Parameters = append(replyPacket.Parameters, []byte(fmt.Sprintf(
						"%d,%d", 38400, 38400))...)
				case TTYPE:
					replyPacket.Parameters = append(replyPacket.Parameters, 0)
					replyPacket.Parameters = append(replyPacket.Parameters, []byte(fmt.Sprintf(
						"%s", c.conf.TTYOptions.TermType))...)
				default:
					replyPacket.OptionCode = WONT
				}
			default:
				replyPacket.OptionCode = WONT
			}
		} else {
			replyPacket.OptionCode = WONT
		}

	case DO:
		switch p.CommandCode {
		case TTYPE, TSPEED:
			replyPacket.OptionCode = WILL
		case NAWS:
			replyPacket.OptionCode = WILL
			c.enableWindows = true
			// 窗口大小
			var subOpt OptionPacket
			subOpt.OptionCode = SB
			subOpt.CommandCode = NAWS
			params := make([]byte, 4)
			binary.BigEndian.PutUint16(params[:2], uint16(c.conf.TTYOptions.Wide))
			binary.BigEndian.PutUint16(params[2:], uint16(c.conf.TTYOptions.High))
			subOpt.Parameters = params
			return []OptionPacket{replyPacket, subOpt}
		default:
			replyPacket.OptionCode = WONT
		}
	case WILL:
		switch p.CommandCode {
		case XDISPLOC:
			replyPacket.OptionCode = DONT
		default:
			replyPacket.OptionCode = DO
		}
	case DONT:
		replyPacket.OptionCode = WONT
	case WONT:
		replyPacket.OptionCode = DONT
	default:
		c.LogF("match option code unknown: %b", p.OptionCode)
	}
	return []OptionPacket{replyPacket}
}

func (c *Client) Write(b []byte) (int, error) {
	return c.sock.Write(b)
}

func (c *Client) Close() error {
	return c.sock.Close()
}

func (c *Client) WindowChange(w, h int) error {
	if !c.enableWindows {
		return nil
	}
	if w > MAX_WINDOW_WIDTH {
		w = MAX_WINDOW_WIDTH
	}
	if h > MAX_WINDOW_HEIGHT {
		h = MAX_WINDOW_HEIGHT
	}
	var p OptionPacket
	p.OptionCode = SB
	p.CommandCode = NAWS
	params := make([]byte, 4)
	binary.BigEndian.PutUint16(params[:2], uint16(w))
	binary.BigEndian.PutUint16(params[2:], uint16(h))
	p.Parameters = params
	if err := c.replyOptionPackets(p); err != nil {
		c.LogF("[Telnet client] window change %s", err)
		return err
	}
	c.conf.TTYOptions.Wide = w
	c.conf.TTYOptions.High = h
	return nil

}

func Dial(network, addr string, config *Config) (*Client, error) {
	conn, err := net.DialTimeout(network, addr, config.Timeout)
	if err != nil {
		return nil, err
	}
	return NewClientConn(conn, config)
}

func WithLogger(log Log) Opt {
	return func(client *Client) {
		client.LogF = log
	}
}

type Opt func(*Client)

func NewClientConn(conn net.Conn, config *Config, opts ...Opt) (*Client, error) {
	fullConf := *config
	fullConf.SetDefaults()
	var autoLogin bool
	if config.Username != "" && config.Password != "" {
		autoLogin = true
	}
	client := &Client{
		sock:      conn,
		conf:      &fullConf,
		autoLogin: autoLogin,
		loginStatus: &status{
			usernameDone: false,
			passwordDone: false,
		},
	}
	client.LogF = defaultStdoutF
	for _, opt := range opts {
		opt(client)
	}
	if err := client.handshake(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("telnet: handshake failed: %s", err)
	}
	return client, nil
}

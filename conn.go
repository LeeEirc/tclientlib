package tclientlib

import (
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"time"

	"log"
)

const prefixLen = 2
const defaultTimeout = time.Second * 15

type Client struct {
	conf    *ClientConfig
	sock    net.Conn
	prefix  [prefixLen]byte
	oneByte [1]byte

	enableWindows bool

	autoLogin bool
}

func (c *Client) clientHandshake() error {
	echoPacket := OptionPacket{OptionCode: DO, CommandCode: ECHO}
	SGAPacket := OptionPacket{OptionCode: DO, CommandCode: SGA}
	_ = c.replyOptionPacket(SGAPacket)
	_ = c.replyOptionPacket(echoPacket)
	for {
		p, err := c.readOptionPacket()
		if err != nil {
			log.Println("Telnet read option packet err: ", err)
			return err
		}
		switch p[0] {
		case IAC:
			c.handleOption(p)
		default:
			if c.autoLogin {
				return c.login()
			}
			log.Println("Telnet client manual login")
			return nil
		}

	}
}

func (c *Client) handleOption(option []byte) {
	var p OptionPacket
	log.Printf("Telnet server %s %s\n", CodeTOASCII[option[1]], CodeTOASCII[option[2]])
	p.OptionCode = option[1]
	cmd := option[2]
	p.CommandCode = cmd
	switch option[1] {
	case SB:
		switch cmd {
		case OLD_ENVIRON, NEW_ENVIRON:
		//	switch option[3] {
		//	case 1: // send command
		//		sub := subOptionPacket{subCommand: 0, options: make([]byte, 0)}
		//		sub.options = append(sub.options, 3)
		//		sub.options = append(sub.options, []byte(c.conf.User)...)
		//		p.Parameters = &sub
		//	}
		//	// subCommand 0 is , 1 Send , 2 INFO
		//	// VALUE     1
		//	// ESC       2
		//	// USERVAR   3
		//case TTYPE:
		//	switch option[3] {
		//	case 1: // send command
		//		sub := subOptionPacket{subCommand: 0, options: make([]byte, 0)}
		//		sub.options = append(sub.options, []byte(c.conf.TTYOptions.Xterm)...)
		//		p.Parameters = &sub
		//	}
		//case NAWS:
		//	sub := subOptionPacket{subCommand: IAC, options: make([]byte, 0)}
		//	sub.options = append(sub.options, []byte(fmt.Sprintf("%d%d%d%d",
		//		0, c.conf.TTYOptions.Wide, 0, c.conf.TTYOptions.High))...)
		//	p.Parameters = &sub
		//default:
		//	return
		//
		}
	default:
		switch option[1] {
		case DO:
			switch option[2] {
			case ECHO:
				p.OptionCode = WONT
			case TTYPE, NEW_ENVIRON:
				p.OptionCode = WILL
			case NAWS:
				p.OptionCode = WILL
				c.enableWindows = true
			default:
				p.OptionCode = WONT
			}
		case WILL:
			switch option[2] {
			case ECHO:
				p.OptionCode = DO
			case SGA:
				p.OptionCode = DO
			default:
				p.OptionCode = DONT
			}
		case DONT:
			p.OptionCode = WONT
		case WONT:
			p.OptionCode = DONT
		}
	}
	log.Printf("Telnet client %s %s\n", CodeTOASCII[p.OptionCode], CodeTOASCII[p.CommandCode])
	if err := c.replyOptionPacket(p); err != nil {
		log.Println("Telnet handler option err: ", err)
	}

}

func (c *Client) login() error {
	buf := make([]byte, 1024)
	for {
		nr, err := c.sock.Read(buf)
		if err != nil {
			return err
		}
		result := c.handleLoginData(buf[:nr])
		switch result {
		case AuthSuccess:
			return nil
		case AuthFailed:
			return errors.New("failed login")
		default:
			continue
		}

	}
}

func (c *Client) handleLoginData(data []byte) AuthStatus {
	if incorrectPattern.Match(data) {
		log.Printf("incorrect pattern match:%s \n", data)
		return AuthFailed
	} else if usernamePattern.Match(data) {
		_, _ = c.sock.Write([]byte(c.conf.User + "\r\n"))
		log.Printf("Username pattern match: %s \n", data)
		return AuthPartial
	} else if passwordPattern.Match(data) {
		_, _ = c.sock.Write([]byte(c.conf.Password + "\r\n"))
		log.Printf("Password pattern match: %s \n", data)
		return AuthPartial
	} else if successPattern.Match(data) {
		log.Printf("successPattern match: %s \n", data)
		return AuthSuccess
	}
	if c.conf.CustomString != "" && c.conf.customSuccessPattern != nil {
		if c.conf.customSuccessPattern.Match(data) {
			log.Printf("CustomString match: %s \n", data)
			return AuthSuccess
		}
	}
	log.Printf("unmatch data: %s \n", data)
	return AuthPartial
}

func (c *Client) readOptionPacket() ([]byte, error) {
	if _, err := io.ReadFull(c.sock, c.oneByte[:]); err != nil {
		return nil, err
	}
	p := make([]byte, 0, 3)
	p = append(p, c.oneByte[0])
	switch c.oneByte[0] {
	case IAC:
		if _, err := io.ReadFull(c.sock, c.prefix[:]); err != nil {
			return nil, err
		}
		p = append(p, c.prefix[:]...)
		switch c.prefix[0] {
		case SB:
			for {
				if _, err := io.ReadFull(c.sock, c.oneByte[:]); err != nil {
					return nil, err
				}
				switch c.oneByte[0] {
				case IAC:
					continue
				case SE:
					return p, nil
				default:
					p = append(p, c.oneByte[0])
				}
			}
		}
	}
	return p, nil
}

func (c *Client) replyOptionPacket(p OptionPacket) error {
	_, err := c.sock.Write(p.Bytes())
	return err
}

func (c *Client) Read(b []byte) (int, error) {
	return c.sock.Read(b)
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
	var p OptionPacket
	p.OptionCode = SB
	p.CommandCode = NAWS
	//sub := subOptionPacket{subCommand: IAC, options: make([]byte, 0)}
	//sub.options = append(sub.options, []byte(fmt.Sprintf("%d%d%d%d",
	//	c.conf.TTYOptions.Wide, w, c.conf.TTYOptions.High, h))...)
	//p.Parameters = &sub
	if err := c.replyOptionPacket(p); err != nil {
		return err
	}
	c.conf.TTYOptions.Wide = w
	c.conf.TTYOptions.High = h
	return nil

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

func Dial(network, addr string, config *ClientConfig) (*Client, error) {
	conn, err := net.DialTimeout(network, addr, config.Timeout)
	if err != nil {
		return nil, err
	}
	return NewClientConn(conn, config)
}

func NewClientConn(c net.Conn, config *ClientConfig) (*Client, error) {
	fullConf := *config
	fullConf.SetDefaults()
	var autoLogin bool
	if config.User != "" && config.Password != "" {
		autoLogin = true
	}
	conn := &Client{
		sock:      c,
		conf:      config,
		autoLogin: autoLogin,
	}
	if err := conn.clientHandshake(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("telnet: handshake failed: %s", err)
	}
	return conn, nil
}

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

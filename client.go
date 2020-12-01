package tclientlib

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"time"

	"log"
)

const defaultTimeout = time.Second * 15

type Client struct {
	conf          *ClientConfig
	sock          net.Conn
	enableWindows bool
	autoLogin     bool
}

func (c *Client) handshake() error {
	if c.autoLogin {
		return c.loginAuthentication()
	}
	return nil
}

func (c *Client) loginAuthentication() error {
	buf := make([]byte, 1024)
	for {
		nr, err := c.Read(buf)
		if err != nil {
			return err
		}
		result := c.handleLoginData(buf[:nr])
		switch result {
		case AuthSuccess:
			_, _ = c.Write([]byte("\r\n"))
			return nil
		case AuthFailed:
			return errors.New("failed login")
		default:
			continue
		}
	}
}

func (c *Client) handleLoginData(data []byte) AuthStatus {
	if c.conf.UsernameRegex.Match(data) {
		_, _ = c.sock.Write([]byte(c.conf.User + "\r\n"))
		log.Printf("Username pattern match: %s \n", bytes.TrimSpace(data))
		return AuthPartial
	} else if c.conf.PasswordRegex.Match(data) {
		_, _ = c.sock.Write([]byte(c.conf.Password + "\r\n"))
		log.Printf("Password pattern match: %s \n", bytes.TrimSpace(data))
		return AuthPartial
	} else if c.conf.LoginSuccessRegex.Match(data) {
		log.Printf("successPattern match: %s \n", bytes.TrimSpace(data))
		return AuthSuccess
	} else if c.conf.LoginFailureRegex.Match(data) {
		log.Printf("incorrect pattern match:%s \n", bytes.TrimSpace(data))
		return AuthFailed
	}
	log.Printf("unmatch data: %s \n", bytes.TrimSpace(data))
	return AuthPartial
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
	innerBuf := make([]byte, len(p))
	var (
		ok     bool
		nr     int
		err    error
		packet OptionPacket
		remain []byte
	)
	// 劫持解析option的包，过滤处理 option packet
	allPackets := make([]OptionPacket, 0, 5)
loop:
	for {
		nr, err = c.sock.Read(innerBuf)
		if err != nil {
			return 0, err
		}
		remain = innerBuf[:nr]

		for {
			if packet, remain, ok = ReadOptionPacket(remain); ok {
				allPackets = append(allPackets, c.handleOptionPacket(packet))
				continue
			}
			if len(allPackets) > 0 {
				if err := c.replyOptionPackets(allPackets...); err != nil {
					return 0, err
				}
			}
			allPackets = allPackets[:0]
			if len(remain) == 0 {
				goto loop
			}
			break loop
		}
	}
	return copy(p, remain), err
}

func (c *Client) handleOptionPacket(p OptionPacket) OptionPacket {
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
					if c.conf.User != "" {
						replyPacket.Parameters = append(replyPacket.Parameters, 0)
						replyPacket.Parameters = append(replyPacket.Parameters, []byte("USER")...)
						replyPacket.Parameters = append(replyPacket.Parameters, 1)
						replyPacket.Parameters = append(replyPacket.Parameters, []byte(c.conf.User)...)
					}
				case TSPEED:
					replyPacket.Parameters = append(replyPacket.Parameters, 0)
					replyPacket.Parameters = append(replyPacket.Parameters, []byte(fmt.Sprintf(
						"%d%d", 38400, 38400))...)
				case TTYPE:
					replyPacket.Parameters = append(replyPacket.Parameters, 0)
					replyPacket.Parameters = append(replyPacket.Parameters, []byte(fmt.Sprintf(
						"%s", c.conf.TTYOptions.TermType))...)
				default:
					replyPacket.OptionCode = WONT
				}
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
			//var extraPacket OptionPacket
			//extraPacket.CommandCode = p.CommandCode
			//extraPacket.OptionCode = SB
			//extraPacket.Parameters = make([]byte, 0)
			//extraPacket.Parameters = append(extraPacket.Parameters, []byte(fmt.Sprintf("%d%d%d%d",
			//	0, c.conf.TTYOptions.Wide, 0, c.conf.TTYOptions.High))...)
			//extraPackets = append(extraPackets, extraPacket)
			// 窗口大小
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
		log.Printf("match option code unknown: %b\n", p.OptionCode)
	}
	return replyPacket
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
	p.Parameters = []byte(fmt.Sprintf("%d%d%d%d",
		0, w, 0, h))
	if err := c.replyOptionPackets(p); err != nil {
		return err
	}
	c.conf.TTYOptions.Wide = w
	c.conf.TTYOptions.High = h
	return nil

}

func Dial(network, addr string, config *ClientConfig) (*Client, error) {
	conn, err := net.DialTimeout(network, addr, config.Timeout)
	if err != nil {
		return nil, err
	}
	return NewClientConn(conn, config)
}

func NewClientConn(conn net.Conn, config *ClientConfig) (*Client, error) {
	fullConf := *config
	fullConf.SetDefaults()
	var autoLogin bool
	if config.User != "" && config.Password != "" {
		autoLogin = true
	}
	client := &Client{
		sock:      conn,
		conf:      &fullConf,
		autoLogin: autoLogin,
	}
	if err := client.handshake(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("telnet: handshake failed: %s", err)
	}
	return client, nil
}

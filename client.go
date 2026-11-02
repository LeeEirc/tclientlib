package tclientlib

import (
	"fmt"
	"io"
	"net"
	"sync"
)

// Client is a single interactive Telnet connection. Unlike the wire-level
// transport, Read and Write operate only on terminal application data.
type Client struct {
	conn      net.Conn
	transport *transport
	reader    io.Reader
	writer    io.Writer

	closeOnce sync.Once
	closeErr  error
}

// Dial opens a network connection, performs Telnet negotiation while reading,
// and completes prompt-based login when User or Password is configured.
func Dial(network, addr string, config *ClientConfig) (*Client, error) {
	fullConfig, err := prepareConfig(config)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout(network, addr, fullConfig.Timeout)
	if err != nil {
		return nil, err
	}
	return newClientConn(conn, fullConfig)
}

// NewClientConn creates an authenticated Telnet client over an existing
// net.Conn. This is the counterpart of Dial for gateway and tunneled links.
func NewClientConn(conn net.Conn, config *ClientConfig) (*Client, error) {
	if conn == nil {
		return nil, fmt.Errorf("%w: connection is nil", ErrInvalidConfig)
	}
	fullConfig, err := prepareConfig(config)
	if err != nil {
		return nil, err
	}
	return newClientConn(conn, fullConfig)
}

func newClientConn(conn net.Conn, config clientConfig) (*Client, error) {
	transport := newTransport(conn, config)
	var reader io.Reader = transport
	var writer io.Writer = transport
	if config.WrapStream != nil {
		reader, writer = config.WrapStream(reader, writer)
		if reader == nil || writer == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: WrapStream returned nil", ErrInvalidConfig)
		}
	}

	client := &Client{
		conn:      conn,
		transport: transport,
		reader:    reader,
		writer:    writer,
	}
	if config.User != "" || config.Password != "" {
		if err := login(conn, reader, writer, config); err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("telnet: handshake failed: %w", err)
		}
	}
	return client, nil
}

func (c *Client) Read(p []byte) (int, error) {
	return c.reader.Read(p)
}

// Write sends terminal application data. Telnet IAC bytes are escaped by the
// transport after optional stream wrappers have encoded the data.
func (c *Client) Write(p []byte) (int, error) {
	return c.writer.Write(p)
}

// WindowChange updates the remote terminal size. Its argument order matches
// ssh.Session.WindowChange: height first, width second.
func (c *Client) WindowChange(height, width int) error {
	return c.transport.windowChange(height, width)
}

// SendCommand sends a standalone Telnet command without passing it through the
// application stream or charset wrapper.
func (c *Client) SendCommand(command Command) error {
	return c.transport.sendCommand(command)
}

func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		if closer, ok := c.writer.(io.Closer); ok {
			_ = closer.Close()
		}
		c.closeErr = c.conn.Close()
	})
	return c.closeErr
}

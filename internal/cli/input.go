package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/LeeEirc/tclientlib"
	"golang.org/x/term"
)

const escapeKey = 0x1d // Ctrl+]

var errQuit = errors.New("local quit")

type interactiveClient interface {
	io.Writer
	SendCommand(tclientlib.Command) error
	TerminalMode() tclientlib.TerminalMode
}

// terminalInput maps local keys, not arbitrary application data. A CR's LF or
// NUL follower is consumed even when it arrives in a later stdin read. This
// prevents a pasted CRLF from becoming two Enter keys.
type terminalInput struct {
	client      interactiveClient
	local       io.Writer
	ending      []byte
	echo        string
	afterCR     bool
	commandMode bool
	command     []byte
	tooLong     bool
}

func (t *terminalInput) feed(p []byte) error {
	for _, b := range p {
		if t.afterCR {
			t.afterCR = false
			if b == '\n' || b == 0 {
				continue
			}
		}
		if t.commandMode {
			if err := t.commandByte(b); err != nil {
				return err
			}
			continue
		}
		if b == escapeKey {
			t.commandMode = true
			t.command = t.command[:0]
			t.tooLong = false
			if _, err := io.WriteString(t.local, "\r\ntclient> "); err != nil {
				return err
			}
			continue
		}
		data := []byte{b}
		if b == '\r' || b == '\n' {
			data = t.ending
			t.afterCR = b == '\r'
		}
		if err := writeFull(t.client, data); err != nil {
			return err
		}
		if t.localEcho() {
			switch b {
			case '\r', '\n':
				data = []byte("\r\n")
			case 8, 127:
				data = []byte("\b \b")
			}
			if err := writeFull(t.local, data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (t *terminalInput) localEcho() bool {
	return t.echo == "on" || t.echo == "auto" && !t.client.TerminalMode().RemoteEcho
}

func (t *terminalInput) commandByte(b byte) error {
	switch b {
	case escapeKey:
		if len(t.command) == 0 && !t.tooLong {
			t.commandMode = false
			if _, err := io.WriteString(t.local, "\r\n"); err != nil {
				return err
			}
			return writeFull(t.client, []byte{escapeKey})
		}
	case '\r', '\n':
		t.afterCR = b == '\r'
		if _, err := io.WriteString(t.local, "\r\n"); err != nil {
			return err
		}
		if t.tooLong {
			if _, err := io.WriteString(t.local, "Command too long.\r\n"); err != nil {
				return err
			}
		} else if err := t.executeCommand(); err != nil {
			return err
		}
		t.command = t.command[:0]
		t.tooLong = false
		if t.commandMode {
			_, err := io.WriteString(t.local, "tclient> ")
			return err
		}
	case 3: // Ctrl+C cancels a local command; in session mode it is sent remotely.
		t.commandMode = false
		t.command = t.command[:0]
		_, err := io.WriteString(t.local, "^C\r\n")
		return err
	case 8, 127:
		if len(t.command) > 0 && !t.tooLong {
			t.command = t.command[:len(t.command)-1]
			_, err := io.WriteString(t.local, "\b \b")
			return err
		}
	default:
		// Commands are ASCII. Ignore terminal control sequences and cap storage.
		if b >= 32 && b < 127 && !t.tooLong {
			if len(t.command) == 1024 {
				t.tooLong = true
				return nil
			}
			t.command = append(t.command, b)
			return writeFull(t.local, []byte{b})
		}
	}
	return nil
}

func (t *terminalInput) executeCommand() error {
	words := strings.Fields(string(t.command))
	if len(words) == 0 || len(words) == 1 && words[0] == "continue" {
		t.commandMode = false
		return nil
	}
	if len(words) == 1 {
		switch words[0] {
		case "quit":
			return errQuit
		case "help":
			_, err := io.WriteString(t.local, "help | status | send brk/ip/ayt/nop | continue (or Enter) | quit\r\nCtrl+] twice sends a literal Ctrl+]. Input is character-at-a-time.\r\n")
			return err
		case "status":
			m := t.client.TerminalMode()
			_, err := fmt.Fprintf(t.local, "Remote echo: %t; local echo: %t; binary send/receive: %t/%t; SGA send/receive: %t/%t\r\n", m.RemoteEcho, t.localEcho(), m.SendBinary, m.ReceiveBinary, m.LocalSuppressGA, m.RemoteSuppressGA)
			return err
		}
	}
	if len(words) == 2 && words[0] == "send" {
		commands := map[string]tclientlib.Command{
			"brk": tclientlib.CommandBreak, "ip": tclientlib.CommandInterruptProcess,
			"ayt": tclientlib.CommandAreYouThere, "nop": tclientlib.CommandNoOperation,
		}
		if command, ok := commands[words[1]]; ok {
			return t.client.SendCommand(command)
		}
	}
	_, err := io.WriteString(t.local, "Unknown command. Use help.\r\n")
	return err
}

func copyInput(ctx context.Context, input *os.File, client interactiveClient, terminal *terminalInput) error {
	buffer := make([]byte, 4096)
	for {
		n, err := readInput(ctx, input, buffer)
		if n > 0 {
			var writeErr error
			if terminal != nil {
				writeErr = terminal.feed(buffer[:n])
			} else {
				writeErr = writeFull(client, buffer[:n])
			}
			if writeErr != nil {
				return writeErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func readPassword(ctx context.Context, input *os.File, output io.Writer) (password string, afterCR bool, retErr error) {
	fd := int(input.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", false, err
	}
	defer func() {
		if err := term.Restore(fd, state); retErr == nil {
			retErr = err
		}
		_, _ = io.WriteString(output, "\r\n")
	}()
	if _, err := io.WriteString(output, "Password: "); err != nil {
		return "", false, err
	}
	var secret []byte
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()
	var single [1]byte
	for {
		n, err := readInput(ctx, input, single[:])
		if err != nil {
			return "", false, err
		}
		if n == 0 {
			return "", false, io.EOF
		}
		switch single[0] {
		case '\r', '\n':
			return string(secret), single[0] == '\r', nil
		case 3:
			return "", false, context.Canceled
		case 4:
			return "", false, io.EOF
		case 8, 127:
			if len(secret) > 0 {
				_, size := utf8.DecodeLastRune(secret)
				start := len(secret) - size
				for i := start; i < len(secret); i++ {
					secret[i] = 0
				}
				secret = secret[:start]
			}
		case 21: // Ctrl+U clears the hidden line.
			for i := range secret {
				secret[i] = 0
			}
			secret = secret[:0]
		default:
			if len(secret) == 64*1024 {
				return "", false, fmt.Errorf("password is too long")
			}
			secret = append(secret, single[0])
		}
	}
}

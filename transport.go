package tclientlib

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
)

type transport struct {
	conn    net.Conn
	config  transportConfig
	decoder *decoder
	trace   *tracer

	readMu     sync.Mutex
	writeMu    sync.Mutex
	stateMu    sync.Mutex
	readBuffer []byte
	pending    []byte
	pendingErr error
	nvtCR      bool // Last received NVT application byte was CR; protected by readMu.

	localOptions           [256]bool
	remoteOptions          [256]bool
	oldEnvironmentReversed bool
}

func newTransport(conn net.Conn, config transportConfig) *transport {
	return &transport{
		conn:       conn,
		config:     config,
		decoder:    newDecoder(config.maxSubnegotiation),
		trace:      newTracer(config.Trace),
		readBuffer: make([]byte, 4096),
	}
}

func (t *transport) setTraceRedaction(enabled bool) {
	t.trace.setRedaction(enabled)
}

func (t *transport) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	t.readMu.Lock()
	defer t.readMu.Unlock()

	for {
		if len(t.pending) > 0 {
			n := copy(p, t.pending)
			t.pending = t.pending[n:]
			if len(t.pending) == 0 {
				t.pending = nil
			}
			return n, nil
		}
		if t.pendingErr != nil {
			err := t.pendingErr
			t.pendingErr = nil
			return 0, err
		}

		n, readErr := t.conn.Read(t.readBuffer)
		if n > 0 {
			raw := t.readBuffer[:n]
			t.trace.bytes("wire", "recv", raw)
			data, events, decodeErr := t.decoder.feed(raw)
			offset := 0
			for _, event := range events {
				t.receiveData(data[offset:event.dataOffset])
				offset = event.dataOffset
				t.trace.event("recv", event)
				if err := t.handleEvent(event); err != nil && t.pendingErr == nil {
					t.pendingErr = err
				}
			}
			t.receiveData(data[offset:])
			if decodeErr != nil && t.pendingErr == nil {
				t.trace.printf("decode error: %v", decodeErr)
				t.pendingErr = decodeErr
			}
		}
		if readErr != nil && t.pendingErr == nil {
			t.trace.printf("wire recv error: %v", readErr)
			t.pendingErr = readErr
		}
		if len(t.pending) > 0 || t.pendingErr != nil || n == 0 {
			continue
		}
	}
}

// receiveData maps the NVT stream before charset conversion. CR is delivered
// immediately so a server's standalone CR cannot delay interactive output.
// Only its following NUL is removed; CR LF and unrelated NULs are preserved.
// Read calls this even for empty spans between events so a BINARY transition
// cannot leave a CR pending from the previous mode.
func (t *transport) receiveData(data []byte) {
	t.stateMu.Lock()
	binary := t.remoteOptions[binaryOption]
	t.stateMu.Unlock()

	start := len(t.pending)
	if binary {
		t.nvtCR = false
		t.pending = append(t.pending, data...)
	} else {
		for _, value := range data {
			if t.nvtCR && value == 0 {
				t.nvtCR = false
				continue
			}
			t.pending = append(t.pending, value)
			t.nvtCR = value == '\r'
		}
	}
	t.trace.bytes("application", "recv", t.pending[start:])
}

func (t *transport) Write(p []byte) (int, error) {
	t.trace.bytes("application", "send", p)
	encoded := appendEscapedIAC(make([]byte, 0, len(p)), p)
	written, err := t.writeRaw(encoded)
	if err != nil {
		return sourceBytesWritten(p, written), err
	}
	return len(p), nil
}

func sourceBytesWritten(source []byte, encodedBytes int) int {
	consumed := 0
	for _, value := range source {
		width := 1
		if value == iac {
			width = 2
		}
		if encodedBytes < width {
			break
		}
		encodedBytes -= width
		consumed++
	}
	return consumed
}

func (t *transport) sendCommand(command Command) error {
	if !validCommand(command) {
		return fmt.Errorf("%w: command %d", ErrProtocol, command)
	}
	t.trace.printf("command send %s", telnetCommandName(byte(command)))
	return t.writeAll([]byte{iac, byte(command)})
}

func (t *transport) windowChange(height, width int) error {
	if height <= 0 || width <= 0 {
		return fmt.Errorf("%w: %dx%d", ErrInvalidWindowSize, width, height)
	}
	if height > maxWindowSize {
		height = maxWindowSize
	}
	if width > maxWindowSize {
		width = maxWindowSize
	}

	// Serialize the new size and its packet with option changes and other
	// window updates; an old snapshot must not overtake a later DONT or size.
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	t.stateMu.Lock()
	t.config.Height = height
	t.config.Width = width
	enabled := t.localOptions[windowSize]
	packet := t.windowPacketLocked()
	t.stateMu.Unlock()

	t.trace.printf("window change height=%d width=%d negotiated=%t", height, width, enabled)
	if !enabled {
		return nil
	}
	return t.writePacketsLocked(packet)
}

func (t *transport) handleEvent(event telnetEvent) error {
	if event.typ != negotiationEvent && event.typ != subnegotiationEvent {
		return nil
	}
	// Keep option state changes and their replies ahead of concurrent writes.
	// In particular, data chosen using SendBinary must not overtake WILL.
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	switch event.typ {
	case negotiationEvent:
		return t.writePacketsLocked(t.handleNegotiation(event.command, event.option)...)
	case subnegotiationEvent:
		return t.writePacketsLocked(t.handleSubnegotiation(event.option, event.parameters)...)
	default:
		return nil
	}
}

func (t *transport) handleNegotiation(command, option byte) []optionPacket {
	t.stateMu.Lock()
	defer t.stateMu.Unlock()

	switch command {
	case do:
		if !t.supportsLocalOption(option) {
			return []optionPacket{{command: wont, option: option}}
		}
		if t.localOptions[option] {
			return nil
		}
		t.localOptions[option] = true
		if option == windowSize {
			return []optionPacket{
				{command: will, option: option},
				t.windowPacketLocked(),
			}
		}
		return []optionPacket{{command: will, option: option}}

	case dont:
		if !t.localOptions[option] {
			return nil
		}
		t.localOptions[option] = false
		return []optionPacket{{command: wont, option: option}}

	case will:
		if !t.supportsRemoteOption(option) {
			return []optionPacket{{command: dont, option: option}}
		}
		if t.remoteOptions[option] {
			return nil
		}
		t.remoteOptions[option] = true
		return []optionPacket{{command: do, option: option}}

	case wont:
		if !t.remoteOptions[option] {
			return nil
		}
		t.remoteOptions[option] = false
		return []optionPacket{{command: dont, option: option}}
	}
	return nil
}

func (t *transport) handleSubnegotiation(option byte, parameters []byte) []optionPacket {
	if len(parameters) == 0 || parameters[0] != 1 {
		return nil
	}

	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	if !t.localOptions[option] {
		return nil
	}

	switch option {
	case terminalType:
		params := append([]byte{0}, []byte(t.config.TerminalType)...)
		return []optionPacket{{command: sb, option: option, parameters: params}}
	case terminalSpeed:
		return []optionPacket{{command: sb, option: option, parameters: []byte("\x0038400,38400")}}
	case oldEnvironment, newEnvironment:
		params, reversed := environmentResponse(parameters[1:], t.config.User,
			option == oldEnvironment, t.oldEnvironmentReversed, t.config.maxSubnegotiation)
		if params == nil {
			return nil
		}
		if option == oldEnvironment {
			t.oldEnvironmentReversed = reversed
		}
		return []optionPacket{{command: sb, option: option, parameters: params}}
	default:
		return nil
	}
}

func (t *transport) supportsLocalOption(option byte) bool {
	switch option {
	case binaryOption, suppressGAOption, terminalType, windowSize, terminalSpeed:
		return true
	case oldEnvironment, newEnvironment:
		return t.config.User != ""
	default:
		return false
	}
}

func (t *transport) supportsRemoteOption(option byte) bool {
	switch option {
	case binaryOption, echoOption, suppressGAOption:
		return true
	default:
		return false
	}
}

func (t *transport) windowPacketLocked() optionPacket {
	params := make([]byte, 4)
	binary.BigEndian.PutUint16(params[:2], uint16(t.config.Width))
	binary.BigEndian.PutUint16(params[2:], uint16(t.config.Height))
	return optionPacket{command: sb, option: windowSize, parameters: params}
}

func (t *transport) writePacketsLocked(packets ...optionPacket) error {
	if len(packets) == 0 {
		return nil
	}
	data := make([]byte, 0, len(packets)*3)
	for _, packet := range packets {
		t.trace.packet("send", packet)
		data = append(data, packet.bytes()...)
	}
	_, err := t.writeRawLocked(data)
	return err
}

func (t *transport) writeAll(data []byte) error {
	_, err := t.writeRaw(data)
	return err
}

func (t *transport) writeRaw(data []byte) (int, error) {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	return t.writeRawLocked(data)
}

func (t *transport) writeRawLocked(data []byte) (int, error) {
	total := 0
	for len(data) > 0 {
		n, err := t.conn.Write(data)
		if n > 0 {
			t.trace.bytes("wire", "send", data[:n])
		}
		total += n
		data = data[n:]
		if err != nil {
			t.trace.printf("wire send error: %v", err)
			return total, err
		}
		if n == 0 {
			return total, io.ErrShortWrite
		}
	}
	return total, nil
}

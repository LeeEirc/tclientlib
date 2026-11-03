package tclientlib

import (
	"fmt"
	"io"
	"sync"
)

const maxTraceBytes = 256

type tracer struct {
	writer io.Writer
	mu     sync.Mutex
}

func newTracer(writer io.Writer) *tracer {
	if writer == nil {
		return nil
	}
	return &tracer{writer: writer}
}

func (t *tracer) printf(format string, args ...interface{}) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = fmt.Fprintf(t.writer, "telnet: "+format+"\n", args...)
}

func (t *tracer) bytes(scope, direction string, data []byte) {
	if t == nil || len(data) == 0 {
		return
	}
	sample, suffix := boundedTraceData(data)
	t.printf("%s %s len=%d data=% x%s", scope, direction, len(data), sample, suffix)
}

func (t *tracer) redacted(scope, direction string, length int) {
	if t == nil || length == 0 {
		return
	}
	t.printf("%s %s len=%d data=<redacted>", scope, direction, length)
}

func (t *tracer) event(direction string, event telnetEvent) {
	if t == nil {
		return
	}
	switch event.typ {
	case negotiationEvent:
		t.printf("event %s %s %s", direction,
			telnetCommandName(event.command), telnetOptionName(event.option))
	case subnegotiationEvent:
		sample, suffix := boundedTraceData(event.parameters)
		t.printf("event %s SB %s len=%d data=% x%s", direction,
			telnetOptionName(event.option), len(event.parameters), sample, suffix)
	case commandEvent:
		t.printf("event %s %s", direction, telnetCommandName(event.command))
	}
}

func (t *tracer) packet(direction string, packet optionPacket) {
	if t == nil {
		return
	}
	if packet.command == sb {
		sample, suffix := boundedTraceData(packet.parameters)
		t.printf("packet %s SB %s len=%d data=% x%s", direction,
			telnetOptionName(packet.option), len(packet.parameters), sample, suffix)
		return
	}
	t.printf("packet %s %s %s", direction,
		telnetCommandName(packet.command), telnetOptionName(packet.option))
}

func boundedTraceData(data []byte) ([]byte, string) {
	if len(data) <= maxTraceBytes {
		return data, ""
	}
	return data[:maxTraceBytes], fmt.Sprintf(" truncated=%d", len(data)-maxTraceBytes)
}

func telnetCommandName(command byte) string {
	switch command {
	case se:
		return "SE"
	case nop:
		return "NOP"
	case dm:
		return "DM"
	case brk:
		return "BRK"
	case ip:
		return "IP"
	case ao:
		return "AO"
	case ayt:
		return "AYT"
	case ec:
		return "EC"
	case el:
		return "EL"
	case ga:
		return "GA"
	case sb:
		return "SB"
	case will:
		return "WILL"
	case wont:
		return "WONT"
	case do:
		return "DO"
	case dont:
		return "DONT"
	case iac:
		return "IAC"
	default:
		return fmt.Sprintf("COMMAND(%d)", command)
	}
}

func telnetOptionName(option byte) string {
	switch option {
	case binaryOption:
		return "BINARY"
	case echoOption:
		return "ECHO"
	case suppressGAOption:
		return "SGA"
	case terminalType:
		return "TTYPE"
	case windowSize:
		return "NAWS"
	case terminalSpeed:
		return "TSPEED"
	case oldEnvironment:
		return "OLD_ENVIRON"
	case newEnvironment:
		return "NEW_ENVIRON"
	default:
		return fmt.Sprintf("OPTION(%d)", option)
	}
}

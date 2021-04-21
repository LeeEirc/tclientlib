package tclientlib

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

type OptionPacket struct {
	OptionCode  byte
	CommandCode byte
	Parameters  []byte // SB parameters
}

func (p OptionPacket) Bytes() []byte {
	var buf bytes.Buffer
	buf.WriteByte(IAC)
	buf.WriteByte(p.OptionCode)
	buf.WriteByte(p.CommandCode)
	if p.Parameters != nil {
		buf.Write(p.Parameters)
		buf.WriteByte(IAC)
		buf.WriteByte(SE)
	}
	return buf.Bytes()
}

func (p OptionPacket) String() string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("IAC %s %s",
		CodeTOASCII[p.OptionCode],
		CodeTOASCII[p.CommandCode]))
	if p.Parameters != nil {
		builder.WriteString(" ")
		builder.WriteString(ConvertSubOptions(p.CommandCode, p.Parameters))
		builder.WriteString(" ")
		builder.WriteString("IAC SE")
	}
	return builder.String()

}

func ConvertSubOptions(commandCode byte, parameters []byte) string {
	switch commandCode {
	case NAWS:
		// NAWS (Negotiate About Window Size)
		if len(parameters) != 4 {
			var s strings.Builder
			for i := range parameters {
				s.WriteString(fmt.Sprintf("%q", parameters[i]))
			}
			return s.String()
		}

		return fmt.Sprintf("%d %d (%d) %d %d (%d)",
			parameters[0],
			parameters[1],
			binary.BigEndian.Uint16(parameters[:2]),
			parameters[2],
			parameters[3],
			binary.BigEndian.Uint16(parameters[2:]),
		)
	default:
		var builder strings.Builder
		for i := range parameters {
			builder.WriteString(fmt.Sprintf("%q", parameters[i]))
			builder.WriteString(" ")
		}
		return builder.String()
	}
}

func ReadOptionPacket(p []byte) (packet OptionPacket, rest []byte, ok bool) {
	if len(p) < 3 {
		return packet, p, false
	}
	indexIAC := bytes.IndexByte(p, IAC)
	if indexIAC < 0 {
		return packet, p, false
	}
	rest = make([]byte, 0, len(p))
	rest = append(rest, p[:indexIAC]...)

	if len(p[indexIAC:]) >= 3 {
		packet.OptionCode = p[indexIAC+1]
		packet.CommandCode = p[indexIAC+2]
		remain := p[indexIAC+3:]
		switch packet.OptionCode {
		case WILL, WONT, DO, DONT:
			rest = append(rest, remain...)
			return packet, rest, true
		case SB:
			indexSE := bytes.IndexByte(remain, SE)
			if indexSE < 0 {
				traceLogf("failed index SE: packet(%s) %v\r\n", packet, remain)
				// ENVIRON valid send no var
				rest = append(rest, remain...)
				return packet, p, false
			}
			packet.Parameters = make([]byte, len(remain[:indexSE])-1)
			copy(packet.Parameters, remain[:indexSE])
			rest = append(rest, remain[indexSE+1:]...)
			return packet, rest, true
		default:
			traceLogf("failed found packet %s %v\r\n", packet, remain)
		}
	}
	return packet, p, false
}

func parseParameters(commandCode byte, f func(params []byte, result interface{}) error) {

}

/*
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build
*/

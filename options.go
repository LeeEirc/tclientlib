package tclientlib

import (
	"bytes"
	"fmt"
	"log"
	"strings"
)

type OptionPacket struct {
	OptionCode  byte
	CommandCode byte
	Parameters  []byte // SB parameters
}

func (p *OptionPacket) Bytes() []byte {
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
		return fmt.Sprintf("%d %d %d %d",
			parameters[0],
			parameters[1],
			parameters[2],
			parameters[3],
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
	if len(p) == 0 {
		return
	}
	if p[0] == IAC && len(p) >= 3 {
		packet.OptionCode = p[1]
		packet.CommandCode = p[2]
		switch p[1] {
		case WILL, WONT, DO, DONT:
			return packet, p[3:], true
		case SB:
			remain := p[3:]
			index := bytes.IndexByte(remain, SE)
			if index < 0 {
				log.Printf("%d %v\n", index, remain)
				// ENVIRON valid send no var
				return packet, remain[3:], true
			}
			packet.Parameters = make([]byte, len(remain[:index])-1)
			copy(packet.Parameters, remain[:index])
			return packet, remain[index+1:], true
		default:
			log.Printf("%v %v\n", p[1], p[2:])
		}
	}
	return packet, p, false
}

func parseParameters(commandCode byte, f func(params []byte, result interface{}) error) {

}

/*
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build
*/

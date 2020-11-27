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
	_, _ = builder.WriteString(fmt.Sprintf("IAC %s %s",
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
			log.Panic("parameters should 4\n")
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
				log.Panicf("%d %v", index, remain)
			}
			packet.Parameters = make([]byte, len(remain[:index])-1)
			copy(packet.Parameters, remain[:index])
			return packet, remain[index+1:], true
		default:
			log.Panicf("%v %v", p[1], p[2:])
		}
	}
	return packet, p, false
}

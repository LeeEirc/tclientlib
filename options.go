package tclientlib

import (
	"bytes"
)

type optionPacket struct {
	optionCode  byte
	commandCode byte
	subOption   *subOptionPacket
}

func (p *optionPacket) Bytes() []byte {
	var buf bytes.Buffer
	buf.WriteByte(IAC)
	buf.WriteByte(p.optionCode)
	buf.WriteByte(p.commandCode)
	if p.subOption != nil {
		buf.Write(p.subOption.Bytes())
		buf.WriteByte(IAC)
		buf.WriteByte(SE)
	}
	return buf.Bytes()
}

type subOptionPacket struct {
	subCommand byte
	options    []byte
}

func (s *subOptionPacket) Bytes() []byte {
	if s.subCommand == IAC {
		return s.options
	}
	cp := make([]byte, len(s.options)+1)
	copy(cp, []byte{s.subCommand})
	copy(cp[1:], s.options)
	return cp
}

package tclientlib

type optionPacket struct {
	command    byte
	option     byte
	parameters []byte
}

func (p optionPacket) bytes() []byte {
	if p.command != sb {
		return []byte{iac, p.command, p.option}
	}

	result := make([]byte, 0, len(p.parameters)+5)
	result = append(result, iac, sb, p.option)
	result = appendEscapedIAC(result, p.parameters)
	result = append(result, iac, se)
	return result
}

func appendEscapedIAC(dst, src []byte) []byte {
	for _, value := range src {
		dst = append(dst, value)
		if value == iac {
			dst = append(dst, iac)
		}
	}
	return dst
}

package tclientlib

// LineEnding controls the line terminator used by automatic login actions.
// It applies to usernames, passwords, and automatic continue responses. It
// does not rewrite data passed to Client.Write.
type LineEnding uint8

const (
	// LineEndingCRLF sends the Telnet NVT newline sequence and is the default.
	LineEndingCRLF LineEnding = iota
	// LineEndingCRNUL sends a carriage return without a line feed.
	LineEndingCRNUL
	// LineEndingCR sends a bare carriage return for devices that require it.
	LineEndingCR
	// LineEndingLF sends a bare line feed for devices that require it.
	LineEndingLF
)

func (ending LineEnding) valid() bool {
	return ending >= LineEndingCRLF && ending <= LineEndingLF
}

func (ending LineEnding) bytes() []byte {
	switch ending {
	case LineEndingCRLF:
		return []byte{'\r', '\n'}
	case LineEndingCRNUL:
		return []byte{'\r', 0}
	case LineEndingCR:
		return []byte{'\r'}
	case LineEndingLF:
		return []byte{'\n'}
	default:
		return nil
	}
}

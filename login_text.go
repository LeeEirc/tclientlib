package tclientlib

import "unicode/utf8"

type loginEscapeState uint8

const (
	loginEscapeNone loginEscapeState = iota
	loginEscapeStart
	loginEscapeCSI
	loginEscapeOSC
	loginEscapeOSCEscape
)

type loginText struct {
	line      []byte
	maxLine   int
	pendingCR bool
	escape    loginEscapeState
	overflow  bool
}

func newLoginText(maxLine int) *loginText {
	return &loginText{maxLine: maxLine}
}

// feedByte performs only bounded, linear text normalization. Returned line
// slices alias the scanner and are valid until the next call. CRLF/LF complete
// a line; a bare CR overwrites it. Read/chunk boundaries never complete a line.
func (t *loginText) feedByte(value byte) ([]byte, string) {
	if t.consumeEscape(value) {
		return nil, ""
	}
	if value == 0 || value == '\a' {
		return nil, ""
	}
	if value == '\x1b' {
		t.escape = loginEscapeStart
		return nil, ""
	}
	if t.pendingCR {
		t.pendingCR = false
		if value == '\n' {
			return t.finishLine(), "\r\n"
		}
		t.line = t.line[:0]
		t.overflow = false
	}
	switch value {
	case '\r':
		t.pendingCR = true
	case '\n':
		return t.finishLine(), "\n"
	case '\b', 0x7f:
		t.removeLastRune()
	case '\t':
		t.appendByte(' ')
	default:
		if value >= 0x20 {
			t.appendByte(value)
		}
	}
	return nil, ""
}

// Incomplete escape sequences survive chunk boundaries. UTF-8 validation is
// performed only on bounded prompt candidates, not on every long-line prefix.
func (t *loginText) current() []byte {
	if t.overflow || t.escape != loginEscapeNone {
		return nil
	}
	return t.line
}

func (t *loginText) consumeEscape(value byte) bool {
	switch t.escape {
	case loginEscapeNone:
		return false
	case loginEscapeStart:
		switch value {
		case '[':
			t.escape = loginEscapeCSI
		case ']':
			t.escape = loginEscapeOSC
		default:
			t.escape = loginEscapeNone
		}
		return true
	case loginEscapeCSI:
		if value >= 0x40 && value <= 0x7e {
			t.escape = loginEscapeNone
		}
		return true
	case loginEscapeOSC:
		switch value {
		case '\a':
			t.escape = loginEscapeNone
		case '\x1b':
			t.escape = loginEscapeOSCEscape
		}
		return true
	case loginEscapeOSCEscape:
		if value == '\\' {
			t.escape = loginEscapeNone
		} else {
			t.escape = loginEscapeOSC
		}
		return true
	default:
		t.escape = loginEscapeNone
		return true
	}
}

func (t *loginText) finishLine() []byte {
	line := t.current()
	t.line = t.line[:0]
	t.overflow = false
	return line
}

func (t *loginText) consumeCurrent() {
	t.line = t.line[:0]
	t.pendingCR = false
	t.overflow = false
}

func (t *loginText) appendByte(value byte) {
	if len(t.line) >= t.maxLine {
		// Do not slide a truncated banner's suffix into a synthetic prompt.
		// Ignore this logical line until its next reset or line boundary.
		t.overflow = true
		return
	}
	if !t.overflow {
		t.line = append(t.line, value)
	}
}

func (t *loginText) removeLastRune() {
	if len(t.line) == 0 {
		return
	}
	_, size := utf8.DecodeLastRune(t.line)
	if size <= 0 {
		size = 1
	}
	t.line = t.line[:len(t.line)-size]
}

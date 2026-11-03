package tclientlib

// TerminalMode is a snapshot of the currently negotiated terminal options.
// Each direction is independent. The library never changes the caller's local
// terminal settings. Options are processed as Client.Read consumes the stream.
type TerminalMode struct {
	RemoteEcho       bool
	LocalSuppressGA  bool
	RemoteSuppressGA bool
	SendBinary       bool
	ReceiveBinary    bool
}

// TerminalMode returns a concurrency-safe snapshot of negotiated options.
// SendBinary describes the negotiated wire mode; Write continues to preserve
// application bytes (apart from IAC escaping) in either mode. Callers remain
// responsible for choosing complete CR LF or CR NUL sequences in NVT mode.
func (c *Client) TerminalMode() TerminalMode {
	t := c.transport
	t.stateMu.Lock()
	defer t.stateMu.Unlock()
	return TerminalMode{
		RemoteEcho:       t.remoteOptions[echoOption],
		LocalSuppressGA:  t.localOptions[suppressGAOption],
		RemoteSuppressGA: t.remoteOptions[suppressGAOption],
		SendBinary:       t.localOptions[binaryOption],
		ReceiveBinary:    t.remoteOptions[binaryOption],
	}
}

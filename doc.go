// Package tclientlib provides an SSH-style client API for a single interactive
// Telnet connection. Dial and NewClientConn optionally perform prompt-based
// login when credentials are configured; without credentials they return a
// client for manual interaction. Client.Read and Client.Write exchange terminal
// application data while Telnet framing and option negotiation remain internal.
// TerminalMode reports the negotiated modes without changing the local terminal.
package tclientlib

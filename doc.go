// Package tclientlib provides an SSH-style client API for a single interactive
// Telnet connection. Dial and NewClientConn return an authenticated Client;
// Client.Read and Client.Write exchange terminal application data while Telnet
// framing and option negotiation remain internal.
package tclientlib

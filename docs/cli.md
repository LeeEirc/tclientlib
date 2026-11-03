# tclient CLI

`tclient` uses the same Telnet transport and optional login state machine as the
library. It is an interactive client and a way to reproduce a connection outside
Koko. It is not a complete GNU/NetKit Telnet replacement.

## Build and connect

```bash
go build ./cmd/tclient
./tclient 192.0.2.10
./tclient 192.0.2.10 2323
./tclient 2001:db8::10 23
```

The port defaults to 23. Options go before the host. Use `--help` for the full
option list. This client supports plain TCP Telnet; it does not encrypt traffic.

Interactive terminal support currently targets Unix systems. The PTY regression
suite runs on Linux; macOS can build the same Unix implementation. Windows
supports redirected input and output, with native CI tests for cancelling
blocked pipe operations. Windows rejects terminal input before changing console
settings.

The default is **manual login**. Server prompts, menus, password changes, and
subsequent interactions are displayed without trying to infer authentication
success. No environment username is sent in this mode.

To use the library's automatic login behavior explicitly:

```bash
./tclient --auto-login --user admin --ask-pass 192.0.2.10 23
```

`--ask-pass` reads the password from the terminal without displaying it. There
is no command-line password flag. `--user` and `--ask-pass` require
`--auto-login`, which avoids confusing environment-based Telnet `-l` behavior
with prompt-based authentication. An automatic login failure or required
password change ends the connection; it does not silently fall back to manual
mode. A username-only automatic login is also supported.

`--timeout` bounds connection establishment and initial automatic login. It is
not an interactive-session idle timeout.

## Input, output, and character encoding

```bash
./tclient --charset gbk --line-ending crnul 192.0.2.10 23
./tclient --echo off 192.0.2.10 23
```

`--charset` supports `utf-8`, `gbk`, `gb18030`, and `big5`. Incoming bytes are
decoded before login matching and display; outgoing UTF-8 input is encoded
before Telnet IAC escaping. Charset conversion is a single stream shared by
login and interaction, not a second Telnet implementation.

For terminal input, Enter maps to `--line-ending`:

| Value | Bytes sent |
| --- | --- |
| `crlf` (default) | CR LF |
| `crnul` | CR NUL |
| `cr` | CR |
| `lf` | LF |

A CR followed by LF or NUL is treated as one Enter even when split across
input reads. This prevents pasted CRLF text from producing extra empty lines.
The selected terminator also configures automatic login. The choice is an
application input policy and stays in effect if BINARY is negotiated.

The interactive client sends characters as they are typed. `--echo auto`
enables local echo when remote ECHO is not negotiated, and disables it when the
server announces `WILL ECHO`. `on` and `off` override local echo explicitly.
This is character-at-a-time interaction, without local line editing or
RFC 1184 LINEMODE. Echo negotiation is not inferred from password prompt text.
Use the server's echo policy or an explicit `--echo off` for a peer that does
not negotiate password echo correctly.

Local raw mode is used only for terminal input and is restored before returning
from the CLI, including connection errors, remote disconnects, local quit, and
handled termination signals. Terminal dimensions are sent when NAWS is
negotiated; supported Unix systems also send changes after SIGWINCH. Dimensions
are refreshed after automatic login, and resize writes run separately from the
session loop so a slow peer cannot block local quit or disconnect handling.

For redirected input, no raw terminal mode, local echo, escape commands, or
Enter rewriting is applied:

```bash
printf 'show version\r\nexit\r\n' | ./tclient 192.0.2.10 23
```

Charset conversion, if requested, still applies. After stdin EOF, the encoder
is flushed and the CLI continues reading final server output. The write side
stays open so the library can still reply to Telnet negotiation. Include the
device's logout command when appropriate. `--drain-timeout` bounds this wait
(default 5 seconds); a server that does not close within this interval produces
an I/O error. Redirected output receives application bytes without local
status messages or local echo; these go to stderr.

For an endpoint that needs TCP EOF before producing its reply, explicitly use
`--close-write`. This half-closes TCP after the input encoder has been flushed.
Further Telnet negotiation cannot be answered after half-close, so this option
is appropriate only when the endpoint does not require subsequent negotiation.

## Local commands

Press **Ctrl+]** in an interactive terminal to open the local command prompt:

```text
tclient> help
tclient> status
tclient> send brk
tclient> send ip
tclient> send ayt
tclient> send nop
tclient> continue
tclient> quit
```

`status` reports the negotiated terminal options. `continue` resumes input to
the remote endpoint. These command names are processed locally and are never
sent as terminal text. `send` calls the library's existing `SendCommand` API.
It does not implement TCP urgent data or Telnet SYNCH.

## Tracing

Tracing is disabled by default. To enable it explicitly:

```bash
./tclient --trace-file session.trace 192.0.2.10 23
```

The CLI exclusively creates a new file with permission mode `0600`; it refuses
to overwrite an existing file. **Manual passwords, commands, and output may be
recorded.** Automatic-login redaction only covers the library's initial login
phase. A warning is printed when trace recording is enabled. Filesystem access
controls on non-Unix platforms follow that platform's file-permission behavior.

## Exit codes

| Code | Meaning |
| --- | --- |
| 0 | Normal remote close or local quit; help also succeeds |
| 1 | Connection, transport, terminal, output, or drain failure |
| 2 | Invalid arguments or configuration |
| 3 | Automatic-login failure or required password change |

## Protocol boundaries

The library decodes NVT `CR NUL` on receive unless the remote direction has
negotiated BINARY. BINARY transitions are applied in wire order, including
several transitions within one network read. `CR LF`, standalone NUL, and
binary data are preserved. Telnet IAC escaping still applies in both modes.

`Client.Write` preserves its existing contract: the caller supplies application
bytes and line endings; the transport escapes IAC. It does not silently change
bare CR/LF, buffer a trailing CR, or rewrite binary streams. Only the CLI's TTY
input maps Enter keys. Callers that require NVT newlines must send complete
CR LF or CR NUL sequences themselves.

SGA is accepted independently in both directions. NEW-ENVIRON exports only the
configured USER and respects requested names, ordering, undefined values, and
environment escaping. The legacy ENVIRON option also recognizes peers with
reversed VAR/VALUE control codes. No process environment is exported.

This release does not implement active option toggles, complete half-duplex GA
turn-taking, local line editing, LINEMODE, TCP urgent/SYNCH, `.telnetrc`, Kerberos,
Telnet encryption, or a general Expect scripting language. A successful test
against a simulated endpoint does not certify every network-device firmware.

Relevant specifications: [RFC 854](https://www.rfc-editor.org/rfc/rfc854),
[RFC 856](https://www.rfc-editor.org/rfc/rfc856),
[RFC 858](https://www.rfc-editor.org/rfc/rfc858),
[RFC 1571](https://www.rfc-editor.org/rfc/rfc1571), and
[RFC 1572](https://www.rfc-editor.org/rfc/rfc1572).

## Old example entry point

`_example/telnet_client` delegates to the same implementation as `cmd/tclient`.
It no longer maintains its own terminal, signal, password, or trace logic.
Its flags have changed: use positional `host [port]` instead of `-ip` and
`-port`; use `--auto-login --user ... --ask-pass` instead of `-username` and
`-password`.

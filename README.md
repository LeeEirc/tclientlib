# tclientlib

`tclientlib` is a small Go Telnet client for interactive host and network-device
sessions. Its public API follows the same principle as `golang.org/x/crypto/ssh`:
constructors return a ready-to-use client (with optional prompt-based login),
application reads and writes never expose protocol framing, and protocol controls
use explicit methods.

## Command-line client

Install the interactive client with Go 1.20 or later:

```bash
go install github.com/LeeEirc/tclientlib/cmd/tclient@latest
```

Or build the current checkout:

```bash
go build ./cmd/tclient
./tclient 192.0.2.10 23
./tclient --auto-login --user admin --ask-pass 192.0.2.10 23
./tclient --charset gbk --line-ending crnul 192.0.2.10 23
```

The default is manual interaction. `Ctrl+]` opens local commands such as `help`,
`status`, `send brk`, and `quit`. The client handles terminal restoration, window
changes, negotiated remote echo, and redirected input. See [the CLI guide](docs/cli.md)
for options, exit codes, tracing, and compatibility boundaries.

## API

```go
type ClientConfig struct {
    User     string
    Password string
    Timeout  time.Duration

    TerminalType string
    Width        int
    Height       int
    LineEnding   LineEnding

    UsernamePrompt       *regexp.Regexp
    PasswordPrompt       *regexp.Regexp
    ShellPrompt          *regexp.Regexp
    FailurePrompt        *regexp.Regexp
    PasswordChangePrompt *regexp.Regexp
    ContinuePrompt       *regexp.Regexp

    WrapStream func(io.Reader, io.Writer) (io.Reader, io.Writer)
    Trace      io.Writer
}

func Dial(network, addr string, config *ClientConfig) (*Client, error)
func NewClientConn(conn net.Conn, config *ClientConfig) (*Client, error)

func (*Client) Read([]byte) (int, error)
func (*Client) Write([]byte) (int, error)
func (*Client) Close() error
func (*Client) WindowChange(height, width int) error
func (*Client) SendCommand(Command) error
func (*Client) TerminalMode() TerminalMode
```

There is deliberately one I/O path:

- `Client.Write` always writes terminal application data and escapes Telnet
  `IAC` bytes internally.
- `Client.Read` returns terminal application data only, removing the protocol
  NUL after a carriage return in the non-BINARY receive direction.
- `SendCommand` sends Telnet controls such as `CommandBreak`.
- `Dial` and `NewClientConn` perform initial prompt-based login when credentials
  are configured.

The library does not expose raw writes, a second data writer, or a separate
public login lifecycle.

`TerminalMode()` returns a concurrency-safe snapshot of `RemoteEcho`,
`LocalSuppressGA`, `RemoteSuppressGA`, `SendBinary`, and `ReceiveBinary`. Modes
change as `Read` consumes negotiation packets. The library never changes the
caller's local terminal and does not start a background reader.

Receive NVT decoding follows the exact position of BINARY negotiation in the
stream, including changes within one network read. `CR LF` and standalone NUL
are retained; `CR NUL` becomes `CR` outside BINARY mode. `Client.Write` continues
to preserve caller-supplied CR/LF/NUL bytes and escape IAC. The caller is
responsible for choosing appropriate outgoing line endings; the CLI maps TTY
Enter keys and automatic login uses `LineEnding`.

The client accepts SGA in both directions. Environment negotiation exposes only
the explicitly configured `User`: requested variables are returned in order,
unknown variables are marked undefined, and environment control bytes are
escaped. Process environment variables are never exported.

## Basic usage

```go
config := &tclientlib.ClientConfig{
    User:         "admin",
    Password:     "secret",
    Timeout:      15 * time.Second,
    TerminalType: "xterm-256color",
    Width:        120,
    Height:       40,
}

client, err := tclientlib.Dial("tcp", "192.0.2.10:23", config)
if err != nil {
    return err
}
defer client.Close()

go io.Copy(os.Stdout, client)
_, err = io.Copy(client, os.Stdin)
```

`WindowChange` follows `ssh.Session.WindowChange` and accepts height before
width:

```go
err := client.WindowChange(height, width)
```

Send a Telnet break without constructing raw protocol bytes:

```go
err := client.SendCommand(tclientlib.CommandBreak)
```

## Login matching

Telnet does not define an authentication protocol. The client therefore treats
login as a small expect-style state machine instead of searching an entire
banner for broad success or failure words.

The state machine keeps authentication decisions strict while allowing common
prompt wording:

- username prompts include forms such as `Login:`, `Enter username:`,
  `Please enter your username:`, and `Login as:`;
- password prompts include `Password:`, `Password for admin:`,
  `Please input your password:`, and `Password (required):`;
- default shell detection supports common Unix and network-device forms such as
  `router#`, `my device #`, `host%`, `switch(config)#`, `<HUAWEI>`,
  `[HUAWEI]`, and `[user@host ~]$`;
- explicit failures may include a bounded suffix such as `please try again` or
  `please retry`, while login-history counters remain ordinary banners;
- prompts such as `Press RETURN to get started!` and `Press ENTER to continue`
  receive an Enter key and then continue waiting for the real shell prompt.

Automatic continuation is limited to three responses per connection, preventing
a malformed or hostile endpoint from causing an infinite response loop.

Before matching, login output is normalized into logical terminal lines:

- Telnet framing has already been removed;
- an optional charset wrapper has already decoded the bytes;
- NUL and BEL are ignored;
- backspace and carriage-return overwrite behavior is applied;
- common ANSI CSI and OSC sequences are removed.

Username, password, shell, failure, password-change, and continuation
expressions are matched against one normalized line. A custom expression
replaces the built-in expression for that event and must match the complete
line. `ShellPrompt` represents the final interactive prompt, not a success
message in a banner. Text such as `Last login`, `Login successful`, or `成功`
is not considered proof of authentication.

For example:

```go
config.ShellPrompt = regexp.MustCompile(`(?:<[^>]+>|router(?:\([^)]*\))?[#>])`)
config.FailurePrompt = regexp.MustCompile(`(?:Login incorrect|Access denied)`)
config.ContinuePrompt = regexp.MustCompile(`Press Enter to open the console`)
```

Built-in username and password prompts deliberately require prompt punctuation.
An unusual device that emits a bare prompt without `:` or `：` should provide a
platform-specific expression rather than making the global fallback ambiguous.

Login and the interactive session share one buffered reader. The login state
machine stops as soon as the final shell prompt is recognized. Bytes already
received after that prompt remain buffered for the next `Client.Read`; they are
not discarded and the matched prompt itself is not replayed.

### Automatic line endings

Automatic usernames, passwords, and continue responses use `CR LF` by default.
This does not rewrite data passed to `Client.Write`. Devices with non-standard
input behavior can select another terminator:

```go
config.LineEnding = tclientlib.LineEndingCRNUL // CR NUL
config.LineEnding = tclientlib.LineEndingCR    // bare CR
config.LineEnding = tclientlib.LineEndingLF    // bare LF
```

Supported values are:

- `LineEndingCRLF` — default Telnet NVT newline;
- `LineEndingCRNUL` — carriage return without a line feed;
- `LineEndingCR` — bare carriage return;
- `LineEndingLF` — bare line feed.

When the remote side requires a password change, constructors return an error
that matches `ErrPasswordChangeRequired`:

```go
client, err := tclientlib.Dial("tcp", address, config)
if errors.Is(err, tclientlib.ErrPasswordChangeRequired) {
    // Handle password rotation in the calling application's credential workflow.
}
```

The built-in password-change matcher recognizes direct requirements such as
`Your password has expired`, `Current UNIX password:`, `New password:`, and
`Retype new password:`. Informational text such as `Your password will expire in
7 days` or `Password expired policy: disabled` remains a banner. The library
deliberately does not choose or submit a new password.

## Charset conversion

The same wrapped stream is used for both initial login and the interactive
session. This lets callers decode non-UTF-8 prompts before matching them without
introducing a second constructor or login API:

```go
config.WrapStream = func(reader io.Reader, writer io.Writer) (io.Reader, io.Writer) {
    return transform.NewReader(reader, decoder), transform.NewWriter(writer, encoder)
}
```

## Debugging Telnet traffic

Set `ClientConfig.Trace` to an `io.Writer` to enable line-oriented diagnostics:

```go
traceFile, err := os.Create("telnet.trace")
if err != nil {
    return err
}
defer traceFile.Close()

config.Trace = traceFile
```

The trace includes:

- raw wire bytes in both directions;
- decoded Telnet application data;
- parsed `WILL`, `WONT`, `DO`, `DONT`, command, and subnegotiation events;
- generated negotiation responses;
- login state transitions and terminal resize events.

Tracing is disabled when `Trace` is nil. Each byte dump is bounded, so a large
terminal read does not produce an unbounded log line. Both received and sent byte
dumps are replaced by `<redacted>` while the initial login state machine is
active; semantic negotiation and login-state events remain visible.

**Warning:** post-login commands and terminal output may still contain sensitive
information. Store traces securely and remove them after troubleshooting.

The CLI can write a trace explicitly (the file must not already exist):

```bash
go run ./cmd/tclient --trace-file telnet.trace 192.0.2.10 23
```

Manual login does not run the automatic-login redaction phase. A trace can
therefore contain manually typed passwords as well as commands and output. The
CLI warns when tracing is enabled and creates the file with mode `0600`.

`_example/telnet_client` now delegates to the same CLI, with the same flags.
Replace its old `-ip`/`-port` flags with positional `host [port]`, and replace
`-username`/`-password` with `--auto-login --user ... --ask-pass`.

For failures involving another Telnet client, or for observing traffic without
changing application code, use the transparent monitor:

```bash
cd _example/monitor
go run . -listen 127.0.0.1:9999 -target 192.0.2.10:23
```

Then point the client at `127.0.0.1:9999`. The monitor records raw bidirectional
stream chunks and does not depend on exported parser internals.

The monitor and `Trace` observe the TCP byte stream. User-space `Read` boundaries
are not the original TCP packet boundaries. Use `tcpdump` or Wireshark when the
actual network packet layout, retransmission, or timing is relevant.

## Scope and privilege switching

`tclientlib` owns Telnet framing, option negotiation, initial account login,
terminal resize, and application data transport. Device-specific privilege
switching remains above the transport because it is CLI behavior rather than
part of the Telnet protocol.

JumpServer Koko currently performs these operations after the client connects:

- Cisco: `enable`
- Huawei: `super 15`
- H3C: `super level-15`
- Linux/Unix: `su`, `sudo`, `only_su`, and `only_sudo`

## Historically tested devices

This list records historical project testing and is not a vendor allowlist:

- H3C
  - S7508E
  - S7506E
  - S7503E
- Huawei
  - S5720
  - S6720
  - S12708

The protocol implementation is vendor-neutral and is also used by Koko for
other platforms, including Cisco devices.

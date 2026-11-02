# tclientlib

`tclientlib` is a small Go Telnet client for interactive host and network-device
sessions. Its public API follows the same principle as `golang.org/x/crypto/ssh`:
constructors return a ready-to-use authenticated client, application reads and
writes never expose protocol framing, and protocol controls use explicit methods.

## API

```go
type ClientConfig struct {
    User     string
    Password string
    Timeout  time.Duration

    TerminalType string
    Width        int
    Height       int

    UsernamePrompt *regexp.Regexp
    PasswordPrompt *regexp.Regexp
    SuccessPrompt  *regexp.Regexp
    FailurePrompt  *regexp.Regexp

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
```

There is deliberately one I/O path:

- `Client.Write` always writes terminal application data and escapes Telnet
  `IAC` bytes internally.
- `Client.Read` returns terminal application data only.
- `SendCommand` sends Telnet controls such as `CommandBreak`.
- `Dial` and `NewClientConn` perform initial prompt-based login when credentials
  are configured.

The library does not expose raw writes, a second data writer, or a separate
public login lifecycle.

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
terminal read does not produce an unbounded log line.

**Warning:** traces can contain usernames, passwords, commands, and terminal
output. Store them securely and remove them after troubleshooting.

The example client can write a trace directly:

```bash
cd _example/telnet_client
go run . -ip 192.0.2.10 -port 23 -username admin -trace-file telnet.trace
```

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

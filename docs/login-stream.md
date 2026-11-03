# Login stream boundaries

`Dial` and `NewClientConn` keep the existing public API. Login and the session
share a 4 KiB buffered application reader, after Telnet framing and any
`WrapStream` charset conversion.

## Reads are not messages

The login loop fills the buffer only when it is empty, then scans the bytes
already available using `Peek` and `Discard`. It does not call `ReadByte` in the
login loop, wait for a full 4 KiB block, or add sleeps. A short `Password:` prompt
can therefore receive a response immediately.

Text normalization still examines bytes to carry ANSI, UTF-8, CR/LF, and
backspace state across chunks. It does not copy the entire current line or run
all regular expressions after each byte.

Completed CRLF/LF lines are evaluated once. At the end of an available chunk,
only interactive prompt matching is allowed, and only for a candidate of at
most 512 bytes. Failure messages and password-expiry narratives wait for a line
ending, EOF, or the overall login timeout. For example:

```text
Authentication failed count: 0
Password expired policy: disabled
New password policy: 90 days
```

These remain banners even when a read stops immediately after `failed`,
`expired`, or `New password`. An unterminated explicit failure can be reported
at EOF or timeout; finalization never sends credentials or declares success on
a closed connection.

An unfinished password-change input must end in `:` or `：` before being
classified. `New password:` is rejected without submitting the managed old
password. Custom `PasswordChangePrompt` and `FailurePrompt` still replace their
respective defaults; their narrative matches obey the same completion rules.
Other custom prompts remain full matches against the current normalized line.

Overlong logical lines are ignored until a line reset or ending. They are not
converted into a sliding suffix that could accidentally look like a prompt.

## Preserving session data

Only the consumed portion of a chunk is discarded. Bytes after the accepted
shell prompt remain in the same reader. When CR/LF was consumed to establish a
line boundary, that one- or two-byte terminator is replayed before the buffered
session data. There is no second unbounded transcript or replay buffer.

For `router#\r\nALARM`, the next `Client.Read` still sees `\r\nALARM`; the accepted
prompt is not replayed. ANSI formatting already consumed with the prompt is
not reconstructed. The interactive stream itself is not sanitized.

A delimiter-less textual prompt can always be the prefix of a longer message.
No TCP buffer size or fixed delay can remove that ambiguity. The defaults use
bounded prompt shapes and known-banner filtering; unusual assets should use
precise platform-specific prompt expressions. This is not a protocol-level
proof of authentication.

## Diagnostics and malformed subnegotiations

Trace redaction is applied centrally to wire/application bytes and to both
received and generated `SB` payloads. Option names, lengths, and login-state
events remain visible. Redaction starts before `WrapStream` is invoked and
stays enabled through failed-login cleanup, including writer flushes. After a
successful login, normal trace behavior resumes; post-login traffic and the
standalone monitor can still contain secrets.

An oversized `SB` reports `ErrSubnegotiationTooLong` and discards its payload
until the real `IAC SE`, including across reads. Escaped `IAC IAC` does not end
discarding. Following application bytes and valid negotiations in the same
chunk are retained. A caller that continues reading after the error never
receives the oversized subnegotiation's tail as terminal data.

## Regression checks

The added tests exercise deterministic one-byte, split-point, 1 KiB, and 4 KiB
reads, interactive `net.Pipe` prompts without newlines, EOF/timeout results,
UTF-8 and ANSI fragments, long banners, environment subnegotiation logs, and
cleanup writes. Fuzz targets check diagnostic-banner chunking and oversized
subnegotiation recovery. `BenchmarkLoginLongBanner` measures the 1 KiB, 4 KiB,
and 64 KiB banner paths without adding timing assertions to unit tests.

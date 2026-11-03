# Changelog

The Go library and `tclient` command share the same version. Entries describe
user-visible changes; the GitHub Release also lists the commits since the
previous tag.

## v0.1.0

This is the first release with downloadable `tclient` binaries. It also includes
the library redesign since `v0.0.2`, with the incompatible changes below.

### Added and improved

- A supported client at `cmd/tclient`, with manual sessions, opt-in automatic
  login, charset conversion, local commands, window resizing, and redirected
  input/output. `--version` reports the version, commit, build time, compiler,
  and target platform.
- Archives for Linux, macOS, and Windows on amd64 and arm64, with SHA-256
  checksums and GitHub build attestations. Unix supports interactive terminals;
  Windows currently supports redirected sessions only.
- Version PRs that publish after a `VERSION` change is merged into `master`
  and release checks pass. Manual workflow runs and version-tag pushes remain
  available as alternative release entry points.
- A stateful login matcher with bounded buffering, complete-line prompt
  matching, password-change detection, and preservation of data received after
  the shell prompt. Partial failure messages and banner text no longer count
  as successful login prompts.
- Telnet stream handling for fragmented commands, escaped IAC bytes, directional
  BINARY/NVT decoding, SGA, terminal type/speed, NAWS, and environment negotiation.
  Only the configured user is exposed through environment negotiation.
- `WrapStream` for application-stream adapters, per-client tracing, explicit
  `SendCommand` controls, and a `TerminalMode` negotiation snapshot.
- Regression coverage for protocol/login boundaries, concurrency, native
  Linux/Windows builds, and interactive Linux PTY/TCP sessions.

### Upgrade from v0.0.x

**Review every `WindowChange` call: the argument order changed from
`WindowChange(width, height)` to `WindowChange(height, width)`.** Both arguments
are still integers, so old calls compile but send the wrong dimensions. The
new order matches `ssh.Session.WindowChange`.

The minimum supported Go version is now **Go 1.20**, up from Go 1.15.

#### Configuration and public API

| v0.0.x | v0.1.0 migration |
| --- | --- |
| `Config` | Use `ClientConfig` in `Dial` and `NewClientConn`. |
| `Config.Username` | Use `ClientConfig.User`. |
| `TTYOptions: &TerminalOptions{Wide, High, TermType}` | Set `Width`, `Height`, and `TerminalType` directly on `ClientConfig`. |
| `Config.SetDefaults()` | Constructors copy and validate the configuration and apply defaults; pass a non-nil `&ClientConfig{}` for default settings. |
| `UsernameRegex`, `PasswordRegex` | Use `UsernamePrompt`, `PasswordPrompt`. |
| `BuiltinFailureRegex` | Use `FailurePrompt`. |
| `BuiltinSuccessRegex`, `LoginSuccessRegex` | Replace with `ShellPrompt` matching the final interactive prompt, not a success banner. |
| Exported `Default*Pattern` globals | Leave the corresponding prompt field nil to use its built-in matcher, or supply your own expression. |
| `ErrFailedLogin` | Use `ErrLoginFailed`; test wrapped errors with `errors.Is`. |
| Global `SetMode`, `DebugMode`, `NoPrintMode` | Set `ClientConfig.Trace` to an `io.Writer`; nil disables tracing. |
| Raw protocol exports such as `OptionPacket`, `ReadOptionPacket`, `ConvertSubOptions`, `CodeTOASCII`, protocol constants, and `AuthStatus` | These are no longer public. Use `Read`/`Write` for application data, `SendCommand` with `Command*` values for standalone controls, and `WindowChange` for NAWS. |

For example:

```go
config := &tclientlib.ClientConfig{
    User:         "admin",
    Password:     password,
    TerminalType: "xterm",
    Width:        120,
    Height:       40,
}
client, err := tclientlib.Dial("tcp", "192.0.2.10:23", config)
// Handle err before using client.
// On resize: client.WindowChange(height, width).
```

#### Login and stream behavior

- Automatic login starts when **either** `User` or `Password` is non-empty;
  older releases required both. Leave both empty for manual interaction.
- Login now waits for a final shell prompt. Custom expressions replace the
  matcher for that event and must match a complete normalized logical line.
  A banner containing "success" or "Last login" is not a shell prompt. Review
  device-specific expressions before upgrading.
- Automatic username, password, and continue responses use **CR LF** by default.
  Set `LineEnding: tclientlib.LineEndingCRNUL` to retain the older credential
  terminator, or choose `LineEndingCR`/`LineEndingLF` when the device requires it.
- Positive `Timeout` values are respected, including values below 15 seconds;
  non-positive values default to 15 seconds. The timeout bounds the connection
  attempt and initial login separately. Login failures can be identified with
  `ErrLoginTimeout`, `ErrLoginFailed`, and `ErrPasswordChangeRequired`.
- `Write` is an application-data API and now escapes IAC bytes. Stop writing raw
  Telnet frames through it; use the explicit control methods instead. Caller
  supplied line endings remain unchanged by `Write`.
- In non-BINARY receive mode, `Read` converts CR NUL to CR, while retaining CR LF
  and unrelated NUL bytes. BINARY mode preserves those bytes. Negotiation state
  advances as the application reads; the library does not start a background
  reader or change the local terminal.

See [the API and login guide](README.md#api) for current examples and
[the login stream design](docs/login-stream.md) for matching details.

### Removed example client

`_example/telnet_client` and its separate module have been removed. Use
`cmd/tclient` or a release binary instead; see the
[CLI migration notes](docs/cli.md#migrating-old-example-commands) for the
updated options. The independent `_example/monitor` TCP monitor remains.

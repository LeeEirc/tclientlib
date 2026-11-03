package cli

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/LeeEirc/tclientlib"
)

type fakeClient struct {
	bytes.Buffer
	mode     tclientlib.TerminalMode
	commands []tclientlib.Command
}

func (c *fakeClient) TerminalMode() tclientlib.TerminalMode { return c.mode }
func (c *fakeClient) SendCommand(command tclientlib.Command) error {
	c.commands = append(c.commands, command)
	return nil
}

func TestTerminalEnterAtEverySplit(t *testing.T) {
	input := []byte("a\r\nb\rc\nd\r\x00e\r\r\n")
	for _, ending := range []string{"crlf", "crnul", "cr", "lf"} {
		t.Run(ending, func(t *testing.T) {
			_, eol, _ := (options{lineEnding: ending}).ending()
			want := strings.Join([]string{"a", "b", "c", "d", "e", "", ""}, string(eol))
			for split := 0; split <= len(input); split++ {
				client := new(fakeClient)
				terminal := terminalInput{client: client, local: io.Discard, ending: eol, echo: "off"}
				if err := terminal.feed(input[:split]); err != nil {
					t.Fatal(err)
				}
				if err := terminal.feed(input[split:]); err != nil {
					t.Fatal(err)
				}
				if got := client.String(); got != want {
					t.Fatalf("split %d: got %q, want %q", split, got, want)
				}
			}
		})
	}
}

func TestTerminalCommandsAreLocal(t *testing.T) {
	client := new(fakeClient)
	var local bytes.Buffer
	terminal := terminalInput{client: client, local: &local, ending: []byte("\r\n"), echo: "off"}
	input := []byte("a\x1dhelp\r\nstatus\rsend brk\rsend ip\rsend ayt\rsend nop\rcontinue\r\nb\x1d\x1dc\x1dquit\rignored")
	for _, b := range input {
		err := terminal.feed([]byte{b})
		if errors.Is(err, errQuit) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, want := client.String(), "ab\x1dc"; got != want {
		t.Fatalf("wire got %q, want %q", got, want)
	}
	wantCommands := []tclientlib.Command{tclientlib.CommandBreak, tclientlib.CommandInterruptProcess, tclientlib.CommandAreYouThere, tclientlib.CommandNoOperation}
	if len(client.commands) != len(wantCommands) {
		t.Fatalf("commands = %v", client.commands)
	}
	for i := range wantCommands {
		if client.commands[i] != wantCommands[i] {
			t.Fatalf("commands = %v", client.commands)
		}
	}
	if !strings.Contains(local.String(), "Remote echo:") || !strings.Contains(local.String(), "Ctrl+] twice") {
		t.Fatalf("missing local help/status: %q", local.String())
	}
}

func TestTerminalEchoFollowsNegotiatedMode(t *testing.T) {
	client := new(fakeClient)
	var local bytes.Buffer
	terminal := terminalInput{client: client, local: &local, ending: []byte("\r\n"), echo: "auto"}
	if err := terminal.feed([]byte("a")); err != nil {
		t.Fatal(err)
	}
	client.mode.RemoteEcho = true
	if err := terminal.feed([]byte("b\r")); err != nil {
		t.Fatal(err)
	}
	client.mode.RemoteEcho = false
	if err := terminal.feed([]byte("c\r")); err != nil {
		t.Fatal(err)
	}
	if got, want := local.String(), "ac\r\n"; got != want {
		t.Fatalf("echo got %q, want %q", got, want)
	}
	if got, want := client.String(), "ab\r\nc\r\n"; got != want {
		t.Fatalf("wire got %q, want %q", got, want)
	}
}

func TestTerminalDiscardsOverlongCommandUntilNewline(t *testing.T) {
	client := new(fakeClient)
	var local bytes.Buffer
	terminal := terminalInput{client: client, local: &local, ending: []byte("\r\n"), echo: "off"}
	if err := terminal.feed([]byte("\x1d" + strings.Repeat("x", 4096) + "\rcontinue\rhello")); err != nil {
		t.Fatal(err)
	}
	if got := client.String(); got != "hello" {
		t.Fatalf("command escaped to wire: %q", got)
	}
	if !strings.Contains(local.String(), "Command too long.") {
		t.Fatal("missing command length diagnostic")
	}
}

func TestPasswordCRFollowerDoesNotBecomeEnter(t *testing.T) {
	client := new(fakeClient)
	terminal := terminalInput{client: client, local: io.Discard, ending: []byte("\r\n"), echo: "off", afterCR: true}
	if err := terminal.feed([]byte("\nshow version\r")); err != nil {
		t.Fatal(err)
	}
	if got, want := client.String(), "show version\r\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

type brokenWriter struct{ err error }

func (w brokenWriter) Write([]byte) (int, error) { return 0, w.err }

func TestTerminalReportsLocalOutputError(t *testing.T) {
	want := errors.New("output failed")
	terminal := terminalInput{client: new(fakeClient), local: brokenWriter{want}, ending: []byte("\r\n"), echo: "on"}
	if err := terminal.feed([]byte{escapeKey}); !errors.Is(err, want) {
		t.Fatalf("got %v, want output error", err)
	}
}

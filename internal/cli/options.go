// Package cli implements the interactive tclient command. Telnet framing and
// login stay in the root package; this package owns only local terminal policy.
package cli

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LeeEirc/tclientlib"
	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

type options struct {
	address      string
	timeout      time.Duration
	drainTimeout time.Duration
	closeWrite   bool
	autoLogin    bool
	user         string
	askPass      bool
	terminal     string
	charset      string
	lineEnding   string
	echo         string
	traceFile    string
}

func parseOptions(args []string, output io.Writer) (options, error) {
	var o options
	f := flag.NewFlagSet("tclient", flag.ContinueOnError)
	f.SetOutput(output)
	f.DurationVar(&o.timeout, "timeout", 15*time.Second, "connection and automatic-login timeout")
	f.DurationVar(&o.drainTimeout, "drain-timeout", 5*time.Second, "maximum wait for remote output after stdin EOF")
	f.BoolVar(&o.closeWrite, "close-write", false, "half-close TCP on stdin EOF (disables further Telnet replies)")
	f.BoolVar(&o.autoLogin, "auto-login", false, "use prompt-based automatic login")
	f.StringVar(&o.user, "user", "", "automatic-login username (requires --auto-login)")
	f.BoolVar(&o.askPass, "ask-pass", false, "read an automatic-login password without echo (requires a terminal)")
	terminal := os.Getenv("TERM")
	if terminal == "" {
		terminal = "xterm"
	}
	f.StringVar(&o.terminal, "term", terminal, "terminal type sent to the server")
	f.StringVar(&o.charset, "charset", "utf-8", "remote charset: utf-8, gbk, gb18030, big5")
	f.StringVar(&o.lineEnding, "line-ending", "crlf", "TTY Enter and automatic-login terminator: crlf, crnul, cr, lf")
	f.StringVar(&o.echo, "echo", "auto", "local character echo: auto, on, off (TTY only)")
	f.StringVar(&o.traceFile, "trace-file", "", "create a private diagnostic trace; may contain passwords and output")
	f.Usage = func() {
		fmt.Fprintln(output, "Usage: tclient [options] host [port]")
		fmt.Fprintln(output, "Defaults to manual login and port 23. Options precede the host.")
		fmt.Fprintln(output, "Press Ctrl+] for local commands; use help to list them.")
		f.PrintDefaults()
	}
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() < 1 || f.NArg() > 2 {
		return o, fmt.Errorf("expected host [port]; use --help for usage")
	}
	if o.timeout <= 0 || o.drainTimeout <= 0 {
		return o, fmt.Errorf("timeouts must be positive")
	}
	if !o.autoLogin && (o.user != "" || o.askPass) {
		return o, fmt.Errorf("--user and --ask-pass require --auto-login")
	}
	if o.autoLogin && o.user == "" && !o.askPass {
		return o, fmt.Errorf("--auto-login requires --user or --ask-pass")
	}
	if _, err := o.encoding(); err != nil {
		return o, err
	}
	if _, _, err := o.ending(); err != nil {
		return o, err
	}
	if o.echo != "auto" && o.echo != "on" && o.echo != "off" {
		return o, fmt.Errorf("invalid echo mode %q", o.echo)
	}
	if o.terminal == "" {
		return o, fmt.Errorf("terminal type must not be empty")
	}
	host := f.Arg(0)
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" {
		return o, fmt.Errorf("host must not be empty")
	}
	port := "23"
	if f.NArg() == 2 {
		port = f.Arg(1)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return o, fmt.Errorf("port must be a number from 1 to 65535")
	}
	o.address = net.JoinHostPort(host, port)
	return o, nil
}

func (o options) encoding() (encoding.Encoding, error) {
	switch strings.ToLower(o.charset) {
	case "utf-8", "utf8":
		return nil, nil
	case "gbk":
		return simplifiedchinese.GBK, nil
	case "gb18030":
		return simplifiedchinese.GB18030, nil
	case "big5":
		return traditionalchinese.Big5, nil
	default:
		return nil, fmt.Errorf("unsupported charset %q", o.charset)
	}
}

func (o options) ending() (tclientlib.LineEnding, []byte, error) {
	switch o.lineEnding {
	case "crlf":
		return tclientlib.LineEndingCRLF, []byte{'\r', '\n'}, nil
	case "crnul":
		return tclientlib.LineEndingCRNUL, []byte{'\r', 0}, nil
	case "cr":
		return tclientlib.LineEndingCR, []byte{'\r'}, nil
	case "lf":
		return tclientlib.LineEndingLF, []byte{'\n'}, nil
	default:
		return 0, nil, fmt.Errorf("unsupported line ending %q", o.lineEnding)
	}
}

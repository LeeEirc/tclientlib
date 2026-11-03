// This entry point is retained for existing example checkouts. The supported
// client lives in cmd/tclient; both entry points use the same implementation
// and flags (see docs/cli.md).
package main

import (
	"os"

	"github.com/LeeEirc/tclientlib/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

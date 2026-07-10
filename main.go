// Command hermod mirrors a working directory to a sandbox sandbox and attaches a
// persistent session running there, pausing or tearing down the mirror on detach.
package main

import (
	"os"

	"github.com/zerlok/hermod/internal/cli"
)

func main() {
	os.Exit(cli.Run())
}

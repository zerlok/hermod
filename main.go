// Command hermod attaches a local client to a workspace on a remote: the
// relays that carry files, stdio and calls between the two workstations, and
// the workers that keep running there when the client is gone.
package main

import (
	"os"

	"github.com/zerlok/hermod/internal/cli"
)

func main() {
	os.Exit(cli.Run())
}

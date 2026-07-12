// dcnetlab-node-cli is the operator toolbox inside lab server
// containers (mounted as node-cli). It is a multi-call binary:
// common commands are symlinked to it at deploy time (pkg →
// node-cli), so `pkg list` and `node-cli pkg list` are equivalent.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ifantsai/dcnetlab/serverapps/internal/nodecli"
)

const usage = `usage: node-cli <command> [args]

commands:
  pkg       manage packages from the controller's repository
  program   manage node-local programs through the local agent
            (both are also symlinked as direct commands)
`

func main() {
	// Invoked through a command symlink (e.g. pkg → node-cli), the
	// base name selects the subcommand directly.
	cmd := filepath.Base(os.Args[0])
	args := os.Args[1:]
	if cmd == "node-cli" || cmd == "dcnetlab-node-cli" {
		if len(args) == 0 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}

		cmd, args = args[0], args[1:]
	}

	switch cmd {
	case "pkg":
		os.Exit(nodecli.PkgMain(args))
	case "program":
		os.Exit(nodecli.ProgramMain(args))
	default:
		fmt.Fprintf(os.Stderr, "node-cli: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

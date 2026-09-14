// Command mivia is the MiviaLabs local CLI AI agent entrypoint.
package main

import (
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	tui "github.com/MiviaLabs/mivia-agent/internal/tui/run"
	"github.com/MiviaLabs/mivia-agent/internal/version"
)

func main() {
	cli.SetTUILauncher(tui.RunTUI)
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", version.Binary, err)
		os.Exit(1)
	}
}

// run never materializes embedded instructions into a user's workspace.
func run(args []string) error { return cli.Execute(args) }

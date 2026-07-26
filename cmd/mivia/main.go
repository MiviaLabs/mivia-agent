// Command mivia is the MiviaLabs local CLI AI agent entrypoint.
package main

import (
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/version"
)

func main() {
	if err := cli.Execute(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", version.Binary, err)
		os.Exit(1)
	}
}

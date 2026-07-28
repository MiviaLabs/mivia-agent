// Command mivia is the MiviaLabs local CLI AI agent entrypoint.
package main

import (
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/agentkit"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"github.com/MiviaLabs/mivia-agent/internal/version"
)

func main() {
	// On startup, ensure agent instructions are present in the workspace.
	// If no .ai/ exists, the embedded generic instruction set is written.
	cwd, err := os.Getwd()
	if err == nil {
		if err := agentkit.EnsureInstructions(cwd); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not ensure agent instructions: %v\n", err)
		}
	}

	if err := cli.Execute(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", version.Binary, err)
		os.Exit(1)
	}
}

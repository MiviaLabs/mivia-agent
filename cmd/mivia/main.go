// Command mivia is the MiviaLabs local CLI AI agent entrypoint.
package main

import (
	"fmt"
	"os"

	"github.com/MiviaLabs/mivia-agent/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", version.Binary, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "version", "--version", "-V":
		fmt.Printf("%s %s\n", version.Binary, version.Version)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (try %s help)", args[0], version.Binary)
	}
}

func printUsage() {
	fmt.Printf(`%s - local CLI AI agent (MiviaLabs)

Usage:
  %s version    Print version
  %s help       Show this help

Agent development for this repository: see AGENTS.md and .ai/INDEX.md
`, version.Binary, version.Binary, version.Binary)
}

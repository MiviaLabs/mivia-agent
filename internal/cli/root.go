// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/version"
)

// Execute is the program entry after main.
func Execute(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "version", "--version", "-V":
		fmt.Printf("%s %s\n", version.Binary, version.Version)
		return nil
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return nil
	case "chat":
		return runChat(args[1:])
	case "config":
		return runConfig(args[1:])
	case "doctor":
		return runDoctor(args[1:])
	default:
		return fmt.Errorf("unknown command %q (try %s help)", args[0], version.Binary)
	}
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `%s - local CLI AI agent (MiviaLabs)

Usage:
  %s chat [-p prompt] [--provider name] [--model name] [--config path]
  %s config show [--config path]
  %s doctor [--config path]
  %s version
  %s help

Defaults: provider deepseek, model deepseek-v4-flash
Advanced DeepSeek model: deepseek-v4-pro (via --model or config)

Config: $MIVIA_CONFIG | ./mivia.toml | ~/.config/mivia/config.toml
Secrets: env file or process environment (never in TOML)
`, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary)
}

func flagValue(args []string, names ...string) (string, []string, bool) {
	out := make([]string, 0, len(args))
	var val string
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n && i+1 < len(args) {
				val = args[i+1]
				found = true
				i++
				matched = true
				break
			}
			if strings.HasPrefix(a, n+"=") {
				val = strings.TrimPrefix(a, n+"=")
				found = true
				matched = true
				break
			}
		}
		if !matched {
			out = append(out, a)
		}
	}
	return val, out, found
}

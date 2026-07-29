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
  %s chat [-p prompt] [--provider name] [--model name] [--workspace dir] [--no-tools] [--plain] [--config path]
         [--allow-program name]... [--deny-program name]... [--no-default-allowlist]
         [--disable-tool name]... [--allow-env-var name]... [--deny-env-var name]...
  %s config show [--config path]
  %s doctor [--config path]
  %s version
  %s help

Defaults: provider deepseek, model deepseek-v4-flash, tools ON (coding agent)
Advanced DeepSeek model: deepseek-v4-pro (via --model, config, or /model in chat)

Agent tools: read_file list_dir grep glob write_file search_replace run_command
  --no-tools disables tools (pure chat). --workspace confines file/command tools.
  --plain uses classic terminal UI (if Bubble Tea misbehaves).
  --allow-program  add program to run_command allowlist (repeatable)
  --deny-program   remove program from run_command allowlist (repeatable)
  --no-default-allowlist  start with empty run_command allowlist
  --disable-tool   disable a built-in tool by name (repeatable)
  --allow-env-var  add env var to subprocess allowlist (repeatable)
  --deny-env-var   remove env var from subprocess allowlist (repeatable)

Chat: /help /tools /exit /clear /model /status
  Ctrl-C at prompt exits; Ctrl-C during a reply cancels generation.

Config: $MIVIA_CONFIG | ./.mivia/mivia.toml | ~/.config/mivia/config.toml
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

// flagVar is like flagValue but for repeatable string flags. Each occurrence
// of any name collects one value. Supports both "--flag VALUE" and "--flag=VALUE".
func flagVar(args []string, names ...string) ([]string, []string, bool) {
	var vals []string
	rest := make([]string, 0, len(args))
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n && i+1 < len(args) {
				vals = append(vals, args[i+1])
				found = true
				i++
				matched = true
				break
			}
			if strings.HasPrefix(a, n+"=") {
				vals = append(vals, strings.TrimPrefix(a, n+"="))
				found = true
				matched = true
				break
			}
		}
		if !matched {
			rest = append(rest, a)
		}
	}
	return vals, rest, found
}

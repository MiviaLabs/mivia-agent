// Package cli implements mivia command handlers.
package cli

import (
	"fmt"
	clichat "github.com/MiviaLabs/mivia-agent/internal/clichat"
	"os"
	"strings"

	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	cliorchestrate "github.com/MiviaLabs/mivia-agent/internal/cliorchestrate"
	"github.com/MiviaLabs/mivia-agent/internal/cliworkflow"
	"github.com/MiviaLabs/mivia-agent/internal/cliworktree"
	"github.com/MiviaLabs/mivia-agent/internal/version"
)

// Execute is the program entry after main.
func Execute(args []string) error {
	if len(args) == 0 {
		printUsage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "version":
		return runVersion(args[1:])
	case "--version", "-V":
		fmt.Println(version.String())
		return nil
	case "help", "--help", "-h":
		printUsage(os.Stdout)
		return nil
	case "chat":
		return clichat.RunChat(args[1:])
	case "config":
		return runConfig(args[1:])
	case "doctor":
		return cliorchestrate.RunDoctor(args[1:])
	case "agents":
		return cliagents.RunAgents(args[1:])
	case "sessions":
		return clichat.RunSessions(args[1:])
	case "compact":
		return clichat.RunCompact(args[1:])
	case "memory":
		return runMemory(args[1:])
	case "workflows":
		return cliworkflow.RunWorkflows(args[1:])
	case "workflow":
		return cliworkflow.RunWorkflow(args[1:])
	case "stack":
		return runStack(args[1:])
	case "worktree":
		return cliworktree.RunWorktree(args[1:])
	case "completion":
		return runCompletion(args[1:])
	case "setup":
		return runSetup(args[1:])
	case "login":
		return runLogin(args[1:])
	case "logout":
		return runLogout(args[1:])
	case "register":
		return runRegister(args[1:])
	case "verify":
		return runVerify(args[1:])
	default:
		return fmt.Errorf("unknown command %q (try %s help)", args[0], version.Binary)
	}
}

// runVersion handles the "version" subcommand with optional --json flag.
func runVersion(args []string) error {
	switch {
	case len(args) == 0:
		fmt.Println(version.String())
		return nil
	case len(args) == 1 && args[0] == "--json":
		fmt.Println(version.JSONString())
		return nil
	case len(args) > 1 && args[0] == "--json":
		return fmt.Errorf("unexpected arguments after --json: %v", args[1:])
	default:
		return fmt.Errorf("unknown argument %q (try \"mivia version --json\")", args[0])
	}
}

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText())
}

// usageText is the help body. It is a value rather than a direct write so the
// documented flag set can be asserted by test - a dangerous flag that stops
// being documented is how it starts reading as a feature.
func usageText() string {
	return fmt.Sprintf(`%s - local CLI AI agent (MiviaLabs)

Usage:
  %s chat [-p prompt] [--provider name] [--model name] [--agent name] [--workspace dir] [--session name] [--no-tools] [--plain] [--quiet] [--config path]
         [--allow-program name]... [--deny-program name]...
         [--disable-tool name]... [--allow-env-var name]... [--deny-env-var name]...
  %s config show [--config path]
  %s memory search <query> [--scope project|org|all] [--limit N] [--json] [--workspace dir] [--config path]
  %s doctor [--config path] [--json] [--workspace dir]
  %s agents list [--workspace dir]
  %s agents explain <name> [--workspace dir]
  %s sessions list [--workspace dir] [--json]
  %s sessions show <name> [--workspace dir] [--json] [--limit N]
  %s sessions usage <name> [--workspace dir] [--json]
  %s sessions rename <name> <title> [--workspace dir]
  %s sessions delete <name> [--workspace dir]
  %s compact --session <name> [--json] [--workspace dir]
  %s workflows list [--workspace dir]
  %s workflows show <name> [--workspace dir]
  %s workflows validate [name] [--workspace dir]
  %s workflows explain <name> [--workspace dir]
  %s workflow run <name> [--workspace dir] [--config path] [--input name=value]... [--allow-publish]
  %s workflow runs [--status name] [--limit n] [--watch] [--workspace dir] [--config path]
  %s workflow deliver <run-id> --allow-publish [--workspace dir] [--config path]
  %s workflow resume <run-id> [--workspace dir] [--config path] [--force] [--allow-publish] [--accept-verifier-change] [--accept-skill-change]
  %s stack plan <workflow> [--workspace dir] [--config path]
  %s stack drive <workflow> [--stack <plan-run-id>] [--workspace dir] [--config path]
  %s stack status <workflow> [--stack <plan-run-id>] [--workspace dir] [--config path]
  %s worktree create <name> [--branch ref] [--workspace dir]
  %s worktree list [--workspace dir]
  %s worktree remove <name> [--workspace dir]
  %s completion bash|zsh|fish
  %s setup [--provider name] [--key value] [--env-file path] [--config path] [--yes]
  %s login --email <addr> [--password-stdin] [--server-url <url>]
  %s logout [--server-url <url>]
  %s register --email <addr> --organization-name <name> [--password-stdin] [--server-url <url>]
  %s verify <code> [--server-url <url>]
  %s version [--json]
  %s help

Defaults: provider openrouter, model openai/gpt-5.6-luna, tools ON (coding agent)
Switch providers or models via --provider, --model, config, or /model in chat

Agent tools: read_file list_dir grep glob write_file search_replace multi_edit run_command
  --agent selects a named agent definition from ~/.mivia/agents/ or <workspace>/.mivia/agents/.
  --session resumes a saved session by the name/id "mivia sessions list" reports; fails if it does not exist.
  --no-tools disables tools (pure chat). --workspace confines file/command tools.
  --plain uses classic terminal UI (if Bubble Tea misbehaves).
  --quiet suppresses the startup notices (limits/hooks/diagnostics lines).
  --allow-program  add program to run_command allowlist (repeatable)
  --deny-program   remove program from run_command allowlist (repeatable)
  --disable-tool   disable a built-in tool by name (repeatable)
  --allow-env-var  add env var to subprocess allowlist (repeatable)
  --deny-env-var   remove env var from subprocess allowlist (repeatable)

Chat: /help /tools /hooks /exit /clear /new /model /status
  Ctrl-C at prompt exits; Ctrl-C during a reply cancels generation.

Config: $MIVIA_CONFIG | ./.mivia/mivia.toml | ~/.mivia/mivia.toml
Secrets: env file or process environment (never in TOML)
`, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary, version.Binary)
}

// flagValue returns the value of the first occurrence of any named flag,
// plus the remaining tokens. The space form requires a following value token
// that is not itself a flag: a missing or dash-prefixed value is a caller
// error (DC-9 fail-open), so it is refused with "%s requires a value" instead
// of silently swallowing the next flag as a value. The "=" form stays
// permissive so values that legitimately start with "-" remain expressible as
// --name=--value. The found bool reports whether any name matched.
func flagValue(args []string, names ...string) (string, []string, bool, error) {
	out := make([]string, 0, len(args))
	var val string
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return "", nil, found, fmt.Errorf("%s requires a value", n)
				}
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
	return val, out, found, nil
}

// flagVar is like flagValue but for repeatable string flags. Each occurrence
// of any name collects one value. Supports both "--flag VALUE" and
// "--flag=VALUE". Like flagValue it refuses a missing or dash-prefixed space
// value instead of swallowing a following flag (DC-9).
func flagVar(args []string, names ...string) ([]string, []string, bool, error) {
	var vals []string
	rest := make([]string, 0, len(args))
	found := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		matched := false
		for _, n := range names {
			if a == n {
				if i+1 >= len(args) || strings.HasPrefix(args[i+1], "-") {
					return nil, nil, found, fmt.Errorf("%s requires a value", n)
				}
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
	return vals, rest, found, nil
}

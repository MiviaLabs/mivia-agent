package cliagents

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// runAgents handles the provider-independent agent catalog commands.
func RunAgents(args []string) error {
	return runAgentsWithIO(args, os.Stdout, os.Stderr)
}

// RunAgentsWithIO is the exported view of runAgentsWithIO for callers outside
// cliagents (tests, integration harnesses).
func RunAgentsWithIO(args []string, stdout, stderr io.Writer) error {
	return runAgentsWithIO(args, stdout, stderr)
}

func runAgentsWithIO(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("agents: expected list or explain")
	}
	subcommand := args[0]
	workspaceRoot, name, jsonFlag, err := parseAgentsArgs(subcommand, args[1:])
	if err != nil {
		return err
	}
	view, err := LoadAgentCatalog(workspaceRoot)
	if err != nil {
		return fmt.Errorf("agents: %w", err)
	}
	for _, warning := range view.Report.Warnings {
		fmt.Fprintln(stderr, "warning:", warning)
	}
	switch subcommand {
	case "list":
		if jsonFlag {
			if encErr := writeAgentCatalogJSON(stdout, view); encErr != nil {
				return fmt.Errorf("agents: %w", encErr)
			}
		} else {
			WriteAgentCatalog(stdout, view, stderr)
		}
		if summary := view.Report.DiagnosticSummary(); summary != "none" {
			return fmt.Errorf("agents: %s", summary)
		}
		return nil
	case "explain":
		agent, ok := findCatalogAgent(view, name)
		if !ok {
			WriteAgentCatalog(stdout, view, stderr)
			return fmt.Errorf("agents: unknown agent %q", SafeCatalogText(name, 80))
		}
		writeAgentExplain(stdout, agent)
		if summary := view.Report.DiagnosticSummary(); summary != "none" {
			return fmt.Errorf("agents: %s", summary)
		}
		return nil
	default:
		return fmt.Errorf("agents: unknown subcommand %q", SafeCatalogText(subcommand, 80))
	}
}

func parseAgentsArgs(subcommand string, args []string) (workspaceRoot, name string, jsonFlag bool, err error) {
	if subcommand != "list" && subcommand != "explain" {
		return "", "", false, fmt.Errorf("agents: unknown subcommand %q", SafeCatalogText(subcommand, 80))
	}
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--workspace":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" || strings.HasPrefix(args[i+1], "-") {
				return "", "", false, fmt.Errorf("agents: --workspace requires a directory")
			}
			workspaceRoot = args[i+1]
			i++
		case strings.HasPrefix(arg, "--workspace="):
			workspaceRoot = strings.TrimPrefix(arg, "--workspace=")
			if strings.TrimSpace(workspaceRoot) == "" || strings.HasPrefix(workspaceRoot, "-") {
				return "", "", false, fmt.Errorf("agents: --workspace requires a directory")
			}
		case arg == "--json":
			if subcommand == "explain" {
				return "", "", false, fmt.Errorf("agents explain: --json is not supported for explain")
			}
			if jsonFlag {
				return "", "", false, fmt.Errorf("agents list: duplicate --json flag")
			}
			jsonFlag = true
		case strings.HasPrefix(arg, "-"):
			return "", "", false, fmt.Errorf("agents: unknown flag %q", SafeCatalogText(arg, 80))
		default:
			positional = append(positional, arg)
		}
	}
	if subcommand == "list" {
		if len(positional) != 0 {
			return "", "", false, fmt.Errorf("agents list: unexpected arguments (%d)", len(positional))
		}
		return workspaceRoot, "", jsonFlag, nil
	}
	if len(positional) != 1 || strings.TrimSpace(positional[0]) == "" {
		return "", "", false, fmt.Errorf("agents explain: expected exactly one agent name")
	}
	return workspaceRoot, positional[0], false, nil
}

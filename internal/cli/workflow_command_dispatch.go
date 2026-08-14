package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// The per-subcommand arg parsers keep runWorkflowWithIO a thin dispatcher:
// each command owns its arity and flag validation here.

func runWorkflowCommandRun(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	inputs, rest, _, err := flagVar(args, "--input")
	if err != nil {
		return err
	}
	allowPublish, rest, err := parseWorkflowBoolFlag(rest, "--allow-publish")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("workflow run: expected one workflow name")
	}
	return executeWorkflowRun(rest[0], workspaceRoot, configPath, inputs, allowPublish, stdout, stderr)
}

// runWorkflowCommandRuns parses `workflow runs [--status s] [--limit n] [--json]`.
// It takes no positional arguments.
func runWorkflowCommandRuns(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	status, rest, err := parseWorkflowStringFlag(args, "--status")
	if err != nil {
		return err
	}
	watch, rest, err := parseWorkflowBoolFlag(rest, "--watch")
	if err != nil {
		return err
	}
	jsonMode, rest, err := parseWorkflowBoolFlag(rest, "--json")
	if err != nil {
		return err
	}
	limit, err := parseWorkflowIntFlag(rest, "--limit", 20)
	if err != nil {
		return err
	}
	// runs takes no positional argument, so anything left that is not part
	// of --limit is a typo the operator should hear about rather than have
	// silently ignored.
	for i := 0; i < len(rest); i++ {
		switch {
		case strings.HasPrefix(rest[i], "--limit="):
		case rest[i] == "--limit":
			i++
		default:
			return fmt.Errorf("workflow runs: unexpected argument %q", rest[i])
		}
	}
	if jsonMode {
		if watch {
			return fmt.Errorf("workflow runs: --json and --watch are mutually exclusive")
		}
		return executeWorkflowRunsJSON(workspaceRoot, configPath, status, limit, stdout)
	}
	if watch {
		return executeWorkflowRunsWatch(workspaceRoot, configPath, status, limit, stdout, stderr)
	}
	return executeWorkflowRuns(workspaceRoot, configPath, status, limit, stdout, stderr)
}

func runWorkflowCommandDeliver(args []string, workspaceRoot, configPath string, force bool, stdout, stderr io.Writer) error {
	allowPublish, rest, err := parseWorkflowBoolFlag(args, "--allow-publish")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("workflow deliver: expected one run ID")
	}
	return executeWorkflowDeliver(context.Background(), rest[0], workspaceRoot, configPath, allowPublish, force, stdout, stderr)
}

func runWorkflowCommandResume(args []string, workspaceRoot, configPath string, force bool, stdout, stderr io.Writer) error {
	allowPublish, rest, err := parseWorkflowBoolFlag(args, "--allow-publish")
	if err != nil {
		return err
	}
	acceptVerifierChange, rest, err := parseWorkflowBoolFlag(rest, "--accept-verifier-change")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("workflow resume: expected one run ID")
	}
	return executeWorkflowResume(rest[0], workspaceRoot, configPath, force, allowPublish, acceptVerifierChange, stdout, stderr)
}

func runWorkflowCommandStatus(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	jsonMode, rest, err := parseWorkflowBoolFlag(args, "--json")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("workflow status: expected one run ID")
	}
	if jsonMode {
		return executeWorkflowStatusJSON(rest[0], workspaceRoot, configPath, stdout)
	}
	return executeWorkflowStatus(rest[0], workspaceRoot, configPath, stdout, stderr)
}

func runWorkflowCommandEvents(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	if len(args) < 1 || len(args) > 5 {
		return fmt.Errorf("workflow events: expected a run ID and optional --limit/--offset")
	}
	limit, err := parseWorkflowIntFlag(args[1:], "--limit", 0)
	if err != nil {
		return err
	}
	offset, err := parseWorkflowIntFlag(args[1:], "--offset", 0)
	if err != nil {
		return err
	}
	return executeWorkflowEvents(args[0], workspaceRoot, configPath, limit, offset, stdout, stderr)
}

func runWorkflowCommandApprove(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	actor, rest, err := parseWorkflowStringFlag(args, "--actor")
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return fmt.Errorf("workflow approve: expected a run ID and an approval ID")
	}
	if strings.TrimSpace(actor) == "" {
		actor = workflowApprovalDefaultActor
	}
	return executeWorkflowApprove(rest[0], rest[1], workspaceRoot, configPath, actor, stdout, stderr)
}

func runWorkflowCommandReject(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	actor, rest, err := parseWorkflowStringFlag(args, "--actor")
	if err != nil {
		return err
	}
	reason, rest, err := parseWorkflowStringFlag(rest, "--reason")
	if err != nil {
		return err
	}
	if len(rest) != 2 {
		return fmt.Errorf("workflow reject: expected a run ID and an approval ID")
	}
	if strings.TrimSpace(actor) == "" {
		actor = workflowApprovalDefaultActor
	}
	return executeWorkflowReject(rest[0], rest[1], workspaceRoot, configPath, actor, reason, stdout, stderr)
}

func runWorkflowCommandCancel(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("workflow cancel: expected one run ID")
	}
	return executeWorkflowCancel(args[0], workspaceRoot, configPath, stdout, stderr)
}

func runWorkflowCommandCleanup(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("workflow cleanup: expected one run ID")
	}
	return executeWorkflowCleanup(args[0], workspaceRoot, configPath, stdout, stderr)
}

func runWorkflowCommandDelete(args []string, workspaceRoot, configPath string, force bool, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("workflow delete: expected one run ID")
	}
	return executeWorkflowDelete(args[0], workspaceRoot, configPath, force, stdout, stderr)
}

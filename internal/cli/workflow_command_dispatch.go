package cli

import (
	"fmt"
	"io"
	"strings"
)

// The per-subcommand arg parsers keep runWorkflowWithIO a thin dispatcher:
// each command owns its arity and flag validation here.

func runWorkflowCommandRun(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	inputs, rest, _ := flagVar(args, "--input")
	allowPublish, rest, err := parseWorkflowBoolFlag(rest, "--allow-publish")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("workflow run: expected one workflow name")
	}
	return executeWorkflowRun(rest[0], workspaceRoot, configPath, inputs, allowPublish, stdout, stderr)
}

func runWorkflowCommandDeliver(args []string, workspaceRoot, configPath string, force bool, stdout, stderr io.Writer) error {
	allowPublish, rest, err := parseWorkflowBoolFlag(args, "--allow-publish")
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("workflow deliver: expected one run ID")
	}
	return executeWorkflowDeliver(rest[0], workspaceRoot, configPath, allowPublish, force, stdout, stderr)
}

func runWorkflowCommandResume(args []string, workspaceRoot, configPath string, force bool, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("workflow resume: expected one run ID")
	}
	return executeWorkflowResume(args[0], workspaceRoot, configPath, force, stdout, stderr)
}

func runWorkflowCommandStatus(args []string, workspaceRoot, configPath string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("workflow status: expected one run ID")
	}
	return executeWorkflowStatus(args[0], workspaceRoot, configPath, stdout, stderr)
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

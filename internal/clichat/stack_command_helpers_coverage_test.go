package clichat

// stack_command_helpers_coverage_test.go covers parseStackWorkflowArgs
// on representative inputs. resolveStackID's no-lookup branch is
// exercised via the explicit --stack path; isStackPlanRun needs a
// real workflowledger.Repository and is left to the broader stack_*
// tests in this package.

import (
	"strings"
	"testing"
)

func TestParseStackWorkflowArgs(t *testing.T) {
	// Happy path: workflow name with no remaining args.
	name, stackFlag, rest, err := parseStackWorkflowArgs([]string{"my-workflow"})
	if err != nil || name != "my-workflow" || stackFlag != "" || len(rest) != 0 {
		t.Fatalf("parseStackWorkflowArgs(workflow) = (%q, %q, %v, %v)", name, stackFlag, rest, err)
	}
	// --stack form: explicit stack id + workflow name.
	name, stackFlag, _, err = parseStackWorkflowArgs([]string{"--stack", "stack-id", "wf"})
	if err != nil || name != "wf" || stackFlag != "stack-id" {
		t.Fatalf("parseStackWorkflowArgs(--stack) = (%q, %q, %v)", name, stackFlag, err)
	}
	// Empty args: must error.
	if _, _, _, err := parseStackWorkflowArgs(nil); err == nil {
		t.Fatal("parseStackWorkflowArgs(nil) must error")
	}
	// Two trailing arguments: must error and mention the unknown arg.
	if _, _, _, err := parseStackWorkflowArgs([]string{"wf", "trail"}); err == nil {
		t.Fatal("parseStackWorkflowArgs(wf trail) must error")
	} else if !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("parseStackWorkflowArgs(trail) err = %v", err)
	}
}

func TestResolveStackIDExplicitFlagShortCircuits(t *testing.T) {
	id, err := resolveStackID(nil, "wf", "my-stack")
	if err != nil || id != "my-stack" {
		t.Fatalf("resolveStackID(explicit) = (%q, %v)", id, err)
	}
}

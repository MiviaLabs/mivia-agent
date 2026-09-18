package workflow

// stack_command_helpers_coverage_test.go covers ParseStackWorkflowArgs
// on representative inputs. ResolveStackID's no-lookup branch is
// exercised via the explicit --stack path; isStackPlanRun needs a
// real workflowledger.Repository and is left to the broader stack_*
// tests in this package.

import (
	"strings"
	"testing"
)

func TestParseStackWorkflowArgs(t *testing.T) {
	// Happy path: workflow name with no remaining args.
	name, stackFlag, rest, err := ParseStackWorkflowArgs([]string{"my-workflow"})
	if err != nil || name != "my-workflow" || stackFlag != "" || len(rest) != 0 {
		t.Fatalf("ParseStackWorkflowArgs(workflow) = (%q, %q, %v, %v)", name, stackFlag, rest, err)
	}
	// --stack form: explicit stack id + workflow name.
	name, stackFlag, _, err = ParseStackWorkflowArgs([]string{"--stack", "stack-id", "wf"})
	if err != nil || name != "wf" || stackFlag != "stack-id" {
		t.Fatalf("ParseStackWorkflowArgs(--stack) = (%q, %q, %v)", name, stackFlag, err)
	}
	// Empty args: must error.
	if _, _, _, err := ParseStackWorkflowArgs(nil); err == nil {
		t.Fatal("ParseStackWorkflowArgs(nil) must error")
	}
	// Two trailing arguments: must error and mention the unknown arg.
	if _, _, _, err := ParseStackWorkflowArgs([]string{"wf", "trail"}); err == nil {
		t.Fatal("ParseStackWorkflowArgs(wf trail) must error")
	} else if !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("ParseStackWorkflowArgs(trail) err = %v", err)
	}
}

func TestResolveStackIDExplicitFlagShortCircuits(t *testing.T) {
	id, err := ResolveStackID(nil, "wf", "my-stack")
	if err != nil || id != "my-stack" {
		t.Fatalf("ResolveStackID(explicit) = (%q, %v)", id, err)
	}
}

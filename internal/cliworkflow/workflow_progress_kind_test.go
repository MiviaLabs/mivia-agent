package cliworkflow

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// progressKindSourcePath is the declaration site the scan below reads. Reading
// the SOURCE rather than a hand-written list is the whole point: a list in a
// test is one more place to forget, which is the defect this file gates.
const progressKindSourcePath = "../workflows/controller/progress.go"

// declaredProgressKinds returns every controller.ProgressKind constant value
// declared in the controller package.
func declaredProgressKinds(t *testing.T) []controller.ProgressKind {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, progressKindSourcePath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", progressKindSourcePath, err)
	}
	var kinds []controller.ProgressKind
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		ident, ok := spec.Type.(*ast.Ident)
		if !ok || ident.Name != "ProgressKind" {
			return true
		}
		for _, value := range spec.Values {
			lit, ok := value.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			kinds = append(kinds, controller.ProgressKind(lit.Value[1:len(lit.Value)-1]))
		}
		return true
	})
	return kinds
}

// TestEveryProgressKindMapsToARenderedEventKind is the mirror of
// TestEveryWorkflowEventKindIsClassified in internal/uiadapter.
//
// That test gates the far end of the bridge: every events.Kind the workflow
// publishes must have a notice-policy decision. It cannot see this end, where
// workflowProgressKind's `default:` quietly turns any UNMAPPED controller
// progress kind into events.KindWorkflowStepHeartbeat - the one kind the
// notice policy deliberately never renders. So an unmapped kind is not merely
// unclassified, it is silenced, and both gates pass while the operator sees
// nothing.
//
// That is exactly how controller.ProgressPanelMemberFailed was lost: a panel
// member failing under allow_partial reached the TUI as a heartbeat tick and
// was dropped, so a review synthesized from a degraded panel looked identical
// to a clean one.
func TestEveryProgressKindMapsToARenderedEventKind(t *testing.T) {
	declared := declaredProgressKinds(t)
	// The controller declares far more than a couple of kinds; a short list
	// means the scan broke, not that the mapping got simpler.
	if len(declared) < 10 {
		t.Fatalf("found only %d ProgressKind constants in %s: the scan is broken, not the mapping", len(declared), progressKindSourcePath)
	}
	for _, kind := range declared {
		if kind == controller.ProgressStepHeartbeat {
			continue // the one kind that IS a liveness tick
		}
		if got := workflowProgressKind(kind); got == events.KindWorkflowStepHeartbeat {
			t.Errorf("progress kind %q falls through workflowProgressKind's default to %q, "+
				"which the TUI notice policy never renders: add an explicit case, or make it a heartbeat on purpose",
				kind, got)
		}
	}
}

// TestPanelMemberFailureReachesTheOperator pins the specific regression: the
// degraded-review signal must travel on a kind the notice bridge renders.
func TestPanelMemberFailureReachesTheOperator(t *testing.T) {
	got := workflowProgressKind(controller.ProgressPanelMemberFailed)
	if got != events.KindWorkflowStepCompleted {
		t.Fatalf("panel_member_failed maps to %q, want %q so the failed member reaches the transcript", got, events.KindWorkflowStepCompleted)
	}
}

// TestRunStartedHasAProducer closes the other half of the same gap.
//
// TestEveryProgressKindMapsToARenderedEventKind proves every progress kind
// reaches a kind the UI renders. It cannot see the reverse: an events.Kind
// the notice policy renders that NOTHING can emit. workflow_run_started was
// exactly that - a rule for a line no code path could produce - so the window
// between admitting a run and its first step attempt reported nothing, and an
// operator could not tell a slow start from a wedged one.
func TestRunStartedHasAProducer(t *testing.T) {
	if got := workflowProgressKind(controller.ProgressRunStarted); got != events.KindWorkflowRunStarted {
		t.Fatalf("run_started maps to %q, want %q", got, events.KindWorkflowRunStarted)
	}
	if !strings.Contains(readEngineSource(t), "controller.ProgressRunStarted") {
		t.Fatal("no call site emits ProgressRunStarted: the run_started notice rule renders a line nothing can produce")
	}
}

// readEngineSource returns the launch path's source, so the test above checks
// for a real emit site rather than only for the mapping that would carry one.
func readEngineSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("workflow_tool_engine.go")
	if err != nil {
		t.Fatalf("read workflow_tool_engine.go: %v", err)
	}
	return string(data)
}

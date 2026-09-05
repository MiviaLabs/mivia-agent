package localengine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/controller"
)

// progressKindSourcePath is the declaration site the scan below reads.
const progressKindSourcePath = "../controller/progress.go"

// declaredProgressKinds returns every controller.ProgressKind constant value
// declared in the controller package via AST parsing.
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

// TestEveryProgressKindMappedInLocalEngine proves every progress kind is
// mapped to a rendered event kind in localengine, mirroring the cliworkflow gate.
func TestEveryProgressKindMappedInLocalEngine(t *testing.T) {
	declared := declaredProgressKinds(t)
	if len(declared) < 10 {
		t.Fatalf("found only %d ProgressKind constants in %s: the scan is broken, not the mapping", len(declared), progressKindSourcePath)
	}
	for _, kind := range declared {
		if kind == controller.ProgressStepHeartbeat {
			continue // the one kind that IS a liveness tick
		}
		if got := localProgressKind(kind); got == events.KindWorkflowStepHeartbeat {
			t.Errorf("progress kind %q falls through localProgressKind's default to %q, "+
				"which the TUI notice policy never renders: add an explicit case, or make it a heartbeat on purpose",
				kind, got)
		}
	}
}

// TestLocalPanelMemberFailureReachesTheOperator pins that panel member failure
// in localengine maps to a rendered event kind rather than being swallowed.
func TestLocalPanelMemberFailureReachesTheOperator(t *testing.T) {
	got := localProgressKind(controller.ProgressPanelMemberFailed)
	if got != events.KindWorkflowStepCompleted {
		t.Fatalf("panel_member_failed maps to %q, want %q", got, events.KindWorkflowStepCompleted)
	}
}

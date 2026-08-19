package replay

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

func TestApproverPushAndPending(t *testing.T) {
	a := NewApprover()
	req := ports.ApprovalRequest{ID: "call-1", ToolName: "run_command"}
	a.Push(req)

	select {
	case got := <-a.Pending():
		if got.ID != req.ID {
			t.Errorf("got %+v, want %+v", got, req)
		}
	default:
		t.Fatal("expected the pushed request to be immediately available on Pending()")
	}
}

func TestApproverResolveRecordsDecision(t *testing.T) {
	a := NewApprover()
	a.Resolve("call-1", ports.DecisionOnce)
	a.Resolve("call-2", ports.DecisionDenyAlways)

	got := a.Resolutions()
	if len(got) != 2 {
		t.Fatalf("got %d resolutions, want 2", len(got))
	}
	if got[0] != (Resolution{ID: "call-1", Decision: ports.DecisionOnce}) {
		t.Errorf("got %+v", got[0])
	}
	if got[1] != (Resolution{ID: "call-2", Decision: ports.DecisionDenyAlways}) {
		t.Errorf("got %+v", got[1])
	}
}

func TestApproverResolutionsReturnsCopy(t *testing.T) {
	a := NewApprover()
	a.Resolve("call-1", ports.DecisionOnce)
	got := a.Resolutions()
	got[0].ID = "mutated"
	if a.Resolutions()[0].ID != "call-1" {
		t.Error("Resolutions() must return a copy, mutation leaked into internal state")
	}
}

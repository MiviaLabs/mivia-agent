package sdkadapter

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	"github.com/MiviaLabs/mivia-ai-sdk/toolcallctx"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// idCapableTool records the call ID from its Run context so the test
// can prove the bridge surfaced the same ID both to EmitPending and to
// the wrapped CLI tool.
type idCapableTool struct {
	mu  sync.Mutex
	got string
}

func (*idCapableTool) Name() string               { return "id_tool" }
func (*idCapableTool) Description() string        { return "id tool" }
func (*idCapableTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (t *idCapableTool) Capability(json.RawMessage) tools.Capability {
	return tools.Capability{Class: tools.ExecutionWrite, ResourceKey: "id_tool"}
}
func (t *idCapableTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if tc, ok := toolcallctx.ToolCallFromContext(ctx); ok {
		t.mu.Lock()
		t.got = tc.ID
		t.mu.Unlock()
	}
	return "ran", nil
}

// TestApprovalPendingEmitsToolCallID pins the SDK approval bridge:
// EventToolPending (the surface the UI keys off to render an approval
// prompt and to resolve a decision by ID) must carry the call ID from
// context. A bridge that drops ToolCallID strands the approver: the UI
// resolves "" or no-id, the approver's waiting map has no matching key,
// the decision is silently dropped, and the gate hangs forever.
func TestApprovalPendingEmitsToolCallID(t *testing.T) {
	var pendingMu sync.Mutex
	var pendingID string
	pending := func(id, name, detail, input string) {
		pendingMu.Lock()
		defer pendingMu.Unlock()
		pendingID = id
	}

	gate := func(_ context.Context, _ string, _ json.RawMessage) ApprovalResult {
		return ApprovalResult{Approved: true}
	}

	cli := &idCapableTool{}
	reg := tools.NewRegistry()
	reg.Register(cli)
	sdkReg, err := ConvertToolRegistryWithAdmission(reg, AdmissionPredicates{
		ApprovalGate: gate,
		EmitPending:  pending,
	})
	if err != nil {
		t.Fatal(err)
	}

	wrapped, ok := sdkReg.Get("id_tool")
	if !ok {
		t.Fatal("id_tool not in sdk reg")
	}
	ctx := toolcallctx.WithToolCall(context.Background(), sdkshape.ToolCall{
		ID: "call-XYZ-7", Name: "id_tool", Index: 0, Arguments: []byte(`{}`),
	})
	out, err := wrapped.Run(ctx, sdktools.InOut{Value: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := out.Value.(string); s != "ran" {
		t.Fatalf("tool out=%q, want %q", s, "ran")
	}

	pendingMu.Lock()
	gotID := pendingID
	pendingMu.Unlock()
	if gotID == "" {
		t.Fatal("emitPending never fired; SDK approval bridge dropped the pending event")
	}
	if gotID != "call-XYZ-7" {
		t.Fatalf("emitPending received id=%q, want %q (call ID dropped on the bridge)", gotID, "call-XYZ-7")
	}

	cli.mu.Lock()
	gotToolID := cli.got
	cli.mu.Unlock()
	if gotToolID != "call-XYZ-7" {
		t.Fatalf("wrapped tool saw call ID %q, want %q", gotToolID, "call-XYZ-7")
	}
}

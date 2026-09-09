package sdkadapter

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkagentloop "github.com/MiviaLabs/mivia-ai-sdk/agentloop"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/provider"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// A tool call whose ID is empty is RECORDED by name and never PROMPTED. The
// two keys this file pins are deliberately different, and the difference is
// the point.
//
// call.ID goes empty when a provider stream sends the tool-call NAME delta
// before, or without, the ID delta. The outcome recorder keys id-else-name
// (internal/agent's toolCallKey), so a denial recorded under a blank id is
// dropped and the refusal reaches the operator as a bare "failed" with no
// reason - recordKeyFromContext exists to stop that.
//
// The PROMPT key does not fall back, and this test pins that too. A tool name
// is not a per-call identity: uiadapter cannot register a waiting channel on
// it without two overlapping calls to one tool colliding, and the operator's
// single decision then authorizing the call they were never shown. The
// accepted cost is that an ID-less call raises a prompt nothing can answer and
// returns canceled at its context deadline. A hang is a bad outcome;
// approving an unseen command is a worse one.
func TestIDLessCallIsRecordedByNameButNeverPrompted(t *testing.T) {
	var mu sync.Mutex
	var deniedID, pendingID string

	pred := AdmissionPredicates{
		ApprovalGate: func(context.Context, string, json.RawMessage) ApprovalResult {
			return ApprovalResult{Approved: false, Err: "nope"}
		},
		EmitPending: func(id, name, detail, input string) {
			mu.Lock()
			defer mu.Unlock()
			pendingID = id
		},
		RecordDenied: func(id, name, reason string) {
			mu.Lock()
			defer mu.Unlock()
			deniedID = id
		},
	}

	reg := tools.NewRegistry()
	reg.Register(&deniableTool{})
	sdkReg, err := ConvertToolRegistryWithAdmission(reg, pred)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := sdkReg.Get("deny_tool")
	if !ok {
		t.Fatal("deny_tool not in sdk reg")
	}

	// ID empty, Name set: the shape a name-before-id stream produces.
	ctx := sdkagentloop.WithToolCall(context.Background(), sdkshape.ToolCall{
		Name: "deny_tool", Arguments: []byte(`{}`),
	})
	if _, err := wrapped.Run(ctx, sdktools.InOut{Value: json.RawMessage(`{}`)}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if deniedID != "deny_tool" {
		t.Errorf("RecordDenied id = %q, want the name fallback %q; an outcome recorded "+
			"under an empty id is dropped, and the refusal reaches the operator with "+
			"no reason", deniedID, "deny_tool")
	}
	// The PROMPT key deliberately does not fall back. uiadapter cannot key its
	// waiting map on a name without cross-wiring two overlapping calls to the
	// same tool, so an ID-less call still cannot raise an answerable prompt.
	// Pinned so that a later "fix" to make these two symmetric has to read why
	// they are not.
	if pendingID != "" {
		t.Errorf("EmitPending id = %q, want empty; a name published here would have to "+
			"be registrable by uiadapter, and a tool name is not a per-call identity",
			pendingID)
	}
}

// TestRecordKeyWithoutAToolCall covers the branch for a ctx that carries no
// tool call at all: a direct caller, or a hand-built fixture outside the loop.
// It must answer "" rather than inventing a key, because recordToolOutcome
// drops an empty id - dropping the record is the honest outcome when there is
// no call to attribute it to, and a synthesised key would attach the denial to
// whatever row happened to share it.
func TestRecordKeyWithoutAToolCall(t *testing.T) {
	if got := recordKeyFromContext(context.Background()); got != "" {
		t.Errorf("recordKeyFromContext(bare ctx) = %q, want empty", got)
	}
	// And the prompt key agrees, for the same reason.
	if got := callIDFromContext(context.Background()); got != "" {
		t.Errorf("callIDFromContext(bare ctx) = %q, want empty", got)
	}
}

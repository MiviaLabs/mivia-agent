package subagents

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// unregistrableTool is a registry entry the dispatcher refuses: a handler
// with no name cannot be addressed, so registration fails. It stands in for
// any tool the parent registry holds that the scoped dispatcher cannot
// install.
type unregistrableTool struct{}

func (unregistrableTool) Name() string        { return "" }
func (unregistrableTool) Description() string { return "a tool the dispatcher cannot install" }
func (unregistrableTool) Parameters() map[string]any {
	return map[string]any{"type": "object"}
}
func (unregistrableTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// TestNewScopedLoopFailsClosedWhenTheDispatcherCannotBeBuilt pins the
// authorization boundary. scopedLoop pairs the nested agent loop with the
// dispatcher built from the SAME restricted registry; that pairing is what
// stops a delegated task from executing through the parent's tools. A
// dispatcher that failed to build must abort the pairing entirely, not hand
// back a loop the caller could still run.
func TestNewScopedLoopFailsClosedWhenTheDispatcherCannotBeBuilt(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(unregistrableTool{})
	h := &MultiStepHandler{FullRegistry: reg}

	scoped, err := h.newScopedLoop()
	if err == nil {
		t.Fatal("a scoped loop was built on a dispatcher that could not be constructed")
	}
	if scoped != nil {
		t.Fatalf("newScopedLoop returned a loop (%+v) alongside its error; the pair must be all or nothing", scoped)
	}
	if !strings.HasPrefix(err.Error(), "scoped tool dispatcher: ") {
		t.Fatalf("error %q does not attribute the failure to the scoped dispatcher", err)
	}
	if !strings.Contains(err.Error(), "invalid handler") {
		t.Fatalf("error %q drops the underlying registration failure", err)
	}
}

// TestSeedMessagesPlacesTheMemoryFrameAfterTheSystemPrompt pins the
// position and framing of the core-memory context. RoleSystem is only
// valid at index 0, so the frame rides as a sentinel-named user message,
// and it must precede the task prompt the loop appends: background context
// after the objective reads as a new instruction.
func TestSeedMessagesPlacesTheMemoryFrameAfterTheSystemPrompt(t *testing.T) {
	h := &MultiStepHandler{SystemPrompt: "sub-agent prompt", MemoryContext: "remembered facts"}

	msgs := h.seedMessages()

	if len(msgs) != 2 {
		t.Fatalf("seedMessages returned %d messages; want the system prompt plus the memory frame", len(msgs))
	}
	if msgs[0].Role != provider.RoleSystem || msgs[0].Content != "sub-agent prompt" {
		t.Fatalf("message 0 = %+v; want the system prompt", msgs[0])
	}
	frame := msgs[1]
	if frame.Role != provider.RoleUser {
		t.Fatalf("memory frame role = %q; want user (RoleSystem is only valid at index 0)", frame.Role)
	}
	if frame.Name != chat.MemoryContextMessageName {
		t.Fatalf("memory frame Name = %q; want the %q sentinel so the accounting classifies it as memory", frame.Name, chat.MemoryContextMessageName)
	}
	if frame.Content != "remembered facts" {
		t.Fatalf("memory frame content = %q; want the rendered context verbatim", frame.Content)
	}

	// No memory context: no frame at all, so an empty frame never occupies
	// a slot the task prompt reads as background.
	h.MemoryContext = ""
	if bare := h.seedMessages(); len(bare) != 1 {
		t.Fatalf("seedMessages with no memory context returned %d messages; want only the system prompt", len(bare))
	}
}

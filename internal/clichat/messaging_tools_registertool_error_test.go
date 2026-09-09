package clichat

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// fakePostMessageNameTool is a minimal tools.Tool occupying the
// "post_message" dispatcher slot without going through the registry, so
// registerMessagingTools' own reg.Get(post.Name()) existence check (which
// looks at the REGISTRY, not the dispatcher) does not see it and proceeds
// to call d.RegisterTool, which then collides at the dispatcher level.
type fakePostMessageNameTool struct{}

func (fakePostMessageNameTool) Name() string        { return "post_message" }
func (fakePostMessageNameTool) Description() string { return "occupies the post_message slot" }
func (fakePostMessageNameTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
}
func (fakePostMessageNameTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// TestRegisterMessagingTools_RegisterToolErrorSurfaces pins the
// d.RegisterTool(reg, post) error wrap: a dispatcher that already has a
// "post_message" handler (registered outside the registry passed to
// registerMessagingTools, so the registry-existence check does not skip
// it) must surface the resulting duplicate-handler error rather than
// silently continue.
func TestRegisterMessagingTools_RegisterToolErrorSurfaces(t *testing.T) {
	d := runtime.New(runtime.Policy{})
	if err := d.RegisterTool(tools.NewRegistry(), fakePostMessageNameTool{}); err != nil {
		t.Fatalf("pre-register post_message: %v", err)
	}

	reg := tools.NewRegistry()
	repo := ledger.NewMemoryLedgerRepository()
	err := registerMessagingTools(d, reg, config.DefaultSubagentConfig, repo, nil, nil)
	if err == nil {
		t.Fatal("registerMessagingTools accepted a dispatcher with a colliding post_message handler")
	}
	if !strings.Contains(err.Error(), "register post_message") {
		t.Fatalf("err = %v, want the register-post_message wrap", err)
	}
}

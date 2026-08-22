package clichat

// chat_slash_handlers_coverage_test.go covers the small public helpers in
// chat_slash_handlers.go. The slash handlers themselves depend on real
// session state and a configured workspace; we cover the pure helpers
// (CompactStructuralOnlyNotice, RegistryForState).

import (
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestCompactStructuralOnlyNotice(t *testing.T) {
	if got := CompactStructuralOnlyNotice("pruned older turns"); !strings.Contains(got, "pruned older turns") {
		t.Fatalf("CompactStructuralOnlyNotice must echo reason; got %q", got)
	}
	if got := CompactStructuralOnlyNotice(""); got == "" {
		t.Fatal("CompactStructuralOnlyNotice(empty) must still produce a sentinel string")
	}
}

func TestRegistryForState(t *testing.T) {
	reg := agents.NewRegistry()
	_ = reg.Publish(agents.ResolvedAgent{Name: "alpha"})
	state := &AgentSessionState{
		Registry: reg,

		ToolBase: tools.NewRegistry(),
	}
	got := RegistryForState(state)
	if got == nil {
		t.Fatal("RegistryForState must return the state's registry, not nil")
	}
}

func TestCompactResultMessageAndReportCompactFailure(t *testing.T) {
	sess := chat.NewSession(&config.Resolved{Model: "x", ProviderName: "p"}, nil)
	_ = compactResultMessage(sess, &config.Resolved{}, chat.ContextUsage{})
	reportCompactFailure(nil, errors.New("simulated failure"))
}

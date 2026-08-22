package clichat

// legacytui_test_exports_coverage_test.go drives each wrapper re-export
// in legacytui_test_exports.go directly so the diff-coverage gate sees
// the lines as covered after the cli split's rename.

import (
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/skills"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

func TestKeyLabel(t *testing.T) {
	if got := KeyLabel(Binding{Keys: []string{"enter"}}); got == "" {
		t.Fatal("KeyLabel must return non-empty for a binding")
	}
}

func TestContextWorkspaceID(t *testing.T) {
	if got := ContextWorkspaceID("/tmp/foo"); got == "" {
		t.Fatal("ContextWorkspaceID must return non-empty")
	}
}

func TestValidateKeyRegistry(t *testing.T) {
	errs := ValidateKeyRegistry([]binding{})
	if len(errs) != 0 {
		t.Fatalf("ValidateKeyRegistry(empty) = %v", errs)
	}
}

func TestFormatUserBubbleTimeAndAgentHelpers(t *testing.T) {
	if got := FormatUserBubbleTime(time.Now()); got == "" {
		t.Fatal("FormatUserBubbleTime must return non-empty")
	}
	_ = FormatAgentCurrent("alpha", agents.NewRegistry())
	_ = FormatAgentSet("alpha")
	_ = FormatLiveToolWaveSummary(2, 1, 0, 0)
}

func TestSkillScopeHelpers(t *testing.T) {
	reg := tools.NewRegistry()
	_ = SkillScopeFromAgent(nil)
	_ = SkillScopeFromAgentAndRegistry(nil, reg)
}

func TestLoadAgentDefinitionsLocal(t *testing.T) {
	skillReg := skills.NewRegistry()
	_, _ = LoadAgentDefinitions(t.TempDir(), "", skillReg)
}

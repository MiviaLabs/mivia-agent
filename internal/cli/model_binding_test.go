package cli

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

const (
	inPlaceThinker = "glm-5.2"
	inPlacePlain   = "glm-4.6"
)

// inPlaceSwitchConfig has no provider runtimes, which is what routes
// switchModelCommand through its in-place rename branch.
func inPlaceSwitchConfig() *config.Resolved {
	return &config.Resolved{
		ProviderName: "zai",
		Model:        inPlaceThinker,
		Models:       []string{inPlaceThinker, inPlacePlain},
		ModelProfiles: []config.ModelSpec{
			{
				Name: inPlaceThinker, ContextWindowTokens: 200000,
				ReasoningEfforts: []reasoning.Level{reasoning.Low, reasoning.Medium, reasoning.High},
				Reasoning:        reasoning.High,
				ReasoningDialect: reasoning.DialectThinkingEffort,
			},
			{Name: inPlacePlain, ContextWindowTokens: 200000},
		},
	}
}

// An /effort choice is session state. Publishing it as the model's profile
// would make it configuration, and nothing could restore the real default.
func TestSwitchModelInPlaceDoesNotPublishTheEffortOverride(t *testing.T) {
	res := inPlaceSwitchConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	if err := sess.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if err := switchModelCommand(sess, res, "zai", inPlaceThinker); err != nil {
		t.Fatal(err)
	}
	if got := sess.ReasoningDefault(); got != reasoning.High {
		t.Fatalf("configured default = %q, want high", got)
	}
	if got := sess.ReasoningEffort(); got != reasoning.High {
		t.Fatalf("effective effort = %q, want the configured default high", got)
	}
	if got := sess.ReasoningChoices(); len(got) != 3 {
		t.Fatalf("declared efforts = %v, want the model's three levels", got)
	}
	if err := sess.SetReasoningEffort(reasoning.Medium); err != nil {
		t.Fatalf("/effort is refusing a declared level after the switch: %v", err)
	}
}

// The other half of the contract: a real rename must still drop the previous
// model's reasoning surface.
func TestSwitchModelInPlaceDropsTheReasoningSurfaceOnRename(t *testing.T) {
	res := inPlaceSwitchConfig()
	sess := chat.NewSession(res, welcomeStubCompleter{})
	if err := sess.SetReasoningEffort(reasoning.Low); err != nil {
		t.Fatal(err)
	}
	if err := switchModelCommand(sess, res, "zai", inPlacePlain); err != nil {
		t.Fatal(err)
	}
	if got := sess.ReasoningDefault(); got.Active() {
		t.Fatalf("renamed selection kept reasoning default %q", got)
	}
	if got := sess.ReasoningChoices(); len(got) != 0 {
		t.Fatalf("renamed selection kept declared efforts %v", got)
	}
}

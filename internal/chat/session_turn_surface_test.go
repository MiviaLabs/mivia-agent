package chat

import (
	"context"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// TestScopedTurnAdvertisesTheSessionSnapshot pins plan tools-advertising/01's
// skill-scope decision: a scoped turn (TurnOptions non-nil, the skill-turn
// path) narrows EXECUTION (Registry/Dispatcher come from turn.Tools/turn.
// Dispatcher when set) but still advertises the session's pinned snapshot,
// not the narrower scoped registry - skill activation mid-turn must not
// change the advertised array.
func TestScopedTurnAdvertisesTheSessionSnapshot(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "read_file"})
	full.Register(fixedBodyTool{name: "grep"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	scoped := tools.NewRegistry()
	scoped.Register(fixedBodyTool{name: "read_file"})
	turn := &TurnOptions{Tools: scoped}

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, turn)
	surf := opts.Surface()

	if surf.Registry != scoped {
		t.Fatalf("scoped turn execution registry = %v, want the turn-scoped registry", surf.Registry)
	}
	wantAdvertised := full.OpenAITools()
	if len(surf.ToolSpecs) != len(wantAdvertised) {
		t.Fatalf("scoped turn advertised %d tools, want %d (the full session snapshot, not the %d-tool scoped registry)",
			len(surf.ToolSpecs), len(wantAdvertised), len(scoped.List()))
	}

	// A root (unscoped) turn advertises the identical snapshot.
	var rootOpts agent.Options
	s.wireStepBoundaryAdmission(&rootOpts, nil)
	rootSurf := rootOpts.Surface()
	if len(rootSurf.ToolSpecs) != len(surf.ToolSpecs) {
		t.Fatalf("root and scoped turns advertised different counts: %d vs %d", len(rootSurf.ToolSpecs), len(surf.ToolSpecs))
	}
}

// TestUnadmittedAdvertisedToolAutoStagesAndRefuses pins the model-behavior
// risk resolution for plan tools-advertising/01: advertising the whole
// admissible union means a deferred tool now LOOKS callable before load_tools
// ever ran for it. A call to such a tool must be refused (never executed) but
// also auto-staged, so the model recovers in exactly one extra step instead
// of learning to call load_tools first. A call to a name outside the
// advertised snapshot (hallucinated) must fall through unrecognized.
func TestUnadmittedAdvertisedToolAutoStagesAndRefuses(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "read_file"})
	full.Register(fixedBodyTool{name: "grep"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, nil)
	if opts.UnadmittedToolHandler == nil {
		t.Fatal("UnadmittedToolHandler not wired")
	}

	msg, ok := opts.UnadmittedToolHandler(context.Background(), "grep")
	if !ok {
		t.Fatal("advertised-but-unadmitted tool was not recognized")
	}
	if msg == "" {
		t.Fatal("refusal message is empty")
	}
	stage, has := s.PendingAdmission()
	if !has {
		t.Fatal("the call did not auto-stage grep for admission")
	}
	if len(stage.Names) != 1 || stage.Names[0] != "grep" {
		t.Fatalf("staged names = %v, want [grep]", stage.Names)
	}

	// A hallucinated name outside the advertised snapshot is not recognized:
	// the caller falls through to the generic denial, and nothing is staged.
	if _, ok := opts.UnadmittedToolHandler(context.Background(), "no_such_tool"); ok {
		t.Fatal("a name outside the advertised snapshot must not be recognized")
	}
}

// TestUnadmittedToolHandlerDoesNotAutoStageForScopedTurns pins that a scoped
// skill turn does not own the session's admission state: an unadmitted call
// there is refused but never staged, matching the Surface hook's own
// turn == nil gate for PublishPendingAdmissionAtStepBoundary.
func TestUnadmittedToolHandlerDoesNotAutoStageForScopedTurns(t *testing.T) {
	s := prefixResetSession(t)

	full := tools.NewRegistry()
	full.Register(fixedBodyTool{name: "read_file"})
	full.Register(fixedBodyTool{name: "grep"})
	s.PublishAgentSurface("p", 0, full, nil, nil, "", full.OpenAITools())

	scoped := tools.NewRegistry()
	scoped.Register(fixedBodyTool{name: "read_file"})
	turn := &TurnOptions{Tools: scoped}

	var opts agent.Options
	s.wireStepBoundaryAdmission(&opts, turn)
	if _, ok := opts.UnadmittedToolHandler(context.Background(), "grep"); !ok {
		t.Fatal("advertised tool must still be recognized (refused) in a scoped turn")
	}
	if _, has := s.PendingAdmission(); has {
		t.Fatal("a scoped turn must not auto-stage into the session's admission state")
	}
}

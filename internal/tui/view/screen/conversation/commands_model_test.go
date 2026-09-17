package conversation

import (
	"context"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/replay"
)

type modelSelectionFakeRunner struct {
	*fakeRunner
	selectForProviderCalls []string
	selectForProviderOut   ports.CommandOutcome
}

func (m *modelSelectionFakeRunner) SelectModelForProvider(_ context.Context, provider, model string) ports.CommandOutcome {
	m.selectForProviderCalls = append(m.selectForProviderCalls, provider+"|"+model)
	return m.selectForProviderOut
}

func TestGroupedDuplicateModelSelectionCallsOptionalCapability(t *testing.T) {
	runner := &modelSelectionFakeRunner{
		fakeRunner: &fakeRunner{
			outcome: ports.CommandOutcome{
				ModelChoiceGroups: []ports.ModelChoiceGroup{
					{Provider: "provider-a", Models: []string{"claude-sonnet-5"}},
					{Provider: "provider-b", Models: []string{"claude-sonnet-5"}},
				},
			},
		},
		selectForProviderOut: ports.CommandOutcome{Notice: "model set to claude-sonnet-5 (provider-b)"},
	}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/model")
	if s.modelPicker == nil {
		t.Fatal("expected the model picker to be open")
	}

	// Move down past provider-a's model and provider-b's header to provider-b's model
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // down to header provider-b
	s = next.(Screen)
	next, _ = s.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // down to provider-b's claude-sonnet-5
	s = next.(Screen)

	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if s.modelPicker != nil {
		t.Error("expected the picker to close after a selection")
	}
	if len(runner.selectForProviderCalls) != 1 || runner.selectForProviderCalls[0] != "provider-b|claude-sonnet-5" {
		t.Fatalf("got selectForProviderCalls %v, want [\"provider-b|claude-sonnet-5\"]", runner.selectForProviderCalls)
	}
	if len(runner.selectCalls) != 0 {
		t.Fatalf("got base SelectModel calls %v, want 0", runner.selectCalls)
	}
	if got := lastErrorDetail(t, s); got != "model set to claude-sonnet-5 (provider-b)" {
		t.Fatalf("got notice detail %q, want %q", got, "model set to claude-sonnet-5 (provider-b)")
	}
	if !hasClearScreen(cmd) {
		t.Error("expected closing the picker on selection to clear the screen")
	}
}

func TestGroupedModelSelectionWithoutCapabilityReturnsError(t *testing.T) {
	runner := &fakeRunner{
		outcome: ports.CommandOutcome{
			ModelChoiceGroups: []ports.ModelChoiceGroup{
				{Provider: "provider-a", Models: []string{"model-1"}},
			},
		},
	}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s, _ = sendLine(t, s, "/model")
	if s.modelPicker == nil {
		t.Fatal("expected the model picker to be open")
	}

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)

	if got := lastErrorDetail(t, s); got != "command runner does not support provider-aware model selection" {
		t.Fatalf("got error %q, want runner does not support provider-aware model selection", got)
	}
}

func TestGroupedModelPickerCtrlCAndEscapeRetainBehavior(t *testing.T) {
	runner := &modelSelectionFakeRunner{
		fakeRunner: &fakeRunner{
			outcome: ports.CommandOutcome{
				ModelChoiceGroups: []ports.ModelChoiceGroup{
					{Provider: "provider-a", Models: []string{"model-1"}},
					{Provider: "provider-b", Models: []string{"model-2"}},
				},
			},
		},
	}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)

	// 1. Esc closes picker without notice and calls no runner method
	s, _ = sendLine(t, s, "/model")
	if s.modelPicker == nil {
		t.Fatal("expected model picker to open")
	}
	beforeBlocks := len(s.transcript.Blocks())
	next, cmd := s.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	s = next.(Screen)
	if s.modelPicker != nil {
		t.Error("expected esc to close the grouped model picker")
	}
	if len(s.transcript.Blocks()) != beforeBlocks {
		t.Errorf("got %d blocks, want %d (esc adds no notice)", len(s.transcript.Blocks()), beforeBlocks)
	}
	if len(runner.selectForProviderCalls) != 0 || len(runner.selectCalls) != 0 {
		t.Errorf("runner was called on esc: %v, %v", runner.selectForProviderCalls, runner.selectCalls)
	}
	if !hasClearScreen(cmd) {
		t.Error("expected closing the picker on esc to clear the screen")
	}

	// 2. Ctrl+C closes picker and arms quit
	s, _ = sendLine(t, s, "/model")
	if s.modelPicker == nil {
		t.Fatal("expected model picker to open again")
	}
	next, cmd = s.Update(ctrl('c'))
	s = next.(Screen)
	if s.modelPicker != nil {
		t.Error("expected ctrl+c to close the grouped model picker")
	}
	if !s.quitArmed {
		t.Error("expected quit to be armed on first ctrl+c")
	}
	if !hasClearScreen(cmd) {
		t.Error("expected ctrl+c to clear screen")
	}
}

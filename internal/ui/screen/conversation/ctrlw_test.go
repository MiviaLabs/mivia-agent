package conversation

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

// Ctrl+W worktree-shortcut dispatch tests (split from commands_test.go to
// stay under the file-size policy): every case drives Screen.Update with a
// real ctrl('w') key message through the production key path.

func TestSessionPickerCtrlWWithFilterDoesNotStartWorktree(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	runner := &fakeRunner{
		outcome:       ports.CommandOutcome{SessionChoices: sampleSessions(now)},
		selectOutcome: ports.CommandOutcome{Notice: "resumed"},
	}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s.width, s.height = 60, 24
	s, _ = sendLine(t, s, "/resume")

	// Type a filter first so the picker's filter is non-empty.
	next, _ := s.Update(key("w"))
	s = next.(Screen)
	before := len(runner.selectCalls)

	// Ctrl+W with a non-empty filter must NOT dispatch worktree creation.
	next, _ = s.Update(ctrl('w'))
	s = next.(Screen)
	if len(runner.selectCalls) > before {
		t.Errorf("filtered picker dispatched worktree creation on ctrl+w: %v", runner.selectCalls[before:])
	}
	if s.sessionPicker == nil {
		t.Error("picker should stay open when ctrl+w is guarded by the filter")
	}
}
func TestSessionPickerCtrlWEmptyFilterStartsWorktree(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	runner := &fakeRunner{
		outcome:       ports.CommandOutcome{SessionChoices: sampleSessions(now)},
		selectOutcome: ports.CommandOutcome{Notice: "started"},
	}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s.width, s.height = 60, 24
	s, _ = sendLine(t, s, "/resume")

	// Empty filter: ctrl+w dispatches StartInNewWorktree and closes the picker.
	next, _ := s.Update(ctrl('w'))
	s = next.(Screen)
	if len(runner.selectCalls) != 1 || !strings.Contains(runner.selectCalls[0], "start-new-worktree:") {
		t.Fatalf("ctrl+w did not dispatch worktree start; selectCalls=%v", runner.selectCalls)
	}
	if s.sessionPicker != nil {
		t.Error("picker should close after ctrl+w dispatch")
	}
}
func TestScreenCtrlWOnEmptyComposerDispatchesWorktreeCreation(t *testing.T) {
	runner := &fakeRunner{}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s.width, s.height = 60, 24

	if !s.composer.IsEmpty() {
		t.Fatal("precondition: composer should be empty")
	}
	next, _ := s.Update(ctrl('w'))
	s = next.(Screen)
	if len(runner.selectCalls) != 1 || !strings.Contains(runner.selectCalls[0], "start-new-worktree:") {
		t.Fatalf("empty-composer ctrl+w did not dispatch worktree start; selectCalls=%v", runner.selectCalls)
	}
}
func TestScreenCtrlWWithTextKeepsDeleteWord(t *testing.T) {
	runner := &fakeRunner{}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s.width, s.height = 60, 24

	// Non-empty composer: ctrl+w must fall through to delete-word, not the worktree arm.
	next, _ := s.Update(typeInto("hello"))
	s = next.(Screen)
	before := len(runner.selectCalls)
	next, _ = s.Update(ctrl('w'))
	s = next.(Screen)
	if len(runner.selectCalls) > before {
		t.Errorf("ctrl+w fired worktree start with text in composer: %v", runner.selectCalls[before:])
	}
	// The fall-through must actually DELETE the word - a swallowed key
	// would also leave selectCalls untouched.
	if got := s.composer.Value(); got != "" {
		t.Errorf("ctrl+w did not delete the composer word: %q", got)
	}
}

// TestScreenCtrlWEmbeddedIsInert pins the !s.embedded guard: an embedded
// screen (subagent thread dialog) must not start worktrees on ctrl+w -
// deleting the guard passes every other test in this file.
func TestScreenCtrlWEmbeddedIsInert(t *testing.T) {
	runner := &fakeRunner{}
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	s.SetCommandRunner(runner)
	s.width, s.height = 60, 24
	s.embedded = true

	next, _ := s.Update(ctrl('w'))
	s = next.(Screen)
	if len(runner.selectCalls) != 0 {
		t.Errorf("embedded ctrl+w dispatched runner calls: %v", runner.selectCalls)
	}
}
func typeInto(text string) tea.Msg {
	// Build a paste-style message so the composer receives literal text.
	return tea.KeyPressMsg{Text: text, Code: rune(text[0])}
}

func TestSessionPickerWorktreeRowsDispatchThroughTheRunner(t *testing.T) {
	routeRow := ports.SessionSummary{
		ID: "worktree:wt1", Title: "Worktree · wt1",
		Worktree: "wt1", WorktreeRoute: true,
	}
	boundRow := ports.SessionSummary{
		ID: "bound-wt1", Title: "Worktree Work",
		Worktree:           "wt1",
		WorktreeDir:        "/repo/.mivia/worktrees/wt1",
		WorktreeInstanceID: "wt_0000000000000001",
	}

	t.Run("route row starts a session in the worktree", func(t *testing.T) {
		runner := &fakeRunner{
			outcome: ports.CommandOutcome{Notice: "Started new session in worktree wt1."},
		}
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		s.SetCommandRunner(runner)
		s.width, s.height = 60, 24
		sp := newSessionPicker(s.Theme, s.Tier, []ports.SessionSummary{routeRow})
		s.sessionPicker = &sp
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		s = next.(Screen)

		if s.sessionPicker != nil {
			t.Error("enter left the session picker open")
		}
		if len(runner.selectCalls) != 1 || runner.selectCalls[0] != "start-worktree:wt1" {
			t.Errorf("runner calls = %v, want [start-worktree:wt1]", runner.selectCalls)
		}
	})

	t.Run("bound row resumes through the worktree path", func(t *testing.T) {
		runner := &fakeRunner{
			outcome: ports.CommandOutcome{Notice: "Resumed session in worktree wt1."},
		}
		s := newScreen(t, replay.New(nil, 0), nil, nil)
		s.SetCommandRunner(runner)
		s.width, s.height = 60, 24
		sp := newSessionPicker(s.Theme, s.Tier, []ports.SessionSummary{boundRow})
		s.sessionPicker = &sp
		next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		s = next.(Screen)

		if s.sessionPicker != nil {
			t.Error("enter left the session picker open")
		}
		if len(runner.selectCalls) != 1 || runner.selectCalls[0] != "resume-worktree:bound-wt1" {
			t.Errorf("runner calls = %v, want [resume-worktree:bound-wt1]", runner.selectCalls)
		}
	})
}

func TestSessionPickerWorktreeEnterWithoutRunner(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	sp := newSessionPicker(s.Theme, s.Tier, []ports.SessionSummary{{
		ID: "worktree:wt1", Title: "Worktree · wt1",
		Worktree: "wt1", WorktreeRoute: true,
	}})
	s.sessionPicker = &sp

	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if got := lastErrorDetail(t, s); !strings.Contains(got, "no command runner configured for /resume") {
		t.Errorf("error detail %q, want the no-runner notice", got)
	}
}

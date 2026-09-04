package conversation

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/replay"
)

func TestSessionPickerWorktreeRowsDispatchThroughTheRunner(t *testing.T) {
	routeRow := ports.SessionSummary{ID: "worktree:wt1", Title: "Worktree · wt1", Worktree: "wt1", WorktreeRoute: true}
	boundRow := ports.SessionSummary{ID: "bound-wt1", Title: "Worktree Work", Worktree: "wt1", WorktreeDir: "/repo/.mivia/worktrees/wt1"}
	for _, tc := range []struct {
		name, rowID, call string
		row               ports.SessionSummary
	}{
		{"route row", routeRow.ID, "start-worktree:wt1", routeRow},
		{"bound row", boundRow.ID, "resume-worktree:bound-wt1", boundRow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{outcome: ports.CommandOutcome{Notice: "worktree"}}
			s := newScreen(t, replay.New(nil, 0), nil, nil)
			s.SetCommandRunner(runner)
			s.width, s.height = 60, 24
			sp := newSessionPicker(s.Theme, s.Tier, []ports.SessionSummary{tc.row})
			s.sessionPicker = &sp
			next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			s = next.(Screen)
			if s.sessionPicker != nil || len(runner.selectCalls) != 1 || runner.selectCalls[0] != tc.call {
				t.Fatalf("picker=%v calls=%v, want closed and [%s]", s.sessionPicker != nil, runner.selectCalls, tc.call)
			}
		})
	}
}

func TestSessionPickerWorktreeEnterWithoutRunner(t *testing.T) {
	s := newScreen(t, replay.New(nil, 0), nil, nil)
	sp := newSessionPicker(s.Theme, s.Tier, []ports.SessionSummary{{ID: "worktree:wt1", Title: "Worktree · wt1", Worktree: "wt1", WorktreeRoute: true}})
	s.sessionPicker = &sp
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if got := lastErrorDetail(t, s); !strings.Contains(got, "no command runner configured for /resume") {
		t.Errorf("error detail %q, want the no-runner notice", got)
	}
}

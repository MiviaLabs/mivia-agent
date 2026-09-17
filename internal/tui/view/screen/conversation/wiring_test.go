package conversation

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/tui/kit/config"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/tui/kit/uievent"
	"github.com/MiviaLabs/mivia-agent/internal/tui/view/component/composer"
)

// TestSetCommandsPropagatesToOpenThread pins wiring.go's SetCommands
// thread-forwarding branch: once a subagent thread dialog is open,
// s.thread is non-nil, and a command set pushed onto the OUTER Screen
// must reach the embedded thread's own composer too - the harness calls
// SetCommands at any point in the run, not only before a thread opens.
func TestSetCommandsPropagatesToOpenThread(t *testing.T) {
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}}, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("expected the thread dialog to be open")
	}

	cmds := []composer.Command{{Name: "review", Desc: "review the diff"}}
	s.SetCommands(cmds)

	if got := s.thread.composer.Commands(); len(got) != 1 || got[0].Name != "review" {
		t.Fatalf("thread composer commands = %+v, want the propagated set %+v", got, cmds)
	}
}

// TestSetMentionsPropagatesToOpenThread mirrors
// TestSetCommandsPropagatesToOpenThread for SetMentions.
func TestSetMentionsPropagatesToOpenThread(t *testing.T) {
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}}, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("expected the thread dialog to be open")
	}

	mentions := []composer.Mention{{Path: "wiring.go"}}
	s.SetMentions(mentions)

	if got := s.thread.composer.Mentions(); len(got) != 1 || got[0].Path != "wiring.go" {
		t.Fatalf("thread composer mentions = %+v, want the propagated set %+v", got, mentions)
	}
}

// TestSetCommandRunnerPropagatesToOpenThread mirrors the pair above for
// SetCommandRunner: the thread's own runner field must pick up the new
// runner, not just the outer screen's.
func TestSetCommandRunnerPropagatesToOpenThread(t *testing.T) {
	s := threadScreen(t, stubThreads{"sa-1": &scriptedThread{events: make(chan uievent.Event, 4)}}, false)
	next, _ := s.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	s = next.(Screen)
	if s.thread == nil {
		t.Fatal("expected the thread dialog to be open")
	}

	runner := &fakeRunner{outcome: ports.CommandOutcome{Notice: "ran"}}
	s.SetCommandRunner(runner)

	if s.thread.runner != ports.CommandRunner(runner) {
		t.Fatalf("thread runner = %v, want the propagated runner %v", s.thread.runner, runner)
	}
}

// TestObserveAgentRecordsIntoPanel pins wiring.go's ObserveAgent: it is
// the harness's direct-call seam (distinct from the event-driven
// KindToolOutput path other tests exercise through Update), and must
// still land the progress update in the activity panel's agent rows.
func TestObserveAgentRecordsIntoPanel(t *testing.T) {
	s := sized(t, 1)
	next, _ := s.Update(tea.WindowSizeMsg{Width: uikitconfig.BreakpointWide, Height: 30})
	scr := next.(Screen)

	scr.ObserveAgent("sa-direct", &uievent.Progress{Status: "running", Step: 2, TotalSteps: 5, Log: []string{"scanning"}})

	if len(scr.panel.agents) != 1 {
		t.Fatalf("panel.agents = %+v, want exactly 1 row from ObserveAgent", scr.panel.agents)
	}
	row := scr.panel.agents[0]
	if row.ID != "sa-direct" || row.Status != "running" || row.Step != 2 || row.Total != 5 {
		t.Errorf("panel row = %+v, want ID=sa-direct Status=running Step=2 Total=5", row)
	}
}

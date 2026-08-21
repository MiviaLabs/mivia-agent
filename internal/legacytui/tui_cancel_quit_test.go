package legacytui

import (
	"context"
	"github.com/MiviaLabs/mivia-agent/internal/cli"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TDD: second Ctrl+C after cancel must not strand the UI on
// "(quitting after cancel completes…)" when bridge Done was already drained
// before quitRequested was set.
func TestIntegration_QuitAfterCancel_DoesNotStrand(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "dumb")
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.turnStart = time.Now()
	m.bridge = cli.NewStreamBridge()
	m.workerWG.Add(1)

	m.appendBlock(cli.ChatBlock{Kind: cli.ChatBlockUser, Text: "run tools"})
	m.updateFromDrain(cli.BridgeDrain{
		Tools: []cli.BridgeToolEvt{
			{Start: true, ToolCallID: "c1", Name: "list_dir", Detail: `{"path":"."}`, At: time.Now()},
			{Start: true, ToolCallID: "c2", Name: "grep", Detail: `{"pattern":"x"}`, At: time.Now()},
		},
	})

	// Stage 1: cancel - Finish+Drain consumes synthetic Done once.
	skip, _, _ := m.handleChatCancel()
	if !skip {
		t.Fatal("stage1 must consume key")
	}
	if m.waiting {
		t.Fatal("stage1 must clear waiting")
	}
	if !m.cancelling {
		t.Fatal("stage1 must set cancelling")
	}
	// Worker finishes after stage1; Done drained before stage2.
	m.bridge.Finish(context.Canceled)
	m.workerWG.Done()
	_ = m.drainBridgeAndMaybeFinish()
	if !m.agentDone {
		t.Fatal("drain of worker Finish must set agentDone")
	}

	// Stage 2 / idle: the exit must be reachable immediately - never a wait
	// for a Done that already happened. The cancel unwind is over, so this
	// press lands on the idle path: it arms, and the next press quits. That
	// is one confirmed keystroke, not a strand.
	_, _, cmds := m.handleChatCancel()
	if cmdsContainQuit(cmds) {
		t.Fatal("the press right after a completed cancel must arm, not quit unguarded")
	}
	if !m.quitArmed() {
		t.Fatalf("must arm the quit when the agent is already done, cmds=%v agentDone=%v cancelling=%v quitRequested=%v",
			cmds, m.agentDone, m.cancelling, m.quitRequested)
	}
	_, _, cmds2 := m.handleChatCancel()
	if !cmdsContainQuit(cmds2) {
		t.Fatalf("the confirming ctrl+c must quit, cmds=%v", cmds2)
	}
}

// TDD: when quitRequested is set before worker Done, drain must Quit.
func TestIntegration_QuitAfterCancel_DrainSendsQuit(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.bridge = cli.NewStreamBridge()
	m.workerWG.Add(1)

	skip, _, _ := m.handleChatCancel()
	if !skip || !m.cancelling {
		t.Fatal("stage1 setup")
	}

	// Stage 2 before worker done.
	skip, _, cmds := m.handleChatCancel()
	if !skip {
		t.Fatal("stage2 consume")
	}
	if !m.quitRequested {
		// agentDone may already be true → immediate Quit without quitRequested.
		if cmdsContainQuit(cmds) {
			m.workerWG.Done()
			return
		}
		t.Fatal("stage2 sets quitRequested when worker not done")
	}
	if cmdsContainQuit(cmds) {
		// Immediate quit is fine; still release worker.
		m.workerWG.Done()
		return
	}

	// Worker finishes → drain must Quit.
	m.bridge.Finish(context.Canceled)
	m.workerWG.Done()
	cmds = m.drainBridgeAndMaybeFinish()
	if !cmdsContainQuit(cmds) {
		t.Fatalf("drain with quitRequested must tea.Quit, got %v", cmds)
	}
}

// TDD: force third Ctrl+C always quits (never strand on hung tools).
func TestIntegration_QuitAfterCancel_ForceThirdCtrlC(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newSmokeModel(t)
	m.mode = modeChat
	m.waiting = true
	m.bridge = cli.NewStreamBridge()
	// Worker never finishes (hung tool simulation).
	m.workerWG.Add(1)
	defer m.workerWG.Done()

	_, _, _ = m.handleChatCancel()      // stage1
	_, _, cmds2 := m.handleChatCancel() // stage2 quitRequested
	if cmdsContainQuit(cmds2) {
		// If stage2 already quit, force path is N/A but still OK.
		return
	}
	if !m.quitRequested {
		t.Fatal("want quitRequested after stage2 when worker hung")
	}
	// Stage 2b force: third Ctrl+C while quitRequested.
	_, _, cmds := m.handleChatCancel()
	if !cmdsContainQuit(cmds) {
		t.Fatalf("third Ctrl+C must force Quit, cmds=%v", cmds)
	}
}

// TDD: agentQuitReadyMsg forces quit when waiter completes.
func TestIntegration_QuitAfterCancel_WaiterMsg(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newSmokeModel(t)
	m.mode = modeChat
	m.cancelling = true
	m.quitRequested = true
	model, cmd := m.Update(agentQuitReadyMsg{})
	mm := model.(*TUIModel)
	if mm.quitRequested || mm.cancelling {
		t.Fatal("flags must clear")
	}
	if cmd == nil {
		t.Fatal("want Quit cmd")
	}
	if !cmdsContainQuit([]tea.Cmd{cmd}) {
		t.Fatal("agentQuitReadyMsg must Quit")
	}
}

func cmdsContainQuit(cmds []tea.Cmd) bool {
	for _, c := range cmds {
		if c == nil {
			continue
		}
		ch := make(chan tea.Msg, 1)
		go func(cmd tea.Cmd) {
			// Protect against blocking wait cmds in other tests.
			ch <- cmd()
		}(c)
		select {
		case msg := <-ch:
			if _, ok := msg.(tea.QuitMsg); ok {
				return true
			}
		case <-time.After(30 * time.Millisecond):
			// Blocking cmd (worker wait) - not an immediate Quit.
		}
	}
	return false
}

// Ensure WaitGroup zero-value is usable for smoke models that never Add.
var _ = sync.WaitGroup{}

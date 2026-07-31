package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// commitInFlightTurn closes a turn that is being superseded, committing whatever
// the bridge holds to history. It is a no-op when no turn is in flight.
//
// context.Canceled is passed deliberately: finishStream auto-sends the next
// queued message for any other error, which would make startAI -> finishStream
// -> sendNextQueued -> startAI a live cycle. With Canceled the caller keeps
// control of the queue and the cycle cannot form.
//
// The Ctrl+C path in handleChatCancel does not route through here: it must also
// Finish the bridge, drive the staged cancelling/agentDone bookkeeping, and
// return finishStream's commands. The two paths share the invariant that a turn
// never disappears without being committed, not their surrounding steps.
func (m *tuiModel) commitInFlightTurn() {
	if !m.waiting {
		return
	}
	// Capture the bridge under the mutex: startAI swaps it under the same lock,
	// so reading the field and calling Drain must not be separated by a window.
	m.mu.Lock()
	br := m.bridge
	m.mu.Unlock()
	if br != nil {
		m.updateFromDrain(br.Drain())
	}
	_ = m.finishStream(context.Canceled)
}

func (m *tuiModel) startAI(userText string) {
	m.startAIWithDisplay(userText, userText)
}

// startAIWithDisplay sends sent to the model while rendering only display in
// the transcript and event detail. Skill bodies are workspace-controlled and
// must never become a visible user block or telemetry detail.
func (m *tuiModel) startAIWithDisplay(sent, display string) {
	m.startAIWithPrepared(sent, display, nil)
}

// startSkillAI creates its activation only when the worker starts. A queued
// skill therefore holds no resource root while it waits behind another turn.
func (m *tuiModel) startSkillAI(spec skillSlashSpec) {
	if len(spec.definition.Resources) == 0 || !m.toolsOn || !m.session.UseTools || m.session.Tools == nil {
		m.startAIWithDisplay(renderSkillSlashPrompt(spec.definition.Instructions, spec.args), spec.display)
		return
	}
	m.startAIWithPrepared("", spec.display, func() (string, *chat.TurnOptions, error) { return m.prepareSkillTurn(spec) })
}

// prepareSkillTurn builds the exact per-turn capability surface for a direct
// slash invocation. It is intentionally separate from the asynchronous UI
// start path so a queued command can defer activation until it becomes active.
func (m *tuiModel) prepareSkillTurn(spec skillSlashSpec) (string, *chat.TurnOptions, error) {
	activation, err := spec.definition.Activate()
	if err != nil {
		return "", nil, err
	}
	registry := m.session.Tools.Clone()
	if registry == nil {
		activation.Close()
		return "", nil, fmt.Errorf("skill resources require tools")
	}
	if _, exists := registry.Get(tools.SkillResourceToolName); exists {
		activation.Close()
		return "", nil, fmt.Errorf("skill resource capability conflict")
	}
	reader := tools.NewSkillResourceTool(func(ctx context.Context, id string) (string, string, error) {
		content, err := activation.Read(ctx, id)
		if err != nil {
			return "", "", err
		}
		return content.Text, "skill resource loaded: " + content.ID, nil
	}, activation.ToolKey(), activation.ToolResultBudget())
	registry.Register(reader)
	binding := m.session.CurrentBinding()
	policy := runtime.Policy{}
	if binding.Dispatcher != nil {
		policy = binding.Dispatcher.Policy()
	}
	dispatcher, err := runtime.NewToolDispatcher(registry, policy)
	if err != nil {
		activation.Close()
		return "", nil, err
	}
	return renderSkillSlashPrompt(activation.Prompt(true), spec.args), &chat.TurnOptions{
		Tools:      registry,
		Dispatcher: dispatcher,
		Cleanup: func() {
			dispatcher.Close()
			activation.Close()
		},
	}, nil
}

func (m *tuiModel) startAIWithPrepared(sent, display string, prepare func() (string, *chat.TurnOptions, error)) {
	// A turn may still be running: empty-Enter force-send reaches startAI while
	// waiting. Close it first, or the buffer resets below discard an answer the
	// user is already looking at and two user blocks land back to back.
	m.commitInFlightTurn()

	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	oldBridge := m.bridge
	oldBridge.Close()
	m.bridge = newStreamBridge()
	bridge := m.bridge
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.mu.Unlock()
	m.cancelling = false
	m.quitRequested = false
	m.agentDone = false
	m.waiting = true
	m.turnStart = time.Now()
	m.toolRows = nil
	m.toolWaveTotal = 0
	m.toolWaveDone = 0
	m.subagents.Reset()
	m.streamBuf.Reset()
	m.thinkingBuf.Reset()
	m.toolPanel = toolPanelState{Selected: -1}
	m.stepDetail = ""
	m.stepDetailAt = time.Time{}
	m.stalledWarning = false
	m.liveThinkingScroll = 0
	m.awaitingFirstActivity = true
	m.followOutput = true
	m.turnSeq++
	turnID := fmt.Sprintf("%d", m.turnSeq)
	m.activeTurnID = turnID
	if len(m.blocks) > 0 {
		m.appendBlock(ChatBlock{TurnID: uint64(m.session.UserTurns() + 1), Kind: ChatBlockDivider})
	}
	m.appendBlock(ChatBlock{TurnID: uint64(m.session.UserTurns() + 1), Kind: ChatBlockUser, Text: display, SentAt: time.Now()})
	m.layout()
	m.renderVP()
	m.textarea.Reset()
	m.workerWG.Add(1)
	bridgeCB := agentEventBridgeCallback(bridge)
	genToken := SetSubagentProgress(bridgeCB)
	if m.eventBus != nil {
		m.eventBus.Publish(events.Event{Kind: events.KindTurnStart, Timestamp: time.Now(), TurnID: turnID, Detail: display})
	}
	go func() {
		defer m.workerWG.Done()
		defer ClearSubagentProgress(genToken)
		turnSent := sent
		var turn *chat.TurnOptions
		var err error
		if prepare != nil {
			turnSent, turn, err = prepare()
			if err != nil {
				bridge.Finish(err)
				m.publishTurnEnd(turnID, err)
				return
			}
		}
		if turn != nil {
			_, err = m.session.SendUserWithTurnOptions(ctx, turnSent, display, bridge, agentEventBridgeCallback(bridge), turn)
		} else {
			_, err = m.session.SendUserWithEventAndPersistedText(ctx, turnSent, display, bridge, agentEventBridgeCallback(bridge))
		}
		if ctx.Err() != nil {
			err = context.Canceled
		}
		bridge.Finish(err)
		m.publishTurnEnd(turnID, err)
	}()
}

package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
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
	m.appendBlock(ChatBlock{TurnID: uint64(m.session.UserTurns() + 1), Kind: ChatBlockUser, Text: userText, SentAt: time.Now()})
	m.layout()
	m.renderVP()
	m.textarea.Reset()
	m.workerWG.Add(1)
	bridgeCB := agentEventBridgeCallback(bridge)
	genToken := SetSubagentProgress(bridgeCB)
	if m.eventBus != nil {
		m.eventBus.Publish(events.Event{Kind: events.KindTurnStart, Timestamp: time.Now(), TurnID: turnID, Detail: userText})
	}
	go func() {
		defer m.workerWG.Done()
		defer ClearSubagentProgress(genToken)
		_, err := m.session.SendUserWithEvent(ctx, userText, bridge, agentEventBridgeCallback(bridge))
		if ctx.Err() != nil {
			err = context.Canceled
		}
		bridge.Finish(err)
		m.publishTurnEnd(turnID, err)
	}()
}

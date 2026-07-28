package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/events"
)

func (m *tuiModel) startAI(userText string) {
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

package demoharness

import (
	"context"
	"fmt"
	"sync"
	"time"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/intent"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/uievent"
)

// demoTurnCostTokens and demoTurnCostUSD are how much ContextUsage
// grows per Send call, so /context and /cost show numbers that move as
// the demo session proceeds instead of staying frozen.
const (
	demoTurnInputTokens  = 40
	demoTurnOutputTokens = 90
	demoTurnCostUSD      = 0.004
)

// Send starts one scripted turn: the next script in the scenario, in
// order, cycling once the scenario is exhausted.
func (h *Harness) Send(_ context.Context, in intent.Send) (ports.TurnHandle, error) {
	h.mu.Lock()
	h.history = append(h.history, ports.Message{Role: "user", Text: in.Text, At: time.Now()})
	script := h.scenario[h.turnIdx%len(h.scenario)]
	h.turnIdx++
	turnNum := h.turnIdx
	h.usage.InputTokens += demoTurnInputTokens
	h.usage.OutputTokens += demoTurnOutputTokens
	h.usage.CostUSD += demoTurnCostUSD
	h.mu.Unlock()

	id := fmt.Sprintf("demo-%d", turnNum)
	ch := make(chan uievent.Event)
	cancelCh := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(cancelCh) }) }

	go h.play(id, script, ch, cancelCh)

	return &turnHandle{id: id, events: ch, cancel: cancel}, nil
}

// play streams one scripted turn onto ch. It is the ONE playback
// function every turn shape shares: the difference between a small-talk
// turn, a tool call, a diff, a failing tool, a plan, reasoning, a usage
// summary, or a mid-turn approval is entirely in the script data (see
// turns.go), never a branch in this function.
//
// Cancel is honored at every step: a cancelled turn stops streaming
// immediately and ends with one TurnEndBody{Reason:"cancelled"} event,
// so the transcript keeps whatever partial text had already streamed
// (internal/ui/component/transcript's endTurnUnfinished does that on
// any non-"completed" reason).
func (h *Harness) play(id string, script turnScript, ch chan<- uievent.Event, cancelCh <-chan struct{}) {
	defer close(ch)

	send := func(ev uievent.Event) bool {
		ev.TurnID = id
		select {
		case ch <- ev:
			return true
		case <-cancelCh:
			return false
		}
	}
	wait := func() bool {
		if h.pace <= 0 {
			return true
		}
		select {
		case <-time.After(h.pace):
			return true
		case <-cancelCh:
			return false
		}
	}
	cancelled := func() {
		// A caller that keeps reading Events() until it closes (every
		// documented ports.TurnHandle consumer does) receives this well
		// inside DemoCancelFlushTimeout; the timeout only bounds the
		// goroutine's lifetime against a caller that stopped reading.
		ev := uievent.Event{TurnID: id, Kind: uievent.KindTurnEnd, Body: uievent.TurnEndBody{Reason: "cancelled"}}
		select {
		case ch <- ev:
		case <-time.After(uikitconfig.DemoCancelFlushTimeout):
		}
	}

	for _, ev := range script.Before {
		if !send(ev) {
			cancelled()
			return
		}
		if tp, ok := ev.Body.(uievent.ToolPendingBody); ok {
			approved, ok := h.awaitDecision(tp, id, cancelCh)
			if !ok {
				cancelled()
				return
			}
			tail := script.OnDeny
			if approved {
				tail = script.OnApprove
			}
			if !h.playTail(tail, send, wait) {
				cancelled()
				return
			}
			continue
		}
		if !wait() {
			cancelled()
			return
		}
	}
}

// playTail streams a decision's continuation (OnApprove or OnDeny).
func (h *Harness) playTail(tail []uievent.Event, send func(uievent.Event) bool, wait func() bool) bool {
	for _, ev := range tail {
		if !send(ev) {
			return false
		}
		if !wait() {
			return false
		}
	}
	return true
}

// awaitDecision publishes one approval request to Pending() and blocks
// for the matching Resolve call. ok is false when the turn was
// cancelled first, meaning the caller must end the turn unfinished
// instead of choosing a continuation.
func (h *Harness) awaitDecision(tp uievent.ToolPendingBody, turnID string, cancelCh <-chan struct{}) (approved, ok bool) {
	decisionCh := h.registerWait(tp.ToolCallID)
	req := ports.ApprovalRequest{ID: tp.ToolCallID, ToolName: tp.Name, TurnID: turnID, Args: tp.Args}
	select {
	case h.pendingCh <- req:
	case <-cancelCh:
		h.dropWait(tp.ToolCallID)
		return false, false
	}
	select {
	case decision := <-decisionCh:
		approved := decision == ports.DecisionOnce || decision == ports.DecisionAlways
		return approved, true
	case <-cancelCh:
		h.dropWait(tp.ToolCallID)
		return false, false
	}
}

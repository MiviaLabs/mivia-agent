package uiadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

type compactionHandle struct {
	events chan ports.CompactionEvent
	cancel context.CancelFunc
	onDone func()
}

func (h *compactionHandle) Events() <-chan ports.CompactionEvent { return h.events }
func (h *compactionHandle) Cancel()                              { h.cancel() }

var _ ports.AsyncCompactionRunner = (*CommandRunner)(nil)

// StartCompaction starts the expensive summary operation away from the TUI
// update loop and rejects overlapping manual compactions.
func (r *CommandRunner) StartCompaction(parent context.Context, focus string) (ports.CompactionHandle, error) {
	sess := r.activeSession()
	if sess == nil {
		return nil, errors.New("no active session")
	}
	r.compactionMu.Lock()
	if r.compactionActive {
		r.compactionMu.Unlock()
		return nil, errors.New("compaction already in progress")
	}
	r.compactionActive = true
	r.compactionMu.Unlock()
	ctx, cancel := context.WithCancel(parent)
	h := &compactionHandle{events: make(chan ports.CompactionEvent, 4), cancel: cancel, onDone: func() {
		r.compactionMu.Lock()
		r.compactionActive = false
		r.compactionMu.Unlock()
	}}
	go func() {
		defer h.onDone()
		defer close(h.events)
		sessionID := sess.SessionID
		h.events <- ports.CompactionEvent{SessionID: sessionID, Phase: "compact", Detail: "preparing"}
		h.events <- ports.CompactionEvent{SessionID: sessionID, Phase: "compact", Detail: "summarizing context"}
		err := sess.Compact(ctx, focus)
		if err != nil {
			h.events <- ports.CompactionEvent{SessionID: sessionID, Done: true, Err: err}
			return
		}
		u := sess.ContextUsage()
		h.events <- ports.CompactionEvent{SessionID: sessionID, Done: true, Notice: fmt.Sprintf("Context compacted (%d%% used, %s/%s prompt).", u.Percent, chat.FormatTokenK(u.UsedTokens), chat.FormatTokenK(u.BudgetTokens))}
	}()
	return h, nil
}

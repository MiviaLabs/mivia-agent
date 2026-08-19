// Package demoharness is the demo binary's driver: one STATEFUL fake
// implementing the whole ports surface - ports.Conversation,
// ports.Approver, and ports.CommandRunner - over shared state. A
// /model pick or a tool-approval decision changes what later turns and
// later commands see, unlike a fixture replayed unchanged every time.
//
// The scenario itself is DATA, not code: internal/uikit/demoharness/testdata
// holds one JSON file per scripted turn, in the wire shape
// uievent.LoadFixture already reads. New picks a named scenario - an
// ordered list of those files - and Send plays them in order, cycling
// once the list is exhausted. The one difference in turn shape a script
// can carry - pausing mid-turn for a tool-approval decision - is also
// data (turnScript.OnApprove / OnDeny), not a per-turn code branch.
//
// internal/uikit/** must not import bubbletea or lipgloss; this package
// does not.
package demoharness

import (
	"sync"
	"time"

	uikitconfig "github.com/MiviaLabs/mivia-agent/internal/uikit/config"
	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

var (
	_ ports.Conversation  = (*Harness)(nil)
	_ ports.Approver      = (*Harness)(nil)
	_ ports.CommandRunner = (*Harness)(nil)
)

// Harness is the shared fake state behind every ports interface this
// package implements. mu guards every field below it.
type Harness struct {
	pace time.Duration

	mu        sync.Mutex
	title     string
	scenario  []turnScript
	subagents map[string]subagentFixture
	turnIdx   int
	model     ports.ModelInfo
	agent     string
	usage     ports.Usage
	history   []ports.Message

	pendingCh chan ports.ApprovalRequest
	waiting   map[string]chan ports.Decision
}

// New returns a Harness that replays the named scenario, pacing
// streamed events pace apart (0 replays as fast as possible - the
// common case in tests). An unknown scenario name is an error naming
// the known set (see Scenarios).
func New(scenarioName string, pace time.Duration) (*Harness, error) {
	loaded, err := loadScenario(scenarioName)
	if err != nil {
		return nil, err
	}
	return &Harness{
		pace:      pace,
		title:     loaded.Title,
		scenario:  loaded.Scripts,
		subagents: loaded.Subagents,
		model:     ports.ModelInfo{Name: demoModels[0], Provider: "demo", ContextWindow: demoContextWindow},
		agent:     ports.DefaultAgentName,
		usage: ports.Usage{
			InputTokens: 1200, OutputTokens: 400, CachedTokens: 300, CostUSD: 0.03,
		},
		// Buffered, like internal/uikit/replay's Approver: the shipped
		// conversation.Screen wiring reads approval state straight off
		// the tool.pending event on Events() (handleTurnEvent), not off
		// Pending(), so nothing drains Pending() in production today. An
		// unbuffered channel would deadlock every approval turn against
		// that wiring; the buffer is what lets Run/awaitDecision publish
		// without a reader ever needing to be present.
		pendingCh: make(chan ports.ApprovalRequest, uikitconfig.DemoPendingApprovalBuffer),
		waiting:   make(map[string]chan ports.Decision),
	}, nil
}

// Title returns the current session/scenario title.
func (h *Harness) Title() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.title
}

// History returns every user message sent so far, oldest first.
func (h *Harness) History() []ports.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ports.Message, len(h.history))
	copy(out, h.history)
	return out
}

// Model returns the model currently selected. /model changes it.
func (h *Harness) Model() ports.ModelInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.model
}

// ContextUsage returns the session's token and cost accounting so far.
// Every Send call and /compact change it.
func (h *Harness) ContextUsage() ports.Usage {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.usage
}

package replay

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

var _ ports.Approver = (*Approver)(nil)

// Approver is a minimal in-memory ports.Approver: Resolve is recorded and
// exposed via Resolutions for assertions. Nothing in the replay fake
// produces real approval requests on its own (the canonical fixture has
// none - every tool call in it is auto-approved); Push lets a test or the
// demo binary inject a synthetic one to drive the approval component.
type Approver struct {
	pending chan ports.ApprovalRequest

	mu       sync.Mutex
	resolved []Resolution
}

// Resolution records one Resolve call.
type Resolution struct {
	ID       string
	Decision ports.Decision
}

// NewApprover returns an empty Approver.
func NewApprover() *Approver {
	return &Approver{pending: make(chan ports.ApprovalRequest, 16)}
}

// Push enqueues a synthetic approval request onto Pending().
func (a *Approver) Push(req ports.ApprovalRequest) { a.pending <- req }

func (a *Approver) Pending() <-chan ports.ApprovalRequest { return a.pending }

func (a *Approver) Resolve(id string, decision ports.Decision) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resolved = append(a.resolved, Resolution{ID: id, Decision: decision})
}

// Resolutions returns a copy of every decision recorded so far.
func (a *Approver) Resolutions() []Resolution {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Resolution, len(a.resolved))
	copy(out, a.resolved)
	return out
}

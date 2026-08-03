package coordinator

import (
	"fmt"
	"sync"
)

// askRegistry tracks open asks and one-answer enforcement (plan 53.04).
type askRegistry struct {
	mu sync.Mutex
	// open maps ask message ID → asker task ID (for routing answers back).
	open map[string]string
	// fromRole maps ask ID → asker role (for chain/cycle bookkeeping).
	fromRole map[string]string
	// ancestors maps ask ID → prior ask IDs in the chain (for depth/cycle).
	ancestors map[string][]string
	// answered marks asks that already received one answer (or expired).
	answered map[string]bool
	// asksByTask counts asks posted per run\0task.
	asksByTask map[string]int
	// referralSpawns counts referral-as-spawn per run.
	referralSpawns map[string]int
}

func newAskRegistry() *askRegistry {
	return &askRegistry{
		open:           map[string]string{},
		fromRole:       map[string]string{},
		ancestors:      map[string][]string{},
		answered:       map[string]bool{},
		asksByTask:     map[string]int{},
		referralSpawns: map[string]int{},
	}
}

func (c *coordinator) ensureAsks() *askRegistry {
	if c.asks == nil {
		c.asks = newAskRegistry()
	}
	return c.asks
}

// TryRegisterAsk records an open ask if under maxAsks (maxAsks<=0 → 4).
// Returns false when the per-task ask quota is already exhausted.
func (c *coordinator) TryRegisterAsk(runID, askerTaskID, askerRole, askID string, ancestors []string, maxAsks int) bool {
	if maxAsks <= 0 {
		maxAsks = 4
	}
	reg := c.ensureAsks()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	key := questionKey(runID, askerTaskID)
	if reg.asksByTask[key] >= maxAsks {
		return false
	}
	reg.open[askID] = askerTaskID
	reg.fromRole[askID] = askerRole
	if len(ancestors) > 0 {
		cp := make([]string, len(ancestors))
		copy(cp, ancestors)
		reg.ancestors[askID] = cp
	}
	reg.asksByTask[key]++
	return true
}

// RegisterAsk records an open ask without a quota check (tests / internal).
func (c *coordinator) RegisterAsk(runID, askerTaskID, askerRole, askID string, ancestors []string) {
	_ = c.TryRegisterAsk(runID, askerTaskID, askerRole, askID, ancestors, 1<<30)
}

// AsksUsedByTask returns how many asks this task has registered.
func (c *coordinator) AsksUsedByTask(runID, taskID string) int {
	if c.asks == nil {
		return 0
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	return c.asks.asksByTask[questionKey(runID, taskID)]
}

// ReferralSpawnsUsed returns referral-as-spawn count for the run.
func (c *coordinator) ReferralSpawnsUsed(runID string) int {
	if c.asks == nil {
		return 0
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	return c.asks.referralSpawns[runID]
}

// TryIncReferralSpawn increments the run's referral-as-spawn counter if under
// max (max<=0 → 4). Returns false when the cap is already reached.
func (c *coordinator) TryIncReferralSpawn(runID string, max int) bool {
	if max <= 0 {
		max = 4
	}
	reg := c.ensureAsks()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.referralSpawns[runID] >= max {
		return false
	}
	reg.referralSpawns[runID]++
	return true
}

// IncReferralSpawn increments the run's referral-as-spawn counter (unbounded).
func (c *coordinator) IncReferralSpawn(runID string) {
	_ = c.TryIncReferralSpawn(runID, 1<<30)
}

// DecReferralSpawn rolls back a TryIncReferralSpawn when spawn fails.
func (c *coordinator) DecReferralSpawn(runID string) {
	if c.asks == nil || runID == "" {
		return
	}
	c.asks.mu.Lock()
	if c.asks.referralSpawns[runID] > 0 {
		c.asks.referralSpawns[runID]--
	}
	c.asks.mu.Unlock()
}

// AskLookup returns the asker task for an open ask, if any.
func (c *coordinator) AskLookup(askID string) (askerTaskID string, ok bool) {
	if c.asks == nil {
		return "", false
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.answered[askID] {
		return "", false
	}
	id, ok := c.asks.open[askID]
	return id, ok
}

// AskChainInfo returns depth and whether adding toRole would cycle.
func (c *coordinator) AskChainInfo(parentAskID, toRole string) (depth int, cycle bool, ancestors []string) {
	if c.asks == nil || parentAskID == "" {
		return 0, false, nil
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	anc := append([]string{}, c.asks.ancestors[parentAskID]...)
	anc = append(anc, parentAskID)
	roles := map[string]bool{}
	for _, id := range anc {
		if r := c.asks.fromRole[id]; r != "" {
			roles[r] = true
		}
	}
	if roles[toRole] {
		return len(anc), true, anc
	}
	return len(anc), false, anc
}

// ClaimAskAnswer atomically claims an open ask for answering. Returns the
// asker task id. Fails if unknown or already answered/claimed.
func (c *coordinator) ClaimAskAnswer(askID string) (askerTaskID string, err error) {
	if c.asks == nil {
		return "", fmt.Errorf("unknown ask")
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.answered[askID] {
		return "", fmt.Errorf("ask already answered")
	}
	id, ok := c.asks.open[askID]
	if !ok {
		return "", fmt.Errorf("unknown ask")
	}
	// Claim: mark answered and remove from open before side effects.
	c.asks.answered[askID] = true
	delete(c.asks.open, askID)
	return id, nil
}

// CompleteAskAnswer marks the ask answered. Returns error if already answered
// or unknown (not an open ask).
func (c *coordinator) CompleteAskAnswer(askID string) error {
	_, err := c.ClaimAskAnswer(askID)
	return err
}

// IsAskAnswered reports whether askID already has an answer or was closed.
func (c *coordinator) IsAskAnswered(askID string) bool {
	if c.asks == nil {
		return false
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	return c.asks.answered[askID]
}

// CloseAsk marks an open ask closed without an answer (timeout/cancel/undelivered).
func (c *coordinator) CloseAsk(askID string) {
	if c.asks == nil || askID == "" {
		return
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.answered[askID] {
		return
	}
	if _, ok := c.asks.open[askID]; ok {
		c.asks.answered[askID] = true
		delete(c.asks.open, askID)
	}
}

// UnclaimAskAnswer reopens an ask after a claimed answer failed before durable
// delivery (validation/quota/persist). No-op if the ask was never claimed.
func (c *coordinator) UnclaimAskAnswer(askID, askerTaskID string) {
	if c.asks == nil || askID == "" || askerTaskID == "" {
		return
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if !c.asks.answered[askID] {
		return
	}
	// Only reopen if no durable answer succeeded (still marked answered, not open).
	if _, open := c.asks.open[askID]; open {
		return
	}
	delete(c.asks.answered, askID)
	c.asks.open[askID] = askerTaskID
}

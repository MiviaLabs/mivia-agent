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
	// answered marks asks that already received one answer.
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

// RegisterAsk records an open ask after successful persist. ancestors is the
// chain of prior ask IDs (may be empty).
func (c *coordinator) RegisterAsk(runID, askerTaskID, askerRole, askID string, ancestors []string) {
	if c.asks == nil {
		c.asks = newAskRegistry()
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	c.asks.open[askID] = askerTaskID
	c.asks.fromRole[askID] = askerRole
	if len(ancestors) > 0 {
		cp := make([]string, len(ancestors))
		copy(cp, ancestors)
		c.asks.ancestors[askID] = cp
	}
	key := questionKey(runID, askerTaskID)
	c.asks.asksByTask[key]++
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

// IncReferralSpawn increments the run's referral-as-spawn counter.
func (c *coordinator) IncReferralSpawn(runID string) {
	if c.asks == nil {
		c.asks = newAskRegistry()
	}
	c.asks.mu.Lock()
	c.asks.referralSpawns[runID]++
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

// AskChainInfo returns depth and whether adding toRole would cycle, based on
// ancestor ask IDs and their from roles. parentAskID may be empty.
func (c *coordinator) AskChainInfo(parentAskID, toRole string) (depth int, cycle bool, ancestors []string) {
	if c.asks == nil || parentAskID == "" {
		return 0, false, nil
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	anc := append([]string{}, c.asks.ancestors[parentAskID]...)
	anc = append(anc, parentAskID)
	// Cycle if toRole matches any ancestor's from role or is the immediate from.
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

// CompleteAskAnswer marks the ask answered. Returns false if already answered
// or unknown (not an open ask).
func (c *coordinator) CompleteAskAnswer(askID string) error {
	if c.asks == nil {
		return fmt.Errorf("unknown ask")
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.answered[askID] {
		return fmt.Errorf("ask already answered")
	}
	if _, ok := c.asks.open[askID]; !ok {
		return fmt.Errorf("unknown ask")
	}
	c.asks.answered[askID] = true
	delete(c.asks.open, askID)
	return nil
}

// IsAskAnswered reports whether askID already has an answer.
func (c *coordinator) IsAskAnswered(askID string) bool {
	if c.asks == nil {
		return false
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	return c.asks.answered[askID]
}

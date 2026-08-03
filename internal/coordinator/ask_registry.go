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
	// claimed marks an in-flight answer claim (not yet durable / not terminal close).
	claimed map[string]bool
	// closed marks permanent retirement (timeout, cancel, undelivered, failed referral,
	// or successful one-shot completion). Never reopened by UnclaimAskAnswer.
	closed map[string]bool
	// asksByTask counts asks posted per run\0task.
	asksByTask map[string]int
	// referralSpawns counts referral-as-spawn per run.
	referralSpawns map[string]int
	// referralTaskAsk maps referral task ID → open ask ID (close on fail).
	referralTaskAsk map[string]string
	// byTarget maps run\0task → ask IDs delivered to that task's mailbox
	// (plan 53.04). Recorded in MailboxSend so finalize can decline asks to a
	// task that reaches terminal status without answering. Deduped; pruned when
	// the ask is sealed or completed.
	byTarget map[string][]string
}

func newAskRegistry() *askRegistry {
	return &askRegistry{
		open:            map[string]string{},
		fromRole:        map[string]string{},
		ancestors:       map[string][]string{},
		claimed:         map[string]bool{},
		closed:          map[string]bool{},
		asksByTask:      map[string]int{},
		referralSpawns:  map[string]int{},
		referralTaskAsk: map[string]string{},
		byTarget:        map[string][]string{},
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
	if c.asks.closed[askID] || c.asks.claimed[askID] {
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
// asker task id. Fails if unknown, closed, or already claimed.
func (c *coordinator) ClaimAskAnswer(askID string) (askerTaskID string, err error) {
	if c.asks == nil {
		return "", fmt.Errorf("unknown ask")
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.closed[askID] {
		return "", fmt.Errorf("ask already answered")
	}
	if c.asks.claimed[askID] {
		return "", fmt.Errorf("ask already answered")
	}
	id, ok := c.asks.open[askID]
	if !ok {
		return "", fmt.Errorf("unknown ask")
	}
	// Claim: remove from open; not closed until durable success or CloseAsk.
	c.asks.claimed[askID] = true
	delete(c.asks.open, askID)
	return id, nil
}

// BeginAskAnswer claims an open registry ask for a one-shot answer.
// claimed=true means the caller holds the claim (must CloseAsk or Unclaim).
// err is set when the id is a sealed/claimed registry ask (refuse further answers).
// claimed=false and err=nil means the id is not a registry ask (phase-03 question).
func (c *coordinator) BeginAskAnswer(askID string) (askerTaskID string, claimed bool, err error) {
	if c.asks == nil || askID == "" {
		return "", false, nil
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.closed[askID] || c.asks.claimed[askID] {
		return "", false, fmt.Errorf("ask already answered")
	}
	id, ok := c.asks.open[askID]
	if !ok {
		return "", false, nil
	}
	c.asks.claimed[askID] = true
	delete(c.asks.open, askID)
	return id, true, nil
}

// CompleteAskAnswer permanently closes an open or claimed ask.
func (c *coordinator) CompleteAskAnswer(askID string) error {
	if c.asks == nil {
		return fmt.Errorf("unknown ask")
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.closed[askID] {
		return fmt.Errorf("ask already answered")
	}
	_, open := c.asks.open[askID]
	if !open && !c.asks.claimed[askID] {
		return fmt.Errorf("unknown ask")
	}
	delete(c.asks.open, askID)
	delete(c.asks.claimed, askID)
	c.asks.closed[askID] = true
	c.asks.pruneAskTargetLocked(askID)
	return nil
}

// IsAskAnswered reports whether askID is permanently closed (answered or abandoned).
func (c *coordinator) IsAskAnswered(askID string) bool {
	if c.asks == nil {
		return false
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	return c.asks.closed[askID]
}

// CloseAsk permanently retires an ask without a peer answer (timeout/cancel/
// undelivered/failed referral). No-op if already closed. Does not reopen via Unclaim.
func (c *coordinator) CloseAsk(askID string) {
	_ = c.SealAskAnswer(askID)
}

// SealAskAnswer permanently closes an open or claimed ask. Returns true only
// when this call performed the seal (caller may live-inject). Returns false if
// already sealed or askID is not a registry ask — skip DeliverAnswer/mailbox.
func (c *coordinator) SealAskAnswer(askID string) bool {
	if c.asks == nil || askID == "" {
		return false
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.closed[askID] {
		return false
	}
	_, open := c.asks.open[askID]
	if !open && !c.asks.claimed[askID] {
		return false
	}
	c.asks.closed[askID] = true
	delete(c.asks.open, askID)
	delete(c.asks.claimed, askID)
	c.asks.pruneAskTargetLocked(askID)
	return true
}

// SealOpenAskAnswer permanently closes an ask that is still OPEN and NOT
// claimed. Unlike SealAskAnswer it refuses to seal a claimed ask: a claimed ask
// has a real answer mid-persist, and a decline must let it win (sealing it
// would make the responder's later SealAskAnswer return false and the durable
// real answer would never be delivered). Returns true only when this call
// performed the seal.
func (c *coordinator) SealOpenAskAnswer(askID string) bool {
	if c.asks == nil || askID == "" {
		return false
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.closed[askID] || c.asks.claimed[askID] {
		return false
	}
	if _, ok := c.asks.open[askID]; !ok {
		return false
	}
	c.asks.closed[askID] = true
	delete(c.asks.open, askID)
	c.asks.pruneAskTargetLocked(askID)
	return true
}

// UnclaimAskAnswer reopens an ask after a claimed answer failed before durable
// delivery. No-op if the ask was permanently closed (timeout/cancel/etc.).
func (c *coordinator) UnclaimAskAnswer(askID, askerTaskID string) {
	if c.asks == nil || askID == "" || askerTaskID == "" {
		return
	}
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	if c.asks.closed[askID] {
		return // wait ended or terminal close won
	}
	if !c.asks.claimed[askID] {
		return
	}
	delete(c.asks.claimed, askID)
	c.asks.open[askID] = askerTaskID
}

// recordAskTarget records a successfully mailbox-delivered ask against its
// target task so finalize can decline it if the target completes without
// answering. Dedupes: an ask is recorded at most once per target.
func (c *coordinator) recordAskTarget(runID, taskID, askID string) {
	if c.asks == nil || runID == "" || taskID == "" || askID == "" {
		return
	}
	key := questionKey(runID, taskID)
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	// Skip already-retired asks (re-checked under asks.mu). If the responder
	// sealed/claimed the ask before this record landed, the byTarget prune
	// already ran (or a real answer is in flight) — re-adding a sealed ask here
	// would leak it in byTarget forever (the registry is coordinator-global with
	// no run-end cleanup).
	if c.asks.closed[askID] || c.asks.claimed[askID] {
		return
	}
	for _, id := range c.asks.byTarget[key] {
		if id == askID {
			return
		}
	}
	c.asks.byTarget[key] = append(c.asks.byTarget[key], askID)
}

// pruneAskTargetLocked removes askID from every byTarget list and deletes
// now-empty keys. Caller must hold reg.mu.
func (reg *askRegistry) pruneAskTargetLocked(askID string) {
	if askID == "" {
		return
	}
	for key, ids := range reg.byTarget {
		kept := ids[:0]
		for _, id := range ids {
			if id != askID {
				kept = append(kept, id)
			}
		}
		if len(kept) == 0 {
			delete(reg.byTarget, key)
		} else {
			reg.byTarget[key] = kept
		}
	}
}

// asksTargeting returns the still-open ask IDs that were delivered to
// runID/taskID. Closed and claimed asks are excluded: they are no longer open
// for a terminal decline (a claimed ask may still deliver a real answer).
func (c *coordinator) asksTargeting(runID, taskID string) []string {
	if c.asks == nil || runID == "" || taskID == "" {
		return nil
	}
	key := questionKey(runID, taskID)
	c.asks.mu.Lock()
	defer c.asks.mu.Unlock()
	var out []string
	for _, id := range c.asks.byTarget[key] {
		if c.asks.closed[id] || c.asks.claimed[id] {
			continue
		}
		if _, ok := c.asks.open[id]; ok {
			out = append(out, id)
		}
	}
	return out
}

// resetTaskAsks clears the per-attempt ask counter for a task (FIX R6). Only
// asksByTask is reset — the open/closed/claimed maps are untouched so a
// retried task's in-flight ask bookkeeping is preserved. Called at the retry
// attempt boundary (mintRetryAttempt).
func (c *coordinator) resetTaskAsks(runID, taskID string) {
	if c.asks == nil || runID == "" || taskID == "" {
		return
	}
	key := questionKey(runID, taskID)
	c.asks.mu.Lock()
	delete(c.asks.asksByTask, key)
	c.asks.mu.Unlock()
}

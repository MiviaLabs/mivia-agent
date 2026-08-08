package controller

import (
	"context"
	"fmt"
	"sync"
)

// PanelActorLimiter bounds local panel actors in one process.
type PanelActorLimiter struct {
	slots chan struct{}
	mu    sync.Mutex
	byRun map[string]*panelActorEntry
}

// panelActorEntry owns one keyed slot. Each caller receives a distinct lease
// token so a losing caller cannot free another caller's pending admission.
type panelActorEntry struct {
	runID   string
	ready   chan struct{}
	failed  chan struct{}
	pending int
	locals  int
}

type panelActorLease struct {
	limiter *PanelActorLimiter
	entry   *panelActorEntry
	mu      sync.Mutex
	state   panelLeaseState
}

type panelLeaseState uint8

const (
	panelLeasePending panelLeaseState = iota
	panelLeaseLocal
	panelLeaseReleased
)

// NewPanelActorLimiter creates the fixed process-wide four-slot limiter.
func NewPanelActorLimiter() *PanelActorLimiter {
	return &PanelActorLimiter{slots: make(chan struct{}, 4), byRun: make(map[string]*panelActorEntry)}
}

// SetPanelLimiter installs the process-owned panel actor limiter before Start.
func (c *LinearController) SetPanelLimiter(limiter *PanelActorLimiter) error {
	if limiter == nil {
		return fmt.Errorf("panel limiter is nil")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return fmt.Errorf("workflow run already started")
	}
	c.PanelLimiter = limiter
	return nil
}

// Acquire reserves one slot for a deterministic child run ID.
func (l *PanelActorLimiter) Acquire(ctx context.Context, runID string) (*panelActorLease, error) {
	if l == nil || runID == "" {
		return nil, fmt.Errorf("panel limiter needs a child run ID")
	}
	l.mu.Lock()
	if entry := l.byRun[runID]; entry != nil {
		ready := entry.ready
		failed := entry.failed
		l.mu.Unlock()
		select {
		case <-ready:
			l.mu.Lock()
			if l.byRun[runID] != entry {
				l.mu.Unlock()
				return nil, context.Canceled
			}
			entry.pending++
			l.mu.Unlock()
			return &panelActorLease{limiter: l, entry: entry}, nil
		case <-failed:
			return nil, context.Canceled
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	entry := &panelActorEntry{runID: runID, ready: make(chan struct{}), failed: make(chan struct{})}
	l.byRun[runID] = entry
	l.mu.Unlock()

	select {
	case l.slots <- struct{}{}:
		l.mu.Lock()
		if l.byRun[runID] != entry {
			l.mu.Unlock()
			<-l.slots
			return nil, context.Canceled
		}
		entry.pending = 1
		close(entry.ready)
		l.mu.Unlock()
		return &panelActorLease{limiter: l, entry: entry}, nil
	case <-ctx.Done():
		l.mu.Lock()
		if l.byRun[runID] == entry {
			delete(l.byRun, runID)
			close(entry.failed)
		}
		l.mu.Unlock()
		return nil, ctx.Err()
	}
}

// AttachLocal records that this process owns an actor for the lease.
func (l *panelActorLease) AttachLocal() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.state != panelLeasePending {
		l.mu.Unlock()
		return
	}
	l.state = panelLeaseLocal
	l.mu.Unlock()
	l.limiter.mu.Lock()
	l.entry.pending--
	l.entry.locals++
	l.limiter.mu.Unlock()
}

// ReleaseBeforeActor frees the reservation when admission finds no local actor.
// A concurrent local admission keeps the slot until its actor is terminal.
func (l *panelActorLease) ReleaseBeforeActor() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.state != panelLeasePending {
		l.mu.Unlock()
		return
	}
	l.state = panelLeaseReleased
	l.mu.Unlock()
	l.limiter.releasePending(l.entry)
}

// Release frees a local actor slot after the actor reaches a terminal state.
func (l *panelActorLease) Release() {
	if l == nil || l.limiter == nil {
		return
	}
	l.mu.Lock()
	state := l.state
	if state == panelLeaseReleased {
		l.mu.Unlock()
		return
	}
	l.state = panelLeaseReleased
	l.mu.Unlock()
	if state == panelLeaseLocal {
		l.limiter.releaseLocal(l.entry)
		return
	}
	l.limiter.releasePending(l.entry)
}

func (l *PanelActorLimiter) releasePending(entry *panelActorEntry) {
	l.mu.Lock()
	entry.pending--
	l.releaseEntryLocked(entry)
	l.mu.Unlock()
}

func (l *PanelActorLimiter) releaseLocal(entry *panelActorEntry) {
	l.mu.Lock()
	entry.locals--
	l.releaseEntryLocked(entry)
	l.mu.Unlock()
}

func (l *PanelActorLimiter) releaseEntryLocked(entry *panelActorEntry) {
	if entry.pending != 0 || entry.locals != 0 || l.byRun[entry.runID] != entry {
		return
	}
	delete(l.byRun, entry.runID)
	<-l.slots
}

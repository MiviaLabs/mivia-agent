// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (claim.go) is the fenced single-fire admission and the
// interrupted-run sweep ("Fenced Single-Fire Claims" and "Interrupted
// Run Sweep" in docs/design/automations.md). Execution itself (worktree
// creation, session spawning, per-step dispatch) is executor.go; this file only decides
// "does this fire get to proceed" and "which running rows lost their
// owner".
package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newHolderToken generates a fresh, unpredictable per-claim identifier.
//
// Correction from the plan's suggested convention: internal/ledgercore's
// NewHolderID (crypto/rand + base32, "h-" prefix) is the closest existing
// precedent for this shape, but internal/automation's import-policy allow
// list (.mivia/policy/import-layers.json) does not grant it an edge to
// internal/ledgercore. Rather
// than widen the edge for a four-line helper, this reimplements the same
// crypto/rand-backed approach locally (hex instead of base32 - no
// external behavioral difference, both are opaque unique tokens).
func newHolderToken(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// claimKey is the fenced-claim key for one automation:
// "automation:"+automationID. One key per automation enforces the "one
// in-flight run per automation" concurrency cap - two fires for the same
// automation contend for the same row regardless of run ID.
func claimKey(automationID string) string {
	return "automation:" + automationID
}

// admitFire is the single-fire dedup gate: every fire - scheduled,
// manual RunOnce, or a user-initiated ResumeRun - must win this claim
// before any side effect. ok=false, err=nil is the documented "lost
// claim is a no-op, not an error" outcome for a claim that is genuinely
// live; any other error is real and reported. On success, holder is the
// fresh token that now owns the claim - the caller (startRun in
// executor_run.go, admitResume in resume.go) records it as the run's
// claim_token.
//
// A claim already held is not automatically a loss: if it has gone
// unrenewed past defaultSweepMaxAge, admitFire takes it over atomically
// via storage.TakeoverExpiredClaimFenced instead of refusing - the same
// primitive and threshold sweepInterrupted uses, so a crashed holder's
// claim cannot wedge every future fire indefinitely. A claim that is
// still fresh keeps the original, unchanged refusal (ok=false, err=nil).
// This applies uniformly to RunOnce's and ResumeRun's admission (both
// call this one function); ResumeRun's distinct ErrRunAlreadyActive
// refusal on a genuinely active claim lives in its own caller
// (admitResume), unaffected here. See "Stale-Claim Takeover" in
// docs/design/automations.md for the full rationale.
func (s *Service) admitFire(ctx context.Context, automationID string) (holder string, ok bool, err error) {
	if s.db == nil {
		return "", false, errNoRunStore
	}
	if err := ValidateID(automationID); err != nil {
		return "", false, err
	}
	key := claimKey(automationID)
	token := newHolderToken("automation-run-")
	claim, err := s.db.ClaimRunFenced(ctx, key, token)
	if err == nil {
		return claim.Holder, true, nil
	}
	if !errors.Is(err, storage.ErrClaimHeld) {
		return "", false, fmt.Errorf("automation: admit fire %q: %w", automationID, err)
	}
	// Capture the about-to-be-fenced-out holder BEFORE the takeover, so a
	// successful takeover below can find and close out that exact
	// holder's orphaned run row (see interruptOrphanedRun). This is a
	// best-effort read: a brief race against the takeover itself (the
	// holder changing between this read and the takeover call) means
	// prevHolder can occasionally be stale or empty, in which case
	// interruptOrphanedRun's exact claim_token match simply finds
	// nothing to close out rather than guessing - it never interrupts
	// the wrong row.
	var prevHolder string
	if prior, gerr := s.db.GetClaim(ctx, key); gerr == nil {
		prevHolder = prior.Holder
	}
	claim, err = s.db.TakeoverExpiredClaimFenced(ctx, key, token, defaultSweepMaxAge)
	switch {
	case err == nil:
		// The held claim was past defaultSweepMaxAge: presumed
		// abandoned, taken over atomically. The crashed holder's own
		// run row (if any) never got a terminal write - nothing else
		// closes it out on this lazy path (no sweep necessarily ever
		// ran) - so close it out now, before this fire is admitted,
		// matching the confirmed orphan-row finding: a stale-claim
		// takeover must not leave the prior holder's run permanently
		// stuck in RunRunning.
		s.interruptOrphanedRun(ctx, automationID, prevHolder)
		return claim.Holder, true, nil
	case errors.Is(err, storage.ErrClaimHeld):
		// Held and still fresh: a genuinely in-flight run. Unchanged
		// documented no-op.
		return "", false, nil
	case errors.Is(err, storage.ErrClaimNotHeld):
		// The claim row vanished between the two reads above (e.g. its
		// original holder released it normally in the interim).
		// Nothing holds it now - retry the plain claim once rather than
		// reporting a spurious loss.
		claim, err = s.db.ClaimRunFenced(ctx, key, token)
		if err == nil {
			return claim.Holder, true, nil
		}
		if errors.Is(err, storage.ErrClaimHeld) {
			// A third party claimed it in that same window: a genuine
			// fresh contender, not a stale one.
			return "", false, nil
		}
		return "", false, fmt.Errorf("automation: admit fire %q: %w", automationID, err)
	default:
		return "", false, fmt.Errorf("automation: admit fire %q: %w", automationID, err)
	}
}

// interruptOrphanedRun closes out the run row (if any) that the just
// fenced-out prevHolder token owned, so a lazy stale-claim takeover in
// admitFire does not leave that row wedged in RunRunning forever (the
// confirmed orphan-row finding). Unlike sweepInterrupted - which reads
// every running row across every automation and independently takes
// over and checks each one's OWN claim - this path already knows
// exactly which claim row was just fenced out and by which token; it
// looks up the one automation_runs row carrying that EXACT claim_token,
// under this SAME automationID, so a concurrent, genuinely fresh run for
// this automation (a different, live claim_token) or any run belonging
// to a different automation is never touched. prevHolder=="" (the
// best-effort GetClaim read above found nothing, or the row was
// unclaimed) is a deliberate no-op: there is no token to match against,
// and guessing which row to interrupt is worse than interrupting none.
//
// The write reuses interruptRunningRun (runstore.go), the SAME
// conditional "WHERE state='running'" write sweepInterrupted itself
// uses - a run that reached a terminal state on its own between the
// takeover and this call is left alone - and publishes the change
// through the same s.publishRun path, so a live watcher sees the
// orphaned row settle exactly as it would from the periodic sweep. Any
// error here is logged, not returned: it must never block the fire this
// takeover just won admission for.
func (s *Service) interruptOrphanedRun(ctx context.Context, automationID, prevHolder string) {
	if prevHolder == "" {
		return
	}
	row, ok, err := s.db.GetRunningAutomationRunByClaimToken(ctx, automationID, prevHolder)
	if err != nil {
		log.Printf("automation %q: interrupt orphaned run: lookup by claim token: %v", automationID, err)
		return
	}
	if !ok {
		// No running row carries that exact token - e.g. the crashed
		// holder's claim predates any run row (a claim taken outside
		// startRun's normal path), or the row already reached a
		// terminal state on its own. Nothing to close out.
		return
	}
	r := fromStorageRun(row)
	endedAt := time.Now().UTC()
	message := "interrupted: fenced claim taken over by a new run"
	changed, uerr := s.interruptRunningRun(ctx, r.ID, endedAt, message)
	if uerr != nil {
		log.Printf("automation %q: interrupt orphaned run %q: %v", automationID, r.ID, uerr)
		return
	}
	if !changed {
		// The row reached a terminal state between the lookup and this
		// write; nothing to publish.
		return
	}
	r.State = RunInterrupted
	r.EndedAt = &endedAt
	r.Message = message
	s.publishRun(r)
}

// sweepInterrupted implements the crash-recovery sweep: every run
// currently in RunRunning whose fenced claim ("automation:"+automationID)
// has expired past maxAge is marked RunInterrupted and its claim is
// released, so a later Resume can re-claim it cleanly. The write is
// conditional on the row still being running, so a run that finished
// between the list read and the write is left alone. It returns the
// count of runs it flagged.
//
// A running row with NO claim at all (storage.ErrClaimNotHeld) is treated
// the same as an expired one: a live run always holds a claim, so a
// missing claim on a "running" row is, if anything, a stronger signal the
// owning process is gone than a stale-but-present one.
//
// A running row whose claim is present and NOT yet past maxAge is left
// alone (storage.ErrClaimHeld from TakeoverExpiredClaimFenced) - it may
// still be legitimately in-flight.
func (s *Service) sweepInterrupted(ctx context.Context, maxAge time.Duration) (int, error) {
	if s.db == nil {
		return 0, nil
	}
	rows, err := s.db.ListRunningAutomationRuns(ctx)
	if err != nil {
		return 0, fmt.Errorf("automation: sweep interrupted: list running runs: %w", err)
	}
	count := 0
	for _, row := range rows {
		r := fromStorageRun(row)
		sweepHolder := newHolderToken("automation-sweep-")
		claim, err := s.db.TakeoverExpiredClaimFenced(ctx, claimKey(r.AutomationID), sweepHolder, maxAge)
		switch {
		case err == nil:
			// Claim existed, was expired, and the sweep now owns it -
			// release immediately: the sweep's job is only to flag the
			// run, not to hold the claim for execution (that is
			// ResumeRun's job).
			_ = s.db.ReleaseClaimFenced(ctx, claim)
		case errors.Is(err, storage.ErrClaimNotHeld):
			// No claim row at all: treated as interrupted too (see doc
			// comment above).
		case errors.Is(err, storage.ErrClaimHeld):
			// Claim present and not yet expired: still legitimately
			// in-flight, skip.
			continue
		default:
			return count, fmt.Errorf("automation: sweep interrupted %q: %w", r.ID, err)
		}
		endedAt := time.Now().UTC()
		changed, uerr := s.interruptRunningRun(ctx, r.ID, endedAt, "interrupted: fenced claim expired or missing")
		if uerr != nil {
			return count, fmt.Errorf("automation: sweep interrupted %q: mark interrupted: %w", r.ID, uerr)
		}
		if !changed {
			// The run reached a terminal state after the list read.
			continue
		}
		r.State = RunInterrupted
		r.EndedAt = &endedAt
		r.Message = "interrupted: fenced claim expired or missing"
		s.publishRun(r)
		count++
	}
	return count, nil
}

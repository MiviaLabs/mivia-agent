// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (claim.go) is Chunk 5's fenced single-fire admission (D7) and
// interrupted-run sweep (D13). Execution itself (worktree creation,
// session spawning, per-step dispatch) is chunk 6; this file only decides
// "does this fire get to proceed" and "which running rows lost their
// owner".
package automation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/storage"
)

// newHolderToken generates a fresh, unpredictable per-claim identifier.
//
// Correction from the plan's suggested convention: internal/ledgercore's
// NewHolderID (crypto/rand + base32, "h-" prefix) is the closest existing
// precedent for this shape, but internal/automation's import-policy allow
// list (.mivia/policy/import-layers.json) does not grant it an edge to
// internal/ledgercore, and the plan's own D12 does not list one. Rather
// than widen the edge for a four-line helper, this reimplements the same
// crypto/rand-backed approach locally (hex instead of base32 - no
// external behavioral difference, both are opaque unique tokens).
func newHolderToken(prefix string) string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

// claimKey is the fenced-claim key D7 specifies for one automation:
// "automation:"+automationID. One key per automation enforces the "one
// in-flight run per automation" concurrency cap - two fires for the same
// automation contend for the same row regardless of run ID.
func claimKey(automationID string) string {
	return "automation:" + automationID
}

// admitFire is D7's single-fire dedup gate: every fire - scheduled or
// manual - must win this claim before any side effect. ok=false, err=nil
// is the documented "lost claim is a no-op, not an error" outcome
// (storage.ErrClaimHeld); any other error is real and reported. On
// success, holder is the fresh token that now owns the claim - the caller
// (chunk 6's executor) records it as the run's claim_token.
func (s *Service) admitFire(ctx context.Context, automationID string) (holder string, ok bool, err error) {
	if s.db == nil {
		return "", false, errNoRunStore
	}
	if err := ValidateID(automationID); err != nil {
		return "", false, err
	}
	token := newHolderToken("automation-run-")
	claim, err := s.db.ClaimRunFenced(ctx, claimKey(automationID), token)
	if err != nil {
		if errors.Is(err, storage.ErrClaimHeld) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("automation: admit fire %q: %w", automationID, err)
	}
	return claim.Holder, true, nil
}

// sweepInterrupted implements D13's crash-recovery sweep: every run
// currently in RunRunning whose fenced claim ("automation:"+automationID)
// has expired past maxAge is marked RunInterrupted and its claim is
// released, so a later Resume (chunk 8) can re-claim it cleanly. It
// returns the count of runs it flagged.
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
			// chunk 8's Resume).
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
		if uerr := s.updateRunState(ctx, r.ID, RunInterrupted, r.StepIndex, &endedAt, RunFailNone, "interrupted: fenced claim expired or missing"); uerr != nil {
			return count, fmt.Errorf("automation: sweep interrupted %q: mark interrupted: %w", r.ID, uerr)
		}
		count++
	}
	return count, nil
}

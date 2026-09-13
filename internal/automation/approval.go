// Package automation owns user-defined automations: their TOML
// definitions, their schedules, and their durable run records.
//
// This file (approval.go) is chunk 6's D8 unattended-approval policy:
// the two gate constructors the executor installs on a run's spawned
// session BEFORE the first step is sent, per spec.Unattended
// (UnattendedDeny -> DenyGate, UnattendedAuto -> AutoApproveGate). A
// scheduled or manual-unattended run has no interactive approver
// attached, so neither gate ever blocks waiting for a human - one
// refuses every tool call needing approval, the other approves every
// one of them, and both answer synchronously and immediately.
package automation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// DenyGate returns the fail-fast approval gate for
// UnattendedDeny (the default, D8): any tool call needing approval is
// refused immediately, naming the automation in the denial so the
// failed step's error message points at the automation that produced
// it rather than an opaque "denied" with no context.
func DenyGate(automationID string) func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
	return func(_ context.Context, _ string, _ json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{
			Approved: false,
			Err:      fmt.Sprintf("automation %q: unattended run denies all tool approvals", automationID),
		}
	}
}

// AutoApproveGate returns the opt-in auto-approval gate for
// UnattendedAuto (D8): every tool call needing approval is approved
// immediately. ApprovedForClass is deliberately left false on every
// return: sdkadapter.ApprovalStanding's "always" cache is a per-session
// record of an operator's own explicit choice, and this gate answers on
// behalf of no one - persisting a standing entry here would let a
// later interactive resume of the SAME saved session silently inherit
// an unattended run's blanket approval as if a human had granted it.
func AutoApproveGate() func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult {
	return func(context.Context, string, json.RawMessage) sdkadapter.ApprovalResult {
		return sdkadapter.ApprovalResult{Approved: true, ApprovedForClass: false}
	}
}

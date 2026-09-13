package uiadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// SetApprovalOverride installs gate/policy directly on the pooled session
// identified by sessionID, bypassing the ordinary inheritance path.
//
// It exists for chunk 6's automation executor: CreateFreshInDir's own
// wireEntryLocked unconditionally overwrites a freshly spawned session's
// ApprovalGate/ApprovalPolicy via inheritApprovalLocked (D8's ambient
// posture), so an executor that wants a run-specific override (deny or
// auto per spec.Unattended, D8) must apply it strictly AFTER
// CreateFreshInDir returns - never inside the bind closure, which runs
// before wireEntryLocked and would simply be clobbered.
//
// Returns a named error when sessionID is not a pooled session, rather
// than silently doing nothing: a caller that races this call against a
// session it thinks it just spawned needs to know the override never
// landed.
func (p *SessionPool) SetApprovalOverride(sessionID string, gate func(ctx context.Context, name string, args json.RawMessage) sdkadapter.ApprovalResult, policy string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	sess, ok := p.sessions[sessionID]
	if !ok || sess == nil {
		return fmt.Errorf("uiadapter: set approval override: unknown session %q", sessionID)
	}
	sess.ApprovalGate = gate
	sess.SetApprovalPolicy(policy)
	return nil
}

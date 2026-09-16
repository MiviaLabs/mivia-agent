package adapter

// Split from session_pool.go for maintainability (move-only, no logic change).

import (
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	contextstate "github.com/MiviaLabs/mivia-agent/internal/context/state"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
)

// inheritApprovalLocked carries the approval wiring onto a session the pool
// just built.
//
// /new and /resume hand-copy runtime state from an existing pool member -
// tools, event bus, context store, redaction policy - and carried none of the
// approval state. A threat model measured the result: a session started under
// `deny` with a live approver produced, after /new, policy="" and gate=nil.
// The operator's most restrictive setting silently evaporated on a keystroke
// that looks like housekeeping.
//
// The POLICY comes from config, not from the sibling's live value, because the
// live value may be a transient /yolo. A deliberate, temporary loosening of
// one conversation must not become the starting posture of the next one.
//
// The GATE is inherited, because it is the one the UI is actually reading
// from: the approver is constructed once, bound to the first session, and its
// Pending channel is what the prompt renders from. A fresh session with its
// own unattached gate would block on an approver nobody is watching.
//
// The STANDING cache is not inherited but IS created. "Always allow this call"
// is a decision made about one conversation, so carrying it across /new would
// widen it to a conversation the operator has not seen yet - and leaving the
// session with no cache at all makes the affordance dead rather than fresh:
// DecideApproval guards every standing read and write on a non-nil cache, so
// "a always" would be accepted by the prompt and silently forgotten, in every
// conversation after the first.
func inheritApprovalLocked(sess, existing *chat.Session, res *config.Resolved) {
	if sess == nil {
		return
	}
	if existing != nil {
		sess.ApprovalGate = existing.ApprovalGate
	}
	if sess.ApprovalStanding == nil {
		sess.ApprovalStanding = sdkadapter.NewApprovalStanding()
	}
	policy := ""
	if res != nil {
		policy = res.Approvals.ApprovalPolicy()
	}
	if policy == "" && existing != nil {
		policy = existing.BaseApprovalPolicyValue()
	}
	if policy != "" {
		sess.SetBaseApprovalPolicy(policy)
		sess.SetApprovalPolicy(policy)
	}
}

// inheritEntryStateLocked is the ONE inheritance path every pooled entry
// takes - plain or worktree-bound, fresh or resumed. The source is always
// preferredInheritanceSessionLocked (the launch member when one is known),
// never an arbitrary p.sessions map entry: a rebuild that later fails keeps
// what it inherited, and inheriting a random member handed a worktree
// session ANOTHER worktree's registry, so its file and command tools ran in
// the wrong checkout while the notice said tools stayed on the launch one.
// withPolicies carries the sibling's context policy onto the new session;
// only the worktree /new path omits it.
func (p *SessionPool) inheritEntryStateLocked(sess *chat.Session, withPolicies bool) *chat.Session {
	preferred := p.preferredInheritanceSessionLocked()
	for _, existing := range p.sessions {
		if preferred != nil && existing != preferred {
			continue
		}
		if existing.Tools != nil {
			sess.Tools = existing.Tools
			sess.MaxToolResultChars = existing.MaxToolResultChars
			sess.BatchResultBudgetBytes = existing.BatchResultBudgetBytes
			sess.RefOnlyTools = existing.RefOnlyTools
		}
		if existing.EventBus != nil {
			sess.EventBus = existing.EventBus
		}
		if mgr := existing.ContextManager(); mgr != nil {
			origPrincipal := existing.ContextPrincipal()
			if origPrincipal.IsBound() {
				newPrincipal, err := contextstate.NewPrincipal(origPrincipal.WorkspaceID, sess.SessionID, origPrincipal.SubjectID)
				if err == nil {
					if withPolicies {
						_ = sess.SetContextManager(mgr, newPrincipal, existing.ContextPolicy())
					} else {
						_ = sess.SetContextManager(mgr, newPrincipal)
					}
				}
			}
		}
		// Inherited for the same reason every other field above is: a
		// conversation born from the pool must write context under the same
		// privacy rules as its siblings. Omitting it left a fresh conversation
		// running the ZERO policy, so every payload it wrote was recorded
		// hash-only while a sibling wrote the same content with bytes. Those
		// two writes land on one content ref, and the disagreement used to roll
		// back a whole turn as "payload reference is held by different bytes".
		sess.SetContextRedactionPolicy(existing.ContextRedactionPolicy())
		if store := existing.ContextStore(); store != nil {
			_ = sess.SetContextStore(store)
		}
		return existing
	}
	return nil
}

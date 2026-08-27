// Package sdkadapter - shared approval types.
//
// ApprovalResult and ApprovalStanding live in this package rather than
// internal/agent because internal/sdkadapter already imports nothing
// from internal/agent (the dependency direction is internal/agent ->
// internal/sdkadapter, and reversing it would create a cycle). The
// agent loop's Options.ApprovalGate / Options.ApprovalStanding fields
// reference these types directly.
//
// The values produced by an Approver bridge flow from a real agent's
// internal/uiadapter port through this package's type, so the wire
// shape is the same for the legacy and SDK paths.
package sdkadapter

import (
	"sync"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// ApprovalResult is the verdict returned by Options.ApprovalGate. If
// Approved is false, Err is rendered as the tool's failure message (the
// model sees it as a tool error and may retry or move on). ApprovedForClass
// persists the decision for the rest of the session - the user pressed
// "a always" or "D deny always" - and is consulted by the gate's lookup
// path before the function is invoked again.
type ApprovalResult struct {
	Approved         bool
	ApprovedForClass bool
	Err              string
}

// ApprovalStanding is the per-session "always" cache consulted by the
// approval gate before invoking Options.ApprovalGate. A nil pointer is
// safe: every call falls through to the gate. The same instance backs
// the legacy and SDK paths so a "always" decision persists across
// backends within one session. The zero value is an empty cache.
type ApprovalStanding struct {
	mu    sync.Mutex
	allow map[string]tools.ExecutionClass
	deny  map[string]tools.ExecutionClass
}

// NewApprovalStanding returns an empty cache. Concurrent calls to its
// methods are safe.
func NewApprovalStanding() *ApprovalStanding {
	return &ApprovalStanding{
		allow: map[string]tools.ExecutionClass{},
		deny:  map[string]tools.ExecutionClass{},
	}
}

// Allow records an "always approve" decision for name at class. The
// class tag is carried so a future audit can distinguish a per-class
// standing decision from a per-tool one.
func (s *ApprovalStanding) Allow(name string, class tools.ExecutionClass) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allow == nil {
		s.allow = map[string]tools.ExecutionClass{}
		s.deny = map[string]tools.ExecutionClass{}
	}
	s.allow[name] = class
	delete(s.deny, name)
}

// Deny records an "always deny" decision for name at class.
func (s *ApprovalStanding) Deny(name string, class tools.ExecutionClass) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allow == nil {
		s.allow = map[string]tools.ExecutionClass{}
		s.deny = map[string]tools.ExecutionClass{}
	}
	s.deny[name] = class
	delete(s.allow, name)
}

// Lookup returns the standing verdict for name. The bool is true only
// when a verdict is recorded (allow OR deny); the verdict's direction
// is reported as approved=true (allow) or approved=false (deny).
func (s *ApprovalStanding) Lookup(name string) (approved bool, ok bool) {
	if s == nil {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, hit := s.allow[name]; hit {
		return true, true
	}
	if _, hit := s.deny[name]; hit {
		return false, true
	}
	return false, false
}

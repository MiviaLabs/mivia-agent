package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type validationCoverageHandler struct{ err error }

func (validationCoverageHandler) Invoke(context.Context, Request) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}

func (h validationCoverageHandler) ValidateRequest(Request) error { return h.err }

func TestSessionIDAndDispatcherAllowHelpers(t *testing.T) {
	first, second := NewSessionID(), NewSessionID()
	if first == second || len(first) != 26 || strings.Contains(first, "=") {
		t.Fatalf("session IDs must be distinct unpadded 128-bit base32 values: %q, %q", first, second)
	}
	dispatcher := New(Policy{})
	if dispatcher.Has(Tool, "read_file") {
		t.Fatal("unregistered handler reported as present")
	}
	dispatcher.Allow(Tool, "read_file")
	if !dispatcher.policy.Allow[Tool]["read_file"] {
		t.Fatal("Allow did not create the requested permission")
	}
}

func TestDispatcherValidateCoveragePaths(t *testing.T) {
	dispatcher := New(Policy{MaxBudget: 3})
	if err := dispatcher.Validate(Request{Kind: Subagent, Name: "worker", Budget: -1}); err == nil {
		t.Fatal("negative budget request was accepted")
	}
	if err := dispatcher.Validate(Request{Kind: Subagent, Name: "missing"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("missing handler error = %v", err)
	}

	validatorErr := errors.New("stale routing")
	if err := dispatcher.Register(Subagent, "worker", validationCoverageHandler{err: validatorErr}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	dispatcher.policy.Allow[Subagent]["worker"] = false
	if err := dispatcher.Validate(Request{Kind: Subagent, Name: "worker"}); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("denied handler error = %v", err)
	}
	dispatcher.Allow(Subagent, "worker")
	if err := dispatcher.Validate(Request{Kind: Subagent, Name: "worker"}); !errors.Is(err, validatorErr) {
		t.Fatalf("validator error = %v, want %v", err, validatorErr)
	}
	if err := dispatcher.Register(Subagent, "accepted", validationCoverageHandler{}); err != nil {
		t.Fatalf("Register accepted handler: %v", err)
	}
	if err := dispatcher.Validate(Request{Kind: Subagent, Name: "accepted", Budget: 3}); err != nil {
		t.Fatalf("accepted request error = %v", err)
	}
	if err := dispatcher.Register(Subagent, "plain", testHandler{}); err != nil {
		t.Fatalf("Register plain handler: %v", err)
	}
	if err := dispatcher.Validate(Request{Kind: Subagent, Name: "plain"}); err != nil {
		t.Fatalf("plain handler request error = %v", err)
	}
}

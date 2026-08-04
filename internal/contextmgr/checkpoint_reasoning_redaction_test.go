package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
)

// buildCheckpointRequest drives BuildCommitRequest through the same harness as
// TestBuildCommitRequestMapsCompleteTurn, with one twist: the assistant turn
// carries a reasoning trace whose content matches the redaction policy.
// Everything except the secret text is identical across calls, so any
// observable difference between two builds can only come from the reasoning.
func buildCheckpointRequest(t *testing.T, reasoning string) contextstate.CommitRequest {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	source, err := contextstate.NewSourceID(principal.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	rng, err := contextstate.NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}
	preparation, err := CapturePreparation(
		PrepareInput{Messages: []provider.Message{{Role: provider.RoleUser, Content: "old"}}, Budget: 100, Principal: principal, Binding: binding},
		CheckpointCandidate{SourceRange: rng, SummaryMetadata: []byte(`{"version":1}`)},
		[]provider.Message{{Role: provider.RoleSystem, Content: "system"}, {Role: provider.RoleUser, Content: "old"}}, false, "operation-1",
	)
	if err != nil {
		t.Fatal(err)
	}
	result := TurnResult{
		Ordered: []provider.Message{
			{Role: provider.RoleUser, Content: "question"},
			{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: reasoning},
		},
		Active: []provider.Message{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "question"},
			{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: reasoning},
		},
		SourceEvents: []contextstate.SourceEvent{{ID: source, Kind: "message", Role: "user", Provenance: "host", RedactionStatus: "metadata", Size: 8}},
		TurnID:       7, Outcome: OutcomeComplete,
	}
	request, err := BuildCommitRequest(nil, preparation, result, principal, contextstate.Revision{}, binding)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

// TestCheckpointActiveContextRedactsReasoning pins that chain-of-thought on an
// assistant turn is scrubbed before it reaches the durable checkpoint. The
// ActiveContext is canonical JSON of the post-turn messages; a raw
// reasoning_content field would publish the model's private reasoning to every
// storage consumer.
func TestCheckpointActiveContextRedactsReasoning(t *testing.T) {
	policy, err := redact.Compile([]string{`(?i)secret-[0-9]+`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	old := redact.Current()
	defer redact.SetPolicy(old)
	redact.SetPolicy(policy)

	request := buildCheckpointRequest(t, "secret-1234")
	active := string(request.Checkpoint.ActiveContext)
	if !strings.Contains(active, "[redacted]") {
		t.Fatalf("checkpoint ActiveContext was not redacted: %s", active)
	}
	if strings.Contains(active, "secret-1234") {
		t.Fatalf("checkpoint ActiveContext leaked the reasoning secret: %s", active)
	}
}

// TestCheckpointFingerprintCoversRedactedActiveContext pins the ordering:
// redaction must be applied BEFORE FingerprintCommitRequest hashes the
// request, because the fingerprint input includes the ActiveContext bytes.
// Two commits that differ only in the secret text therefore redact to the
// same durable bytes and must mint the SAME fingerprint; if the raw reasoning
// were fingerprinted instead, the fingerprints would diverge. The stored
// ActiveContext must be the redacted bytes in both cases.
func TestCheckpointFingerprintCoversRedactedActiveContext(t *testing.T) {
	policy, err := redact.Compile([]string{`(?i)secret-[0-9]+`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	old := redact.Current()
	defer redact.SetPolicy(old)
	redact.SetPolicy(policy)

	first := buildCheckpointRequest(t, "secret-1234")
	second := buildCheckpointRequest(t, "secret-5678")

	if first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprint must be computed over the REDACTED ActiveContext: differing secrets produced differing fingerprints (%q vs %q)", first.Fingerprint, second.Fingerprint)
	}
	for name, request := range map[string]contextstate.CommitRequest{"first": first, "second": second} {
		active := string(request.Checkpoint.ActiveContext)
		if !strings.Contains(active, "[redacted]") {
			t.Fatalf("%s checkpoint ActiveContext was not redacted: %s", name, active)
		}
		if strings.Contains(active, "secret-") {
			t.Fatalf("%s checkpoint ActiveContext leaked the reasoning secret: %s", name, active)
		}
	}
}

// TestStructuralPrepareRedactsCandidateReasoning pins the Prepare-side
// candidate redaction (structural.go non-compacted branch): the candidate
// ActiveContext bytes are durable, operator-visible state, so reasoning must
// be scrubbed before they are marshaled, while plan.Messages stay raw for
// replay. Identity without an installed policy (fail-open).
func TestStructuralPrepareRedactsCandidateReasoning(t *testing.T) {
	policy, err := redact.Compile([]string{`(?i)secret-[0-9]+`}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	old := redact.Current()
	defer redact.SetPolicy(old)
	redact.SetPolicy(policy)

	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	source, err := contextstate.NewSourceID(principal.SessionID, 1)
	if err != nil {
		t.Fatal(err)
	}
	rng, err := contextstate.NewSourceRange(source, source)
	if err != nil {
		t.Fatal(err)
	}
	m := StructuralPreparationManager{}
	preparation, err := m.Prepare(context.Background(), PrepareInput{
		Principal:   principal,
		Binding:     binding,
		Revision:    contextstate.Revision{Session: 1, Durable: 1, Source: 1},
		SourceRange: rng,
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "system"},
			{Role: provider.RoleUser, Content: "question"},
			{Role: provider.RoleAssistant, Content: "answer", ReasoningContent: "secret-1234"},
		},
		Budget: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	active := string(preparation.Candidate.ActiveContext)
	if !strings.Contains(active, "[redacted]") {
		t.Fatalf("structural candidate ActiveContext was not redacted: %s", active)
	}
	if strings.Contains(active, "secret-1234") {
		t.Fatalf("structural candidate ActiveContext leaked the reasoning secret: %s", active)
	}
	raw := false
	for _, msg := range preparation.Messages {
		if msg.ReasoningContent == "secret-1234" {
			raw = true
		}
	}
	if !raw {
		t.Fatal("preparation.Messages must keep raw reasoning for replay")
	}
}

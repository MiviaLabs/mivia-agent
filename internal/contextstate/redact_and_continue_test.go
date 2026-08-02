package contextstate

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func newRedactingPolicy() RedactionPolicy {
	return RedactionPolicy{
		Configured: true,
		Patterns:   []string{`(?i)token\s*=\s*\S+`},
		Redactor: func(data []byte) []byte {
			return []byte(strings.ReplaceAll(string(data), "token=hunter2", "[redacted]"))
		},
	}
}

func mustPrincipal(t *testing.T) Principal {
	t.Helper()
	principal, err := NewPrincipal("workspace", "session", "local-user")
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

// TestConfiguredRedactionRedactsInsteadOfRefusing is the behaviour this repo
// chose: a privacy rule may change what is stored, never destroy a turn the
// agent already finished. Classification used to return an error, which failed
// the commit - and because publication is one transaction, the whole turn was
// lost and the session wedged (INV-AG-35).
func TestConfiguredRedactionRedactsInsteadOfRefusing(t *testing.T) {
	principal := mustPrincipal(t)
	payload, err := SanitizeSourcePayload(context.Background(), principal, []byte("here is token=hunter2 ok"), newRedactingPolicy())
	if err != nil {
		t.Fatalf("a secret-bearing message refused the turn: %v", err)
	}
	if !payload.Dereferenceable || payload.HashOnly {
		t.Fatalf("redacted payload = %+v, want stored and dereferenceable", payload)
	}
	if bytes.Contains(payload.Bytes, []byte("hunter2")) {
		t.Fatal("stored payload still carries the secret")
	}
	if !bytes.Contains(payload.Bytes, []byte("[redacted]")) {
		t.Fatalf("stored payload was not redacted: %q", payload.Bytes)
	}
	if payload.Ref.Size != len(payload.Bytes) {
		t.Fatalf("reference size %d does not describe the stored bytes %d", payload.Ref.Size, len(payload.Bytes))
	}
	record := PayloadRecord{Ref: payload.Ref, Retention: payload.Retention, Data: payload.Bytes}
	if err := record.Validate(); err != nil {
		t.Fatalf("redacted payload record is inconsistent: %v", err)
	}
}

// TestUnredactableSecretDegradesToMetadata covers a configured policy that
// cannot clean the value: the payload must degrade to hash-only rather than
// either storing the secret or destroying the turn.
func TestUnredactableSecretDegradesToMetadata(t *testing.T) {
	principal := mustPrincipal(t)
	policy := RedactionPolicy{Configured: true, Patterns: []string{`(?i)token\s*=\s*\S+`}}
	payload, err := SanitizeSourcePayload(context.Background(), principal, []byte("here is token=hunter2 ok"), policy)
	if err != nil {
		t.Fatalf("an unredactable message refused the turn: %v", err)
	}
	if !payload.HashOnly || payload.Dereferenceable || len(payload.Bytes) != 0 {
		t.Fatalf("unredactable payload = %+v, want hash-only", payload)
	}
}

// TestIneffectiveRedactorStillDegradesToMetadata guards the belt: a redactor
// that leaves the flagged value behind must not have its output stored.
func TestIneffectiveRedactorStillDegradesToMetadata(t *testing.T) {
	principal := mustPrincipal(t)
	policy := RedactionPolicy{
		Configured: true,
		Patterns:   []string{`(?i)token\s*=\s*\S+`},
		Redactor:   func(data []byte) []byte { return data },
	}
	payload, err := SanitizeSourcePayload(context.Background(), principal, []byte("here is token=hunter2 ok"), policy)
	if err != nil {
		t.Fatalf("an ineffective redactor refused the turn: %v", err)
	}
	if !payload.HashOnly || len(payload.Bytes) != 0 {
		t.Fatalf("payload = %+v, want hash-only when redaction did not clean it", payload)
	}
}

// TestCleanPayloadUnderConfiguredPolicyIsStoredWhole keeps the ordinary path
// intact: nothing flagged means nothing changed.
func TestCleanPayloadUnderConfiguredPolicyIsStoredWhole(t *testing.T) {
	principal := mustPrincipal(t)
	const clean = "an ordinary sentence"
	payload, err := SanitizeSourcePayload(context.Background(), principal, []byte(clean), newRedactingPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if string(payload.Bytes) != clean || !payload.Dereferenceable {
		t.Fatalf("clean payload = %+v, want stored verbatim", payload)
	}
}

// TestSummaryClassificationStillRefuses pins the deliberate asymmetry. A
// summary is host-generated and regenerable, and refusing one keeps model
// output carrying a secret out of storage and off the wire; a turn is neither.
func TestSummaryClassificationStillRefuses(t *testing.T) {
	policy := newRedactingPolicy()
	if err := policy.Classify([]byte("here is token=hunter2 ok")); err == nil {
		t.Fatal("Classify no longer reports a flagged value")
	}
	if err := policy.Classify([]byte("an ordinary sentence")); err != nil {
		t.Fatalf("Classify rejected clean text: %v", err)
	}
}

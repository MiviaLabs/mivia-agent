package contextmgr

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

// --- Wave 2a: elide-with-ref behavior (RED tests; API arrives with this wave) ---

// elidedPriorToolMessage returns the retained call-old tool message after an
// eliding plan, failing the test when retention dropped it (widen the tail).
func elidedPriorToolMessage(t *testing.T, messages []provider.Message) provider.Message {
	t.Helper()
	for _, msg := range messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-old" {
			return msg
		}
	}
	t.Fatal("call-old tool message missing from retained set; widen tail in fixture")
	return provider.Message{}
}

// elidedRefFromNotice extracts the content reference named by an elision
// notice. The notice wording is a load-bearing interface: the model must be
// able to recover the ref from the message it received.
func elidedRefFromNotice(t *testing.T, content string) string {
	t.Helper()
	const marker = "ref:output:"
	start := strings.Index(content, marker)
	if start < 0 {
		t.Fatalf("elided content %q does not name a content ref", content)
	}
	ref := content[start : start+len(marker)+64]
	if strings.ContainsAny(ref, " \t\r\n") {
		t.Fatalf("extracted ref %q contains whitespace", ref)
	}
	return ref
}

// assertPlainElisionNotice pins today's notice shape: the constant-format
// host notice with no remainder ref.
func assertPlainElisionNotice(t *testing.T, content string) {
	t.Helper()
	if !strings.HasPrefix(content, "[context elided prior tool result; original size about") {
		t.Fatalf("elided content %q is not the plain notice", content)
	}
	if strings.Contains(content, "remainder:") {
		t.Fatalf("elided content %q names a remainder ref without a spool grant", content)
	}
}

func TestElideSpoolsFullBodyAndNamesRef(t *testing.T) {
	big := strings.Repeat("x", elisionContentMinBytes+1)
	messages := elisionHistory(big, "small")
	store := remainder.NewMemoryStore()
	spool := remainder.NewSpool(store)
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
		Spool:      spool,
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	if plan.ElidedBytes != len(big) {
		t.Fatalf("ElidedBytes=%d, want %d", plan.ElidedBytes, len(big))
	}
	elided := elidedPriorToolMessage(t, plan.Messages)
	if !strings.Contains(elided.Content, "remainder:") {
		t.Fatalf("elided content %q does not name a remainder ref", elided.Content)
	}
	ref := elidedRefFromNotice(t, elided.Content)

	got, err := spool.Load(context.Background(), principal.SessionID, ref)
	if err != nil {
		t.Fatalf("spool.Load(%q): %v", principal.SessionID, err)
	}
	if string(got) != big {
		t.Fatalf("spooled body is %d bytes, want %d (full original)", len(got), len(big))
	}
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want exactly 1", store.Len())
	}
}

func TestElideWithNilSpoolKeepsPlainNotice(t *testing.T) {
	big := strings.Repeat("x", elisionContentMinBytes+1)
	messages := elisionHistory(big, "small")
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	assertPlainElisionNotice(t, elidedPriorToolMessage(t, plan.Messages).Content)
}

func TestElideWithNewSpoolNilStoreKeepsPlainNotice(t *testing.T) {
	big := strings.Repeat("x", elisionContentMinBytes+1)
	messages := elisionHistory(big, "small")
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
		Spool:      remainder.NewSpool(nil),
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	assertPlainElisionNotice(t, elidedPriorToolMessage(t, plan.Messages).Content)
}

func TestElideStoreFailureKeepsPlainNotice(t *testing.T) {
	big := strings.Repeat("x", elisionContentMinBytes+1)
	messages := elisionHistory(big, "small")
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
		Spool:      remainder.NewSpool(remainder.FailingStore{}),
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	assertPlainElisionNotice(t, elidedPriorToolMessage(t, plan.Messages).Content)
}

func TestElideEmptyPrincipalKeepsPlainNotice(t *testing.T) {
	big := strings.Repeat("x", elisionContentMinBytes+1)
	messages := elisionHistory(big, "small")

	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
		Spool:      remainder.NewSpool(remainder.NewMemoryStore()),
		// Principal left as the zero value: no SessionID to grant.
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	assertPlainElisionNotice(t, elidedPriorToolMessage(t, plan.Messages).Content)
}

func TestElideRefIsPrincipalScoped(t *testing.T) {
	big := strings.Repeat("x", elisionContentMinBytes+1)
	messages := elisionHistory(big, "small")
	store := remainder.NewMemoryStore()
	spool := remainder.NewSpool(store)
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
		Spool:      spool,
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := elidedRefFromNotice(t, elidedPriorToolMessage(t, plan.Messages).Content)

	got, err := spool.Load(context.Background(), principal.SessionID, ref)
	if err != nil {
		t.Fatalf("owner spool.Load: %v", err)
	}
	if string(got) != big {
		t.Fatalf("owner spooled body is %d bytes, want %d", len(got), len(big))
	}

	_, err = spool.Load(context.Background(), "session-b", ref)
	if !errors.Is(err, remainder.ErrDenied) {
		t.Fatalf("cross-principal spool.Load: %v, want remainder.ErrDenied", err)
	}
}

func TestElideTwoHopArtifactChain(t *testing.T) {
	// refA is a pass-1 ref named inside an already-truncated artifact body.
	refA := "ref:output:" + strings.Repeat("a", 64)
	artifact := "artifact leading content\n" + strings.Repeat("fragment-line\n", 256) +
		"... truncated: kept 512 of 20000 bytes (remainder: " + refA + ", use read_output)"
	if len(artifact) <= elisionContentMinBytes {
		t.Fatalf("artifact body too small to elide: %d bytes", len(artifact))
	}
	messages := elisionHistory(artifact, "small")
	spool := remainder.NewSpool(remainder.NewMemoryStore())
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
		Spool:      spool,
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	elided := elidedPriorToolMessage(t, plan.Messages)
	if !strings.Contains(elided.Content, "remainder:") {
		t.Fatalf("second-hop notice %q does not name a ref", elided.Content)
	}
	refB := elidedRefFromNotice(t, elided.Content)
	if refB == refA {
		t.Fatal("second-hop ref equals pass-1 ref")
	}

	got, err := spool.Load(context.Background(), principal.SessionID, refB)
	if err != nil {
		t.Fatalf("spool.Load(refB): %v", err)
	}
	if string(got) != artifact {
		t.Fatal("spooled artifact body differs from the original")
	}
	if !strings.Contains(string(got), "remainder: "+refA) {
		t.Fatal("spooled artifact body lost its inner pass-1 ref")
	}
}

func TestElideDeterministicAcrossPrepares(t *testing.T) {
	big := strings.Repeat("x", elisionContentMinBytes+1)
	messages := elisionHistory(big, "small")
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}
	budget := forceCompactBudget(t, messages)
	spool := remainder.NewSpool(remainder.NewMemoryStore())

	first, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     budget,
		Force:      true,
		RecentTail: 64,
		Spool:      spool,
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Plan(PlanInput{
		Messages:   messages,
		Budget:     budget,
		Force:      true,
		RecentTail: 64,
		Spool:      spool,
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.IdempotencyKey == "" {
		t.Fatal("empty IdempotencyKey on eliding plan")
	}
	if first.IdempotencyKey != second.IdempotencyKey {
		t.Fatalf("nondeterministic IdempotencyKey: %q vs %q", first.IdempotencyKey, second.IdempotencyKey)
	}
	if len(first.Messages) != len(second.Messages) {
		t.Fatalf("message counts differ: %d vs %d", len(first.Messages), len(second.Messages))
	}
	for i := range first.Messages {
		if first.Messages[i].Content != second.Messages[i].Content {
			t.Fatalf("message %d content differs between prepares", i)
		}
	}
	ref := elidedRefFromNotice(t, elidedPriorToolMessage(t, first.Messages).Content)
	got, err := spool.Load(context.Background(), principal.SessionID, ref)
	if err != nil {
		t.Fatalf("spool.Load of deterministic ref: %v", err)
	}
	if string(got) != big {
		t.Fatalf("spooled body is %d bytes, want %d", len(got), len(big))
	}
}

func TestStructuralManagerElideMintsRef(t *testing.T) {
	big := strings.Repeat("x", elisionContentMinBytes+1)
	messages := elisionHistory(big, "small")
	spool := remainder.NewSpool(remainder.NewMemoryStore())
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}

	prep, err := (StructuralPreparationManager{}).Prepare(context.Background(), PrepareInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 64,
		Spool:      spool,
		Principal:  principal,
		Binding:    binding,
		Revision:   contextstate.Revision{Session: 1, Durable: 1, Source: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prep.Compacted {
		t.Fatal("expected compacted preparation")
	}
	if prep.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", prep.ElidedMessages)
	}
	elided := elidedPriorToolMessage(t, prep.Messages)
	if !strings.Contains(elided.Content, "remainder:") {
		t.Fatalf("prepared notice %q does not name a ref", elided.Content)
	}
	ref := elidedRefFromNotice(t, elided.Content)

	got, err := spool.Load(context.Background(), principal.SessionID, ref)
	if err != nil {
		t.Fatalf("spool.Load: %v", err)
	}
	if string(got) != big {
		t.Fatalf("prepared elision spooled %d bytes, want %d (full original)", len(got), len(big))
	}
}

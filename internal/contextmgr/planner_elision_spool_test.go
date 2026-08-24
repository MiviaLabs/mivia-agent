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

// elisionRetentionFixture builds a transcript in which one oversized prior
// tool body (droppedBody at the call-old exchange) falls outside the
// RecentTail=8 cap and is dropped by retention, while a newer oversized body
// (retainedBody at the call-mid exchange) stays inside the retained tail. Both
// bodies sit before the current objective and are neither mandatory nor the
// latest tool unit, so both are elided; only the retained one may be spooled.
func elisionRetentionFixture() (messages []provider.Message, droppedBody, retainedBody string) {
	droppedBody = strings.Repeat("x", elisionContentMinBytes+1)
	retainedBody = strings.Repeat("y", elisionContentMinBytes+1)
	callOld := plannerToolCall("call-old", "read_file", `{"path":"old.txt"}`)
	callMid := plannerToolCall("call-mid", "read_file", `{"path":"mid.txt"}`)
	callNew := plannerToolCall("call-new", "read_file", `{"path":"new.txt"}`)
	messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "old objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callOld}},
		{Role: provider.RoleTool, ToolCallID: callOld.ID, Name: callOld.Function.Name, Content: droppedBody},
		{Role: provider.RoleAssistant, Content: "done"},
		{Role: provider.RoleUser, Content: "mid1"},
		{Role: provider.RoleAssistant, Content: "mid1 answer"},
		{Role: provider.RoleUser, Content: "mid2"},
		{Role: provider.RoleAssistant, Content: "mid2 answer"},
		{Role: provider.RoleUser, Content: "mid3"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callMid}},
		{Role: provider.RoleTool, ToolCallID: callMid.ID, Name: callMid.Function.Name, Content: retainedBody},
		{Role: provider.RoleAssistant, Content: "mid done"},
		{Role: provider.RoleUser, Content: "current objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{callNew}},
		{Role: provider.RoleTool, ToolCallID: callNew.ID, Name: callNew.Function.Name, Content: "small"},
	}
	return messages, droppedBody, retainedBody
}

// TestElisionSpoolSkipsMessagesDroppedByRetention pins H-1: elision used to
// spool every oversized eligible tool body before retention ran, so a body the
// recent-tail cap then dropped was written to the store with no retained
// message naming its ref - an unreachable body with no production cleanup
// path. The spool must only ever receive bodies that survive retention.
func TestElisionSpoolSkipsMessagesDroppedByRetention(t *testing.T) {
	messages, droppedBody, retainedBody := elisionRetentionFixture()
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
		RecentTail: 8,
		Spool:      spool,
		Principal:  principal,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Compacted {
		t.Fatal("forced plan was not compacted")
	}
	if plan.ElidedMessages != 2 {
		t.Fatalf("ElidedMessages=%d, want 2 (both oversized bodies elided)", plan.ElidedMessages)
	}
	// The dropped body must never reach the store: only the retained elided
	// body may be spooled. Fails before the fix, when elision spooled both
	// bodies (store.Len()==2, one orphaned and unreachable).
	if store.Len() != 1 {
		t.Fatalf("store holds %d bodies, want exactly 1 (the retained elided body only)", store.Len())
	}
	// Every retained elision notice must name the SAME ref, and that ref must
	// load the retained body: no retained notice may reference the dropped
	// body.
	var retainedRef string
	var retainedNotices int
	for _, msg := range plan.Messages {
		if msg.Role != provider.RoleTool || !strings.Contains(msg.Content, "context elided prior tool result") {
			continue
		}
		retainedNotices++
		ref := elidedRefFromNotice(t, msg.Content)
		if retainedRef != "" && ref != retainedRef {
			t.Fatalf("retained notices name distinct refs %q and %q: a dropped body was spooled", retainedRef, ref)
		}
		retainedRef = ref
		got, err := spool.Load(context.Background(), principal.SessionID, ref)
		if err != nil {
			t.Fatalf("retained notice ref %q not loadable: %v", ref, err)
		}
		if string(got) != retainedBody {
			t.Fatalf("retained notice ref loads %d bytes, want the retained body (%d bytes)", len(got), len(retainedBody))
		}
	}
	if retainedRef == "" {
		t.Fatal("no retained elision notice names a ref; the retained body was never spooled")
	}
	if retainedNotices != 1 {
		t.Fatalf("retained elision notices=%d, want exactly 1", retainedNotices)
	}
	// Safety net: the dropped body appears nowhere in the retained set.
	for _, msg := range plan.Messages {
		if strings.Contains(msg.Content, droppedBody) {
			t.Fatal("retained message still carries the dropped oversized body")
		}
	}
}

// TestElisionFailedPlanNeverSpools pins the H-1-RESIDUAL edge: an invalid
// caller-supplied IdempotencyKey fails the plan inside planIdempotencyKey,
// which runs AFTER installRetainedElisionRefs has spooled the retained elided
// bodies. Without the pre-spool key validation the failed plan leaves an
// orphaned body in the store (bytes no retained message names, no production
// cleanup path). A failed plan must never spool anything.
func TestElisionFailedPlanNeverSpools(t *testing.T) {
	messages, _, _ := elisionRetentionFixture()
	store := remainder.NewMemoryStore()
	spool := remainder.NewSpool(store)
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}

	_, err = Plan(PlanInput{
		Messages:   messages,
		Budget:     forceCompactBudget(t, messages),
		Force:      true,
		RecentTail: 8,
		// Control character: rejected by validatePlanKey (the compaction path
		// reaches planIdempotencyKey, which validates caller-supplied keys).
		IdempotencyKey: "compact-\ninvalid",
		Spool:          spool,
		Principal:      principal,
	})
	if err == nil {
		t.Fatal("Plan accepted an invalid caller-supplied IdempotencyKey")
	}
	if !errors.Is(err, contextstate.ErrInvalidDTO) {
		t.Fatalf("error = %v, want contextstate.ErrInvalidDTO", err)
	}
	// The plan failed after retention would have selected the retained elided
	// body, so the old ordering had already spooled it (store.Len()==1 before
	// the fix). With the pre-spool key validation nothing may reach the store.
	if store.Len() != 0 {
		t.Fatalf("failed plan spooled %d bodies; a failed plan must never spool (H-1-RESIDUAL)", store.Len())
	}
}

func TestEmergencyElideSpoolsFullBodyAndNamesRef(t *testing.T) {
	call := plannerToolCall("call-emergency", "read_file", `{}`)
	huge := strings.Repeat("e", 50_000)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "active objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: huge},
	}
	store := remainder.NewMemoryStore()
	spool := remainder.NewSpool(store)
	principal, err := contextstate.NewPrincipal("workspace", "session-a", "subject")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := Plan(PlanInput{
		Messages:  messages,
		Budget:    2000,
		Force:     true,
		Spool:     spool,
		Principal: principal,
	})
	if err != nil {
		t.Fatalf("emergency plan failed: %v", err)
	}
	if plan.ElidedMessages != 1 {
		t.Fatalf("ElidedMessages=%d, want 1", plan.ElidedMessages)
	}
	if store.Len() != 1 {
		t.Fatalf("store.Len()=%d, want 1", store.Len())
	}
	var elidedContent string
	for _, msg := range plan.Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "call-emergency" {
			elidedContent = msg.Content
		}
	}
	if !strings.Contains(elidedContent, "remainder:") {
		t.Fatalf("elided content %q does not name a remainder ref", elidedContent)
	}
	ref := elidedRefFromNotice(t, elidedContent)
	data, err := spool.Load(context.Background(), principal.SessionID, ref)
	if err != nil || string(data) != huge {
		t.Fatalf("spooled body mismatch: got %d bytes, err=%v", len(data), err)
	}
}

func TestEmergencyElideSkippedWhenNotCheaper(t *testing.T) {
	call := plannerToolCall("call-expensive", "read_file", `{}`)
	huge := strings.Repeat("k", 5000)
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "active objective"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{call}},
		{Role: provider.RoleTool, ToolCallID: call.ID, Name: call.Function.Name, Content: huge},
	}
	origCost := messageTokenCost
	defer func() { messageTokenCost = origCost }()
	// Make notice candidate cost strictly higher so elision skips it
	messageTokenCost = func(msg provider.Message) int {
		if strings.HasPrefix(msg.Content, "[context elided") {
			return 100_000
		}
		return 10
	}

	_, err := Plan(PlanInput{
		Messages: messages,
		Budget:   15,
		Force:    true,
	})
	if !errors.Is(err, contextstate.ErrPromptBudgetExceeded) {
		t.Fatalf("error = %v, want ErrPromptBudgetExceeded when emergency elision cannot reduce cost", err)
	}
}

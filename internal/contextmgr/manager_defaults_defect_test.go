package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// managerDefaultsHistory builds the fixture shared by the manager-defaults
// defect tests: system + current objective + 8 optional assistant tail
// messages. The tail cap is what manager RecentTail must control; before the
// fix the manager ignored the field and the default tail of 8 was used.
func managerDefaultsHistory() []provider.Message {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "current objective"},
	}
	for index := 1; index <= 8; index++ {
		messages = append(messages, provider.Message{
			Role:    provider.RoleAssistant,
			Content: "older answer " + strings.Repeat("x", index),
		})
	}
	return messages
}

func managerDefaultsPrincipal(t *testing.T) (contextstate.Principal, contextstate.BindingRevision) {
	t.Helper()
	principal, err := contextstate.NewPrincipal("workspace", "session", "subject")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := contextstate.NewBindingRevision("provider", "model", 1)
	if err != nil {
		t.Fatal(err)
	}
	return principal, binding
}

// TestStructuralPreparationHonorsRecentTailDefault drives the real manager
// entry (StructuralPreparationManager.Prepare, reached by chat
// sendPlainContext / finishAgentTurn and the agent loop prepareStep). The
// manager's RecentTail default must cap the optional retained tail; before the
// fix the field was ignored and the default tail of 8 was retained.
func TestStructuralPreparationHonorsRecentTailDefault(t *testing.T) {
	principal, binding := managerDefaultsPrincipal(t)
	messages := managerDefaultsHistory()
	cost, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	prep, err := (StructuralPreparationManager{RecentTail: 2, OutputReserve: 64}).Prepare(context.Background(), PrepareInput{
		Messages: messages, Budget: cost * 10, Force: true,
		Principal: principal, Binding: binding, Revision: contextstate.NewRevision(1, 1, 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !prep.Compacted {
		t.Fatal("forced preparation was not compacted")
	}
	if len(prep.Messages) != 4 {
		t.Fatalf("retained %d messages, want 4 (system + objective + 2 tail): %#v", len(prep.Messages), prep.Messages)
	}
	if !containsPlannerMessage(prep.Messages, provider.RoleAssistant, "older answer "+strings.Repeat("x", 8)) {
		t.Fatal("newest optional tail message was dropped")
	}
	if !containsPlannerMessage(prep.Messages, provider.RoleAssistant, "older answer "+strings.Repeat("x", 7)) {
		t.Fatal("second-newest optional tail message was dropped")
	}
	if containsPlannerMessage(prep.Messages, provider.RoleAssistant, "older answer "+strings.Repeat("x", 1)) {
		t.Fatal("old tail message survived a RecentTail cap of 2")
	}
}

// TestStructuralPreparationOutputReserveChangesIdempotencyKey pins the second
// half of the same defect: the manager's OutputReserve default feeds the plan
// fingerprint. Two managers with identical retained sets but different
// OutputReserve defaults must mint different idempotency keys; before the fix
// both ignored the field and minted identical keys.
func TestStructuralPreparationOutputReserveChangesIdempotencyKey(t *testing.T) {
	principal, binding := managerDefaultsPrincipal(t)
	messages := managerDefaultsHistory()
	cost, err := provider.EstimatePromptCost(messages, nil, provider.ContextAccountingProfile{})
	if err != nil {
		t.Fatal(err)
	}
	base := PrepareInput{
		Messages: messages, Budget: cost * 10, Force: true,
		Principal: principal, Binding: binding, Revision: contextstate.NewRevision(1, 1, 1),
	}
	withReserve, err := (StructuralPreparationManager{RecentTail: 2, OutputReserve: 64}).Prepare(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	withoutReserve, err := (StructuralPreparationManager{RecentTail: 2}).Prepare(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if withReserve.Token.IdempotencyKey == "" || withoutReserve.Token.IdempotencyKey == "" {
		t.Fatal("empty idempotency key")
	}
	if withReserve.Token.IdempotencyKey == withoutReserve.Token.IdempotencyKey {
		t.Fatal("idempotency key ignored the manager OutputReserve default")
	}
}

package chat

import (
	"slices"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// The operator-facing minimum for [tools] batch_result_budget_bytes IS the
// agent loop's degrade floor. If the two ever drift apart, config would accept
// a budget the loop cannot honour (or reject one it could) - and neither
// package can see the other's constant, so this is the only place the
// agreement can be pinned.
func TestBatchResultBudgetFloorMatchesTheLoopDegradeFloor(t *testing.T) {
	if config.MinBatchResultBudgetBytes != agent.BatchDegradeFloorBytes {
		t.Fatalf("config floor %d != loop degrade floor %d",
			config.MinBatchResultBudgetBytes, agent.BatchDegradeFloorBytes)
	}
}

// A session built from config carries the knob to the agent loop. Without this
// the whole mechanism is unreachable from a config file, which is the only way
// an operator can turn it on. The config field is a *int, so the literal must
// be pinned through a pointer exactly as resolveToolsConfig would hand it to
// the session.
func TestNewSessionCarriesTheBatchResultBudgetFromConfig(t *testing.T) {
	budget := 512 << 10
	res := &config.Resolved{
		ProviderName: "test", Model: "test-model",
		Tools: config.ToolsConfig{BatchResultBudgetBytes: &budget},
	}
	sess := NewSession(res, nil)
	if sess.BatchResultBudgetBytes != 512<<10 {
		t.Fatalf("session batch budget = %d, want %d", sess.BatchResultBudgetBytes, 512<<10)
	}
}

// A hand-built Resolved (like the in-package session fixtures that bypass
// config.Load) leaves BatchResultBudgetBytes nil. The session must resolve the
// absent key to the derived budget sentinel - never panic on the nil pointer
// and never fall back to "off" (0). This pins the nil-safe batchResultBudget
// helper.
func TestNewSessionBatchResultBudgetDefaultsToDerivedWhenUnset(t *testing.T) {
	sess := NewSession(&config.Resolved{ProviderName: "test", Model: "test-model"}, nil)
	if sess.BatchResultBudgetBytes != config.BatchResultBudgetDerived {
		t.Fatalf("session batch budget = %d, want derived %d",
			sess.BatchResultBudgetBytes, config.BatchResultBudgetDerived)
	}
}

// An explicit 0 is "off" and must survive the nil-safe resolution untouched:
// only an ABSENT key derives; an operator who deliberately disabled the budget
// gets a session that never degrades batches.
func TestNewSessionBatchResultBudgetExplicitZeroIsOff(t *testing.T) {
	zero := 0
	res := &config.Resolved{
		ProviderName: "test", Model: "test-model",
		Tools: config.ToolsConfig{BatchResultBudgetBytes: &zero},
	}
	sess := NewSession(res, nil)
	if sess.BatchResultBudgetBytes != 0 {
		t.Fatalf("session batch budget = %d, want 0 (off)", sess.BatchResultBudgetBytes)
	}
}

// TestNewSessionCarriesRefOnlyToolsFromConfig is the RefOnlyTools mirror of
// TestNewSessionCarriesTheBatchResultBudgetFromConfig: the knob lives on
// config.ToolsConfig (resolved from [tools] ref_only_tools by config.Load, the
// same struct that carries batch_result_budget_bytes) and NewSession copies it
// onto the session verbatim, in order, so the loop can spool those tools'
// results to references instead of inlining them.
func TestNewSessionCarriesRefOnlyToolsFromConfig(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "test", Model: "test-model",
		Tools: config.ToolsConfig{RefOnlyTools: []string{"read_file", "grep"}},
	}
	sess := NewSession(res, nil)
	if !slices.Equal(sess.RefOnlyTools, []string{"read_file", "grep"}) {
		t.Fatalf("session ref-only tools = %v, want [read_file grep]", sess.RefOnlyTools)
	}
}

// sendAgent copies exactly snapshot.batchResultBudget and snapshot.refOnlyTools
// into the agent.Options a real turn runs on (session.go), so pinning the
// beginAgentTurn snapshot values pins the loop options end-to-end: config ->
// session -> snapshot -> agent.Options. The snapshot must equal the session's
// own values - that is what makes the agent.Options handed to the loop carry
// RefOnlyTools == the session's value. Uses the in-package session/snapshot
// pattern from the turn-fence tests.
func TestWiringLoopOptionsCarriesBatchResultBudgetAndRefOnlyTools(t *testing.T) {
	budget := 512 << 10
	wantRefOnly := []string{"read_file", "grep"}
	res := &config.Resolved{
		ProviderName: "test", Model: "test-model",
		Tools: config.ToolsConfig{
			BatchResultBudgetBytes: &budget,
			RefOnlyTools:           wantRefOnly,
		},
	}
	sess := NewSession(res, &fakeCompleter{out: "ok"})
	if !slices.Equal(sess.RefOnlyTools, wantRefOnly) {
		t.Fatalf("session ref-only tools = %v, want %v", sess.RefOnlyTools, wantRefOnly)
	}
	snapshot, done, err := sess.beginAgentTurn("probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if snapshot.batchResultBudget != 512<<10 {
		t.Fatalf("snapshot batch budget = %d, want %d", snapshot.batchResultBudget, 512<<10)
	}
	if snapshot.batchResultBudget != sess.BatchResultBudgetBytes {
		t.Fatalf("snapshot batch budget = %d, want session value %d", snapshot.batchResultBudget, sess.BatchResultBudgetBytes)
	}
	if !slices.Equal(snapshot.refOnlyTools, sess.RefOnlyTools) {
		t.Fatalf("snapshot ref-only tools = %v, want session value %v", snapshot.refOnlyTools, sess.RefOnlyTools)
	}
}

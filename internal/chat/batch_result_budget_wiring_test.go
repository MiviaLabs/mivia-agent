package chat

import (
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
// an operator can turn it on.
func TestNewSessionCarriesTheBatchBudgetFromConfig(t *testing.T) {
	res := &config.Resolved{
		ProviderName: "test", Model: "test-model",
		Tools: config.ToolsConfig{BatchResultBudgetBytes: 512 << 10},
	}
	sess := NewSession(res, nil)
	if sess.BatchResultBudgetBytes != 512<<10 {
		t.Fatalf("session batch budget = %d, want %d", sess.BatchResultBudgetBytes, 512<<10)
	}
}

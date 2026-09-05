package uiadapter

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/agents"
	"github.com/MiviaLabs/mivia-agent/internal/chat"
	"github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/events"
	"github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// A worktree session's registry is built through BuildToolsForRoot, which
// passed nil for both the session bus and the session's ledger repository.
// The ledger repo is what stamps child runs with the owner the access gate
// compares, so a workflow started in a worktree session registered no child
// runs and that session's own inspect/cancel tools could not see the runs it
// had just started - a long workflow, unstoppable and uninspectable, with the
// only signal a stderr log line the TUI never shows.
func TestWorktreeRegistryCarriesSessionWorkflowWiring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	type wiring struct {
		bus  func() *events.Bus
		repo ledger.LedgerRepository
	}
	seen := map[string]wiring{}
	prev := cliagents.WireWorkflowToolOptionsVar
	cliagents.WireWorkflowToolOptionsVar = func(_ *tools.DefaultOptions, root string, _ *config.Resolved, bus func() *events.Bus, _, _ bool, repo ledger.LedgerRepository) {
		seen[root] = wiring{bus: bus, repo: repo}
	}
	t.Cleanup(func() { cliagents.WireWorkflowToolOptionsVar = prev })

	res := &config.Resolved{ProviderName: "fake", Model: "m1"}
	launch := chat.NewSession(res, nil)
	launch.SessionID = "launch"
	launch.EventBus = events.New()
	launch.Tools = baseRegistryAt(t, t.TempDir(), false)
	repo := ledger.NewMemoryLedgerRepository()
	state := &cliagents.AgentSessionState{Registry: agents.NewRegistry(), WorkspaceRoot: t.TempDir(), LedgerRepo: repo}
	pool := NewSessionPool(launch, res, state, true)
	t.Cleanup(pool.CloseAll)

	wt := otherRoot(t, t.TempDir())
	if _, err := pool.CreateFreshInDir(nil, wt); err != nil {
		t.Fatalf("CreateFreshInDir: %v", err)
	}
	got, ok := seen[wt]
	if !ok {
		t.Fatalf("no workflow wiring for the worktree root %q: saw %v", wt, seen)
	}
	if got.repo != repo {
		t.Errorf("worktree workflow wiring carries repo %v, want the session's %v - child runs are registered against the owner the access gate compares", got.repo, repo)
	}
	if got.bus == nil || got.bus() != launch.EventBus {
		t.Error("worktree workflow wiring carries no session event bus, so a run started there publishes no progress")
	}
}

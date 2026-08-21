package controller

import (
	"context"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/definition"
	workflowledger "github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// storeContentCountingRepository counts StoreContent calls so a test can
// prove no content was written before an error is returned.
type storeContentCountingRepository struct {
	workflowledger.Repository
	storeContentCalls int
}

func (r *storeContentCountingRepository) StoreContent(ctx context.Context, ref string, data []byte) error {
	r.storeContentCalls++
	return r.Repository.StoreContent(ctx, ref, data)
}

// Architecture-review regression: buildPanelTaskSpec must validate its
// input/output schemas before storing any content. A refactor briefly
// inverted this order (store first, validate after), so a malformed schema
// on an otherwise-admitted panel step would write panel content to the
// repository before failing - fail-fast-before-side-effects, not
// side-effects-then-fail.
func TestBuildPanelTaskSpecValidatesSchemasBeforeStoringContent(t *testing.T) {
	counting := &storeContentCountingRepository{Repository: workflowledger.NewMemoryRepository()}
	ctrl, err := newTestPanelWorkSpecController(t, counting)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctrl.buildPanelTaskSpec(context.Background(), panelWorkSpecParams{
		RunID: "run-1", TaskID: "task-1", AgentName: "panel-reviewer", AgentDigest: "sha256:" + fortyByteHexPad("a"),
		Skill: "bug-audit", Provider: "deepseek", Model: "deepseek-v4-flash",
		Input: []byte(`"prompt"`), InputSchema: []byte(`{"type":"string"}`), OutputSchema: []byte(`not valid json`),
		Deadline: time.Now().Add(time.Hour), Limits: runtime.WorkLimits{MaxTurns: 1, MaxPromptTokens: 1, MaxOutputTokens: 1, MaxOutputPerCall: 1, MaxToolCalls: 1},
	})
	if err == nil {
		t.Fatal("buildPanelTaskSpec() error = nil, want a schema validation error")
	}
	if counting.storeContentCalls != 0 {
		t.Fatalf("StoreContent was called %d times before the schema error, want 0", counting.storeContentCalls)
	}
}

func newTestPanelWorkSpecController(t *testing.T, repo workflowledger.Repository) (*LinearController, error) {
	t.Helper()
	snapshot, err := workflowledger.MarshalSnapshot(workflowledger.Snapshot{})
	if err != nil {
		return nil, err
	}
	return NewLinearController(repo, &linearRunner{}, &definition.CompiledWorkflow{}, nil, nil, "wfr-panel-work-spec", snapshot)
}

func fortyByteHexPad(s string) string {
	for len(s) < 64 {
		s += "0"
	}
	return s
}

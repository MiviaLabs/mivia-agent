package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	coordledger "github.com/MiviaLabs/mivia-agent/internal/ledger"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// Test fixtures for the panel coordinator. They mirror the ledger package's
// own panel fixtures: the coordinator needs attempts whose referenced
// content digests and coordinator fingerprints verify, so building them
// requires the real fingerprint machinery.

func panelDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validPanelTask(name string) ledger.PanelTaskSpec {
	deadline := time.Now().Add(time.Minute).UTC()
	input := fmt.Sprintf(`{"input":%q}`, name)
	work := ledger.PanelTaskSpec{
		TaskName: name, InputRef: "ref:input:" + name, InputDigest: panelDigest(input), InputSchemaRef: "ref:input-schema:" + name, InputSchemaDigest: panelDigest(`{}`),
		Budget: 1, Scope: "panel", AgentName: "agent", AgentDigest: panelDigest("agent"), Skill: "skill", Provider: "provider", Model: "model",
		OutputSchemaDigest: panelDigest(`{}`), OutputSchemaRef: "ref:output-schema:" + name, Timeout: time.Second, DeadlineAt: deadline,
		WorkLimits:                    ledger.PanelWorkLimits{MaxTurns: 1, MaxPromptTokens: 1, MaxOutputTokens: 1, MaxOutputPerCall: 1, MaxToolCalls: 1, DeadlineAt: deadline},
		Policy:                        coordledger.RunPolicy{NoRetry: true, FailInterrupted: true},
		CoordinatorRequestFingerprint: "sha256:" + panelDigest("request:"+name),
	}
	work.WorkFingerprint = work.WorkFingerprintValue()
	return work
}

func panelTaskWithID(t *testing.T, name, taskID string) ledger.PanelTaskSpec {
	t.Helper()
	work := validPanelTask(name)
	input := fmt.Sprintf(`{"input":%q}`, name)
	fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{{ID: taskID, Name: work.TaskName, Input: []byte(input), InputSchema: map[string]any{}, OutputSchema: map[string]any{}, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model, WorkLimits: work.WorkLimits, DisableProviderReplay: true}}, work.Policy)
	if err != nil {
		t.Fatal(err)
	}
	work.CoordinatorRequestFingerprint = fingerprint
	work.WorkFingerprint = work.WorkFingerprintValue()
	return work
}

func storePanelTask(t *testing.T, repo *ledger.StorageRepository, work ledger.PanelTaskSpec) {
	t.Helper()
	input := fmt.Sprintf(`{"input":%q}`, work.TaskName)
	for _, item := range []struct{ ref, data string }{{work.InputRef, input}, {work.InputSchemaRef, `{}`}, {work.OutputSchemaRef, `{}`}} {
		if err := repo.StoreContent(context.Background(), item.ref, []byte(item.data)); err != nil {
			t.Fatal(err)
		}
	}
}

func storePanelExecution(t *testing.T, repo *ledger.StorageRepository, panel *ledger.PanelExecution) {
	t.Helper()
	for _, member := range panel.Members {
		storePanelTask(t, repo, member.Work)
	}
}

func validPanelExecution(t *testing.T, runID, attemptID string) *ledger.PanelExecution {
	members := make([]ledger.PanelMemberExecution, 2)
	for i, memberID := range []string{"member-0", "member-1"} {
		childRun, childTask := ledger.PanelChildIDs(runID, attemptID, memberID)
		members[i] = ledger.PanelMemberExecution{MemberID: memberID, CoordinatorRunID: childRun, TaskID: childTask, Work: panelTaskWithID(t, memberID, childTask), Order: i}
	}
	synthesisRun, synthesisTask := ledger.PanelChildIDs(runID, attemptID, "synthesis")
	return &ledger.PanelExecution{Phase: ledger.PanelPhaseMembersAdmitted, Members: members, SynthesisRunID: synthesisRun, SynthesisTaskID: synthesisTask}
}

func validSynthesisTask(t *testing.T, runID, attemptID string) ledger.PanelTaskSpec {
	_, taskID := ledger.PanelChildIDs(runID, attemptID, "synthesis")
	return panelTaskWithID(t, "synthesis", taskID)
}

func newMemoryRepo(t *testing.T) *ledger.StorageRepository {
	t.Helper()
	return ledger.NewMemoryRepository()
}

func runID(t *testing.T) string {
	t.Helper()
	return "wfr-" + t.Name()
}

func newRun(t *testing.T, run string) (ledger.RunSnapshot, []byte) {
	t.Helper()
	raw, err := ledger.MarshalSnapshot(ledger.Snapshot{SchemaVersion: 1, DefinitionTOML: []byte("x"), DefinitionDigest: "d"})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return ledger.RunSnapshot{RunID: run, WorkflowName: "test-wf", Status: ledger.RunStatusPending, ActiveStepID: "start"}, raw
}

package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

// testPanelContentValidator mirrors panel.ValidateTaskContent
// (internal/workflows/panel): the ledger's own tests cannot import that
// package (the panel package imports ledger, so an in-package test import
// would cycle). If the two implementations drift, the panel package's
// ValidateTaskContent tests and these persistence tests diverge - keep the
// bodies identical.
func testPanelContentValidator(ctx context.Context, repo Repository, workflowRunID, taskID string, work PanelTaskSpec) error {
	if err := work.Validate(); err != nil {
		return err
	}
	content := make([][]byte, 3)
	for i, item := range []struct{ ref, digest string }{{work.InputRef, work.InputDigest}, {work.InputSchemaRef, work.InputSchemaDigest}, {work.OutputSchemaRef, work.OutputSchemaDigest}} {
		data, err := repo.LoadContent(ctx, item.ref)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != item.digest {
			return ErrConflict
		}
		content[i] = data
	}
	var inputSchema, outputSchema map[string]any
	if json.Unmarshal(content[1], &inputSchema) != nil || json.Unmarshal(content[2], &outputSchema) != nil {
		return ErrConflict
	}
	compiled, err := jschema.Compile(inputSchema)
	if err != nil {
		return ErrConflict
	}
	if _, err := compiled.ValidateJSONBytes(content[0]); err != nil {
		return ErrConflict
	}
	task := subagents.Task{ID: taskID, Name: work.TaskName, Input: json.RawMessage(content[0]), InputSchema: inputSchema, OutputSchema: outputSchema, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model, WorkLimits: work.WorkLimits, DisableProviderReplay: true, SessionID: workflowRunID}
	fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{task}, work.Policy)
	if err != nil || fingerprint != work.CoordinatorRequestFingerprint {
		return ErrConflict
	}
	return nil
}

func init() {
	SetPanelContentValidator(testPanelContentValidator)
}

// TestPanelAttemptFailsClosedWithoutValidator pins the fail-closed default:
// with no registered validator the repository refuses every panel attempt.
func TestPanelAttemptFailsClosedWithoutValidator(t *testing.T) {
	SetPanelContentValidator(nil)
	defer SetPanelContentValidator(testPanelContentValidator)

	repo := newMemoryRepo(t)
	ctx := context.Background()
	run := runID(t)
	snap, raw := newRun(t, run)
	if err := repo.CreateRun(ctx, snap, raw); err != nil {
		t.Fatal(err)
	}
	attempt := StepAttempt{AttemptID: "attempt", RunID: run, StepID: "panel", AttemptNo: 1, PanelExecution: validPanelExecution(t, run, "attempt")}
	storePanelExecution(t, repo, attempt.PanelExecution)
	if err := repo.CreateStepAttempt(ctx, attempt); err == nil {
		t.Fatal("panel attempt without a registered validator must be refused")
	}
}

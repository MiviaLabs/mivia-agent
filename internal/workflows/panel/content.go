// Package panel binds panel child operations to persisted panel state.
// The durable panel types (PanelTaskSpec, PanelExecution, phases, child IDs)
// live in internal/workflows/ledger; this package owns the coordinator that
// admits, joins, resumes and cancels panel children against that state.
package panel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
	"github.com/MiviaLabs/mivia-agent/internal/workflows/ledger"
)

// ValidateTaskContent reconstructs the same subagents.Task fingerprint
// PanelCoordinator.request produces at creation. workflowRunID must match
// what request used as SessionID (the panel's owning run id, threaded there
// as PanelCoordinator.workflowRunID) or every validation here mismatches and
// fails closed with ledger.ErrConflict even for an untampered task.
func ValidateTaskContent(ctx context.Context, repo ledger.Repository, workflowRunID, taskID string, work ledger.PanelTaskSpec) error {
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
			return ledger.ErrConflict
		}
		content[i] = data
	}
	var inputSchema, outputSchema map[string]any
	if json.Unmarshal(content[1], &inputSchema) != nil || json.Unmarshal(content[2], &outputSchema) != nil {
		return ledger.ErrConflict
	}
	compiled, err := jschema.Compile(inputSchema)
	if err != nil {
		return ledger.ErrConflict
	}
	if _, err := compiled.ValidateJSONBytes(content[0]); err != nil {
		return ledger.ErrConflict
	}
	task := subagents.Task{ID: taskID, Name: work.TaskName, Input: json.RawMessage(content[0]), InputSchema: inputSchema, OutputSchema: outputSchema, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model, WorkLimits: work.WorkLimits, DisableProviderReplay: true, SessionID: workflowRunID}
	fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{task}, work.Policy)
	if err != nil || fingerprint != work.CoordinatorRequestFingerprint {
		return ledger.ErrConflict
	}
	return nil
}

// The ledger's persistence layer refuses panel attempts unless a content
// validator is registered; this init() supplies the real one for every
// binary that links the panel package (all of them link the controller,
// which constructs PanelCoordinator).
func init() {
	ledger.SetPanelContentValidator(ValidateTaskContent)
}

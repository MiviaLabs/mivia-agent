package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/subagents"
)

func (s *StorageRepository) validateInitialPanelAttempt(ctx context.Context, attempt StepAttempt) error {
	if err := attempt.PanelExecution.validateInitial(attempt.RunID, attempt.AttemptID); err != nil {
		return err
	}
	if attempt.PanelExecution == nil {
		return nil
	}
	for _, member := range attempt.PanelExecution.Members {
		if err := s.validatePanelTaskContent(ctx, attempt.RunID, member.TaskID, member.Work); err != nil {
			return err
		}
	}
	return nil
}

// validatePanelTaskContent reconstructs the same subagents.Task fingerprint
// PanelCoordinator.request produced at creation. workflowRunID must match
// what request used as SessionID (the panel's owning run id, threaded there
// as PanelCoordinator.workflowRunID) or every validation here mismatches and
// fails closed with ErrConflict even for an untampered task.
func (s *StorageRepository) validatePanelTaskContent(ctx context.Context, workflowRunID, taskID string, work PanelTaskSpec) error {
	if err := work.Validate(); err != nil {
		return err
	}
	content := make([][]byte, 3)
	for i, item := range []struct{ ref, digest string }{{work.InputRef, work.InputDigest}, {work.InputSchemaRef, work.InputSchemaDigest}, {work.OutputSchemaRef, work.OutputSchemaDigest}} {
		data, err := s.LoadContent(ctx, item.ref)
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

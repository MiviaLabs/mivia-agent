package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/MiviaLabs/mivia-agent/internal/coordinator"
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
		if err := s.validatePanelTaskContent(ctx, member.TaskID, member.Work); err != nil {
			return err
		}
	}
	return nil
}

func (s *StorageRepository) validatePanelTaskContent(ctx context.Context, taskID string, work PanelTaskSpec) error {
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
	task := subagents.Task{ID: taskID, Name: work.TaskName, Input: json.RawMessage(content[0]), InputSchema: inputSchema, OutputSchema: outputSchema, Timeout: work.Timeout, Budget: work.Budget, Scope: work.Scope, AgentName: work.AgentName, AgentDigest: work.AgentDigest, Skill: work.Skill, ProviderName: work.Provider, Model: work.Model}
	fingerprint, err := coordinator.RequestFingerprint([]subagents.Task{task}, work.Policy)
	if err != nil || fingerprint != work.CoordinatorRequestFingerprint {
		return ErrConflict
	}
	return nil
}

package contextmgr

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// planCompact runs elision, retention, costing, and checkpoint fingerprinting
// for a request that has already crossed the compaction trigger (or Force).
func planCompact(input PlanInput, result PlanResult, rng contextstate.SourceRange, target int) (PlanResult, error) {
	objective, objectiveIndex, err := currentObjective(input.Messages, input.CurrentObjective)
	if err != nil {
		return PlanResult{}, err
	}
	// One private clone for elision, selection, costing, fingerprinting, and
	// the returned messages. Input.Messages is never mutated.
	working := cloneMessages(input.Messages)
	mandatory := mandatoryIndexes(working, objectiveIndex)
	working, elision := elideToolResults(working, objectiveIndex, mandatory)
	planInput := input
	planInput.Messages = working
	retained, err := retainMessages(planInput, objective, objectiveIndex, target)
	if err != nil {
		return PlanResult{}, err
	}
	after, err := estimatePromptCost(retained, input.Tools)
	if err != nil {
		return PlanResult{}, invalidPlan("request_cost", err.Error())
	}
	// No budget re-check here: retainMessages already rejects a mandatory set
	// that exceeds the budget, and everything it adds after that is capped at
	// target (half the budget).
	after = applyCalibration(after, input.CalibrationRatio)
	key, err := planIdempotencyKey(input, rng, target, retained)
	if err != nil {
		return PlanResult{}, err
	}
	active, err := marshalCanonical(retained)
	if err != nil {
		return PlanResult{}, err
	}
	result.Messages = cloneMessages(retained)
	result.Candidate = CheckpointCandidate{
		ActiveContext: active,
		SourceEvents:  append([]contextstate.SourceEvent(nil), input.SourceEvents...),
		SourceRange:   rng,
	}
	result.AfterTokens = after
	result.Compacted = true
	result.ElidedMessages = elision.Messages
	result.ElidedBytes = elision.Bytes
	result.IdempotencyKey = key
	return result, nil
}

// elisionContentMinBytes is the exclusive lower bound on tool-result body
// size. Bodies of this length or shorter are never replaced.
const elisionContentMinBytes = 2048

// ElisionStats is a content-free aggregate of tool-result replacements made
// during one compaction plan. Bytes is the sum of original Content lengths.
type ElisionStats struct {
	Messages int
	Bytes    int
}

// mandatoryIndexes returns the message indexes that structural retention must
// keep whole: optional system at index 0, the current user objective, and the
// latest complete assistant+tool unit. Shared by elision eligibility and
// retainMessages so the two cannot drift.
func mandatoryIndexes(messages []provider.Message, objectiveIndex int) map[int]struct{} {
	mandatory := make(map[int]struct{}, len(messages))
	if len(messages) > 0 && messages[0].Role == provider.RoleSystem {
		mandatory[0] = struct{}{}
	}
	if objectiveIndex >= 0 && objectiveIndex < len(messages) {
		mandatory[objectiveIndex] = struct{}{}
	}
	markLatestToolUnit(messageUnits(messages), messages, mandatory)
	return mandatory
}

// Test seams for defensive error paths that are hard to reach through Plan's
// outer validation (same tools already priced before planCompact runs).
var (
	messageTokenCost   = provider.MessageTokens
	estimatePromptCost = provider.EstimatePromptCost
	marshalCanonical   = func(v any) ([]byte, error) { return contextstate.MarshalCanonical(v) }
)

// elideToolResults replaces eligible prior-turn oversized tool-result bodies
// with a host-authored notice. messages must already be a private clone; the
// function mutates Content in place and never changes Role, ToolCallID, Name,
// or ToolCalls. A candidate is skipped when the notice is not strictly cheaper
// by messageTokenCost.
func elideToolResults(messages []provider.Message, objectiveIndex int, mandatory map[int]struct{}) ([]provider.Message, ElisionStats) {
	var stats ElisionStats
	for index := range messages {
		if messages[index].Role != provider.RoleTool {
			continue
		}
		if index >= objectiveIndex {
			continue
		}
		if _, ok := mandatory[index]; ok {
			continue
		}
		originalLen := len(messages[index].Content)
		if originalLen <= elisionContentMinBytes {
			continue
		}
		notice := elisionNotice(originalLen)
		candidate := messages[index]
		candidate.Content = notice
		if messageTokenCost(candidate) >= messageTokenCost(messages[index]) {
			continue
		}
		messages[index].Content = notice
		stats.Messages++
		stats.Bytes += originalLen
	}
	return messages, stats
}

// elisionNotice is a constant-format, non-imperative host notice. It never
// includes digests, excerpts, tool names, or arguments.
func elisionNotice(originalBytes int) string {
	return fmt.Sprintf("[context elided prior tool result; original size about %s]", sizeBucketLabel(originalBytes))
}

// sizeBucketLabel rounds n up to the next power of two and renders it as
// KiB or MiB (powers of 1024).
func sizeBucketLabel(n int) string {
	if n <= 0 {
		return "0 KiB"
	}
	bucket := ceilPowerOfTwo(n)
	const (
		kib = 1024
		mib = 1024 * 1024
	)
	if bucket >= mib {
		return fmt.Sprintf("%d MiB", bucket/mib)
	}
	// For sub-KiB powers of two still express as KiB fractions? Plan only
	// requires KiB/MiB for the elision notice; eligible bodies are > 2 KiB.
	if bucket < kib {
		return "1 KiB"
	}
	return fmt.Sprintf("%d KiB", bucket/kib)
}

func ceilPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		// Cap before shift overflow on large inputs.
		if p >= 1<<30 {
			return p
		}
		p <<= 1
	}
	return p
}

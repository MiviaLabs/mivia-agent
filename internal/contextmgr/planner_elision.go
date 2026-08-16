package contextmgr

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

// planCompact runs elision, retention, costing, and checkpoint fingerprinting
// for a request that has already crossed the compaction trigger (or Force).
func planCompact(input PlanInput, result PlanResult, rng contextstate.SourceRange, target, schemaCost int) (PlanResult, error) {
	objective, objectiveIndex, err := currentObjective(input.Messages, input.CurrentObjective)
	if err != nil {
		return PlanResult{}, err
	}
	// One private clone for elision, selection, costing, fingerprinting, and
	// the returned messages. Input.Messages is never mutated.
	working := cloneMessages(input.Messages)
	mandatory := mandatoryIndexes(working, objectiveIndex, input.PreserveNames)
	working, elision, reasoningElision, deferred := elideForCompaction(working, objectiveIndex, mandatory, input.Spool, input.Principal)
	planInput := input
	planInput.Messages = working
	retained, retainedIndexes, err := retainMessages(planInput, objective, objectiveIndex, target, schemaCost)
	if err != nil {
		return PlanResult{}, err
	}
	// H-1-RESIDUAL: reject an invalid caller-supplied idempotency key BEFORE
	// the first spool write. planIdempotencyKey can fail on this plan only
	// when input.IdempotencyKey is non-empty and fails validatePlanKey (the
	// derived path marshals plain strings and cannot fail), and a failure
	// there happens AFTER installRetainedElisionRefs has spooled retained
	// bodies - bytes no retained message names and no production cleanup path
	// reaches. Pre-validating here keeps "no spool writes on a failed plan"
	// total. planIdempotencyKey re-validates on its own path, so this is an
	// ordering guard, not a second rule.
	if input.IdempotencyKey != "" {
		if err := validatePlanKey(input.IdempotencyKey); err != nil {
			return PlanResult{}, err
		}
	}
	// H-1: spool elided bodies only after retention has decided what survives.
	// Elision above installs only the plain notice (feeding the cost decision
	// and retention math); a body that retention drops must never be written
	// to the store, because an unref'd spooled body is unreachable and has no
	// production cleanup path. The ref-naming notice is installed before the
	// idempotency fingerprint, so refs stay deterministic and the key is
	// unchanged from the pre-fix pipeline.
	retained = installRetainedElisionRefs(retained, retainedIndexes, deferred, input.Spool, input.Principal)
	// No budget re-check here: retainMessages already rejects a mandatory set
	// that exceeds the budget, and everything it adds after that is capped at
	// target (half the budget).
	after := applyCalibration(provider.EstimateMessagesPromptCost(retained, schemaCost, input.ContextAccounting), input.CalibrationRatio)
	key, err := planIdempotencyKey(input, rng, target, retained)
	if err != nil {
		return PlanResult{}, err
	}
	// Candidate ActiveContext bytes are durable, operator-visible state:
	// redact reasoning before they are marshaled (identity without an
	// installed policy), matching the non-compacted structural path.
	// plan.Messages itself stays raw for replay.
	active, err := marshalCanonical(redactReasoningMessages(retained))
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
	result.ElidedReasoningMessages = reasoningElision.Messages
	result.ElidedReasoningBytes = reasoningElision.Bytes
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
// keep whole: optional system at index 0, the current user objective, every
// message whose Name is in preserveNames (host-owned frames the caller
// declared session surface), and the latest complete assistant+tool unit.
// Shared by elision eligibility and retainMessages so the two cannot drift.
func mandatoryIndexes(messages []provider.Message, objectiveIndex int, preserveNames []string) map[int]struct{} {
	mandatory := make(map[int]struct{}, len(messages))
	if len(messages) > 0 && messages[0].Role == provider.RoleSystem {
		mandatory[0] = struct{}{}
	}
	for index, message := range messages {
		if message.Name != "" && containsPreservedName(message.Name, preserveNames) {
			mandatory[index] = struct{}{}
		}
	}
	if objectiveIndex >= 0 && objectiveIndex < len(messages) {
		mandatory[objectiveIndex] = struct{}{}
	}
	markLatestToolUnit(messageUnits(messages), messages, mandatory)
	return mandatory
}

// containsPreservedName reports whether name is listed in preserveNames.
// Empty names never match: an unnamed message is ordinary conversation
// history and stays subject to the recent-tail retention.
func containsPreservedName(name string, preserveNames []string) bool {
	if name == "" {
		return false
	}
	for _, candidate := range preserveNames {
		if name == candidate {
			return true
		}
	}
	return false
}

// Test seams for defensive error paths that are hard to reach through the
// public API (estimateToolSchemaCost, for one, fails only on a ToolSpec that
// cannot be marshalled, which callers cannot construct).
var (
	messageTokenCost       = provider.MessageTokens
	estimateToolSchemaCost = provider.EstimateToolSchemaCost
	marshalCanonical       = func(v any) ([]byte, error) { return contextstate.MarshalCanonical(v) }
)

// reasoningElisionMarker replaces stale assistant ReasoningContent on the
// compaction path. The marker must NOT be the empty string: a provider's
// documented-400 repair (provider.RepairReasoningLessToolExchanges, active when
// RejectReasoningLessToolTurns is set) wire-drops a non-terminal assistant
// tool-call turn with empty reasoning TOGETHER with its tool results. A
// non-empty marker keeps those retained exchanges on the wire.
const reasoningElisionMarker = "[reasoning elided by context compaction]"

// elideForCompaction runs the two in-place elision passes for one compaction
// plan: oversized prior tool-result bodies, then stale assistant reasoning.
// planCompact and the fuzz replica (optionalTailIsSuffix) both call this
// helper, so the pipelines cannot drift. messages must already be a private
// clone.
func elideForCompaction(messages []provider.Message, objectiveIndex int, mandatory map[int]struct{}, spool *remainder.Spool, principal contextstate.Principal) ([]provider.Message, ElisionStats, ElisionStats, []deferredElision) {
	messages, toolStats, deferred := elideToolResultsWithSpool(messages, objectiveIndex, mandatory, spool, principal)
	reasoningStats := elideStaleReasoning(messages, objectiveIndex, mandatory)
	return messages, toolStats, reasoningStats, deferred
}

// elideStaleReasoning replaces the ReasoningContent of prior-turn assistant
// messages with reasoningElisionMarker. messages must already be a private
// clone; the function mutates ReasoningContent in place and never changes
// Role, Content, ToolCallID, Name, or ToolCalls. It skips the current
// objective and later messages, mandatory messages, empty reasoning, and
// already-marked reasoning (so re-planning is idempotent). A candidate is
// skipped when the marker is not strictly cheaper by messageTokenCost.
// Bytes is the sum of original ReasoningContent lengths.
func elideStaleReasoning(messages []provider.Message, objectiveIndex int, mandatory map[int]struct{}) ElisionStats {
	var stats ElisionStats
	for index := range messages {
		if messages[index].Role != provider.RoleAssistant {
			continue
		}
		if index >= objectiveIndex {
			continue
		}
		if _, ok := mandatory[index]; ok {
			continue
		}
		reasoning := messages[index].ReasoningContent
		if reasoning == "" || reasoning == reasoningElisionMarker {
			continue
		}
		candidate := messages[index]
		candidate.ReasoningContent = reasoningElisionMarker
		if messageTokenCost(candidate) >= messageTokenCost(messages[index]) {
			continue
		}
		messages[index].ReasoningContent = reasoningElisionMarker
		stats.Messages++
		stats.Bytes += len(reasoning)
	}
	return stats
}

// elideToolResults replaces eligible prior-turn oversized tool-result bodies
// with a plain host-authored notice. It is the spool-free compatibility seam
// (nil spool, empty principal): the fuzz invariants and threshold tests drive
// the plain form directly, and it is byte-identical to before the spool
// grant landed.
func elideToolResults(messages []provider.Message, objectiveIndex int, mandatory map[int]struct{}) ([]provider.Message, ElisionStats) {
	messages, stats, _ := elideToolResultsWithSpool(messages, objectiveIndex, mandatory, nil, contextstate.Principal{})
	return messages, stats
}

// deferredElision records an elided prior-turn tool body that MAY still be
// spooled. elideToolResultsWithSpool installs only the plain notice and
// returns the record; planCompact spools the body AFTER retention decides it
// survived (installRetainedElisionRefs), so a body that retention drops is
// never written to the store (H-1). index is the message index in the
// pre-retention working set, the same index space retainMessages' retained
// indexes use.
type deferredElision struct {
	index        int
	originalLen  int
	originalBody string
}

// elideToolResultsWithSpool replaces eligible prior-turn oversized tool-result
// bodies with the plain host-authored notice. messages must already be a
// private clone; the function mutates Content in place and never changes
// Role, ToolCallID, Name, or ToolCalls. A candidate is skipped when the notice
// is not strictly cheaper by messageTokenCost.
//
// The full body is NOT stored here. When spool is non-nil and the elision
// wins, a deferredElision record is returned so planCompact can spool the body
// after retention and name the minted ref in the notice for read_output. A nil
// spool, empty principal, nil store, or store failure yield "" at install time
// and the plain notice stays: a failed spool must never invent a ref
// (INV-AG-10).
func elideToolResultsWithSpool(messages []provider.Message, objectiveIndex int, mandatory map[int]struct{}, spool *remainder.Spool, principal contextstate.Principal) ([]provider.Message, ElisionStats, []deferredElision) {
	var stats ElisionStats
	var deferred []deferredElision
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
		originalBody := messages[index].Content
		originalLen := len(originalBody)
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
		if spool != nil {
			deferred = append(deferred, deferredElision{index: index, originalLen: originalLen, originalBody: originalBody})
		}
	}
	return messages, stats, deferred
}

// installRetainedElisionRefs spools the elided bodies that survived retention
// and swaps the plain notice for the ref-naming notice on the retained
// messages. retainedIndexes[i] is the pre-retention index of retained[i], the
// same index space deferredElision.index uses, so the pairing is exact even
// when two different messages carry identical content (refs are
// content-addressed and value matching would be unsafe). A nil spool, empty
// principal, nil store, or store failure yield "" and the plain notice stays
// (INV-AG-10: a failed spool never invents a ref).
func installRetainedElisionRefs(retained []provider.Message, retainedIndexes []int, deferred []deferredElision, spool *remainder.Spool, principal contextstate.Principal) []provider.Message {
	if len(deferred) == 0 || len(retainedIndexes) != len(retained) {
		return retained
	}
	byIndex := make(map[int]deferredElision, len(deferred))
	for _, record := range deferred {
		byIndex[record.index] = record
	}
	for position, index := range retainedIndexes {
		record, ok := byIndex[index]
		if !ok {
			continue
		}
		ref := ""
		if spool != nil {
			ref = spool.Spool(context.Background(), principal.SessionID, []byte(record.originalBody))
		}
		retained[position].Content = elisionNoticeWithRef(record.originalLen, ref)
	}
	return retained
}

// elisionNotice is the plain, spool-free host notice. It is the ref-less
// compatibility seam kept callable for the threshold and bucket tests.
func elisionNotice(originalBytes int) string {
	return elisionNoticeWithRef(originalBytes, "")
}

// elisionNoticeWithRef is a constant-format, non-imperative host notice. It
// never includes digests, excerpts, tool names, or arguments. When ref is
// non-empty the notice names the spooled remainder so the model can fetch the
// full body back with read_output; when ref is empty the notice is
// byte-identical to the plain form (nil spool, empty principal, nil store, or
// store failure: a failed spool must never invent a ref, INV-AG-10).
func elisionNoticeWithRef(originalBytes int, ref string) string {
	size := sizeBucketLabel(originalBytes)
	if ref == "" {
		return fmt.Sprintf("[context elided prior tool result; original size about %s]", size)
	}
	return fmt.Sprintf("[context elided prior tool result; original size about %s; remainder: %s — use read_output to fetch the full body]", size, ref)
}

// sizeBucketLabel rounds n up to the next power of two and renders it as
// KiB or MiB (powers of 1024). The ceiling saturates at the largest
// representable power of two (1<<(intSize-2)): for bodies above that the
// label reports the saturated bucket, because the true ceiling is not
// representable as a positive int.
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

// maxInt is the largest value representable by int — the exact stdlib
// definition of math.MaxInt (Go 1.17+), written out so 32-bit and 64-bit
// builds agree without an import.
const maxInt = int(^uint(0) >> 1)

// ceilPowerOfTwo rounds n up to the smallest power of two >= n, saturating at
// the largest representable power of two (1<<(intSize-2)) so a doubling can
// never overflow int. For n above that saturation point the true ceiling is
// not representable as a positive int, so the saturated value is returned.
func ceilPowerOfTwo(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		// Saturate at the largest representable power of two: doubling p from
		// 1<<(intSize-2) would wrap to a negative int (then to 0) and spin
		// forever, and returning a value below n would break the ceiling
		// contract.
		if p > maxInt>>1 {
			return p
		}
		p <<= 1
	}
	return p
}

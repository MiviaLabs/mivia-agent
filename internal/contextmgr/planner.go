package contextmgr

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

const (
	compactionAlgorithm       = "context-compact-v1"
	defaultRecentTailMessages = 8
	maxRecentTailMessages     = 64
)

// PlanInput contains only immutable inputs to the structural planner. The
// planner stays pure except for an explicit caller-provided Spool seam: when
// Spool is nil there are no side effects and output is byte-identical to
// before; when set, elision spools bytes through that seam only.
type PlanInput struct {
	Messages         []provider.Message
	Budget           int
	Tools            []provider.ToolSpec
	OutputReserve    int
	Force            bool
	CurrentObjective string
	SourceRange      contextstate.SourceRange
	SourceEvents     []contextstate.SourceEvent
	IdempotencyKey   string
	RecentTail       int
	// Revision is the session revision the compaction starts FROM. It is
	// part of the derived idempotency key: two compactions of the same
	// retained set off the same source range (a resumed process compacting
	// an already-compacted session again) are different operations that
	// commit different work, so sharing a key made the store reject the
	// second as a conflicting retry. Retries of the SAME compaction carry
	// the same revision and stay idempotent.
	Revision contextstate.Revision
	// PreserveNames lists provider.Message.Name values that structural
	// retention keeps whole alongside the mandatory set. The chat layer uses
	// it for the session-owned core-memory context frame so compaction never
	// drops it.
	PreserveNames []string
	// CalibrationRatio scales token estimates to correct for heuristic drift.
	// 0 means use 1.0 (no correction). Should come from a Calibration.Ratio.
	CalibrationRatio float64
	// Spool: nil = elision mints no refs (plain notices), keeping the planner free of storage side effects.
	Spool *remainder.Spool
	// Principal: the session principal that receives the remainder grant when Spool is set.
	Principal contextstate.Principal
}

// PlannerInput is a descriptive alias for callers that use the planner as a
// standalone preparation boundary.
type PlannerInput = PlanInput

// PlanResult is the deterministic structural result. Summary generation and
// durable publication happen in later seams; this value is safe to discard.
type PlanResult struct {
	Messages      []provider.Message
	Candidate     CheckpointCandidate
	BeforeTokens  int
	AfterTokens   int
	TriggerTokens int
	TargetTokens  int
	Compacted     bool
	// ElidedMessages and ElidedBytes are content-free aggregates of prior-turn
	// tool-result replacements applied on the compaction path. Both are zero
	// when the request is below the trigger or no body was eligible.
	ElidedMessages int
	ElidedBytes    int
	// ElidedReasoningMessages and ElidedReasoningBytes are content-free
	// aggregates of stale assistant reasoning replaced with
	// reasoningElisionMarker on the compaction path. Both are zero when the
	// request is below the trigger or no reasoning was eligible.
	ElidedReasoningMessages int
	ElidedReasoningBytes    int
	SourceRange             contextstate.SourceRange
	IdempotencyKey          string
}

// PlannerResult is a descriptive alias for PlanResult.
type PlannerResult = PlanResult

// Plan applies threshold/target math and strict structural retention. A
// request exactly at the trigger is compacted; a request exactly at the hard
// budget is accepted, while one token over is rejected.
func Plan(input PlanInput) (PlanResult, error) {
	if input.Budget <= 0 {
		return PlanResult{}, invalidPlan("budget", "must be positive")
	}
	if input.OutputReserve < 0 {
		return PlanResult{}, invalidPlan("output_reserve", "must not be negative")
	}
	if len(input.Messages) == 0 {
		return PlanResult{}, invalidPlan("messages", "must not be empty")
	}
	if err := validateMessageShape(input.Messages); err != nil {
		return PlanResult{}, err
	}
	// Validate RecentTail on every path, not only compaction: the same
	// out-of-range value must not be silently accepted below the trigger and
	// rejected on the compaction path (DC-9). 0 is the default-8 marker and
	// stays valid; retainMessages keeps this check as a redundant guard.
	if input.RecentTail < 0 || input.RecentTail > maxRecentTailMessages {
		return PlanResult{}, invalidPlan("recent_tail", fmt.Sprintf("must be between 0 and %d", maxRecentTailMessages))
	}
	rng, err := planSourceRange(input)
	if err != nil {
		return PlanResult{}, err
	}
	// Price the tool schemas exactly once for this plan (plan tools/05 D5).
	// Every candidate selection below is scored against the SAME tool list, so
	// re-marshaling all of them per candidate is pure waste - and with a full
	// tool catalogue the schema block dominates the marshaling cost.
	schemaCost, err := estimateToolSchemaCost(input.Tools)
	if err != nil {
		return PlanResult{}, invalidPlan("request_cost", err.Error())
	}
	before := applyCalibration(provider.EstimateMessagesPromptCost(input.Messages, schemaCost), input.CalibrationRatio)
	trigger := percentFloor(input.Budget, 4, 5)
	target := percentFloor(input.Budget, 1, 2)
	result := PlanResult{
		Messages:      cloneMessages(input.Messages),
		BeforeTokens:  before,
		AfterTokens:   before,
		TriggerTokens: trigger,
		TargetTokens:  target,
		SourceRange:   rng,
	}
	if !input.Force && before < trigger {
		return result, nil
	}
	return planCompact(input, result, rng, target, schemaCost)
}

func invalidPlan(field, reason string) error {
	return fmt.Errorf("%w: planner %s: %s", contextstate.ErrInvalidDTO, field, reason)
}

func promptOverflow(after, budget int, objective provider.Message, schemaCost int, ratio float64) error {
	objectiveCost := applyCalibration(provider.EstimateMessagesPromptCost([]provider.Message{objective}, schemaCost), ratio)
	if objectiveCost > budget {
		return fmt.Errorf("%w: current objective cost %d exceeds budget %d", contextstate.ErrPromptBudgetExceeded, objectiveCost, budget)
	}
	return fmt.Errorf("%w: retained request cost %d exceeds budget %d", contextstate.ErrPromptBudgetExceeded, after, budget)
}

func percentFloor(value, numerator, denominator int) int {
	quotient, remainder := value/denominator, value%denominator
	return quotient*numerator + remainder*numerator/denominator
}

func currentObjective(messages []provider.Message, explicit string) (provider.Message, int, error) {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role != provider.RoleUser {
			continue
		}
		if explicit != "" && messages[index].Content != explicit {
			return provider.Message{}, -1, invalidPlan("current_objective", "does not match the latest user message")
		}
		return messages[index], index, nil
	}
	return provider.Message{}, -1, fmt.Errorf("%w: planner current objective is missing", contextstate.ErrPromptBudgetExceeded)
}

func retainMessages(input PlanInput, objective provider.Message, objectiveIndex, target, schemaCost int) ([]provider.Message, error) {
	units := messageUnits(input.Messages)
	mandatory := mandatoryIndexes(input.Messages, objectiveIndex, input.PreserveNames)

	selected := make(map[int]struct{}, len(mandatory))
	for index := range mandatory {
		selected[index] = struct{}{}
	}
	selectedCost := calibratedCost(input.Messages, selected, schemaCost, input.CalibrationRatio)
	if selectedCost > input.Budget {
		return nil, promptOverflow(selectedCost, input.Budget, objective, schemaCost, input.CalibrationRatio)
	}
	tailLimit := input.RecentTail
	if tailLimit == 0 {
		tailLimit = defaultRecentTailMessages
	}
	if tailLimit < 0 || tailLimit > maxRecentTailMessages {
		return nil, invalidPlan("recent_tail", fmt.Sprintf("must be between 0 and %d", maxRecentTailMessages))
	}
	runningCost := selectedCost
	tailCount := 0
	for unitIndex := len(units) - 1; unitIndex >= 0; unitIndex-- {
		unit := units[unitIndex]
		if unitSelected(unit, selected) {
			continue
		}
		// The recent-tail cap stops the newest-to-oldest walk: an optional
		// unit that would exceed the cap is dropped along with everything
		// older, so the retained optional tail stays a contiguous suffix of
		// the newest messages (DC-6). Continuing here skipped past the
		// oversized unit and then filled OLDER units, leaving a hole in the
		// retained tail.
		if tailCount+len(unit) > tailLimit {
			break
		}
		// Estimate cost incrementally: only compute the marginal cost of
		// adding this unit rather than re-estimating the entire selection.
		unitTokens := 0
		for _, index := range unit {
			unitTokens += provider.EstimateMessageTokens(input.Messages[index])
		}
		candidateCost := runningCost + applyCalibration(unitTokens, input.CalibrationRatio)
		if candidateCost > target {
			break
		}
		for _, index := range unit {
			selected[index] = struct{}{}
		}
		runningCost = candidateCost
		tailCount += len(unit)
	}
	retained := messagesFromIndexes(input.Messages, selected)
	if err := validateMessageShape(retained); err != nil {
		return nil, err
	}
	return retained, nil
}

type messageUnit []int

func messageUnits(messages []provider.Message) []messageUnit {
	units := make([]messageUnit, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		unit := messageUnit{index}
		if messages[index].Role == provider.RoleAssistant && len(messages[index].ToolCalls) > 0 {
			pending := make(map[string]struct{}, len(messages[index].ToolCalls))
			for _, call := range messages[index].ToolCalls {
				pending[call.ID] = struct{}{}
			}
			for next := index + 1; next < len(messages) && len(pending) > 0; next++ {
				if messages[next].Role != provider.RoleTool {
					break
				}
				if _, exists := pending[messages[next].ToolCallID]; !exists {
					break
				}
				unit = append(unit, next)
				delete(pending, messages[next].ToolCallID)
			}
			index = unit[len(unit)-1]
		}
		units = append(units, unit)
	}
	return units
}

func markLatestToolUnit(units []messageUnit, messages []provider.Message, selected map[int]struct{}) {
	for unitIndex := len(units) - 1; unitIndex >= 0; unitIndex-- {
		unit := units[unitIndex]
		if messages[unit[0]].Role != provider.RoleAssistant || len(messages[unit[0]].ToolCalls) == 0 {
			continue
		}
		for _, index := range unit {
			selected[index] = struct{}{}
		}
		return
	}
}

func unitSelected(unit messageUnit, selected map[int]struct{}) bool {
	for _, index := range unit {
		if _, ok := selected[index]; ok {
			return true
		}
	}
	return false
}

func calibratedCost(messages []provider.Message, selected map[int]struct{}, schemaCost int, ratio float64) int {
	return applyCalibration(provider.EstimateMessagesPromptCost(messagesFromIndexes(messages, selected), schemaCost), ratio)
}

func messagesFromIndexes(messages []provider.Message, selected map[int]struct{}) []provider.Message {
	indexes := make([]int, 0, len(selected))
	for index := range selected {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	output := make([]provider.Message, 0, len(indexes))
	for _, index := range indexes {
		output = append(output, cloneMessages(messages[index : index+1])[0])
	}
	return output
}

func planSourceRange(input PlanInput) (contextstate.SourceRange, error) {
	var derived contextstate.SourceRange
	if len(input.SourceEvents) > 0 {
		first := input.SourceEvents[0].ID
		last := first
		for index, event := range input.SourceEvents {
			if err := event.Validate(); err != nil {
				return contextstate.SourceRange{}, err
			}
			if event.ID.SessionID != first.SessionID || event.ID.Sequence != first.Sequence+uint64(index) {
				return contextstate.SourceRange{}, invalidPlan("source_events", "events are not contiguous")
			}
			last = event.ID
		}
		derived = contextstate.SourceRange{Start: first, End: last}
	}
	if isZeroSourceRange(input.SourceRange) {
		return derived, nil
	}
	if err := input.SourceRange.Validate(); err != nil {
		return contextstate.SourceRange{}, err
	}
	if !isZeroSourceRange(derived) {
		if input.SourceRange.Start.SessionID != derived.Start.SessionID || input.SourceRange.Start.Sequence > derived.Start.Sequence || input.SourceRange.End.Sequence < derived.End.Sequence {
			return contextstate.SourceRange{}, invalidPlan("source_range", "does not cover source events")
		}
	}
	return input.SourceRange, nil
}

func isZeroSourceRange(r contextstate.SourceRange) bool {
	return r.Start.SessionID == "" && r.End.SessionID == "" && r.Start.Sequence == 0 && r.End.Sequence == 0
}

func planIdempotencyKey(input PlanInput, rng contextstate.SourceRange, target int, retained []provider.Message) (string, error) {
	if input.IdempotencyKey != "" {
		if err := validatePlanKey(input.IdempotencyKey); err != nil {
			return "", err
		}
		return input.IdempotencyKey, nil
	}
	canonical, err := contextstate.MarshalCanonical(struct {
		Algorithm     string
		Range         contextstate.SourceRange
		Budget        int
		Target        int
		OutputReserve int
		FromRevision  contextstate.Revision
		Messages      []plannerMessageFingerprint
	}{compactionAlgorithm, rng, input.Budget, target, input.OutputReserve, input.Revision, plannerMessages(retained)})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "compact-" + hex.EncodeToString(digest[:]), nil
}

type plannerMessageFingerprint struct {
	Role             string
	Content          string
	ReasoningContent string
	ToolCalls        []plannerToolCallFingerprint
	ToolCallID       string
	Name             string
}

type plannerToolCallFingerprint struct {
	ID        string
	Type      string
	Name      string
	Arguments string
}

func plannerMessages(messages []provider.Message) []plannerMessageFingerprint {
	output := make([]plannerMessageFingerprint, len(messages))
	for index, message := range messages {
		output[index] = plannerMessageFingerprint{
			Role:             message.Role,
			Content:          message.Content,
			ReasoningContent: message.ReasoningContent,
			ToolCallID:       message.ToolCallID,
			Name:             message.Name,
		}
		for _, call := range message.ToolCalls {
			output[index].ToolCalls = append(output[index].ToolCalls, plannerToolCallFingerprint{
				ID: call.ID, Type: call.Type, Name: call.Function.Name, Arguments: call.Function.Arguments,
			})
		}
	}
	return output
}

func validatePlanKey(key string) error {
	if len(key) > contextstate.MaxIdentifierBytes || strings.TrimSpace(key) != key || key == "" {
		return invalidPlan("idempotency_key", "outside identifier limit")
	}
	for _, r := range key {
		if unicode.IsControl(r) {
			return invalidPlan("idempotency_key", "contains control characters")
		}
	}
	return nil
}

// validateMessageShape is shared by planning and durable turn mapping. It
// delegates pairing semantics to provider, then wraps failures in the stable
// context DTO error family.
//
// User-message Names are exempt from the shape gate: host-owned frames (the
// session's core-memory context, preserved through PlanInput.PreserveNames)
// legitimately ride on a user message with a sentinel Name that the wire
// keeps, so the structural check evaluates the message without it. Every
// other pairing rule (tool pairing, roles, empty content, tool-call ids)
// still applies exactly as provider defines it.
func validateMessageShape(messages []provider.Message) error {
	if err := provider.ValidateToolPairing(maskUserMessageNames(messages)); err != nil {
		return fmt.Errorf("%w: message shape: %v", contextstate.ErrInvalidDTO, err)
	}
	return nil
}

// maskUserMessageNames returns a shallow clone in which the Name of every
// user-role message is cleared for pairing validation. Host frames are the
// only named user messages this planner ever sees (chat input never sets
// Name), and their Name is what makes them preservable, so it must not fail
// the structural gate. Assistant and tool messages are not masked: a Name
// there is never a host frame and stays a hard shape error.
func maskUserMessageNames(messages []provider.Message) []provider.Message {
	output := make([]provider.Message, len(messages))
	copy(output, messages)
	for index := range output {
		if output[index].Role == provider.RoleUser {
			output[index].Name = ""
		}
	}
	return output
}

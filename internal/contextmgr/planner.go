package contextmgr

import (
	"fmt"
	"sort"
	"strings"

	"github.com/MiviaLabs/mivia-agent/internal/contextstate"
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/remainder"
)

const (
	compactionAlgorithm       = "context-compact-v1"
	defaultRecentTailMessages = 8
	maxRecentTailMessages     = 64
	// salvageUserTurns bounds how many otherwise-dropped user messages the
	// salvage pass may re-admit. See salvageUserMessages for why the pass
	// exists; the bound keeps compaction's output size predictable.
	salvageUserTurns = 4
)

// PlanInput contains only immutable inputs to the structural planner. The
// planner stays pure except for an explicit caller-provided Spool seam: when
// Spool is nil there are no side effects and output is byte-identical to
// before; when set, elision spools bytes through that seam only.
type PlanInput struct {
	Messages []provider.Message
	Budget   int
	Tools    []provider.ToolSpec
	// OutputReserve is validated (must not be negative) and folded into the
	// idempotency-key fingerprint (planIdempotencyKey) - it is NOT subtracted
	// from Budget anywhere in the trigger/target math below. Budget already
	// excludes the reserved completion allowance (callers derive it from
	// config.EffectivePromptTokens, which does the subtraction once, upstream
	// of the planner). Using OutputReserve again here would double-subtract
	// the same reserve. If a future caller needs the planner itself to
	// account for an output reserve, pass a Budget that has NOT already
	// excluded it and remove this comment along with the double-subtraction
	// it warns against - do not add a second, silent subtraction beside this
	// one.
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
	// ContextAccounting carries the bound provider's declared context-billing
	// profile (provider.ContextAccountingProfile) opaquely: the planner never
	// interprets its fields itself, only passes it to the provider estimators
	// it already calls. The zero value is the conservative "bill everything"
	// default, so a caller that leaves this unset behaves exactly as before
	// the field existed.
	ContextAccounting provider.ContextAccountingProfile
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
	before := applyCalibration(provider.EstimateMessagesPromptCost(input.Messages, schemaCost, input.ContextAccounting), input.CalibrationRatio)
	// Percentages of Budget alone - see PlanInput.OutputReserve for why it is
	// not subtracted here too.
	trigger := PercentFloor(input.Budget, 4, 5)
	target := PercentFloor(input.Budget, 1, 2)
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

func promptOverflow(after, budget int, objective provider.Message, schemaCost int, ratio float64, profile provider.ContextAccountingProfile) error {
	objectiveCost := applyCalibration(provider.EstimateMessagesPromptCost([]provider.Message{objective}, schemaCost, profile), ratio)
	if objectiveCost > budget {
		return fmt.Errorf("%w: current objective cost %d exceeds budget %d", contextstate.ErrPromptBudgetExceeded, objectiveCost, budget)
	}
	return fmt.Errorf("%w: retained request cost %d exceeds budget %d", contextstate.ErrPromptBudgetExceeded, after, budget)
}

// PercentFloor returns floor(value * numerator / denominator) without
// overflowing on large token budgets. Shared by Plan's trigger/target math
// and any other caller that needs the same hysteresis shape (trigger at
// numerator/denominator of a budget, prune down to a lower target).
func PercentFloor(value, numerator, denominator int) int {
	quotient, remainder := value/denominator, value%denominator
	return quotient*numerator + remainder*numerator/denominator
}

func currentObjective(messages []provider.Message, explicit string) (provider.Message, int, error) {
	if explicit != "" {
		for index := len(messages) - 1; index >= 0; index-- {
			if messages[index].Role == provider.RoleUser && messages[index].Content == explicit {
				return messages[index], index, nil
			}
		}
		return provider.Message{}, -1, invalidPlan("current_objective", "does not match any user message")
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == provider.RoleUser {
			return messages[index], index, nil
		}
	}
	return provider.Message{}, -1, fmt.Errorf("%w: planner current objective is missing", contextstate.ErrPromptBudgetExceeded)
}

func retainMessages(input PlanInput, objective provider.Message, objectiveIndex, target, schemaCost int, deferred []deferredElision) ([]provider.Message, []int, error) {
	units := messageUnits(input.Messages)
	mandatory := mandatoryIndexes(input.Messages, objectiveIndex, input.PreserveNames)

	selected := make(map[int]struct{}, len(mandatory))
	for index := range mandatory {
		selected[index] = struct{}{}
	}
	selectedCost := calibratedCost(input.Messages, selected, schemaCost, input.CalibrationRatio, input.ContextAccounting)
	if selectedCost > input.Budget {
		return nil, nil, promptOverflow(selectedCost, input.Budget, objective, schemaCost, input.CalibrationRatio, input.ContextAccounting)
	}
	tailLimit := input.RecentTail
	if tailLimit == 0 {
		tailLimit = defaultRecentTailMessages
	}
	if tailLimit < 0 || tailLimit > maxRecentTailMessages {
		return nil, nil, invalidPlan("recent_tail", fmt.Sprintf("must be between 0 and %d", maxRecentTailMessages))
	}
	var deferredMap map[int]struct{}
	if len(deferred) > 0 {
		deferredMap = make(map[int]struct{}, len(deferred))
		for _, d := range deferred {
			deferredMap[d.index] = struct{}{}
		}
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
		// the newest messages (DC-6). Ref-less elision placeholders carry
		// zero recoverable information and do not consume tail slots.
		unitSlots := unitTailSlots(input.Messages, unit, deferredMap)
		if tailCount+unitSlots > tailLimit {
			break
		}
		// Estimate cost incrementally: only compute the marginal cost of
		// adding this unit rather than re-estimating the entire selection.
		unitTokens := 0
		for _, index := range unit {
			unitTokens += provider.EstimateMessageTokensAt(input.Messages, index, input.ContextAccounting)
		}
		candidateCost := runningCost + applyCalibration(unitTokens, input.CalibrationRatio)
		if candidateCost > target {
			break
		}
		for _, index := range unit {
			selected[index] = struct{}{}
		}
		runningCost = candidateCost
		tailCount += unitSlots
	}
	salvageUserMessages(input, selected, objectiveIndex, runningCost, target)
	retained := messagesFromIndexes(input.Messages, selected)
	if err := validateMessageShape(retained); err != nil {
		return nil, nil, err
	}
	return retained, indexesFromSelection(selected), nil
}

// isReflessElisionPlaceholder reports whether a message is a tool-result
// elision notice carrying no remainder reference and not scheduled to receive
// one. Such notices carry zero recoverable information and must not consume
// recent-tail retention slots.
func isReflessElisionPlaceholder(msg provider.Message, isDeferred bool) bool {
	if msg.Role != provider.RoleTool {
		return false
	}
	if !strings.HasPrefix(msg.Content, elisionNoticePrefix) {
		return false
	}
	if strings.Contains(msg.Content, "; remainder: ") {
		return false
	}
	if isDeferred {
		return false
	}
	return true
}

// unitTailSlots computes how many retention slots unit charges against
// RecentTail. Ref-less elision placeholders carry zero recoverable
// information and are not charged (cost 0 slots); every other message
// in the unit costs 1 slot.
func unitTailSlots(messages []provider.Message, unit messageUnit, deferred map[int]struct{}) int {
	slots := 0
	for _, index := range unit {
		isDeferred := false
		if deferred != nil {
			_, isDeferred = deferred[index]
		}
		if isReflessElisionPlaceholder(messages[index], isDeferred) {
			continue
		}
		slots++
	}
	return slots
}

// salvageUserMessages rescues user turns from a compaction that would
// otherwise leave the model with no statement of the task at all. It is a
// RESCUE, not a retention policy - see retainsUserContext for the narrow
// trigger - so a user-heavy transcript that already retains task context
// through the ordinary tail walk is untouched and /compact still shrinks it.
//
// Why the rescue exists: the objective anchor is only ever the newest user
// message, typically a bare "continue" after a resume, so every earlier user
// turn is optional and competes for recent-tail slots against tool traffic
// orders of magnitude bulkier. A real session lost its entire task statement
// this way. A user turn is not derived content - it is the premise the rest
// of the transcript is evidence for - and it is almost always the cheapest
// message in the history. The oldest unselected turn gets a reserved slot
// because a long session's opening message is usually the task statement,
// the one message nothing else surviving can reconstruct.
//
// Cannot overflow (respects the tail walk's own token target, never touches
// the mandatory set) and cannot break tool pairing (selection is emitted in
// ascending original index order, so a salvaged message lands exactly where
// it sat).
func salvageUserMessages(input PlanInput, selected map[int]struct{}, objectiveIndex, runningCost, target int) {
	if retainsUserContext(input, selected, objectiveIndex) {
		return
	}
	candidates := make([]int, 0, salvageUserTurns+1)
	for index := len(input.Messages) - 1; index >= 0; index-- {
		if input.Messages[index].Role != provider.RoleUser {
			continue
		}
		// A NAMED user message is a host frame - the core-memory block, a
		// rendered context summary - not something the user typed. Those have
		// their own retention mechanism (PreserveNames) and their own
		// lifecycle; salvage speaks only for genuine user turns, so claiming
		// them here would silently override the caller's preservation policy.
		if input.Messages[index].Name != "" {
			continue
		}
		if _, ok := selected[index]; ok {
			continue
		}
		candidates = append(candidates, index)
	}
	if len(candidates) == 0 {
		return
	}
	// candidates is newest-first; keep that order for the bounded head and
	// append the oldest turn so it is considered even in a long history.
	oldest := candidates[len(candidates)-1]
	if len(candidates) > salvageUserTurns {
		candidates = candidates[:salvageUserTurns]
		candidates = append(candidates, oldest)
	}
	for _, index := range candidates {
		if _, ok := selected[index]; ok {
			continue
		}
		tokens := applyCalibration(provider.EstimateMessageTokensAt(input.Messages, index, input.ContextAccounting), input.CalibrationRatio)
		if runningCost+tokens > target {
			continue
		}
		selected[index] = struct{}{}
		runningCost += tokens
	}
}

// retainsUserContext reports whether the retained set already carries a
// genuine (unnamed) user turn beyond the objective anchor itself. The
// objective is always the newest user message and is retained regardless -
// what this checks is whether the model would otherwise see ANY earlier
// statement of what it is working on.
func retainsUserContext(input PlanInput, selected map[int]struct{}, objectiveIndex int) bool {
	for index := range selected {
		if index == objectiveIndex {
			continue
		}
		message := input.Messages[index]
		if message.Role == provider.RoleUser && message.Name == "" {
			return true
		}
	}
	return false
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

func calibratedCost(messages []provider.Message, selected map[int]struct{}, schemaCost int, ratio float64, profile provider.ContextAccountingProfile) int {
	return applyCalibration(provider.EstimateMessagesPromptCost(messagesFromIndexes(messages, selected), schemaCost, profile), ratio)
}

// indexesFromSelection returns the ascending message indexes in selected. It
// is the single source of order for retained messages and the retained-index
// pairing that installRetainedElisionRefs uses, so the two can never disagree.
func indexesFromSelection(selected map[int]struct{}) []int {
	indexes := make([]int, 0, len(selected))
	for index := range selected {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	return indexes
}

func messagesFromIndexes(messages []provider.Message, selected map[int]struct{}) []provider.Message {
	indexes := indexesFromSelection(selected)
	output := make([]provider.Message, 0, len(indexes))
	for _, index := range indexes {
		output = append(output, cloneMessages(messages[index : index+1])[0])
	}
	return output
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

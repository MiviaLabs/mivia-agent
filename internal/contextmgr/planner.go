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
)

const (
	compactionAlgorithm       = "context-compact-v1"
	defaultRecentTailMessages = 8
	maxRecentTailMessages     = 64
)

// PlanInput contains only immutable inputs to the structural planner. The
// planner has no provider, storage, filesystem, or session side effects.
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
}

// PlannerInput is a descriptive alias for callers that use the planner as a
// standalone preparation boundary.
type PlannerInput = PlanInput

// PlanResult is the deterministic structural result. Summary generation and
// durable publication happen in later seams; this value is safe to discard.
type PlanResult struct {
	Messages       []provider.Message
	Candidate      CheckpointCandidate
	BeforeTokens   int
	AfterTokens    int
	TriggerTokens  int
	TargetTokens   int
	Compacted      bool
	SourceRange    contextstate.SourceRange
	IdempotencyKey string
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
	rng, err := planSourceRange(input)
	if err != nil {
		return PlanResult{}, err
	}
	before, err := provider.EstimateRequestCost(input.Messages, input.Tools, input.OutputReserve)
	if err != nil {
		return PlanResult{}, invalidPlan("request_cost", err.Error())
	}
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
	objective, objectiveIndex, err := currentObjective(input.Messages, input.CurrentObjective)
	if err != nil {
		return PlanResult{}, err
	}
	retained, err := retainMessages(input, objective, objectiveIndex, target)
	if err != nil {
		return PlanResult{}, err
	}
	after, err := provider.EstimateRequestCost(retained, input.Tools, input.OutputReserve)
	if err != nil {
		return PlanResult{}, invalidPlan("request_cost", err.Error())
	}
	if after > input.Budget {
		return PlanResult{}, promptOverflow(after, input.Budget, objective, input.Tools, input.OutputReserve)
	}
	key, err := planIdempotencyKey(input, rng, target, retained)
	if err != nil {
		return PlanResult{}, err
	}
	active, err := contextstate.MarshalCanonical(retained)
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
	result.IdempotencyKey = key
	return result, nil
}

func invalidPlan(field, reason string) error {
	return fmt.Errorf("%w: planner %s: %s", contextstate.ErrInvalidDTO, field, reason)
}

func promptOverflow(after, budget int, objective provider.Message, tools []provider.ToolSpec, reserve int) error {
	objectiveCost, err := provider.EstimateRequestCost([]provider.Message{objective}, tools, reserve)
	if err == nil && objectiveCost > budget {
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

func retainMessages(input PlanInput, objective provider.Message, objectiveIndex, target int) ([]provider.Message, error) {
	units := messageUnits(input.Messages)
	mandatory := make(map[int]struct{}, len(input.Messages))
	if input.Messages[0].Role == provider.RoleSystem {
		mandatory[0] = struct{}{}
	}
	mandatory[objectiveIndex] = struct{}{}
	markLatestToolUnit(units, input.Messages, mandatory)

	selected := make(map[int]struct{}, len(mandatory))
	for index := range mandatory {
		selected[index] = struct{}{}
	}
	selectedCost, err := costForSelected(input.Messages, selected, input.Tools, input.OutputReserve)
	if err != nil {
		return nil, invalidPlan("request_cost", err.Error())
	}
	if selectedCost > input.Budget {
		return nil, promptOverflow(selectedCost, input.Budget, objective, input.Tools, input.OutputReserve)
	}
	tailLimit := input.RecentTail
	if tailLimit == 0 {
		tailLimit = defaultRecentTailMessages
	}
	if tailLimit < 0 || tailLimit > maxRecentTailMessages {
		return nil, invalidPlan("recent_tail", fmt.Sprintf("must be between 0 and %d", maxRecentTailMessages))
	}
	tailCount := 0
	for unitIndex := len(units) - 1; unitIndex >= 0; unitIndex-- {
		unit := units[unitIndex]
		if unitSelected(unit, selected) || tailCount+len(unit) > tailLimit {
			continue
		}
		candidate := cloneIndexSet(selected)
		for _, index := range unit {
			candidate[index] = struct{}{}
		}
		cost, err := costForSelected(input.Messages, candidate, input.Tools, input.OutputReserve)
		if err != nil {
			return nil, invalidPlan("request_cost", err.Error())
		}
		if cost > target {
			break
		}
		selected = candidate
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

func cloneIndexSet(input map[int]struct{}) map[int]struct{} {
	output := make(map[int]struct{}, len(input)+1)
	for index := range input {
		output[index] = struct{}{}
	}
	return output
}

func costForSelected(messages []provider.Message, selected map[int]struct{}, tools []provider.ToolSpec, reserve int) (int, error) {
	return provider.EstimateRequestCost(messagesFromIndexes(messages, selected), tools, reserve)
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
		Messages      []plannerMessageFingerprint
	}{compactionAlgorithm, rng, input.Budget, target, input.OutputReserve, plannerMessages(retained)})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "compact-" + hex.EncodeToString(digest[:]), nil
}

type plannerMessageFingerprint struct {
	Role       string
	Content    string
	ToolCalls  []plannerToolCallFingerprint
	ToolCallID string
	Name       string
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
			Role: message.Role, Content: message.Content, ToolCallID: message.ToolCallID, Name: message.Name,
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
func validateMessageShape(messages []provider.Message) error {
	if err := provider.ValidateToolPairing(messages); err != nil {
		return fmt.Errorf("%w: message shape: %v", contextstate.ErrInvalidDTO, err)
	}
	return nil
}

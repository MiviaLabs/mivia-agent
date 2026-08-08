package runtime

import "time"

// WorkLimits bounds cumulative work for one task. A zero numeric field is
// unlimited. A zero deadline is unset.
type WorkLimits struct {
	MaxTurns         int       `json:"max_turns,omitempty"`
	MaxPromptTokens  int       `json:"max_prompt_tokens,omitempty"`
	MaxOutputTokens  int       `json:"max_output_tokens,omitempty"`
	MaxOutputPerCall int       `json:"max_output_per_call,omitempty"`
	MaxToolCalls     int       `json:"max_tool_calls,omitempty"`
	DeadlineAt       time.Time `json:"deadline_at,omitempty"`
}

// LowestPositiveWorkLimits returns the tightest positive limit for each field.
func LowestPositiveWorkLimits(limits ...WorkLimits) WorkLimits {
	var result WorkLimits
	for _, limit := range limits {
		result.MaxTurns = lowestPositive(result.MaxTurns, limit.MaxTurns)
		result.MaxPromptTokens = lowestPositive(result.MaxPromptTokens, limit.MaxPromptTokens)
		result.MaxOutputTokens = lowestPositive(result.MaxOutputTokens, limit.MaxOutputTokens)
		result.MaxOutputPerCall = lowestPositive(result.MaxOutputPerCall, limit.MaxOutputPerCall)
		result.MaxToolCalls = lowestPositive(result.MaxToolCalls, limit.MaxToolCalls)
		if !limit.DeadlineAt.IsZero() && (result.DeadlineAt.IsZero() || limit.DeadlineAt.Before(result.DeadlineAt)) {
			result.DeadlineAt = limit.DeadlineAt
		}
	}
	return result
}

func lowestPositive(current, candidate int) int {
	if candidate <= 0 || (current > 0 && current <= candidate) {
		return current
	}
	return candidate
}

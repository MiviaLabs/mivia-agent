package agent

import (
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// workLimitMeter tracks one loop's cumulative provider and tool reservations.
type workLimitMeter struct {
	limits       runtime.WorkLimits
	promptTokens int
	outputTokens int
	toolCalls    int
}

// outputCap derives the maximum output allocation for one provider request.
// It preserves an unbounded request only when no output work limit is set.
func (m *workLimitMeter) outputCap(requested *int) (*int, error) {
	if m == nil {
		return requested, nil
	}
	cap := 0
	if requested != nil && *requested > 0 {
		cap = *requested
	}
	if m.limits.MaxOutputPerCall > 0 && (cap <= 0 || m.limits.MaxOutputPerCall < cap) {
		cap = m.limits.MaxOutputPerCall
	}
	if m.limits.MaxOutputTokens > 0 {
		remaining := m.limits.MaxOutputTokens - m.outputTokens
		if remaining <= 0 {
			return nil, fmt.Errorf("work limit exceeded: output tokens")
		}
		// A positive remaining budget is an allocation, not a rejection: clamp
		// the per-call ceiling down to the remainder so the request still runs
		// with the largest output the cumulative bound permits. Only a truly
		// exhausted budget (remaining <= 0 above) fails, and it fails before
		// any provider call.
		if cap <= 0 || remaining < cap {
			cap = remaining
		}
	}
	if cap <= 0 {
		return nil, nil
	}
	return &cap, nil
}

func (m *workLimitMeter) reserveProvider(promptTokens, outputTokens int) error {
	if m == nil {
		return nil
	}
	if outputTokens <= 0 {
		outputTokens = m.limits.MaxOutputPerCall
		if outputTokens <= 0 && m.limits.MaxOutputTokens > 0 {
			outputTokens = m.limits.MaxOutputTokens - m.outputTokens
			if outputTokens <= 0 {
				return fmt.Errorf("work limit exceeded: output tokens")
			}
		}
	}
	if m.limits.MaxOutputPerCall > 0 && outputTokens > m.limits.MaxOutputPerCall {
		return fmt.Errorf("work limit exceeded: output tokens per call")
	}
	if m.limits.MaxPromptTokens > 0 && promptTokens > m.limits.MaxPromptTokens-m.promptTokens {
		return fmt.Errorf("work limit exceeded: prompt tokens")
	}
	if m.limits.MaxOutputTokens > 0 && outputTokens > m.limits.MaxOutputTokens-m.outputTokens {
		return fmt.Errorf("work limit exceeded: output tokens")
	}
	m.promptTokens += promptTokens
	m.outputTokens += outputTokens
	return nil
}

// reservePromptOnly charges prompt tokens against MaxPromptTokens without
// touching the output allowance. It backs recovery paths (the prompt-too-long
// compaction retry) where the retried prompt is genuinely new work but the
// completion's output was already reserved by the rejected attempt: charging
// output twice for one logical completion would hard-fail a finite
// MaxOutputTokens budget (DC-6 broken bound on the DC-8 retry path). The
// cumulative prompt bound still holds; a nil receiver is a no-op, mirroring
// reserveProvider.
func (m *workLimitMeter) reservePromptOnly(promptTokens int) error {
	if m == nil {
		return nil
	}
	if m.limits.MaxPromptTokens > 0 && promptTokens > m.limits.MaxPromptTokens-m.promptTokens {
		return fmt.Errorf("work limit exceeded: prompt tokens")
	}
	m.promptTokens += promptTokens
	return nil
}

func requestOutputReserve(req provider.Request) int {
	if req.MaxTokens == nil {
		return 0
	}
	return *req.MaxTokens
}

func (m *workLimitMeter) reserveToolBatch(count int) error {
	if m == nil || count == 0 {
		return nil
	}
	if m.limits.MaxToolCalls > 0 && count > m.limits.MaxToolCalls-m.toolCalls {
		return fmt.Errorf("work limit exceeded: tool calls")
	}
	m.toolCalls += count
	return nil
}

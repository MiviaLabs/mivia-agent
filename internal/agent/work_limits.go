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

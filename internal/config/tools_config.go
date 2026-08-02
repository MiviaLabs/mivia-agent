package config

import "fmt"

// Validation for the two [tools] knobs that bound tool-result bytes: the
// per-call ceiling and the aggregate per-batch budget. Both reject values that
// would leave the model with results too small to use, rather than accepting a
// number and quietly producing stubs.

func resolveToolsConfig(tc ToolsConfig) ToolsConfig {
	def := DefaultToolsConfig
	if tc.RunTimeoutSec <= 0 {
		tc.RunTimeoutSec = def.RunTimeoutSec
	}
	if tc.MaxReadBytes <= 0 {
		tc.MaxReadBytes = def.MaxReadBytes
	}
	if tc.MaxWriteKB <= 0 {
		tc.MaxWriteKB = def.MaxWriteKB
	}
	if tc.MaxOutputBytes <= 0 {
		tc.MaxOutputBytes = def.MaxOutputBytes
	}
	if tc.MaxListDirEntries <= 0 {
		tc.MaxListDirEntries = def.MaxListDirEntries
	}
	// No defaulting: 0 means uncapped. Negative is normalized to 0 so every
	// consumer can treat <=0 uniformly as "no cap".
	if tc.MaxToolResultBytes < 0 {
		tc.MaxToolResultBytes = 0
	}
	// No defaulting either: 0 is off, -1 is derived, positive is literal. Any
	// other negative is normalized to the derived sentinel so consumers can
	// treat "< 0" uniformly - Validate rejects the values that are typos
	// rather than intent.
	if tc.BatchResultBudgetBytes < 0 {
		tc.BatchResultBudgetBytes = BatchResultBudgetDerived
	}
	// Unlike MaxToolResultBytes there is no "uncapped" state: the tools that
	// read Tavily responses declare this number as their result budget, and an
	// undeclared budget is exactly what the dispatcher's backstop destroys.
	if tc.MaxTavilyResponseBytes <= 0 {
		tc.MaxTavilyResponseBytes = def.MaxTavilyResponseBytes
	}
	// Unlike the Tavily bound there IS a valid unlimited state for fetch_url:
	// it truncates an over-bound body instead of refusing it, so an unbounded
	// read still yields a bounded, usable result. But Go cannot tell an unset
	// knob from an explicit 0 (both decode to the zero value), so a <= 0 here
	// resolves to the built-in default - exactly like MaxTavilyResponseBytes.
	// fetch_url itself preserves a 0 it receives via direct construction as
	// unlimited (see internal/tools/fetch_url.go).
	if tc.MaxFetchKB <= 0 {
		tc.MaxFetchKB = def.MaxFetchKB
	}
	// B7: RunAllowlist + RunAllowlistOnly are mutually exclusive - prefer RunAllowlistOnly
	if len(tc.RunAllowlist) > 0 && len(tc.RunAllowlistOnly) > 0 {
		tc.RunAllowlist = nil
	}
	// B7: EnvAllowlist + EnvAllowlistOnly are mutually exclusive - prefer EnvAllowlistOnly
	if len(tc.EnvAllowlist) > 0 && len(tc.EnvAllowlistOnly) > 0 {
		tc.EnvAllowlist = nil
	}
	return tc
}

func validateToolResultBudgets(tc ToolsConfig) error {
	// A positive cap below 1024 bytes starves every tool envelope (error
	// strings, JSON framing) and yields useless truncated stubs; reject it
	// rather than let the loop silently destroy every result.
	if tc.MaxToolResultBytes > 0 && tc.MaxToolResultBytes < 1024 {
		return fmt.Errorf("[tools] max_tool_result_bytes must be 0 (uncapped) or >= 1024, got %d",
			tc.MaxToolResultBytes)
	}
	// A batch budget under the degrade floor cannot be honoured: the first
	// result that does not fit is re-cut to the floor anyway, so every batch
	// would overshoot and every result after it would be a bare reference.
	// Reject it rather than ship a bound that only pretends to hold.
	if v := tc.BatchResultBudgetBytes; v > 0 && v < MinBatchResultBudgetBytes {
		return fmt.Errorf("[tools] batch_result_budget_bytes must be 0 (off), %d (derive from the prompt budget), or >= %d, got %d",
			BatchResultBudgetDerived, MinBatchResultBudgetBytes, v)
	}
	return nil
}

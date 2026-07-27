package agent

import "strings"

func limitToolBatchResults(results []toolExecResult, max int) {
	if max <= 0 {
		return
	}
	remaining := max
	for i := range results {
		if remaining <= 0 {
			results[i].result = ""
			results[i].truncated = true
			continue
		}
		if len(results[i].result) > remaining {
			results[i].result = truncateResult(results[i].result, remaining)
			results[i].truncated = true
		}
		remaining -= len(results[i].result)
	}
}

func truncateResult(result string, max int) string {
	if max <= 0 || len(result) <= max {
		if max <= 0 {
			return ""
		}
		return result
	}
	return result[:max]
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

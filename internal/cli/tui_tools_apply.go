package cli

import (
	"strings"
)

// ToolResultFailed implements tool result failed.
func ToolResultFailed(body string) bool {
	if body == "" {
		return false
	}
	low := strings.ToLower(strings.TrimSpace(body))
	if strings.HasPrefix(low, "error") || low == "failed" || strings.HasPrefix(low, "failed ") {
		return true
	}
	// Any non-zero exit= token (exit=1, exit=127, exit=timeout, exit=error, …).
	if i := strings.Index(low, "exit="); i >= 0 {
		rest := low[i+len("exit="):]
		if strings.HasPrefix(rest, "0") && (len(rest) == 1 || rest[1] < '0' || rest[1] > '9') {
			return false
		}
		return true
	}
	return false
}

package ports

import (
	"strconv"
	"strings"
)

// ToolCallOK reports whether a historical tool call succeeded, from its
// persisted output alone (no live "ok" signal survives a session
// resume). This is the single ok-classifier shared by the live event
// path (internal/uiadapter/event_kind.go's translateToolEnd, which is
// gated on the agent loop's own Detail string) and the historical
// replay path (internal/uiadapter's History() and
// internal/ui/screen/conversation/session_state_builder.go's replay),
// so a failed tool call reads the same way whether it is rendered live
// or reconstructed on resume.
//
// run_command's output carries its own "exit=N" status line; every
// other tool call's failure is a persisted "error: ..." body (see
// internal/agent/sdk_dispatcher_shim.go).
func ToolCallOK(tc ToolCall) bool {
	if strings.EqualFold(tc.Name, "run_command") {
		for _, line := range strings.Split(tc.Output, "\n") {
			status, ok := strings.CutPrefix(strings.TrimSpace(line), "exit=")
			if !ok {
				continue
			}
			code, err := strconv.Atoi(status)
			return err == nil && code == 0
		}
	}
	return !strings.HasPrefix(strings.ToLower(strings.TrimSpace(tc.Output)), "error:")
}

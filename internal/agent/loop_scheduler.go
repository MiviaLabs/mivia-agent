package agent

import (
	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
)

// duplicateDeliveryNotice is the fixed model-visible marker a duplicate tool
// delivery carries in place of the recorded body (Wave C). A duplicate is an
// identical call with the same tool and arguments that ran - or is running -
// earlier in this step; the dedup cache served its recorded result, so the
// tool did not run again. The notice is deliberately short (< 1024 bytes) so
// it can never itself be truncated, and it starts with the literal prefix the
// tests assert on.
const duplicateDeliveryNotice = "note: duplicate delivery suppressed — an identical call with the same tool and arguments ran or is running earlier in this step; this result was served from the dedup cache and the tool did not run again"

type toolExecResult struct {
	index    int // original position in ToolCalls slice
	toolCall provider.ToolCall
	// result is the model-visible body as pass 1 built it, hook context
	// included. It is what enters history when no batch budget is configured,
	// and what the operator-facing tool_end preview is drawn from.
	result    string
	truncated bool // whether result was truncated for history
	err       error
	// duplicate marks a result served from the dedup cache rather than
	// executed. Its model-visible body is the suppression notice, never the
	// recorded body, so the operator row needs its own failure signal.
	duplicate bool
	// originalBody is the recorded body a duplicate was served from, retained
	// ONLY for the operator row: toolEndDetail judges a duplicate's failure
	// signal against this original output (a run_command duplicate reports its
	// child exit in the recorded header with err==nil), because the notice that
	// replaced it carries no status of its own.
	originalBody string
	// parts is the same result in structured form, which is what the batch
	// shaper needs: a second pass has to know the ORIGINAL body's size and
	// where its bytes live, and neither survives being flattened into one
	// string (D9). Every construction site must populate it - a result with
	// an empty parts.cappedBody would be charged as zero bytes and emitted as
	// an empty tool message.
	parts           resultParts
	ephemeralMarker string
	// hookRuns are the lifecycle hooks that fired for this call, for display.
	hookRuns []runtime.HookRun
}

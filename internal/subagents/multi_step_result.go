package subagents

// The subagent result envelope: the JSON contract a dispatched task's parent
// reads. Kept in its own file so multi_step.go stays under the 500-line
// prefer line; this file owns the envelope vocabulary and the terminal-status
// classification shared with EventSubagentDone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// terminalStatus classifies a run's exit error into the fixed termination
// vocabulary shared by the result envelope and the EventSubagentDone status:
// nil -> "completed", context.Canceled -> "canceled",
// context.DeadlineExceeded -> "timed_out", ErrSchemaViolation or any other
// non-nil error -> "error".
func terminalStatus(err error) string {
	switch {
	case err == nil:
		return "completed"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timed_out"
	case errors.Is(err, ErrSchemaViolation):
		return "error"
	default:
		return "error"
	}
}

func buildResult(reply string, messageCount int, elapsed time.Duration, stepCount int64, err error) (json.RawMessage, error) {
	result := map[string]any{
		"output":     reply,
		"steps":      messageCount / 2,
		"elapsed":    elapsed.Round(time.Millisecond).String(),
		"step_count": stepCount,
	}
	if err != nil {
		// No content reference is emitted here. This layer has no repository, so
		// nothing stores the error or partial reply bytes under any key, and a
		// reference whose bytes nothing holds is worse than none: it hands the
		// model a pointer that cannot resolve. The resolvable reference for this
		// same task already exists on the correct path - the coordinator mints
		// and stores it from subagents.Result.Output/.Err.
		//
		// Interrupts get the pool/ledger vocabulary (canceled/timed_out) and
		// keep the partial reply the loop had already produced (DC-9/DC-12):
		// agent.Loop.Run returns lastText on an interrupted step, and the pool
		// records the same classification in Result.Status. Only genuine
		// failures (schema violation included) are "error" with the output
		// deleted, so a raw provider body can never leak.
		if status := terminalStatus(err); status == "canceled" || status == "timed_out" {
			result["status"] = status
		} else {
			result["status"] = "error"
			if errors.Is(err, ErrSchemaViolation) {
				result["schema"] = "violation"
			}
			delete(result, "output")
		}
	} else {
		result["status"] = "completed"
		// A subagent that did all its work via tool calls (grep, read_file)
		// can finish with empty reply text. Without a fallback the parent
		// sees "completed" with no output at all. Synthesize a minimal
		// summary so the result is never silently empty.
		if reply == "" && stepCount > 0 {
			result["output"] = fmt.Sprintf("(subagent completed %d steps with no final text reply)", stepCount)
		}
	}

	payload, marshalErr := json.Marshal(result)
	if err == nil {
		// The map holds only strings and integers, so marshalErr stays nil
		// in practice; the join keeps the error contract without a branch
		// no input can reach.
		err = marshalErr
	}
	if err != nil {
		return payload, err
	}
	return payload, nil
}

// buildResultStructured is the schema-valid success path: output is the parsed
// object and schema is "ok" so parents may consume without re-validating.
func buildResultStructured(output any, messageCount int, elapsed time.Duration, stepCount int64) (json.RawMessage, error) {
	result := map[string]any{
		"output":     output,
		"schema":     "ok",
		"status":     "completed",
		"steps":      messageCount / 2,
		"elapsed":    elapsed.Round(time.Millisecond).String(),
		"step_count": stepCount,
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

package subagents

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/agent"
	"github.com/MiviaLabs/mivia-agent/internal/jschema"
	"github.com/MiviaLabs/mivia-agent/internal/redact"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/textutil"
)

// compileOutputSchema resolves task > handler schema and compiles it.
// On success with a schema, promptAppendix is the host instruction to append.
// Only a nil schema means "no schema": an empty object {} is a valid JSON
// Schema (match anything) and must be enforced as declared, not silently
// replaced by the handler's fallback.
func (h *MultiStepHandler) compileOutputSchema(req runtime.Request) (compiled *jschema.Compiled, promptAppendix string, err error) {
	schemaMap := req.OutputSchema
	if schemaMap == nil {
		schemaMap = h.OutputSchema
	}
	if schemaMap == nil {
		return nil, "", nil
	}
	compiled, err = jschema.Compile(schemaMap)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	return compiled, jschema.PromptAppendix(compiled.Raw()), nil
}

// finishReasonLength is the provider finish reason that reports a reply was
// truncated by the output budget. Re-prompting with the same budget cannot
// repair truncation, so the corrective turn must say so instead of reporting
// the truncated bytes as ordinary invalid JSON.
const finishReasonLength = "length"

// truncationCorrectiveMessage builds the corrective user turn for a reply the
// provider cut off (finish_reason "length"). The previous output is NEVER
// inlined: the model already holds its own truncated reply in context, and
// replaying the bytes would amplify any instruction embedded in them. The
// schema contract is restated whole (a cut contract would make the retry
// repair blind, exactly like jschema.FormatCorrectiveWithSchema), the prefix
// is soft-capped at jschema.MaxCorrectiveBytes (the contract gives way to
// nothing — when the contract alone fills the budget the prefix yields), and
// redaction is applied last like the ordinary corrective message.
func truncationCorrectiveMessage(schema map[string]any, redact func(string) string) string {
	prefix := "Your previous reply was cut off by the output limit. " +
		"Reply again with ONLY the required JSON, as concise as possible."
	schemaSection := ""
	if contract := jschema.ModelSchemaContract(schema); contract != "" {
		schemaSection = "\nThe required schema is:\n" + contract
	}
	budget := jschema.MaxCorrectiveBytes - len(schemaSection)
	if len(prefix) > budget {
		prefix = textutil.TruncateRuneSafe(prefix, budget)
	}
	msg := prefix + schemaSection
	if redact != nil {
		msg = redact(msg)
	}
	return msg
}

// runValidatedReply runs the agent loop, re-entering with corrective user turns
// when a compiled schema is present. remainingSteps is the original MaxSteps
// budget (0 = unlimited); stepCount tracks EventStep emissions across re-entries.
func (h *MultiStepHandler) runValidatedReply(
	callCtx context.Context,
	loop *agent.Loop,
	opts agent.Options,
	taskPrompt string,
	compiled *jschema.Compiled,
	remainingSteps int,
	stepCount *atomic.Int64,
) (reply string, structured any, runErr error) {
	retryMax := h.SchemaRetryMax
	if retryMax <= 0 {
		retryMax = 2
	}
	effectiveSteps := schemaRepairStepBudget(remainingSteps, opts.WorkLimits.MaxTurns)
	userTurn := taskPrompt
	// lastInvalid is the normalized (fence-stripped) candidate that failed
	// validation on the previous iteration. A byte-identical repeat means the
	// corrective turn produced zero repair progress; re-entering the loop
	// would only burn another LLM call on the same dead end, so fail fast
	// instead of spending the whole retry budget.
	var lastInvalid string
	for attempt := 0; ; attempt++ {
		opts.PreserveWorkLimits = attempt > 0
		// Each loop.Run resets its local MaxSteps counter, so shrink MaxSteps
		// to the remaining effective budget — re-entry must not extend step
		// allowance.
		if effectiveSteps > 0 {
			used := int(stepCount.Load())
			left := effectiveSteps - used
			if left <= 0 {
				return "", nil, fmt.Errorf("%w: no step budget remaining for schema retry", ErrSchemaViolation)
			}
			opts.MaxSteps = left
		}
		if callCtx.Err() != nil {
			return reply, structured, callCtx.Err()
		}
		reply, runErr = loop.Run(callCtx, userTurn, opts)
		if runErr != nil {
			return reply, structured, runErr
		}
		if compiled == nil {
			return reply, nil, nil
		}
		candidate := jschema.ExtractOutputCandidate(reply)
		inst, vErr := compiled.ValidateJSONBytes([]byte(candidate))
		if vErr == nil {
			return candidate, inst, nil
		}
		if attempt > 0 && candidate == lastInvalid {
			return "", nil, fmt.Errorf("%w: no progress on schema repair: repeated the identical invalid output", ErrSchemaViolation)
		}
		lastInvalid = candidate
		if attempt >= retryMax {
			return "", nil, schemaRepairExhaustedError(loop.LastFinishReason, vErr)
		}
		userTurn = schemaRepairCorrectiveTurn(loop.LastFinishReason, vErr, compiled.Raw())
	}
}

// schemaRepairStepBudget folds the per-Run MaxTurns cap into the whole
// invocation's step budget. Each loop.Run re-applies opts.WorkLimits.MaxTurns
// as a fresh per-call cap (agent.Loop.Run), so without folding it in here every
// re-entry would re-grant a full turn budget and a task could exceed its
// declared MaxTurns (runtime.WorkLimits: "bounds cumulative work for one
// task"; rule 50-08: children inherit remaining budget). Byte-identical when
// MaxTurns is 0/unset or >= remainingSteps.
func schemaRepairStepBudget(remainingSteps, maxTurns int) int {
	if maxTurns > 0 && (remainingSteps <= 0 || maxTurns < remainingSteps) {
		return maxTurns
	}
	return remainingSteps
}

// schemaRepairExhaustedError names the true cause when the retry budget is
// spent. A reply the provider cut off (finish_reason "length") is reported as
// truncation so the durable error ref and run summary record the actual
// mechanism instead of misreporting it as ordinary invalid JSON (DC-9). The
// failed output itself is never inlined; the wrapped jschema detail is already
// bounded by the corrective-budget cap.
func schemaRepairExhaustedError(finishReason string, vErr error) error {
	if finishReason == finishReasonLength {
		return fmt.Errorf("%w: reply truncated by the output limit (finish_reason %q): %v", ErrSchemaViolation, finishReasonLength, vErr)
	}
	return fmt.Errorf("%w: %v", ErrSchemaViolation, vErr)
}

// schemaRepairCorrectiveTurn builds the user turn for the next repair attempt.
// A reply the provider cut off (finish_reason "length") gets the
// truncation-aware message, which never inlines the failed output (the model
// already holds its own truncated reply in context; replaying the bytes would
// amplify any instruction embedded in them) and restates the schema contract
// whole. Any other invalid reply keeps the ordinary corrective message so the
// established repair behavior is unchanged.
func schemaRepairCorrectiveTurn(finishReason string, vErr error, schema map[string]any) string {
	if finishReason == finishReasonLength {
		return truncationCorrectiveMessage(schema, redact.Text)
	}
	return jschema.FormatCorrectiveWithSchema(vErr, schema, redact.Text)
}

// finishRun builds the multi-step result envelope from a validated reply.
func finishRun(loop *agent.Loop, reply string, structured any, elapsed time.Duration, stepCount int64, runErr error) (json.RawMessage, error) {
	if structured != nil {
		return buildResultStructured(structured, len(loop.Messages), elapsed, stepCount)
	}
	return buildResult(reply, len(loop.Messages), elapsed, stepCount, runErr)
}

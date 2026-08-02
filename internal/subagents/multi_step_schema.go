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
)

// compileOutputSchema resolves task > handler schema and compiles it.
// On success with a schema, promptAppendix is the host instruction to append.
func (h *MultiStepHandler) compileOutputSchema(req runtime.Request) (compiled *jschema.Compiled, promptAppendix string, err error) {
	schemaMap := req.OutputSchema
	if len(schemaMap) == 0 {
		schemaMap = h.OutputSchema
	}
	if len(schemaMap) == 0 {
		return nil, "", nil
	}
	compiled, err = jschema.Compile(schemaMap)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrSchemaViolation, err)
	}
	return compiled, jschema.PromptAppendix(compiled.Raw()), nil
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
	userTurn := taskPrompt
	for attempt := 0; ; attempt++ {
		// Each loop.Run resets its local MaxSteps counter, so shrink MaxSteps
		// to remaining budget — re-entry must not extend step allowance.
		if remainingSteps > 0 {
			used := int(stepCount.Load())
			left := remainingSteps - used
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
		candidate := jschema.StripOneCodeFence(reply)
		inst, vErr := compiled.ValidateJSONBytes([]byte(candidate))
		if vErr == nil {
			return candidate, inst, nil
		}
		if attempt >= retryMax {
			// Never inline known-malformed output.
			return "", nil, fmt.Errorf("%w: %v", ErrSchemaViolation, vErr)
		}
		userTurn = jschema.FormatCorrective(vErr, redact.Text)
	}
}

// finishRun builds the multi-step result envelope from a validated reply.
func finishRun(loop *agent.Loop, reply string, structured any, elapsed time.Duration, stepCount int64, runErr error) (json.RawMessage, error) {
	if structured != nil {
		return buildResultStructured(structured, len(loop.Messages), elapsed, stepCount)
	}
	return buildResult(reply, len(loop.Messages), elapsed, stepCount, runErr)
}

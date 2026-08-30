package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// doJSON makes the call and repeats it while the response body arrives
// incomplete. A truncated body still carries status 200, so the retry
// transport treats it as a success and only the decode below finds the cut.
func (c *OpenAICompat) doJSON(ctx context.Context, req Request) (*chatResponseBody, error) {
	if req.DisableProviderReplay {
		return c.doJSONOnce(ctx, req)
	}
	return retryOnIncompleteBody(ctx, func() (*chatResponseBody, error) {
		return c.doJSONOnce(ctx, req)
	})
}

func (c *OpenAICompat) doJSONOnce(ctx context.Context, req Request) (*chatResponseBody, error) {
	resp, _, err := c.doChatRequest(ctx, req)
	if err != nil {
		return nil, asTransient(err)
	}
	defer resp.Body.Close()
	// The non-streaming read is the operationally common path: nested/subagent
	// turns never stream (MultiStepHandler never sets FinalWriter), so every
	// subagent-context turn lands here. Without the watchdog, a dead-but-open
	// connection sat silent for up to the transport's absolute client-wall
	// backstop with no observable signal.
	raw, err := io.ReadAll(io.LimitReader(c.wrapWithIdleWatchdog(resp.Body), maxJSONResponseBytes+1))
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, asTransient(fmt.Errorf("%s: read response: %w (request deadline %s)", c.name, markTransientReadDeadline(ctx, req.Timeout, err), deadlineLabel(req.Timeout)))
		}
		return nil, asTransient(fmt.Errorf("%s: read response: %w", c.name, err))
	}
	if len(raw) > maxJSONResponseBytes {
		return nil, fmt.Errorf("%s: response exceeds %d byte limit", c.name, maxJSONResponseBytes)
	}
	if c.errorParser != nil {
		// The parser reads the full body, already bounded by
		// maxJSONResponseBytes. Truncating here broke json.Unmarshal for
		// error bodies whose message exceeds 4096 bytes, so a z.ai code-1261
		// (prompt too long) rejection was never read and ErrPromptTooLong was
		// never wrapped - the agent loop could not compact and retry.
		if err := c.errorParser(resp.StatusCode, raw); err != nil {
			return nil, err
		}
	}
	var body chatResponseBody
	if err := json.Unmarshal(raw, &body); err != nil {
		// Left unwrapped on purpose: a decode failure is not by itself a
		// transport fault (IsTransient excludes JSON syntax errors, because at
		// agent-output parsing sites they are bad answers). retryOnIncompleteBody
		// retries a body that was provably cut and marks the final error
		// TransientError when its per-call budget is spent, where the cut is
		// known.
		return nil, fmt.Errorf("%s: decode response: %w", c.name, err)
	}
	return &body, nil
}

package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
)

// maxMaxTokensClampRetries bounds re-issues after a max-tokens-cap rejection,
// each with the cap halved. Each retry sends a strictly smaller max_tokens
// (a), so a rejected request is never replayed with the same body: the
// sequence the upstream saw can never recur, because every re-issue carries a
// cap below the one it just refused.
const maxMaxTokensClampRetries = 2

// clampMaxTokensForRetry returns a copy of req with max_tokens halved when
// err wraps ErrMaxTokensExceeded and a clamped re-issue is allowed, or nil
// when no retry should happen. The returned pointer is a fresh Request: the
// caller's request is never mutated.
//
// Retry preconditions (b): a nil cap cannot be halved (there is nothing to
// clamp), and a cap <= 1 cannot halve with progress (the next re-issue would
// carry the same 1, replaying a rejected body); DisableProviderReplay forbids
// a second request entirely, because the caller has declared that a re-issue
// could double-bill a turn. Halving floors at 1, so the last re-issue before
// the budget is spent still sends a strictly smaller value.
func (c *OpenAICompat) clampMaxTokensForRetry(req Request, err error) *Request {
	if err == nil || req.DisableProviderReplay || req.MaxTokens == nil || *req.MaxTokens <= 1 {
		return nil
	}
	if !errors.Is(err, ErrMaxTokensExceeded) {
		return nil
	}
	half := *req.MaxTokens / 2
	if half < 1 {
		half = 1
	}
	retry := req
	retry.MaxTokens = &half
	return &retry
}

// doChatRequest performs one chat.completions exchange for req and returns the
// raw HTTP response, the request that was actually sent (carrying any
// clamp-adjusted max_tokens), and any error. On a max-tokens-cap rejection it
// re-issues the request with the cap halved, bounded by
// maxMaxTokensClampRetries.
//
// BODY OWNERSHIP (c): every rejected body is read by httpError (which runs
// errorParser over the full body) and closed here before the next attempt;
// the returned OK body is left open and unread for the caller's single defer
// resp.Body.Close(). Callers must NOT close the body on the error path - it is
// already closed - and must NOT read the OK body here.
//
// TRANSPORT FAILURES (d): a request that never reached the provider is
// wrapped exactly like the pre-merge per-site paths
// (fmt.Errorf("%s: request failed: %w", c.name, err) over
// markTransientReadDeadline), and each call site applies its own asTransient
// decision on the returned error, so error surfaces do not change.
//
// CLAMP BOUNDARY (e): clamping happens only at the initial-HTTP stage, before
// any SSE content is read or delivered. The stream call sites hand this
// function a req.Stream=true request and take back the (possibly clamped) req
// to drive their read loop, so nothing delivered is ever re-asked.
func (c *OpenAICompat) doChatRequest(ctx context.Context, req Request) (*http.Response, Request, error) {
	// Materialize the wire max_tokens onto req itself before the clamp loop
	// below ever runs. marshalBody (via effectiveMaxTokens) falls back to
	// reasoningMaxTokensFloor whenever req.MaxTokens is nil, but
	// clampMaxTokensForRetry's recovery gate reads req.MaxTokens directly -
	// leaving it nil here would silently disable that recovery for exactly
	// the request shape (unset MaxTokens, reasoning-active) the floor
	// fallback exists for: a cap rejection on the guessed floor would have
	// nothing to halve and retry.
	req.MaxTokens = c.effectiveMaxTokens(req)
	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return nil, req, err
	}
	resp, err := c.client.Do(httpReq)
	if err != nil {
		err = markTransientReadDeadline(ctx, req.Timeout, err)
		return nil, req, fmt.Errorf("%s: request failed: %w", c.name, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp, req, nil
	}
	httpErr := c.httpError(resp)
	_ = resp.Body.Close()
	for attempts := 0; attempts < maxMaxTokensClampRetries; attempts++ {
		clamped := c.clampMaxTokensForRetry(req, httpErr)
		if clamped == nil {
			break
		}
		req = *clamped
		httpReq, err = c.newRequest(ctx, req)
		if err != nil {
			return nil, req, err
		}
		resp, err = c.client.Do(httpReq)
		if err != nil {
			err = markTransientReadDeadline(ctx, req.Timeout, err)
			return nil, req, fmt.Errorf("%s: request failed: %w", c.name, err)
		}
		if resp.StatusCode == http.StatusOK {
			return resp, req, nil
		}
		httpErr = c.httpError(resp)
		_ = resp.Body.Close()
	}
	return nil, req, httpErr
}

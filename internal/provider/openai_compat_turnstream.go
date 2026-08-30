package provider

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
)

// maxStreamTransportAttempts is the SSE attempt budget for one logical
// stream-transport turn: the first try plus two stall retries. Worst case
// per logical turn is 3 x (response-header bound + content-idle bound) of
// SSE, then one terminal doJSON attempt. At the compiled defaults (120s +
// 90s) that is 630s - inside the 1800s default request timeout, with the
// 3600s subagent TotalTimeout as the outer wall. The content-idle bound is
// configurable ([provider] stream_content_idle_timeout_seconds); an operator
// who raises it far enough must keep the request timeout above the product.
const maxStreamTransportAttempts = 3

// maxStreamStallRetries bounds the fresh-connection retries after one SSE
// attempt aborted on a stall. Two retries plus the first try fill the
// maxStreamTransportAttempts budget.
const maxStreamStallRetries = 2

// isStreamStall reports whether err is the content-idle abort class. The
// byte-level watchdog wraps the same sentinel, so a fully silent connection
// retries on a fresh connection too.
func isStreamStall(err error) bool {
	return errors.Is(err, ErrStreamIdle)
}

// retryOnStreamStall calls once, then repeats the call while it aborts on a
// stream stall, with ZERO delay between attempts: a stalled connection gains
// nothing from a wait, and the fresh dial is the recovery. It returns the
// last stall error when every try stalls, so the caller decides what a
// spent budget means.
//
// Stall retries intentionally ignore `received`: the non-stream contract
// never delivered anything to a consumer (the StreamWriter is nil on this
// path), so a re-ask duplicates only provider-side generation. The
// never-re-ask-a-delivered-turn invariant does not apply here.
func retryOnStreamStall[T any](ctx context.Context, call func() (T, error)) (T, error) {
	var zero T
	result, err := call()
	if !isStreamStall(err) {
		return result, err
	}
	for attempt := 0; attempt < maxStreamStallRetries; attempt++ {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		default:
		}
		result, err = call()
		if !isStreamStall(err) {
			return result, err
		}
	}
	return zero, err
}

// streamHostileStatus reports whether a non-OK status is in the rejection
// class that marks a provider stream-hostile. 429 and 408 are absent on
// purpose: they are transient statuses the shared transport already retries,
// not a statement about the stream shape.
func streamHostileStatus(code int) bool {
	switch code {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// streamHostileBody reports whether a rejection response carries a JSON body.
// The content-type header is the only structural signal readable before
// httpError drains the body, and every OpenAI-compatible provider tags its
// JSON rejections application/json. A non-JSON rejection is not proof the
// provider cannot stream, so it never sets the memory.
func streamHostileBody(resp *http.Response) bool {
	return strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json")
}

// streamTransportAttempt runs one SSE-shaped exchange and returns the 200
// response with its body unread. rejected reports a stream-shape rejection in
// the hostile class; the response body of a rejection is drained and closed
// here, so each attempt that fails leaves a fresh connection for the next
// one by construction.
func (c *OpenAICompat) streamTransportAttempt(ctx context.Context, req Request) (resp *http.Response, err error, rejected bool) {
	httpReq, err := c.newRequest(ctx, req)
	if err != nil {
		return nil, err, false
	}
	resp, err = c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", c.name, markTransientReadDeadline(ctx, req.Timeout, err)), false
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil, false
	}
	rejected = streamHostileStatus(resp.StatusCode) && streamHostileBody(resp)
	httpErr := c.httpError(resp)
	_ = resp.Body.Close()
	return nil, httpErr, rejected
}

// sseTurnAttempt is the outcome of one SSE attempt on the stream-transport
// path. needFallback stops the stall-retry loop and asks for the terminal
// non-stream attempt; it never carries an error.
type sseTurnAttempt struct {
	resp         *Response
	needFallback bool
}

// chatTurnStreamTransport serves a Request.StreamTransport turn: stream:true
// on the wire, the full non-stream *Response contract on the return path.
//
// PRECEDENCE: up to three SSE attempts (first try plus two stall retries on
// fresh connections, zero delay between attempts), then ONE terminal doJSON
// attempt. Exhaustion surfaces only when the fallback also fails. DisableProviderReplay
// gates both the stall retry and the fallback, so it leaves one SSE attempt
// and nothing else.
func (c *OpenAICompat) chatTurnStreamTransport(ctx context.Context, req Request) (*Response, error) {
	callCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	// A provider already remembered as stream-hostile never sees another
	// stream request. The non-stream call is this turn's first and only
	// provider request, so DisableProviderReplay does not block it.
	if c.streamHostile.Load() {
		return c.ChatTurn(callCtx, c.nonStreamRequest(req))
	}

	streamReq := req
	streamReq.Stream = true
	streamReq.StreamWriter = nil

	attempt := func() (sseTurnAttempt, error) { return c.sseTurnAttemptOnce(callCtx, req, streamReq) }

	var res sseTurnAttempt
	var err error
	if req.DisableProviderReplay {
		res, err = attempt()
	} else {
		res, err = retryOnStreamStall(callCtx, attempt)
	}
	if err != nil {
		if isStreamStall(err) && !req.DisableProviderReplay {
			return c.streamTransportFallback(callCtx, req, err)
		}
		return nil, asTransient(err)
	}
	if res.needFallback {
		return c.streamTransportFallback(callCtx, req, nil)
	}
	return res.resp, nil
}

// sseTurnAttemptOnce runs one SSE-shaped exchange and reads it to the end:
// fresh connection, content watchdog armed, response assembled back into the
// non-stream contract. Its verdicts feed the precedence in
// chatTurnStreamTransport: a stream-shape JSON rejection marks the provider
// hostile and asks for the fallback, a stall with zero data lines does the
// same, and a stream that finished without an answer falls back without any
// memory.
func (c *OpenAICompat) sseTurnAttemptOnce(ctx context.Context, req, streamReq Request) (sseTurnAttempt, error) {
	resp, err, rejected := c.streamTransportAttempt(ctx, streamReq)
	if err != nil {
		if rejected {
			c.streamHostile.Store(true)
			log.Printf("%s: provider rejected the stream request; using the non-stream path for this and future turns (%v)", c.name, err)
			return sseTurnAttempt{needFallback: true}, nil
		}
		if errors.Is(err, ErrMaxTokensExceeded) {
			// A max-tokens-cap rejection is a request-shape problem, not a
			// stream-shape one: the provider can stream fine, this request
			// just asked for more completion tokens than the serving route
			// allows. Fall back to the non-stream path so its clamp-and-retry
			// loop (clampMaxTokensForRetry via doChatRequest) can recover it,
			// exactly like every other request path in this client. Do not
			// mark the provider stream-hostile over a cap problem.
			return sseTurnAttempt{needFallback: true}, nil
		}
		return sseTurnAttempt{}, err
	}
	wd := newSSEContentWatchdogReader(c.wrapWithIdleWatchdog(resp.Body), c.name)
	content, reasoning, webSearch, toolCalls, finishReason, received, usage, rerr := c.readTurnStream(ctx, wd, nil, req.Timeout)
	wd.Close()
	resp.Body.Close()
	if rerr != nil {
		if isStreamStall(rerr) && !wd.sawDataLine() {
			// Zero data lines: the provider never streamed at all, so the
			// buffered non-streaming path is the shape it speaks. Fail open
			// to it instead of burning the retry budget on connections that
			// cannot improve. Replay disabled keeps this turn single-attempt:
			// remember the provider, but surface the stall.
			c.streamHostile.Store(true)
			log.Printf("%s: stream attempt stalled with no data line; using the non-stream path for this and future turns", c.name)
			if req.DisableProviderReplay {
				return sseTurnAttempt{}, rerr
			}
			return sseTurnAttempt{needFallback: true}, nil
		}
		if errors.Is(rerr, ErrMaxTokensExceeded) {
			// Same recovery as the pre-body rejection above: an in-band
			// (HTTP 200) max-tokens-cap error is still a cap problem the
			// non-stream fallback's clamp loop can fix, not a reason to mark
			// the provider stream-hostile. This is the common shape for a
			// provider that reports the rejection as an SSE data chunk
			// instead of a non-200 status (e.g. llmgateway's per-route cap).
			return sseTurnAttempt{needFallback: true}, nil
		}
		return sseTurnAttempt{}, rerr
	}
	if !received {
		return sseTurnAttempt{needFallback: true}, nil
	}
	return sseTurnAttempt{resp: &Response{
		Content:          content,
		ReasoningContent: reasoning,
		WebSearch:        webSearch,
		ToolCalls:        toolCalls,
		FinishReason:     finishReason,
		CacheUsage:       c.cacheUsage(usage),
		TokenUsage:       deriveTokenUsage(usage),
	}}, nil
}

// nonStreamRequest strips the stream shaping from req, so it can drive the
// plain non-stream path. Every field the fallback needs (model, messages,
// tools, reasoning, timeout, replay suppression, session key) passes through.
func (c *OpenAICompat) nonStreamRequest(req Request) Request {
	req.Stream = false
	req.StreamWriter = nil
	req.StreamTransport = false
	return req
}

// streamTransportFallback is the terminal non-stream attempt of a
// stream-transport turn. It runs once, after the SSE budget is spent or the
// stream completed without an answer. stallErr is the stall that spent the
// budget, if any; when the fallback also fails, that stall - a transient the
// caller can retry at the step level - stays the surfaced cause.
func (c *OpenAICompat) streamTransportFallback(ctx context.Context, req Request, stallErr error) (*Response, error) {
	if req.DisableProviderReplay {
		if stallErr != nil {
			return nil, asTransient(stallErr)
		}
		return nil, fmt.Errorf("%s: stream delivered no response", c.name)
	}
	resp, err := c.ChatTurn(ctx, c.nonStreamRequest(req))
	if err != nil {
		if stallErr != nil {
			return nil, fmt.Errorf("%s: %w (all %d stream attempts stalled; the non-stream fallback also failed: %v)", c.name, stallErr, maxStreamTransportAttempts, err)
		}
		return nil, err
	}
	return resp, nil
}

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// maxIncompleteBodyRetries bounds how many times one call is repeated after
// the response body arrives incomplete. Two retries clear a single truncated
// response without turning a real outage into a long stall.
const maxIncompleteBodyRetries = 2

// incompleteBodyRetryDelay is the wait before repeating the call. The body
// arrived cut short rather than refused, so the server is reachable and a
// short wait is enough. The delay doubles for the second retry.
const incompleteBodyRetryDelay = 500 * time.Millisecond

// isIncompleteBody reports whether err says the response body ended early.
//
// The HTTP layer cannot see this. A truncated body still arrives with status
// 200, so the retry round tripper returns it as a success and only the decode
// finds the cut. Such a call never reached the model's full answer, so it is a
// transport fault, not an answer the caller must accept.
//
// A body that is malformed for another reason also matches the syntax test.
// Repeating that call costs one more request and then fails the same way, so
// the wider test is the safe one: it never turns a real failure into a pass.
func isIncompleteBody(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var syntax *json.SyntaxError
	return errors.As(err, &syntax)
}

// retryOnIncompleteBody calls once, then repeats the call while the response
// body arrives incomplete. It returns the last error when every try is cut
// short, so the caller still sees why the call failed.
//
// Before this, one truncated response failed the workflow step that made the
// call. A step failure routes to the failure terminal, so a run lost all of
// its finished work because a single response body ended early.
func retryOnIncompleteBody[T any](ctx context.Context, call func() (T, error)) (T, error) {
	var zero T
	result, err := call()
	if !isIncompleteBody(err) {
		return result, err
	}
	delay := incompleteBodyRetryDelay
	for attempt := 0; attempt < maxIncompleteBodyRetries; attempt++ {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
		result, err = call()
		if !isIncompleteBody(err) {
			return result, err
		}
	}
	// The per-call budget is spent on a body that was provably cut short
	// (isIncompleteBody matched on the last call), so the call never delivered
	// an answer and a step-level retry may still recover. Mark the final error
	// transient EXPLICITLY: asTransient would no-op here, because IsTransient
	// deliberately excludes the JSON syntax errors a cut body decodes to. An
	// error the read path already marked (io.ErrUnexpectedEOF wrapped at the
	// point of the read) keeps its single mark. A mid-retry permanent refusal
	// never reaches this line: it exits through the checks above, unwrapped.
	var marked *TransientError
	if !errors.As(err, &marked) {
		err = &TransientError{Err: err}
	}
	return zero, err
}

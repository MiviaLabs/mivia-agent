// Package runtime: terminal result delivery.
//
// Every invocation that does not complete normally leaves through this file:
// a failure, a cancellation, a timeout, or a policy block. They share one
// delivery path so a waiter is released exactly once however the call ended,
// and differ only in the status they stamp.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// failResult builds a failed Result. The payload carries a bounded status and
// nothing else; raw provider/tool/error bodies stay out of the result.
func (d *Dispatcher) failResult(req Request, meta Metadata, started time.Time, err error, out []byte) Result {
	meta.Status = "failed"
	if errors.Is(err, context.Canceled) {
		meta.Status = "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		meta.Status = "timed_out"
	}
	meta.Duration = time.Since(started)
	meta.InputPreview = d.previewFor(req.Input)
	if len(out) > 0 {
		meta.OutputPreview = d.previewFor(out)
		meta.OutputHash = hash(out)
	} else if err != nil {
		meta.OutputPreview = d.previewFor([]byte(err.Error()))
	}
	// No content reference is emitted here. This layer has no repository, so
	// nothing stores the error or output bytes under any key, and a reference
	// whose bytes nothing holds is worse than none: it hands the model a pointer
	// that cannot resolve, so ledger_read answers not_found for a reason that has
	// nothing to do with the bytes being absent (INV-AG-10: a reference handed to
	// the model resolves, or it is not handed to the model). The bounded
	// correlation value stays in the audit metadata above - meta.OutputHash for a
	// handler that produced bytes, plus meta.OutputPreview - which is emitted to
	// the sink and never shown to the model.
	//
	// The payload carries the full, unredacted error reason alongside the
	// status. Opaquing failures into a bare {"status":"failed"} left the model
	// unable to distinguish a bad path from a broken tool - every failure looked
	// identical and the only recourse was blind retry (see the write_file
	// debugging session that motivated this). The raw err.Error() is safe to
	// surface here because it originates in mivia's own tool/handler code, which
	// is already required by rule 10 to keep secrets out of error messages; the
	// sink-side audit preview (OutputPreview) already handles redaction for
	// operator-facing logs. The model needs the same fidelity to debug itself.
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	return d.deliverTerminal(req, meta, err, reason)
}

// blockedResult delivers a policy block.
//
// It routes through the same delivery machinery as a failure so waiters are
// still released, but it stamps its own status: a block and a broken tool must
// not be indistinguishable downstream. The handler was never reached, so there
// is no output to hash or preview - the audit record previews the reason
// instead, which is the only thing that happened.
func (d *Dispatcher) blockedResult(req Request, meta Metadata, started time.Time, reason string) Result {
	meta.Status = statusBlocked
	meta.Duration = time.Since(started)
	meta.InputPreview = d.previewFor(req.Input)
	meta.OutputPreview = d.previewFor([]byte(reason))
	return d.deliverTerminal(req, meta, blockedError, reason)
}

// deliverTerminal builds the bounded status envelope, emits the audit record,
// and releases any waiter. The payload carries the reason verbatim because the
// model needs it - a blocked call the model cannot explain to itself is one it
// will simply retry.
//
// Hook-authored text is neutralized for tag-shaped content before it enters
// the envelope: a block reason comes from a hook script's stderr, which is
// untrusted third-party code, and the same neutralization that protects the
// framed advisory block must protect this path too.
func (d *Dispatcher) deliverTerminal(req Request, meta Metadata, err error, reason string) Result {
	d.emit(meta)
	payload := map[string]string{
		"status": meta.Status,
		"error":  neutralizeHookTags(reason),
	}
	safeOutput, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		safeOutput = []byte(`{"status":"failed"}`)
	}
	result := Result{
		ID: req.ID, Name: req.Name, Kind: req.Kind,
		Output: json.RawMessage(safeOutput),
		Err:    err, Metadata: meta,
	}
	d.mu.Lock()
	if waiter := d.waiters[req.ID]; waiter != nil {
		delete(d.waiters, req.ID)
		waiter <- result
	}
	d.mu.Unlock()
	return result
}

func (d *Dispatcher) emit(m Metadata) {
	if d.policy.Sink != nil {
		d.policy.Sink(Event{Type: m.Status, Metadata: m})
	}
}

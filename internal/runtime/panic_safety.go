package runtime

import (
	"context"
	"encoding/json"
	"fmt"
)

// safeInvoke calls h.Invoke and converts a panic into an error instead of
// letting it unwind through execute. A panic here would otherwise skip
// failResult, finish, and releaseIDKeyed's deferred call in Invoke would run
// with the named result still zero-valued - releasing the dedup/turn-result
// bookkeeping for this invocation ID without ever recording a terminal
// result for it (a stuck reservation, not just a lost answer).
func safeInvoke(h Handler, ctx context.Context, req Request) (out json.RawMessage, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("handler %q panicked: %v", req.Name, rec)
		}
	}()
	return h.Invoke(ctx, req)
}

// ephemeralMarker calls the handler's own EphemeralResultMarker on the
// SUCCESS path (execute has already returned a real answer), purely to build
// the event-log output preview. A panic here (bug-audit finding) escaped
// safeInvoke's recovery, which wraps only h.Invoke - defeating the whole
// point of that fix for this side interface: the task genuinely succeeded,
// but describing it for the event log crashed the process anyway. Recovered
// here and treated as "no marker" (falls back to the normal output preview)
// rather than turning a real success into a failure.
func ephemeralMarker(h Handler, req Request) (marker string) {
	ephemeral, ok := h.(ephemeralResultHandler)
	if !ok {
		return ""
	}
	defer func() {
		if rec := recover(); rec != nil {
			marker = ""
		}
	}()
	return ephemeral.EphemeralResultMarker(req)
}

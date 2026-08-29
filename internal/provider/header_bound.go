package provider

import (
	"context"
	"net/http"
)

// A response-header bound catches a peer that accepts a request and then goes
// silent. It only means that for a request whose headers are supposed to
// arrive immediately.
//
// A streaming request is one: the provider opens the stream, headers come
// back, and the model's work shows up afterwards as body bytes, where the
// stream watchdogs measure it. A non-stream completion is the opposite - it
// sends nothing at all until the whole answer exists, so the wait for its
// headers IS the generation. A header bound placed on that request is not a
// stall detector; it is a ceiling on how long a model is allowed to think,
// enforced below the layer that knows what the caller's budget was.
//
// The two phases therefore need different bounds, and net/http has no
// per-request ResponseHeaderTimeout - the setting lives on the Transport. The
// context marker below lets one client carry both transports and pick per
// request, which is why compatBaseRoundTripper returns a modalHeaderTransport
// rather than a single clone.
type generationHeaderPhaseKey struct{}

// withGenerationHeaderPhase marks a request whose wait for response headers is
// the model's generation time, so no header bound applies to it. Such a
// request is still bounded: by the caller's own request timeout, and by the
// client-wide backstop.
func withGenerationHeaderPhase(ctx context.Context) context.Context {
	return context.WithValue(ctx, generationHeaderPhaseKey{}, true)
}

func generationHeaderPhase(ctx context.Context) bool {
	marked, _ := ctx.Value(generationHeaderPhaseKey{}).(bool)
	return marked
}

// modalHeaderTransport routes a request to the transport whose header bound
// matches what the request's header phase actually measures. Both transports
// are built together from one template, so every other setting - the pinned
// dial that is the loopback security gate above all - is identical between
// them; only ResponseHeaderTimeout differs.
//
// The cost is one connection pool per phase per client rather than one per
// client. That is the price of the setting being transport-scoped in net/http.
type modalHeaderTransport struct {
	// streamed bounds the accept-to-headers wait for requests that answer
	// immediately.
	streamed *http.Transport
	// generation has no header bound: its header wait is the model working.
	generation *http.Transport
}

var _ http.RoundTripper = (*modalHeaderTransport)(nil)

func (m *modalHeaderTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if generationHeaderPhase(req.Context()) {
		return m.generation.RoundTrip(req)
	}
	return m.streamed.RoundTrip(req)
}

// CloseIdleConnections releases both pools, so a caller that closes idle
// connections is not silently leaving one pool untouched.
func (m *modalHeaderTransport) CloseIdleConnections() {
	m.streamed.CloseIdleConnections()
	m.generation.CloseIdleConnections()
}

package provider

import (
	"sync/atomic"
	"time"
)

// httpClientTimeoutNs holds the active http.Client wall as nanoseconds,
// process-wide, mirroring the stream watchdog atomics in idle_watchdog.go.
//
// Invariant: the wall stays above every configured per-request deadline plus
// a margin (config.resolveProviderHTTPTimeout derives it that way). A spent
// request budget then reports as the terminal context deadline; the wall
// never cuts a request first, so a wall hit is never mistaken for a
// retryable transport fault.
//
// Construction order: NewForProvider calls SetHTTPClientTimeout before any
// factory builds a client, so every client it constructs reads the derived
// wall. A client built outside NewForProvider gets DefaultHTTPTimeout.
var httpClientTimeoutNs atomic.Int64

func init() {
	httpClientTimeoutNs.Store(int64(DefaultHTTPTimeout))
}

// SetHTTPClientTimeout overrides the process-wide http.Client wall. A
// non-positive value is ignored, so a caller that knows no wall never
// resets it to zero (an unlimited client).
func SetHTTPClientTimeout(d time.Duration) {
	if d > 0 {
		httpClientTimeoutNs.Store(int64(d))
	}
}

func httpClientTimeout() time.Duration {
	return time.Duration(httpClientTimeoutNs.Load())
}

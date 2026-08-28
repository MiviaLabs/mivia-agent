package provider

import (
	"log"
	"sync"
	"time"
)

// TransportRetryEvent reports one granted transport retry: a transient
// failure was retryable, the attempt cap allowed another try, and the
// cumulative budget granted it. StatusCode carries the failed attempt's HTTP
// status; a transport-level failure (nil response) carries StatusCode 0 and
// the failure in Err. Err is nil when a status response was retried.
type TransportRetryEvent struct {
	// Attempt is the zero-based attempt number that just failed.
	Attempt int
	// MaxRetries is the configured retry cap the gate enforced.
	MaxRetries int
	// StatusCode is the failed attempt's HTTP status. 0 means the attempt
	// failed below the HTTP layer (connect, header wait, reset).
	StatusCode int
	// Err is the failed attempt's transport error. nil when the retry
	// follows a status response.
	Err error
	// Delay is the backoff wait before the next attempt.
	Delay time.Duration
}

// The observer slot is process-global on purpose - one active provider
// configuration per process - so a package-level knob is simpler than
// threading an option through every OpenAI-compatible factory. This mirrors
// idle_watchdog.go's stream-watchdog storage. The mutex keeps
// SetTransportRetryObserver race-free against concurrent chat traffic; the
// observer callback itself runs outside the lock.
var (
	transportRetryObserverMu sync.Mutex
	transportRetryObserver   = defaultTransportRetryObserver
)

// defaultTransportRetryObserver writes one log line per granted retry. This
// is the baseline before any observer is installed, and what
// SetTransportRetryObserver(nil) restores.
func defaultTransportRetryObserver(e TransportRetryEvent) {
	log.Printf("provider: transport retry attempt %d/%d (status %d, wait %s): %v", e.Attempt, e.MaxRetries, e.StatusCode, e.Delay, e.Err)
}

// SetTransportRetryObserver installs fn as the observer called once per
// granted transport retry. Process-global on purpose: mivia runs one active
// provider configuration per process, so a package-level knob is simpler
// than a new option on every factory (same justification as the stream
// watchdogs, idle_watchdog.go). It is also the test seam until a
// structured-log consumer exists. nil restores the default logging observer.
func SetTransportRetryObserver(fn func(TransportRetryEvent)) {
	transportRetryObserverMu.Lock()
	defer transportRetryObserverMu.Unlock()
	if fn == nil {
		fn = defaultTransportRetryObserver
	}
	transportRetryObserver = fn
}

// notifyTransportRetry calls the active observer with e. The read is
// lock-guarded so SetTransportRetryObserver stays race-free against chat
// traffic; the callback runs outside the lock, so an observer that calls
// back into the provider cannot deadlock.
func notifyTransportRetry(e TransportRetryEvent) {
	transportRetryObserverMu.Lock()
	fn := transportRetryObserver
	transportRetryObserverMu.Unlock()
	if fn != nil {
		fn(e)
	}
}

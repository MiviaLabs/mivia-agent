package tools

import (
	"context"
	"net"
	"net/http"
	"time"
)

// Centralizes the network-hardening defaults every outbound tool client must
// apply. A client missing these can hang forever on a slow or silent server,
// independent of whatever ctx deadline the caller happens to supply - see
// fetch_url's slow-loris hang, which is why this now lives in one place
// instead of being reinvented (or forgotten) per tool.
const (
	toolHTTPDialTimeout           = 10 * time.Second
	toolHTTPTLSHandshakeTimeout   = 10 * time.Second
	toolHTTPResponseHeaderTimeout = 30 * time.Second
	toolHTTPOverallTimeout        = 120 * time.Second
	toolHTTPIdleConnTimeout       = 30 * time.Second
	toolHTTPExpectContinueTimeout = 1 * time.Second
	toolHTTPMaxIdleConns          = 10

	// toolNetworkCapabilityTimeout is the dispatcher-level ctx grant every
	// network-backed tool declares via Capability. It sits above
	// toolHTTPOverallTimeout so the client's own timeout fires first with a
	// specific network error, rather than a generic dispatcher cancellation.
	toolNetworkCapabilityTimeout = 150 * time.Second
)

// boundedHTTPClientConfig configures newBoundedHTTPClient. The zero value
// selects the package defaults for both timeouts. dialContext and
// checkRedirect are used verbatim when set - fetch_url is the one caller
// that needs both, for SSRF re-validation on every dial and every redirect -
// and fall back to a plain bounded dialer / no redirect hook otherwise.
type boundedHTTPClientConfig struct {
	dialContext           func(ctx context.Context, network, addr string) (net.Conn, error)
	checkRedirect         func(req *http.Request, via []*http.Request) error
	responseHeaderTimeout time.Duration
	overallTimeout        time.Duration
}

// newBoundedHTTPClient builds an *http.Client that cannot hang past its
// configured bounds regardless of the caller's ctx: ResponseHeaderTimeout
// covers a server that connects and then goes silent, and Client.Timeout
// covers one that trickles its body forever after headers arrive. Every
// outbound tool client is built through this one function so the hardening
// applies uniformly and a future tool cannot reintroduce the gap fetch_url
// once had.
func newBoundedHTTPClient(cfg boundedHTTPClientConfig) *http.Client {
	responseHeaderTimeout := cfg.responseHeaderTimeout
	if responseHeaderTimeout <= 0 {
		responseHeaderTimeout = toolHTTPResponseHeaderTimeout
	}
	overallTimeout := cfg.overallTimeout
	if overallTimeout <= 0 {
		overallTimeout = toolHTTPOverallTimeout
	}
	dialContext := cfg.dialContext
	if dialContext == nil {
		dialContext = (&net.Dialer{Timeout: toolHTTPDialTimeout, KeepAlive: 30 * time.Second}).DialContext
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          toolHTTPMaxIdleConns,
		IdleConnTimeout:       toolHTTPIdleConnTimeout,
		TLSHandshakeTimeout:   toolHTTPTLSHandshakeTimeout,
		ResponseHeaderTimeout: responseHeaderTimeout,
		ExpectContinueTimeout: toolHTTPExpectContinueTimeout,
		DialContext:           dialContext,
	}
	return &http.Client{
		Transport:     transport,
		Timeout:       overallTimeout,
		CheckRedirect: cfg.checkRedirect,
	}
}

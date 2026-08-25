package provider

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// lookupLocalhost resolves the literal "localhost" during keyless loopback
// gate construction. Production behavior is net.LookupIP; the indirection is
// a test seam: on hosts-file platforms a swapped net.DefaultResolver never
// sees "localhost" (the hosts file answers it before DNS), so the
// DNS-rebinding tests substitute a hostile answer here instead.
var lookupLocalhost = net.LookupIP

// newLoopbackDialContext resolves the base URL host once at construction and
// returns a DialContext that pins every dial to the resolved loopback
// addresses. It is the resolve-once half of the loopback-http gate (plan
// §12 item 1, generalized beyond ollama - see NewForProvider):
// config.IsOllamaLoopback approves the literal hostname at config time, and
// this function turns that approval into a fixed, verified address set, so
// the per-request dial can never follow a resolver that has since moved
// "localhost" to a non-loopback address. providerName only labels error
// text; the loopback check itself is identical for every caller.
//
// The returned DialContext rewrites the dial address host unconditionally.
// Redirects and proxy dials are pinned too: local plaintext traffic must
// never transit an external proxy. The port always comes from the
// per-request address, so a pinned dial reaches the same service the caller
// asked for, on the loopback host verified at construction.
//
// Fail closed: a localhost that resolves to any non-loopback address, or to
// nothing, returns an error here - a client is never constructed on the
// strength of an unverified loopback claim.
func newLoopbackDialContext(providerName, baseURL string) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: parse base_url %q: %w", providerName, baseURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%s: base_url %q has no hostname", providerName, baseURL)
	}

	var pinned []net.IP
	if ip := net.ParseIP(host); ip != nil {
		// Defense in depth: IsOllamaLoopback already restricts the hostname,
		// but the dial contract must hold even for direct callers.
		if !ip.IsLoopback() {
			return nil, fmt.Errorf("%s: %s is not a loopback address; refusing plaintext local mode (set base_url to https://, or to a verified http://127.0.0.1 address)", providerName, host)
		}
		pinned = []net.IP{ip}
	} else if strings.EqualFold(host, "localhost") {
		ips, err := lookupLocalhost(host)
		if err != nil {
			return nil, fmt.Errorf("%s: cannot resolve localhost for local daemon mode: %v (set base_url to http://127.0.0.1:PORT/...)", providerName, err)
		}
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return nil, fmt.Errorf("%s: localhost resolves to non-loopback address %s; refusing plaintext local mode (set base_url to http://127.0.0.1:PORT/...)", providerName, ip)
			}
			pinned = append(pinned, ip)
		}
		if len(pinned) == 0 {
			return nil, fmt.Errorf("%s: localhost resolved to no loopback addresses; refusing plaintext local mode (set base_url to http://127.0.0.1:PORT/...)", providerName)
		}
	} else {
		return nil, fmt.Errorf("%s: host %q is not a loopback host; refusing plaintext local mode (set base_url to http://127.0.0.1:PORT/...)", providerName, host)
	}

	dialer := new(net.Dialer)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			// http always passes host:port; dial the address unchanged only
			// for a shape this transport never produces.
			return dialer.DialContext(ctx, network, addr)
		}
		var firstErr error
		for _, ip := range pinned {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		return nil, firstErr
	}, nil
}

// compatBaseRoundTripper returns the base transport a client wraps with retry
// logic. A nil DialContext keeps http.DefaultTransport exactly as it is; a
// dial pinning context gets a clone of the default transport with only the
// dial replaced, so nothing else about the transport differs from today.
func compatBaseRoundTripper(dialContext func(ctx context.Context, network, addr string) (net.Conn, error)) http.RoundTripper {
	if dialContext == nil {
		return http.DefaultTransport
	}
	clone := http.DefaultTransport.(*http.Transport).Clone()
	clone.DialContext = dialContext
	return clone
}

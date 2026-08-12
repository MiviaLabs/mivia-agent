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
// addresses. It is the resolve-once half of the keyless ollama loopback gate
// (plan §12 item 1): config.IsOllamaLoopback approves the literal hostname at
// config time, and this function turns that approval into a fixed, verified
// address set, so the per-request dial can never follow a resolver that has
// since moved "localhost" to a non-loopback address.
//
// The returned DialContext rewrites the dial address host unconditionally.
// Redirects and proxy dials are pinned too: keyless local traffic must never
// transit an external proxy. The port always comes from the per-request
// address, so a pinned dial reaches the same service the caller asked for, on
// the loopback host verified at construction.
//
// Fail closed: a localhost that resolves to any non-loopback address, or to
// nothing, returns an error here - keyless mode is never constructed.
func newLoopbackDialContext(baseURL string) (func(ctx context.Context, network, addr string) (net.Conn, error), error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("ollama: parse base_url %q: %w", baseURL, err)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("ollama: base_url %q has no hostname", baseURL)
	}

	var pinned []net.IP
	if ip := net.ParseIP(host); ip != nil {
		// Defense in depth: IsOllamaLoopback already restricts the hostname,
		// but the dial contract must hold even for direct callers.
		if !ip.IsLoopback() {
			return nil, fmt.Errorf("ollama: %s is not a loopback address; refusing keyless local daemon mode (set base_url to http://127.0.0.1:11434/v1)", host)
		}
		pinned = []net.IP{ip}
	} else if strings.EqualFold(host, "localhost") {
		ips, err := lookupLocalhost(host)
		if err != nil {
			return nil, fmt.Errorf("ollama: cannot resolve localhost for local daemon mode: %v (set base_url to http://127.0.0.1:11434/v1)", err)
		}
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return nil, fmt.Errorf("ollama: localhost resolves to non-loopback address %s; refusing keyless local daemon mode (set base_url to http://127.0.0.1:11434/v1)", ip)
			}
			pinned = append(pinned, ip)
		}
		if len(pinned) == 0 {
			return nil, fmt.Errorf("ollama: localhost resolved to no loopback addresses; refusing keyless local daemon mode (set base_url to http://127.0.0.1:11434/v1)")
		}
	} else {
		return nil, fmt.Errorf("ollama: host %q is not a loopback host; refusing keyless local daemon mode (set base_url to http://127.0.0.1:11434/v1)", host)
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

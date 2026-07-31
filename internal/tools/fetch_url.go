package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// fetchURLTool fetches a single URL with SSRF protection.
type fetchURLTool struct {
	ws                *workspace.Root
	maxLocalBytes     int
	maxFetchKB        int
	httpClient        *http.Client
	fetchClient       *http.Client
	allowPrivateFetch bool
}

// resultBudget is the byte bound on the page text this tool returns.
func (t *fetchURLTool) resultBudget() int {
	if t.maxLocalBytes <= 0 {
		return 256 * 1024
	}
	return t.maxLocalBytes
}

// ResultBudgetBytes declares that bound for dispatcher output-backstop
// derivation (see tools.ResultBudgetTool). The URL echo and status line ride
// above it and are covered by the derivation's input allowance and slack.
func (t *fetchURLTool) ResultBudgetBytes() int { return t.resultBudget() }

func (t *fetchURLTool) Name() string { return "fetch_url" }
func (t *fetchURLTool) Description() string {
	return "Fetch and read the contents of a URL. Uses SSRF protection to block private/internal addresses. Prefer over run_command for reading URLs."
}
func (t *fetchURLTool) Parameters() map[string]any {
	return schemaObject(map[string]any{
		"url": map[string]any{
			"type":        "string",
			"description": "URL to fetch",
		},
	}, []string{"url"})
}
func (t *fetchURLTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		URL string `json:"url"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	if !t.allowPrivateFetch {
		if err := validateFetchURL(ctx, in.URL); err != nil {
			return "", err
		}
	}
	return t.doFetch(ctx, in.URL)
}

func (t *fetchURLTool) doFetch(ctx context.Context, rawURL string) (string, error) {
	client := t.pickClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("fetch request: %w", err)
	}
	setBrowserHeaders(req)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	maxFetch := t.maxFetchKB
	if maxFetch <= 0 {
		maxFetch = 100
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxFetch)*1024))
	if err != nil {
		return "", fmt.Errorf("fetch read: %w", err)
	}
	text := stripHTMLTags(string(body))
	text = strings.TrimSpace(text)
	if len(text) == 0 {
		return "(empty page)", nil
	}
	maxOut := t.resultBudget()
	if len(text) > maxOut {
		text = truncateUTF8(text, maxOut) + "\n... (content truncated)"
	}
	return fmt.Sprintf("URL: %s\nStatus: %s\n\n%s", rawURL, resp.Status, text), nil
}

func (t *fetchURLTool) pickClient() *http.Client {
	if t.fetchClient != nil {
		return t.fetchClient
	}
	return t.httpClient
}

// --- SSRF protection ---

const maxFetchRedirects = 5

// cgnatNet is RFC 6598 shared address space (100.64.0.0/10).
var cgnatNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// isBlockedFetchIP reports whether ip must not be contacted for URL fetch.
func isBlockedFetchIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() {
		return true
	}
	if cgnatNet != nil && cgnatNet.Contains(ip) {
		return true
	}
	return false
}

// validateFetchURL enforces http(s) and rejects hosts that resolve (or are)
// private / reserved addresses. Fail-closed on DNS failure.
func validateFetchURL(ctx context.Context, raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid URL: must be http or https")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid URL: must be http or https")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("invalid URL: missing host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedFetchIP(ip) {
			return fmt.Errorf("blocked URL: private or reserved address")
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("blocked URL: hostname resolution failed: %w", err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("blocked URL: hostname resolution failed: no addresses")
	}
	for _, a := range addrs {
		if isBlockedFetchIP(a.IP) {
			return fmt.Errorf("blocked URL: private or reserved address")
		}
	}
	return nil
}

// newSafeFetchHTTPClient builds a client that re-validates redirect targets.
func newSafeFetchHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	baseDialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if ip := net.ParseIP(host); ip != nil {
				if isBlockedFetchIP(ip) {
					return nil, fmt.Errorf("blocked dial: private or reserved address")
				}
				return baseDialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, fmt.Errorf("blocked dial: resolve failed: %w", err)
			}
			var firstErr error
			for _, a := range ips {
				if isBlockedFetchIP(a.IP) {
					if firstErr == nil {
						firstErr = fmt.Errorf("blocked dial: private or reserved address")
					}
					continue
				}
				conn, dialErr := baseDialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				firstErr = dialErr
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("blocked dial: no allowed addresses")
			}
			return nil, firstErr
		},
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxFetchRedirects {
				return fmt.Errorf("stopped after %d redirects", maxFetchRedirects)
			}
			if err := validateFetchURL(req.Context(), req.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

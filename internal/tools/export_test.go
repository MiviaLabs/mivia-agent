package tools

// Test-only seam. The Tavily-backed tools carry an unexported base-URL
// override so tests can point them at an httptest server. Proving the
// dispatcher never destroys their results means driving the PRODUCTION
// composition (NewDefaultRegistry + runtime.NewToolDispatcher), and that test
// cannot live in package tools — runtime imports tools, so importing runtime
// back would be a cycle. It lives in package tools_test instead, which needs
// this door.
//
// The alternative, a TavilyBaseURL field on the production DefaultOptions, was
// rejected: its only caller would be a test, and the Tavily client has no SSRF
// guard (unlike fetchURLTool's), so an operator-settable base URL would be a
// way to redirect a credentialed request at an arbitrary host.

// RedirectTavilyToolsForTest points every Tavily-backed tool in r at baseURL.
func RedirectTavilyToolsForTest(r *Registry, baseURL string) {
	for _, tool := range r.List() {
		switch t := tool.(type) {
		case *webSearchTool:
			t.tavilyBaseURL = baseURL
		case *extractTool:
			t.tavilyBaseURL = baseURL
		}
	}
}

// DefaultTavilyResponseBytesForTest exposes the package's built-in bound so a
// test can pin it against the config package's default without package tools
// taking a dependency on package config.
const DefaultTavilyResponseBytesForTest = defaultTavilyResponseBytes

package tools_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/runtime"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Audit regression, round 3 (following 0f6e524, 2dca36b and e4fbb4b).
//
// `search` (Tavily path) and `extract` read the provider's JSON response with
// no bound and declared no result budget, so they were the last two recorded
// KNOWN GAPs in the registry-wide result-size gate. The dispatcher DESTROYS -
// never truncates - any result over its derived output backstop, which was
// 331776 bytes: a single extracted documentation page, or a search answer of a
// few hundred KB, came back to the model as
// {"error":"output budget exceeded","status":"failed"}. The request was made,
// the credit was spent, and the content was thrown away.
//
// The fix is a bound, NOT a truncation. Each test below drives the PRODUCTION
// composition - tools.NewDefaultRegistry + runtime.NewToolDispatcher(reg,
// runtime.Policy{}) + Invoke, the same path the agent loop builds when
// Options.Dispatcher is nil - against a Tavily-shaped response far larger than
// the old ceiling, and asserts BOTH that the result survives and that the
// content arrives byte-identical to what the server sent.

// tavilyRegressionServer serves the same JSON body on every Tavily endpoint.
func tavilyRegressionServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// tavilyRegressionDispatch builds the default registry with a provider key,
// redirects it at srv, and runs one call through a production dispatcher.
func tavilyRegressionDispatch(t *testing.T, srv *httptest.Server, name, input string) (string, int) {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws, TavilyAPIKey: "test-key"})
	tools.RedirectTavilyToolsForTest(reg, srv.URL)

	d, err := runtime.NewToolDispatcher(reg, runtime.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	res := d.Invoke(context.Background(), runtime.Request{
		ID: "tavily-regression", Kind: runtime.Tool, Name: name, Input: json.RawMessage(input),
	})
	body := string(res.Output)
	if res.Err != nil && !strings.Contains(body, "output budget exceeded") {
		t.Fatalf("%s failed unexpectedly: %v (body=%q)", name, res.Err, body[:min(len(body), 200)])
	}
	return body, d.Policy().MaxOutputBytes
}

// assertWholeAndUndestroyed is the point of this file: the result survived the
// dispatcher AND carries the server's bytes unmodified. A truncated result
// would satisfy the first half and fail the second.
func assertWholeAndUndestroyed(t *testing.T, tool, body, sent string, ceiling int) {
	t.Helper()
	if strings.Contains(body, "output budget exceeded") {
		t.Fatalf("dispatcher destroyed a %d-byte %s result at ceiling %d: %s", len(sent), tool, ceiling, body)
	}
	if strings.Contains(body, "truncated") {
		t.Fatalf("%s result was truncated; fetched content must reach the model whole. tail=%q",
			tool, body[max(0, len(body)-160):])
	}
	if !strings.Contains(body, sent) {
		t.Fatalf("%s content did not arrive byte-identical: sent %d bytes, result is %d bytes",
			tool, len(sent), len(body))
	}
}

// TestRegression_TavilyExtractLargePageReachesModelWhole: an extracted page of
// 900000 bytes - over 2.7x the pre-fix 331776 ceiling - must reach the model
// intact, not as an "output budget exceeded" stub and not cut short.
func TestRegression_TavilyExtractLargePageReachesModelWhole(t *testing.T) {
	const contentLen = 900_000
	content := strings.Repeat("extracted-page-body ", contentLen/20)
	raw, err := json.Marshal(map[string]any{
		"results": []map[string]string{{"url": "https://example.test/p", "content": content}},
	})
	if err != nil {
		t.Fatal(err)
	}

	body, ceiling := tavilyRegressionDispatch(t, tavilyRegressionServer(t, string(raw)),
		"extract", `{"url":"https://example.test/p"}`)
	assertWholeAndUndestroyed(t, "extract", body, content, ceiling)
}

// TestRegression_TavilySearchLargeAnswerReachesModelWhole: the same for the
// search path, whose answer field is appended to the composed result whole.
func TestRegression_TavilySearchLargeAnswerReachesModelWhole(t *testing.T) {
	const answerLen = 900_000
	answer := strings.Repeat("synthesized-answer-text ", answerLen/24)
	raw, err := json.Marshal(map[string]any{
		"results": []map[string]any{{"title": "T", "url": "https://example.test/p", "content": "c"}},
		"answer":  answer,
	})
	if err != nil {
		t.Fatal(err)
	}

	body, ceiling := tavilyRegressionDispatch(t, tavilyRegressionServer(t, string(raw)),
		"search", `{"query":"q"}`)
	assertWholeAndUndestroyed(t, "search", body, answer, ceiling)
}

// TestRegression_TavilyResultsClearTheDerivedCeiling states the property the
// two tests above depend on: with a provider key configured, the backstop the
// dispatcher derives sits above the bound the tools declare, so no response
// they are willing to accept can ever be destroyed.
func TestRegression_TavilyResultsClearTheDerivedCeiling(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewDefaultRegistry(tools.DefaultOptions{Workspace: ws, TavilyAPIKey: "test-key"})
	ceiling := runtime.DeriveOutputCeiling(reg, 0)
	for _, name := range []string{"search", "extract"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		budgeted, ok := tool.(tools.ResultBudgetTool)
		if !ok {
			t.Fatalf("%s declares no result budget; the dispatcher cannot clear what it cannot read", name)
		}
		if budget := budgeted.ResultBudgetBytes(); budget > ceiling {
			t.Errorf("%s accepts results up to %d bytes but the dispatcher destroys above %d", name, budget, ceiling)
		}
	}
}

// The tools package cannot import the config package, so its built-in default
// is a duplicated constant. Pin the two together or they drift and an operator
// who never sets the key gets a different bound than the docs state.
func TestTavilyResponseBudgetDefaultMatchesConfig(t *testing.T) {
	if got, want := tools.DefaultTavilyResponseBytesForTest, config.DefaultToolsConfig.MaxTavilyResponseBytes; got != want {
		t.Fatalf("tools default %d != config default %d", got, want)
	}
}

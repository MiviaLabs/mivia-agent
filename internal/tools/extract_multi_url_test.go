package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// extract accepts a comma-separated URL list and sends every URL to the
// provider, but it used to compose only Results[0] - silently dropping content
// the caller requested and the provider had already billed for. These tests pin
// that every returned result reaches the model, whole.

func TestExtractReturnsEveryRequestedURLNotJustTheFirst(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"results": []map[string]string{
			{"url": "https://a.example/one", "content": "AAA content one"},
			{"url": "https://b.example/two", "content": "BBB content two"},
			{"url": "https://c.example/three", "content": "CCC content three"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := newBudgetedExtractTool(t, tavilyBodyServer(t, string(body)), 1<<20)

	out, err := tool.Execute(context.Background(),
		json.RawMessage(`{"url":"https://a.example/one,https://b.example/two,https://c.example/three"}`))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	for _, want := range []string{
		"AAA content one", "BBB content two", "CCC content three",
		"https://a.example/one", "https://b.example/two", "https://c.example/three",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("extract dropped %q from a multi-URL result; got:\n%s", want, out)
		}
	}
}

// A provider that returns fewer results than URLs requested has dropped some.
// Reporting full coverage for a partial answer is the dishonesty this guards.
func TestExtractReportsShortProviderReturn(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"results": []map[string]string{
			{"url": "https://a.example/one", "content": "only one came back"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := newBudgetedExtractTool(t, tavilyBodyServer(t, string(body)), 1<<20)

	out, err := tool.Execute(context.Background(),
		json.RawMessage(`{"url":"https://a.example/one,https://b.example/two,https://c.example/three"}`))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.Contains(out, "1 of 3") {
		t.Errorf("short provider return not reported; got:\n%s", out)
	}
}

// When the provider supplies a URL, extract echoes that rather than the
// model-supplied argument, so an unbounded request-side string never reaches
// the result at all. Previously a long url argument was echoed verbatim and
// could exceed the declared budget on its own.
func TestExtractPrefersProviderURLOverUnboundedArgument(t *testing.T) {
	body, err := json.Marshal(map[string]any{
		"results": []map[string]string{{"url": "https://short.example/p", "content": ""}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := newBudgetedExtractTool(t, tavilyBodyServer(t, string(body)), 1024)

	longURL := "https://example.test/" + strings.Repeat("u", 1100)
	args, err := json.Marshal(map[string]string{"url": longURL})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("provider URL is short, so the result is within budget: %v", err)
	}
	if strings.Contains(out, strings.Repeat("u", 1100)) {
		t.Error("unbounded model-supplied URL was echoed into the result")
	}
	if !strings.Contains(out, "https://short.example/p") {
		t.Errorf("provider URL missing from result:\n%s", out)
	}
}

// Single-URL extraction keeps its existing shape and returns content whole.
func TestExtractSingleURLShapeUnchanged(t *testing.T) {
	content := strings.Repeat("z", 9000)
	body, err := json.Marshal(map[string]any{
		"results": []map[string]string{{"url": "https://a.example/one", "content": content}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tool := newBudgetedExtractTool(t, tavilyBodyServer(t, string(body)), 1<<20)

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://a.example/one"}`))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !strings.HasPrefix(out, "Tavily extract: https://a.example/one\n\n") {
		t.Errorf("single-URL header shape changed; got prefix:\n%.60s", out)
	}
	if !strings.Contains(out, content) {
		t.Error("single-URL content did not reach the model whole")
	}
	if strings.Contains(out, "of 1 requested") {
		t.Errorf("spurious short-return note on a complete single-URL result:\n%s", out)
	}
}

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tavilyRequestCapturingServer records the request body it receives and answers
// with a minimal well-formed search response.
func tavilyRequestCapturingServer(t *testing.T, into *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*into = string(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":[{"title":"T","url":"https://e.test/p","content":"c"}]}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// extractContent splits the url argument on commas and sends every resulting
// URL to the provider. That capability has to be declared, or a caller reads
// "URL to extract content from" and never learns the list form exists while
// the code silently bills for one.
func TestExtractURLParameterDocumentsTheListForm(t *testing.T) {
	tool := &extractTool{tavilyKey: "k"}
	props, _ := tool.Parameters()["properties"].(map[string]any)
	urlProp, _ := props["url"].(map[string]any)
	desc, _ := urlProp["description"].(string)

	if !strings.Contains(strings.ToLower(desc), "comma") {
		t.Errorf("url accepts a comma-separated list but its description does not say so: %q", desc)
	}
}

// include_raw_content is not in the schema, and schemaObject sets
// additionalProperties:false, so Registry.Execute rejects it before the tool
// ever decodes arguments. This pins that reachability fact: it is the reason
// the dead request-side plumbing could be removed without a behaviour change,
// and the reason re-adding the parameter would need a response field to land
// in first.
func TestSearchRejectsUndeclaredRawContentParameter(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&webSearchTool{tavilyKey: "k"})

	_, err := reg.Execute(context.Background(), "search",
		json.RawMessage(`{"query":"q","include_raw_content":true}`))
	if err == nil {
		t.Fatal("include_raw_content was accepted; it has no response field to land in")
	}
	if !strings.Contains(err.Error(), "unknown field") {
		t.Errorf("rejection should identify an undeclared field, got: %v", err)
	}
}

// The composed search output must never depend on a field the response struct
// cannot hold. Asserting the outgoing request omits include_raw_content keeps
// the request and the response shape in agreement.
func TestSearchRequestNeverAsksForRawContent(t *testing.T) {
	var got string
	srv := tavilyRequestCapturingServer(t, &got)
	tool := newBudgetedSearchTool(t, srv, 1<<20)

	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"q"}`)); err != nil {
		t.Fatalf("search: %v", err)
	}
	if strings.Contains(got, "include_raw_content") {
		t.Errorf("request asked for raw content the response struct discards: %s", got)
	}
}

package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
)

// TestIntegrationInspectRepositoryCompletesSearchEvidenceInOneToolCall proves
// the turn reduction this tool exists for (plan 66): finding a match AND
// seeing its surrounding lines used to cost a grep call plus a read_file
// call. Here one inspect_repository call, through the real agent loop and
// the real filesystem, must deliver both.
func TestIntegrationInspectRepositoryCompletesSearchEvidenceInOneToolCall(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "searching for the config key",
			toolCalls: []provider.ToolCall{toolCall("call_inspect", "inspect_repository",
				`{"query":"timeout_seconds","max_results":10,"context_lines":1}`)},
		},
		{content: "found it"},
	})
	content := "[server]\nhost = \"0.0.0.0\"\ntimeout_seconds = 30\nretries = 3\n"
	if err := os.WriteFile(filepath.Join(h.ws.Abs, "app.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	body := runToolStep(t, h, "find the timeout setting and its surrounding lines", "call_inspect")

	var out struct {
		Results []struct {
			Path    string   `json:"path"`
			Line    int      `json:"line"`
			Text    string   `json:"text"`
			Context []string `json:"context"`
		} `json:"results"`
		ResultCount int  `json:"result_count"`
		Truncated   bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("tool result is not valid JSON: %v\nbody=%s", err, body)
	}
	if out.ResultCount != 1 || len(out.Results) != 1 {
		t.Fatalf("result_count/results=%+v, want exactly 1 match", out)
	}
	r := out.Results[0]
	if r.Path != "app.toml" || r.Line != 3 {
		t.Fatalf("match location = %+v, want app.toml:3", r)
	}
	if !strings.Contains(r.Text, "timeout_seconds") {
		t.Fatalf("match text missing the query: %q", r.Text)
	}
	// The one call must have carried the surrounding lines too - the part
	// that previously required a second, separate read_file call.
	wantContext := []string{`host = "0.0.0.0"`, "retries = 3"}
	if len(r.Context) != len(wantContext) {
		t.Fatalf("context=%v, want %v", r.Context, wantContext)
	}
	for i := range wantContext {
		if r.Context[i] != wantContext[i] {
			t.Fatalf("context=%v, want %v", r.Context, wantContext)
		}
	}
}

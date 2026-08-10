package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/provider"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
)

// End-to-end coverage for the in-place edit tools: a scripted provider issues
// the tool call, the real agent loop builds the production dispatcher, the
// real tool touches the real filesystem, and the assertions are made on what
// reaches the model plus what is on disk afterwards.
//
// The unit tests in internal/tools call the tools through the registry, which
// proves the tools; these prove the WIRING - that a batched edit is dispatched
// as a write, that its diff survives the loop's result handling rather than
// being destroyed or tail-cut, and that a refused or failed edit reaches the
// model as usable guidance instead of a bare failure.

func writeWorkspaceFile(t *testing.T, ws, name, content string, mode os.FileMode) string {
	t.Helper()
	abs := filepath.Join(ws, name)
	if err := os.WriteFile(abs, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(abs, mode); err != nil {
		t.Fatal(err)
	}
	return abs
}

func readWorkspaceFile(t *testing.T, abs string) string {
	t.Helper()
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestIntegration_MultiEditAppliesBatchAndReachesModel: the whole path, on a
// file that is also an executable script - the case where a lost mode bit
// breaks a build silently, because no diff shows it.
func TestIntegration_MultiEditAppliesBatchAndReachesModel(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "applying the batch",
			toolCalls: []provider.ToolCall{toolCall("call_multi", "multi_edit",
				`{"path":"deploy.sh","edits":[`+
					`{"old_string":"REGION=us-east-1","new_string":"REGION=eu-west-1"},`+
					`{"old_string":"echo deploying","new_string":"echo deploying to $REGION"},`+
					`{"old_string":"retry","new_string":"backoff","replace_all":true}]}`)},
		},
		{content: "batch applied"},
	})
	script := "#!/bin/sh\nREGION=us-east-1\necho deploying\nretry\nretry\n"
	abs := writeWorkspaceFile(t, h.ws.Abs, "deploy.sh", script, 0o755)

	body := runToolStep(t, h, "update the deploy script", "call_multi")

	if strings.Contains(body, "output budget exceeded") {
		t.Fatalf("dispatcher destroyed the edit result: %q", body)
	}
	if !strings.Contains(body, "updated deploy.sh (3 edits, 4 replacements") {
		t.Fatalf("batch stats did not reach the model: %q", head(body))
	}
	if !strings.Contains(body, "--- a/deploy.sh") || !strings.Contains(body, "+++ b/deploy.sh") {
		t.Fatalf("unified diff framing did not reach the model: %q", head(body))
	}
	if strings.Contains(body, "... (truncated") {
		t.Fatalf("loop tail-cut a result inside its declared budget: tail=%q", tail(body))
	}

	want := "#!/bin/sh\nREGION=eu-west-1\necho deploying to $REGION\nbackoff\nbackoff\n"
	if got := readWorkspaceFile(t, abs); got != want {
		t.Fatalf("file on disk = %q, want %q", got, want)
	}
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no Unix permission bits: os.WriteFile(0755) reports
	// 0666 there because the mode only tracks the read-only attribute.
	// The mode-preservation contract is Unix-specific, so it is asserted
	// only where the filesystem can express it.
	if runtime.GOOS != "windows" && st.Mode().Perm() != 0o755 {
		t.Fatalf("mode after edit = %04o, want 0755", st.Mode().Perm())
	}
}

// TestIntegration_MultiEditFailedBatchLeavesFileUntouched: the model must get
// an error it can act on - which edit failed and why - and the file must be
// exactly as it was, not half-edited into a state nobody has seen.
func TestIntegration_MultiEditFailedBatchLeavesFileUntouched(t *testing.T) {
	h := newIntegrationHelper(t, []scriptedStep{
		{
			content: "applying the batch",
			toolCalls: []provider.ToolCall{toolCall("call_multi", "multi_edit",
				`{"path":"config.toml","edits":[`+
					`{"old_string":"port = 8080","new_string":"port = 9090"},`+
					`{"old_string":"tls = true","new_string":"tls = false"}]}`)},
		},
		{content: "batch failed"},
	})
	const original = "port = 8080\nhost = \"localhost\"\n"
	abs := writeWorkspaceFile(t, h.ws.Abs, "config.toml", original, 0o644)

	body := runToolStep(t, h, "reconfigure the service", "call_multi")

	if !strings.Contains(body, "edit 2/2") {
		t.Fatalf("model cannot tell which edit failed: %q", body)
	}
	if !strings.Contains(body, "old_string not found") {
		t.Fatalf("model cannot tell why the edit failed: %q", body)
	}
	if got := readWorkspaceFile(t, abs); got != original {
		t.Fatalf("failed batch was partially applied: %q", got)
	}
}

// TestIntegration_EditToolsRefuseOversizeFileWithGuidance: with an explicit
// max_read_bytes, a file above the bound must come back as instructions the
// model can follow, not as a destroyed result or a bare error - the whole
// point of the guard is that the agent has somewhere to go next.
func TestIntegration_EditToolsRefuseOversizeFileWithGuidance(t *testing.T) {
	h := newIntegrationHelperWithOpts(t, []scriptedStep{
		{
			content: "editing the log",
			toolCalls: []provider.ToolCall{toolCall("call_edit", "search_replace",
				`{"path":"huge.log","old_string":"ERROR","new_string":"WARN","replace_all":true}`)},
		},
		{content: "edit refused"},
	}, tools.DefaultOptions{MaxReadBytes: 2048})
	big := strings.Repeat("ERROR something went wrong\n", 500) // ~13 KiB
	abs := writeWorkspaceFile(t, h.ws.Abs, "huge.log", big, 0o644)

	body := runToolStep(t, h, "downgrade the errors", "call_edit")

	for _, want := range []string{"too large", "huge.log", "2048", "read_file", "write_file"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal reaching the model does not mention %q: %q", want, body)
		}
	}
	if got := readWorkspaceFile(t, abs); got != big {
		t.Fatal("refused edit modified the file")
	}
}

// TestIntegration_MultiEditIsRegisteredForTheModel: a tool the model is never
// offered cannot be called. This pins the tool onto the wire schema the loop
// sends, not just into the registry.
func TestIntegration_MultiEditIsRegisteredForTheModel(t *testing.T) {
	h := newIntegrationHelper(t, nil)
	var found map[string]any
	for _, spec := range h.reg.OpenAITools() {
		fn, ok := spec["function"].(map[string]any)
		if !ok {
			continue
		}
		if fn["name"] == "multi_edit" {
			found = fn
			break
		}
	}
	if found == nil {
		t.Fatal("multi_edit is missing from the tool schema sent to the provider")
	}
	desc, _ := found["description"].(string)
	if !strings.Contains(strings.ToLower(desc), "all-or-nothing") {
		t.Errorf("description does not state the atomicity guarantee: %q", desc)
	}
	params, ok := found["parameters"].(map[string]any)
	if !ok {
		t.Fatal("multi_edit publishes no parameter schema")
	}
	required := params["required"]
	if fmt.Sprint(required) != "[path edits]" {
		t.Fatalf("required = %v, want [path edits]", required)
	}
}

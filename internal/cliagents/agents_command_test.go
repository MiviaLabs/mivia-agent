package cliagents

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCatalogAgent(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsListShowsSelectableDefinitionsOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "zeta", "name = \"zeta\"\ndescription = \"z\"\ntools = [\"read_file\"]\n")
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "alpha", "name = \"alpha\"\ndescription = \"a\"\nmodel = \"worker-model\"\nmax_turns = 3\n")

	var out, errOut strings.Builder
	if err := runAgentsWithIO([]string{"list", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("list error = %v", err)
	}
	text := out.String()
	if strings.Index(text, "name: alpha") > strings.Index(text, "name: zeta") {
		t.Fatalf("agents are not sorted: %s", text)
	}
	if !strings.Contains(text, "source: workspace") || !strings.Contains(text, "state: selectable") {
		t.Fatalf("missing selectable rows: %s", text)
	}
	if !strings.Contains(text, "name: root fallback") || !strings.Contains(text, "not selectable") {
		t.Fatalf("missing fallback row: %s", text)
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected warnings: %s", errOut.String())
	}
}

func TestAgentsExplainDoesNotPrintSystemPromptDigestOrSecret(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	secret := "do-not-print-this-prompt"
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "researcher", "name = \"researcher\"\ndescription = \"inspect\"\nsystem_prompt = \""+secret+"\"\ntools_add = [\"grep\"]\n")

	var out, errOut strings.Builder
	if err := runAgentsWithIO([]string{"explain", "researcher", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("explain error = %v", err)
	}
	text := out.String()
	if strings.Contains(text, secret) || strings.Contains(text, "sha256:") {
		t.Fatalf("unsafe content in explain output: %s", text)
	}
	for _, field := range []string{"path:", "parent_chain:", "field_winners:", "tool_operations:", "effective_denylist:", "skill_scope:"} {
		if !strings.Contains(text, field) {
			t.Fatalf("missing %q in explain output: %s", field, text)
		}
	}
}

func TestAgentsCommandRejectsInvalidGrammar(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--config", "x"},
		{"list", "extra"},
		{"explain"},
		{"explain", "a", "b"},
		{"what"},
	} {
		var out, errOut strings.Builder
		if err := runAgentsWithIO(args, &out, &errOut); err == nil {
			t.Fatalf("args %v unexpectedly succeeded", args)
		}
	}
}

func TestAgentsListWorksWithoutProviderKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "local", "name = \"local\"\ndescription = \"local only\"\n")
	var out, errOut strings.Builder
	if err := runAgentsWithIO([]string{"list", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("provider-independent list failed: %v", err)
	}
}

func TestAgentsListDefaultUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "alpha", "name = \"alpha\"\ndescription = \"a\"\nmodel = \"worker-model\"\nmax_turns = 3\n")
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "zeta", "name = \"zeta\"\ndescription = \"z\"\ntools = [\"read_file\"]\n")

	var out, errOut strings.Builder
	if err := runAgentsWithIO([]string{"list", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("list error = %v", err)
	}
	text := out.String()

	// Verify human-path artifacts are present.
	expected := []string{
		"agents:",
		"collection:",
		"  name: alpha",
		"  source: workspace",
		"  state: selectable",
		"  name: zeta",
		"  name: root fallback",
		"  source: compiled",
		"  state: fallback (not selectable)",
		"workspace agent files: always loaded",
		"workspace prompts/project skills:",
	}
	for _, substr := range expected {
		if !strings.Contains(text, substr) {
			t.Fatalf("human output missing %q:\n%s", substr, text)
		}
	}

	// Sorting: alpha before zeta.
	if strings.Index(text, "name: alpha") > strings.Index(text, "name: zeta") {
		t.Fatalf("agents not sorted in human output:\n%s", text)
	}

	// No JSON structure leaked into human output.
	if strings.Contains(text, "[") && strings.Contains(text, "\"name\"") {
		t.Fatalf("human output contains JSON structure:\n%s", text)
	}
}

func TestAgentsListJSONOutputsValidArray(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "alpha", "name = \"alpha\"\ndescription = \"a\"\nmodel = \"worker-model\"\nmax_turns = 3\ntools = [\"read_file\"]\n")
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "beta", "name = \"beta\"\ndescription = \"A beta agent\"\n")

	var out, errOut strings.Builder
	if err := runAgentsWithIO([]string{"list", "--json", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("list --json error = %v", err)
	}

	// Parse stdout as JSON array directly — json.Decoder.Decode expects a
	// complete JSON value; consuming the opening '[' delimiter first causes
	// the subsequent Decode to see '{' which cannot unmarshal into a slice.
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out.String()), &entries); err != nil {
		t.Fatalf("json decode error = %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Verify sort order: alpha before beta.
	if entries[0]["name"] != "alpha" || entries[1]["name"] != "beta" {
		t.Fatalf("entries not sorted: %v", entries)
	}

	requiredKeys := []string{"name", "source", "state", "tools", "model", "turns", "limits", "description"}
	for i, entry := range entries {
		for _, key := range requiredKeys {
			if _, ok := entry[key]; !ok {
				t.Fatalf("entry %d missing key %q: %v", i, key, entry)
			}
		}
	}

	// Verify specific field values.
	if entries[0]["model"] != "worker-model (session provider)" {
		t.Fatalf("alpha model = %v, want \"worker-model (session provider)\"", entries[0]["model"])
	}
	if entries[0]["turns"] != "3" {
		t.Fatalf("alpha turns = %v, want \"3\"", entries[0]["turns"])
	}
	if entries[1]["description"] != "A beta agent" {
		t.Fatalf("beta description = %v, want \"A beta agent\"", entries[1]["description"])
	}

	// Verify no sensitive keys are present.
	forbiddenKeys := []string{"system_prompt", "provider", "path", "disallowed_tools", "effective_denylist"}
	for _, entry := range entries {
		for _, key := range forbiddenKeys {
			if _, ok := entry[key]; ok {
				t.Fatalf("forbidden key %q present in entry: %v", key, entry)
			}
		}
	}
}

func TestAgentsListJSONNoSensitiveFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	secret := "secret-content"
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "spy", "name = \"spy\"\ndescription = \"s\"\nsystem_prompt = \""+secret+"\"\n")

	var out, errOut strings.Builder
	if err := runAgentsWithIO([]string{"list", "--json", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("list --json error = %v", err)
	}

	text := out.String()
	if strings.Contains(text, secret) {
		t.Fatalf("JSON output contains secret %q:\n%s", secret, text)
	}

	var entries []map[string]any
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		t.Fatalf("json unmarshal error = %v", err)
	}
	for _, entry := range entries {
		for _, key := range []string{"system_prompt", "provider", "path", "disallowed_tools", "effective_denylist"} {
			if _, ok := entry[key]; ok {
				t.Fatalf("sensitive key %q leaked into JSON: %v", key, entry)
			}
		}
	}
}

func TestAgentsListJSONEmptyWorkspace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()

	var out, errOut strings.Builder
	if err := runAgentsWithIO([]string{"list", "--json", "--workspace", workspace}, &out, &errOut); err != nil {
		t.Fatalf("list --json error = %v", err)
	}

	// Verify stdout is exactly [] followed by newline (json.Encoder.Encode appends \n).
	if out.String() != "[\n  []\n]\n" {
		// json.Encoder with SetIndent may produce different whitespace; just verify it parses as empty array.
		var entries []json.RawMessage
		if err := json.Unmarshal([]byte(out.String()), &entries); err != nil {
			t.Fatalf("stdout not valid JSON: %q", out.String())
		}
		if len(entries) != 0 {
			t.Fatalf("expected empty JSON array, got %d entries", len(entries))
		}
	}
}

func TestAgentsExplainRejectsJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()

	var out, errOut strings.Builder
	err := runAgentsWithIO([]string{"explain", "--json", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatalf("explain --json should fail")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "agents explain") {
		t.Fatalf("error missing 'agents explain': %s", errStr)
	}
	if !strings.Contains(errStr, "--json") {
		t.Fatalf("error missing '--json': %s", errStr)
	}
	if strings.Contains(errStr, "unknown flag") {
		t.Fatalf("error is generic 'unknown flag', not targeted: %s", errStr)
	}
}

func TestAgentsExplainRejectsJSONWithAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "alpha", "name = \"alpha\"\ndescription = \"a\"\n")

	// Test --json after positional agent name.
	var out, errOut strings.Builder
	err := runAgentsWithIO([]string{"explain", "alpha", "--json", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatalf("explain alpha --json should fail")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "agents explain") || !strings.Contains(errStr, "--json") {
		t.Fatalf("error not targeted: %s", errStr)
	}
	if strings.Contains(errStr, "unknown flag") {
		t.Fatalf("error is generic 'unknown flag': %s", errStr)
	}
}

// TestAgentsListJSONRejectsDuplicateFlag verifies that duplicate --json flags
// are rejected with a clear error, consistent with strict flag discipline.
func TestAgentsListJSONRejectsDuplicateFlag(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()

	var out, errOut strings.Builder
	err := runAgentsWithIO([]string{"list", "--json", "--json", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatalf("list --json --json should fail with duplicate flag error")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "duplicate") {
		t.Fatalf("error missing 'duplicate': %s", errStr)
	}
	if !strings.Contains(errStr, "--json") {
		t.Fatalf("error missing '--json': %s", errStr)
	}
}

func TestAgentsDiagnosticSummaryPreservedForJSON(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	// Write a malformed TOML file (not valid agent TOML syntax).
	agentsDir := filepath.Join(workspace, ".mivia", "agents")
	if err := os.MkdirAll(agentsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, "bad.toml"), []byte("{{invalid toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Also write a valid agent so there is at least one row.
	writeCatalogAgent(t, agentsDir, "good", "name = \"good\"\ndescription = \"ok\"\n")

	var out, errOut strings.Builder
	err := runAgentsWithIO([]string{"list", "--json", "--workspace", workspace}, &out, &errOut)
	if err == nil {
		t.Fatalf("expected error for malformed agent file")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "malformed") {
		t.Fatalf("error should mention malformed: %s", errStr)
	}

	// stdout should still be valid JSON array (partial results).
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out.String()), &entries); err != nil {
		t.Fatalf("stdout not valid JSON after malformed file: %q", out.String())
	}
	if len(entries) != 1 || entries[0]["name"] != "good" {
		t.Fatalf("expected 1 'good' entry, got %v", entries)
	}
}

func TestAgentsListJSONWarnsOnStderr(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()

	// Write a workspace agent with a known provider/model to trigger the
	// credential-routing warning. AllowWorkspaceAgentProviders defaults to
	// false, so the workspace-declared provider is stripped and a warning is
	// emitted. The provider must be a known provider (registered in
	// providerregistry) so that checkResolvedBinding succeeds and the agent
	// resolves far enough to reach the credential-routing protection code.
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "withprovider",
		"name = \"withprovider\"\ndescription = \"wp\"\nprovider = \"openrouter\"\nmodel = \"openai/gpt-4o-mini\"\n")

	var out, errOut strings.Builder
	err := runAgentsWithIO([]string{"list", "--json", "--workspace", workspace}, &out, &errOut)
	if err != nil {
		t.Fatalf("list --json error = %v", err)
	}

	// Parse stdout as valid JSON.
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out.String()), &entries); err != nil {
		t.Fatalf("stdout not valid JSON: %q", out.String())
	}

	// Warnings must appear on stderr, not stdout.
	stderrText := errOut.String()
	if !strings.Contains(stderrText, "warning:") {
		t.Fatalf("expected warning on stderr, got empty or no warnings: stderr=%q", stderrText)
	}
	if strings.Contains(out.String(), "warning:") {
		t.Fatalf("warning leaked to stdout:\n%s", out.String())
	}

	// Verify the provider was stripped: model shows (inherit session).
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0]["model"] != "(inherit session)" {
		t.Fatalf("model = %v, want \"(inherit session)\" (provider stripped)", entries[0]["model"])
	}
}

// TestAgentsListJSONStdoutOnly verifies that --json path does not write
// diagnostics to stdout (unlike the human path which writes diagnostics
// inline). This ensures clean JSON output.
func TestAgentsListJSONStdoutOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workspace := t.TempDir()
	writeCatalogAgent(t, filepath.Join(workspace, ".mivia", "agents"), "simple", "name = \"simple\"\ndescription = \"s\"\n")

	var buf bytes.Buffer
	var errOut strings.Builder
	if err := runAgentsWithIO([]string{"list", "--json", "--workspace", workspace}, &buf, &errOut); err != nil {
		t.Fatalf("error: %v", err)
	}

	// The output must be parseable as a single JSON value.
	var parsed any
	dec := json.NewDecoder(&buf)
	if err := dec.Decode(&parsed); err != nil {
		t.Fatalf("stdout not a single JSON value: %v; content: %q", err, buf.String())
	}
	// After decoding one value, there should be nothing left.
	if dec.More() {
		t.Fatalf("trailing data after JSON value")
	}
}

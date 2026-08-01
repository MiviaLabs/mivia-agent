package cli

import (
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

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/config"
	"github.com/MiviaLabs/mivia-agent/internal/memory"
)

// memorySearchJSONProbe mirrors the JSON object shape documented for
// `mivia memory search --json`. It is a test-side probe: the production
// struct lives in memory_command.go and must emit exactly these fields.
type memorySearchJSONProbe struct {
	ID      string   `json:"id"`
	Scope   string   `json:"scope"`
	Org     string   `json:"org"`
	Title   string   `json:"title"`
	Verdict string   `json:"verdict"`
	Tags    []string `json:"tags"`
	Created string   `json:"created"`
	Summary string   `json:"summary"`
}

// writeMemoryTestConfig writes a workspace .mivia/mivia.toml with a [memory]
// section and returns the config path. HOME and MIVIA_CONFIG are isolated so
// the real user config and the real memory database are never touched.
func writeMemoryTestConfig(t *testing.T, root string, enabled bool) string {
	t.Helper()
	return writeMemoryTestConfigPath(t, root, enabled, ".mivia/memory.db")
}

func writeMemoryTestConfigPath(t *testing.T, root string, enabled bool, storePath string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MIVIA_CONFIG", "")
	dir := filepath.Join(root, ".mivia")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "mivia.toml")
	body := fmt.Sprintf(`[provider]
name = "deepseek"

[providers.deepseek]
models = [{ name = "deepseek-v4-flash", context_window_tokens = 128000 }]

[memory]
enabled = %t
store_backend = "sqlite"
store_path = %q
max_search_results = 8
`, enabled, storePath)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

// saveTestMemories opens the workspace store directly, saves three
// project-scoped entries, and closes it. The entries share words so a single
// query can match several of them ("fix", "ci", "org").
func saveTestMemories(t *testing.T, root string) {
	t.Helper()
	enabled := true
	mc := config.MemoryConfig{
		Enabled:          &enabled,
		StoreBackend:     "sqlite",
		StorePath:        ".mivia/memory.db",
		MaxSearchResults: 8,
	}
	store, err := openMemoryStore(root, mc)
	if err != nil {
		t.Fatalf("openMemoryStore: %v", err)
	}
	defer store.Close()
	entries := []memory.Entry{
		{
			Title:   "Deploy pipeline fix",
			Scope:   memory.ScopeProject,
			Verdict: memory.VerdictGood,
			Created: "2026-08-01",
			Tags:    []string{"deploy", "ci"},
			Summary: "Pinned the runner image to fix flaky deploys in CI.",
			Why:     "learned during incident 42",
		},
		{
			Title:   "SQLite WAL on CI",
			Scope:   memory.ScopeProject,
			Verdict: memory.VerdictBad,
			Created: "2026-08-02",
			Tags:    []string{"sqlite"},
			Summary: "The wal checkpoint fix keeps the main database current.",
			Why:     "learned during CI setup",
		},
		{
			Title:   "Org review cadence",
			Scope:   memory.ScopeProject,
			Verdict: memory.VerdictNeutral,
			Created: "2026-08-03",
			Tags:    []string{"org", "review"},
			Summary: "Weekly org reviews keep the org store tidy.",
			Why:     "org policy",
		},
	}
	for _, e := range entries {
		if _, err := store.Save(context.Background(), e); err != nil {
			t.Fatalf("save %q: %v", e.Title, err)
		}
	}
}

// TestParseMemorySearchArgsRejectsBadInput covers the parse failures: missing
// query, unknown flag, invalid scope, non-numeric limit, duplicate --json,
// and value flags without a directory/path value.
func TestParseMemorySearchArgsRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing query", nil, "query"},
		{"blank query", []string{"   "}, "query"},
		{"unknown flag", []string{"fix", "--bogus"}, "unknown flag"},
		{"invalid scope", []string{"fix", "--scope", "global"}, "scope"},
		{"invalid scope equals", []string{"fix", "--scope=global"}, "scope"},
		{"non-numeric limit", []string{"fix", "--limit", "abc"}, "limit"},
		{"limit dash value", []string{"fix", "--limit", "-3"}, "requires a value"},
		{"duplicate json", []string{"fix", "--json", "--json"}, "duplicate --json"},
		{"workspace missing value", []string{"fix", "--workspace"}, "requires a directory"},
		{"workspace dash value", []string{"fix", "--workspace", "--json"}, "requires a directory"},
		{"workspace empty equals", []string{"fix", "--workspace="}, "requires a directory"},
		{"workspace dash equals", []string{"fix", "--workspace=-x"}, "requires a directory"},
		{"config missing value", []string{"fix", "--config"}, "requires a path"},
		{"config dash equals", []string{"fix", "--config=-x"}, "requires a path"},
		{"json with value", []string{"fix", "--json=1"}, "unknown flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, _, _, err := parseMemorySearchArgs(tc.args)
			if err == nil {
				t.Fatalf("parseMemorySearchArgs(%v) returned nil error", tc.args)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("parseMemorySearchArgs(%v) error = %q, want contains %q", tc.args, err, tc.want)
			}
		})
	}
}

// TestParseMemorySearchArgsValid covers the accepted forms: multi-word query
// join, --scope=all equals form, --limit with a value, --json, --workspace,
// and --config.
func TestParseMemorySearchArgsValid(t *testing.T) {
	query, scope, limit, jsonFlag, workspaceRoot, configPath, err := parseMemorySearchArgs([]string{
		"deploy", "pipeline", "fix", "--scope=all", "--limit", "5", "--json",
		"--workspace", "/tmp/ws", "--config", "/tmp/cfg.toml",
	})
	if err != nil {
		t.Fatalf("parseMemorySearchArgs: %v", err)
	}
	if query != "deploy pipeline fix" {
		t.Errorf("query = %q, want multi-word join", query)
	}
	if scope != memory.ScopeAll {
		t.Errorf("scope = %q, want all", scope)
	}
	if limit != 5 {
		t.Errorf("limit = %d, want 5", limit)
	}
	if !jsonFlag {
		t.Error("jsonFlag = false, want true")
	}
	if workspaceRoot != "/tmp/ws" || configPath != "/tmp/cfg.toml" {
		t.Errorf("workspace = %q config = %q", workspaceRoot, configPath)
	}
}

// TestParseMemorySearchArgsDefaults pins the defaults: scope all, limit 0
// (the store clamps to max_search_results), and json off.
func TestParseMemorySearchArgsDefaults(t *testing.T) {
	query, scope, limit, jsonFlag, workspaceRoot, configPath, err := parseMemorySearchArgs([]string{"fix"})
	if err != nil {
		t.Fatalf("parseMemorySearchArgs: %v", err)
	}
	if query != "fix" {
		t.Errorf("query = %q", query)
	}
	if scope != memory.ScopeAll {
		t.Errorf("scope = %q, want all", scope)
	}
	if limit != 0 {
		t.Errorf("limit = %d, want 0 (store clamps)", limit)
	}
	if jsonFlag {
		t.Error("jsonFlag = true, want false")
	}
	if workspaceRoot != "" || configPath != "" {
		t.Errorf("workspace = %q config = %q, want empty defaults", workspaceRoot, configPath)
	}
}

// TestParseMemorySearchArgsZeroLimitDefaultsToStoreClamp pins that a numeric
// limit <= 0 is accepted and passed as MaxResults=0 so the store clamps to
// [memory] max_search_results (non-numeric values are rejected above).
func TestParseMemorySearchArgsZeroLimitDefaultsToStoreClamp(t *testing.T) {
	for _, args := range [][]string{
		{"fix", "--limit", "0"},
		{"fix", "--limit=-2"},
	} {
		_, _, limit, _, _, _, err := parseMemorySearchArgs(args)
		if err != nil {
			t.Fatalf("parseMemorySearchArgs(%v) error = %v", args, err)
		}
		if limit != 0 {
			t.Fatalf("parseMemorySearchArgs(%v) limit = %d, want 0", args, limit)
		}
	}
}

// TestMemorySearchEndToEndHuman runs the command with a real temp workspace
// and asserts the ranked human list carries titles, scope, verdict, created
// date, tags, and summary snippets.
func TestMemorySearchEndToEndHuman(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err != nil {
		t.Fatalf("runMemoryWithIO: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "1.") || !strings.Contains(text, "2.") {
		t.Fatalf("human output lacks ranked entries:\n%s", text)
	}
	for _, want := range []string{
		"Deploy pipeline fix",
		"SQLite WAL on CI",
		"scope: project",
		"verdict: good",
		"verdict: bad",
		"created: 2026-08-01",
		"created: 2026-08-02",
		"tags: deploy, ci",
		"tags: sqlite",
		"Pinned the runner image",
		"The wal checkpoint fix",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("human output missing %q:\n%s", want, text)
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("unexpected stderr: %q", errOut.String())
	}
}

// failingWriter is an io.Writer that always fails, forcing the JSON encoder's
// write error branch in writeMemorySearchJSON.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

// TestMemorySearchParseError covers the runMemoryWithIO branch that returns a
// parseMemorySearchArgs failure unchanged.
func TestMemorySearchParseError(t *testing.T) {
	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "--bogus"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("runMemoryWithIO error = %v, want unknown flag", err)
	}
}

// TestMemorySearchResolveWorkspaceError covers the chatWorkspaceRoot failure
// branch: filepath.Abs errors when the process working directory no longer
// exists. The test is intentionally sequential (no t.Parallel): it changes the
// process cwd, and parallel tests only run after sequential tests finish.
func TestMemorySearchResolveWorkspaceError(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("macOS getcwd does not fail after the cwd directory is removed; this test needs the Linux stale-cwd quirk")
	}
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(origWD); err != nil {
			t.Errorf("restore cwd: %v", err)
		}
	}()
	// Removing the process cwd succeeds (the kernel keeps a reference); the
	// next getcwd then fails, which is exactly the filepath.Abs error path.
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	err = runMemoryWithIO([]string{"search", "fix"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "resolve workspace") {
		t.Fatalf("runMemoryWithIO error = %v, want resolve workspace failure", err)
	}
}

// TestMemorySearchConfigLoadError covers the config.Load failure branch: an
// invalid config document errors even with AllowMissingConfig set.
func TestMemorySearchConfigLoadError(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "bad.toml")
	if err := os.WriteFile(bad, []byte("this is not [valid toml"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--workspace", root, "--config", bad}, &out, &errOut)
	if err == nil {
		t.Fatal("runMemoryWithIO: want config load error")
	}
}

// TestMemorySearchStoreSearchError covers the store.Search failure branch: the
// read-only store opens lazily (sql.Open does not touch the file), so a corrupt
// database file fails on the first query inside Search.
func TestMemorySearchStoreSearchError(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfigPath(t, root, true, ".mivia/corrupt.db")
	if err := os.WriteFile(filepath.Join(root, ".mivia", "corrupt.db"), []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "memory search:") {
		t.Fatalf("runMemoryWithIO error = %v, want memory search failure", err)
	}
}

// TestWriteMemorySearchJSONEncodeError covers the JSON encoder write-failure
// branch in writeMemorySearchJSON.
func TestWriteMemorySearchJSONEncodeError(t *testing.T) {
	err := writeMemorySearchJSON(failingWriter{}, []memory.Result{{ID: "1", Title: "t"}})
	if err == nil || !strings.Contains(err.Error(), "json encode failed") {
		t.Fatalf("writeMemorySearchJSON error = %v, want json encode failure", err)
	}
}

// TestMemorySearchEndToEndJSON runs the command with --json and asserts the
// output parses as a JSON array with the documented fields.
func TestMemorySearchEndToEndJSON(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "deploy", "--json", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err != nil {
		t.Fatalf("runMemoryWithIO: %v", err)
	}
	var results []memorySearchJSONProbe
	if err := json.Unmarshal([]byte(out.String()), &results); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out.String())
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1\n%s", len(results), out.String())
	}
	r := results[0]
	if r.Title != "Deploy pipeline fix" {
		t.Errorf("title = %q", r.Title)
	}
	if r.Scope != "project" || r.Verdict != "good" || r.Org != "" {
		t.Errorf("metadata = scope %q verdict %q org %q", r.Scope, r.Verdict, r.Org)
	}
	if r.Created != "2026-08-01" {
		t.Errorf("created = %q", r.Created)
	}
	if strings.Join(r.Tags, ",") != "deploy,ci" {
		t.Errorf("tags = %v", r.Tags)
	}
	if !strings.Contains(r.Summary, "Pinned the runner image") {
		t.Errorf("summary = %q", r.Summary)
	}
	if r.ID == "" {
		t.Error("id must not be empty")
	}
}

// TestMemorySearchScopeProjectFilters pins that --scope project searches only
// the project store.
func TestMemorySearchScopeProjectFilters(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--scope", "project", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err != nil {
		t.Fatalf("runMemoryWithIO: %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "Deploy pipeline fix") || !strings.Contains(text, "SQLite WAL on CI") {
		t.Fatalf("project scope output missing project entries:\n%s", text)
	}
	if strings.Contains(text, "Org review cadence") {
		t.Fatalf("project scope leaked an org entry:\n%s", text)
	}
}

// TestMemorySearchOrgScopeWithoutOrgIDEmpty pins that org scope without a
// configured org identity returns empty results and no error.
func TestMemorySearchOrgScopeWithoutOrgIDEmpty(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--scope", "org", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err != nil {
		t.Fatalf("org scope without org_id must not error: %v", err)
	}
	if !strings.Contains(out.String(), "no memories found") {
		t.Fatalf("expected friendly empty output, got: %q", out.String())
	}
}

// TestMemorySearchUnknownQueryFriendlyEmpty pins that a query with no matches
// prints a friendly single line and exits 0 (no error).
func TestMemorySearchUnknownQueryFriendlyEmpty(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "zzzznope", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err != nil {
		t.Fatalf("unknown query must not error: %v", err)
	}
	if !strings.Contains(out.String(), "no memories found") {
		t.Fatalf("expected friendly empty output, got: %q", out.String())
	}
}

// TestMemorySearchLimitZeroReturnsResults pins that --limit 0 is accepted and
// the store clamps to max_search_results (all matches come back).
func TestMemorySearchLimitZeroReturnsResults(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--limit", "0", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err != nil {
		t.Fatalf("--limit 0 must not error: %v", err)
	}
	if !strings.Contains(out.String(), "Deploy pipeline fix") {
		t.Fatalf("--limit 0 must return results, got: %q", out.String())
	}
}

// TestMemorySearchLimitCapsResults pins that a positive --limit caps the
// result count.
func TestMemorySearchLimitCapsResults(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--limit", "1", "--json", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err != nil {
		t.Fatalf("runMemoryWithIO: %v", err)
	}
	var results []memorySearchJSONProbe
	if err := json.Unmarshal([]byte(out.String()), &results); err != nil {
		t.Fatalf("stdout is not a JSON array: %v\n%s", err, out.String())
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1 (limited)", len(results))
	}
}

// TestMemorySearchDisabledMemoryError pins that memory disabled in the config
// is an error mentioning [memory] enabled.
func TestMemorySearchDisabledMemoryError(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, false)

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err == nil {
		t.Fatal("disabled memory must return an error")
	}
	if !strings.Contains(err.Error(), "memory") || !strings.Contains(err.Error(), "[memory]") || !strings.Contains(err.Error(), "enabled") {
		t.Fatalf("error = %q, want a [memory] enabled mention", err)
	}
}

// TestMemorySearchStoreOpenFailure pins that a store open failure (escaping
// store_path) surfaces as an error.
func TestMemorySearchStoreOpenFailure(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfigPath(t, root, true, "../escape.db")

	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"search", "fix", "--workspace", root, "--config", cfgPath}, &out, &errOut)
	if err == nil {
		t.Fatal("escaping store_path must fail the search")
	}
	if !strings.Contains(err.Error(), "memory") {
		t.Fatalf("error = %q, want a memory mention", err)
	}
}

// TestRunMemoryRejectsUnknownSubcommand pins the subcommand gate: only
// "search" is accepted.
func TestRunMemoryRejectsUnknownSubcommand(t *testing.T) {
	var out, errOut strings.Builder
	err := runMemoryWithIO([]string{"list"}, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("runMemoryWithIO([list]) error = %v", err)
	}
	err = runMemoryWithIO(nil, &out, &errOut)
	if err == nil || !strings.Contains(err.Error(), "search") {
		t.Fatalf("runMemoryWithIO(nil) error = %v", err)
	}
}

// TestExecuteMemorySearchRoutesToCommand pins root dispatch: Execute routes
// "memory search ..." into the memory command and prints results.
func TestExecuteMemorySearchRoutesToCommand(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeMemoryTestConfig(t, root, true)
	saveTestMemories(t, root)

	done := captureStdout(t)
	defer done()
	err := Execute([]string{"memory", "search", "deploy", "--workspace", root, "--config", cfgPath})
	stdout := done()
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(stdout, "Deploy pipeline fix") {
		t.Fatalf("dispatch output = %q", stdout)
	}
}

// TestUsageTextDocumentsMemorySearch pins that the documented flag surface is
// present in the help body.
func TestUsageTextDocumentsMemorySearch(t *testing.T) {
	text := usageText()
	for _, want := range []string{
		"memory search",
		"--scope project|org|all",
		"--limit N",
		"--json",
		"--workspace dir",
		"--config path",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("usageText missing %q", want)
		}
	}
}

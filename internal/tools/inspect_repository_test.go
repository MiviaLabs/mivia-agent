package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testMinInspectRepositoryBytes mirrors config.MinInspectRepositoryBytes
// (4 KiB). Duplicated as a literal here rather than importing internal/config,
// which internal/tools does not otherwise depend on.
const testMinInspectRepositoryBytes = 4 << 10

func execInspect(t *testing.T, reg *Registry, argsJSON string) inspectOutput {
	t.Helper()
	raw, err := reg.Execute(context.Background(), "inspect_repository", json.RawMessage(argsJSON))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out inspectOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw=%s", err, raw)
	}
	return out
}

func execInspectErr(t *testing.T, reg *Registry, argsJSON string) error {
	t.Helper()
	_, err := reg.Execute(context.Background(), "inspect_repository", json.RawMessage(argsJSON))
	if err == nil {
		t.Fatalf("expected error, got none")
	}
	return err
}

// --- Wave 1: schema and config ---

func TestInspectRepositorySchemaRejectsInvalidModeAndBounds(t *testing.T) {
	_, reg := setupWS(t)

	cases := []string{
		`{"paths":["."],"max_results":10}`,                    // missing query
		`{"query":"foo"}`,                                     // missing max_results
		`{"query":"foo","max_results":0}`,                     // max_results below min
		`{"query":"foo","max_results":101}`,                   // max_results above max
		`{"query":"foo","max_results":10,"context_lines":-1}`, // context_lines below min
		`{"query":"foo","max_results":10,"context_lines":11}`, // context_lines above max
		`{"query":"foo","max_results":10,"paths":[]}`,         // empty path list
		`{"query":"foo","max_results":10,"paths":["a","a"]}`,  // duplicate normalized path
		`{"query":"foo","max_results":10,"mode":"list"}`,      // unknown field
		`{"query":"(","max_results":10}`,                      // invalid regex
	}
	for _, args := range cases {
		if err := execInspectErr(t, reg, args); err == nil {
			t.Errorf("args=%s: expected rejection", args)
		}
	}
}

// --- Wave 2: engine behavior ---

func writeFile(t *testing.T, ws string, rel, content string) {
	t.Helper()
	abs := filepath.Join(ws, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspectRepositorySearchSortsAndDeduplicates(t *testing.T) {
	ws, reg := setupWS(t)
	writeFile(t, ws.Abs, "b.txt", "needle\n")
	writeFile(t, ws.Abs, "a.txt", "needle\nneedle\n")

	out := execInspect(t, reg, `{"query":"needle","max_results":10}`)
	if len(out.Results) != 3 {
		t.Fatalf("results=%d, want 3: %+v", len(out.Results), out.Results)
	}
	for i := 1; i < len(out.Results); i++ {
		prev, cur := out.Results[i-1], out.Results[i]
		if prev.Path > cur.Path || (prev.Path == cur.Path && prev.Line > cur.Line) {
			t.Fatalf("results not sorted by (path, line): %+v", out.Results)
		}
	}
	// Re-run with overlapping scopes over the same file: identical results
	// (same path+line) must not be duplicated.
	out2 := execInspect(t, reg, `{"query":"needle","max_results":10,"paths":[".","a.txt"]}`)
	seen := map[string]bool{}
	for _, r := range out2.Results {
		key := r.Path + ":" + string(rune(r.Line))
		if seen[key] {
			t.Fatalf("duplicate result: %+v", r)
		}
		seen[key] = true
	}
}

func TestInspectRepositoryHonorsPathsGlobIgnoreAndSecretPolicy(t *testing.T) {
	ws, reg := setupWS(t)
	writeFile(t, ws.Abs, "src/keep.go", "needle in go\n")
	writeFile(t, ws.Abs, "src/skip.md", "needle in md\n")
	writeFile(t, ws.Abs, "vendor/dep.go", "needle in vendor\n")
	writeFile(t, ws.Abs, ".env", "needle in secret\n")

	out := execInspect(t, reg, `{"query":"needle","max_results":10,"glob":"*.go"}`)
	for _, r := range out.Results {
		if strings.Contains(r.Path, "vendor/") {
			t.Fatalf("vendor/ must be ignored by default, got %+v", r)
		}
		if strings.HasSuffix(r.Path, ".md") {
			t.Fatalf("glob filter leaked a .md result: %+v", r)
		}
		if r.Path == ".env" {
			t.Fatalf("secret path must never be reported: %+v", r)
		}
	}
	foundGo := false
	for _, r := range out.Results {
		if r.Path == "src/keep.go" {
			foundGo = true
		}
	}
	if !foundGo {
		t.Fatalf("expected src/keep.go to match, got %+v", out.Results)
	}

	scoped := execInspect(t, reg, `{"query":"needle","max_results":10,"paths":["src"]}`)
	for _, r := range scoped.Results {
		if !strings.HasPrefix(r.Path, "src/") {
			t.Fatalf("scoped search leaked outside paths: %+v", r)
		}
	}
}

func TestInspectRepositoryContextWindowsAreExact(t *testing.T) {
	ws, reg := setupWS(t)
	writeFile(t, ws.Abs, "f.txt", "l1\nl2\nneedle\nl4\nl5\n")

	out := execInspect(t, reg, `{"query":"needle","max_results":10,"context_lines":2}`)
	if len(out.Results) != 1 {
		t.Fatalf("results=%d, want 1", len(out.Results))
	}
	r := out.Results[0]
	if r.Line != 3 {
		t.Fatalf("line=%d, want 3", r.Line)
	}
	want := []string{"l1", "l2", "l4", "l5"}
	if len(r.Context) != len(want) {
		t.Fatalf("context=%v, want %v", r.Context, want)
	}
	for i := range want {
		if r.Context[i] != want[i] {
			t.Fatalf("context=%v, want %v", r.Context, want)
		}
	}

	// At the end of the file, "after" context clips at EOF instead of
	// fabricating lines: only 2 lines follow l4 (needle at line 3, l5 at
	// line 5 is unrelated here) so a match on the last line has 0 "after".
	edge := execInspect(t, reg, `{"query":"^l5$","max_results":10,"context_lines":3,"paths":["f.txt"]}`)
	if len(edge.Results) != 1 {
		t.Fatalf("results=%+v, want 1", edge.Results)
	}
	if want := []string{"l2", "needle", "l4"}; len(edge.Results[0].Context) != len(want) {
		t.Fatalf("boundary context=%v, want %v (clipped before-context, no after)", edge.Results[0].Context, want)
	} else {
		for i := range want {
			if edge.Results[0].Context[i] != want[i] {
				t.Fatalf("boundary context=%v, want %v", edge.Results[0].Context, want)
			}
		}
	}
}

func TestInspectRepositoryReportsResultAndByteTruncationHonestly(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{MaxInspectRepositoryBytes: testMinInspectRepositoryBytes})
	for i := 0; i < 50; i++ {
		writeFile(t, ws.Abs, "many.txt", strings.Repeat("needle\n", 50))
	}

	out := execInspect(t, reg, `{"query":"needle","max_results":3}`)
	if len(out.Results) > 3 {
		t.Fatalf("results=%d exceeds max_results=3", len(out.Results))
	}
	if !out.Truncated {
		t.Fatalf("expected truncated=true")
	}
	if out.TruncationReason != inspectTruncResultLimit && out.TruncationReason != inspectTruncByteLimit {
		t.Fatalf("unexpected truncation_reason %q", out.TruncationReason)
	}

	full := execInspect(t, reg, `{"query":"missing-needle-xyz","max_results":100}`)
	if full.Truncated {
		t.Fatalf("did not expect truncation: %+v", full)
	}
	if full.TruncationReason != "" {
		t.Fatalf("truncation_reason must be empty when untruncated, got %q", full.TruncationReason)
	}
}

// --- Wave 3: registry, budget, capability, provenance ---

func TestDefaultRegistryRegistersInspectRepositoryWithBudget(t *testing.T) {
	_, reg := setupWS(t)
	tool, ok := reg.Get("inspect_repository")
	if !ok {
		t.Fatal("inspect_repository not registered")
	}
	budgetTool, ok := tool.(ResultBudgetTool)
	if !ok {
		t.Fatal("inspect_repository does not implement ResultBudgetTool")
	}
	if budgetTool.ResultBudgetBytes() <= 0 {
		t.Fatalf("ResultBudgetBytes() = %d, want > 0", budgetTool.ResultBudgetBytes())
	}
}

func TestInspectRepositoryOutputIsValidJSONAndWithinBudget(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{MaxInspectRepositoryBytes: testMinInspectRepositoryBytes})
	writeFile(t, ws.Abs, "f.txt", strings.Repeat("needle\n", 200))

	raw, err := reg.Execute(context.Background(), "inspect_repository", json.RawMessage(`{"query":"needle","max_results":100,"context_lines":5}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !json.Valid([]byte(raw)) {
		t.Fatalf("output is not valid JSON: %s", raw)
	}
	if len(raw) > testMinInspectRepositoryBytes {
		t.Fatalf("output len=%d exceeds configured budget %d", len(raw), testMinInspectRepositoryBytes)
	}
}

func TestInspectRepositoryCapabilityUsesStableReadKey(t *testing.T) {
	_, reg := setupWS(t)
	tool, ok := reg.Get("inspect_repository")
	if !ok {
		t.Fatal("inspect_repository not registered")
	}
	capable, ok := tool.(CapableTool)
	if !ok {
		t.Fatal("inspect_repository does not implement CapableTool")
	}
	args := json.RawMessage(`{"query":"needle","max_results":10,"paths":["."],"glob":"*.go"}`)
	c1 := capable.Capability(args)
	c2 := capable.Capability(args)
	if c1.Class != ExecutionRead {
		t.Fatalf("Class=%v, want ExecutionRead", c1.Class)
	}
	if c1.ResourceKey == "" || c1.ResourceKey != c2.ResourceKey {
		t.Fatalf("ResourceKey not stable: %q vs %q", c1.ResourceKey, c2.ResourceKey)
	}
	other := capable.Capability(json.RawMessage(`{"query":"other","max_results":10}`))
	if other.ResourceKey == c1.ResourceKey {
		t.Fatalf("different queries produced the same resource key")
	}
}

func TestInspectRepositoryDoesNotUseAbsoluteWorkspacePathOrRawQueryInProvenance(t *testing.T) {
	ws, reg := setupWS(t)
	writeFile(t, ws.Abs, "f.txt", "super-secret-query-token\n")

	out := execInspect(t, reg, `{"query":"super-secret-query-token","max_results":10}`)
	if out.Provenance.WorkspaceRoot != "." {
		t.Fatalf("workspace_root=%q, want \".\"", out.Provenance.WorkspaceRoot)
	}
	if strings.Contains(out.Provenance.WorkspaceRoot, ws.Abs) {
		t.Fatalf("provenance leaked the absolute workspace root: %+v", out.Provenance)
	}
	for _, p := range out.Provenance.Paths {
		if filepath.IsAbs(p) {
			t.Fatalf("provenance path is absolute: %q", p)
		}
	}
	if out.Provenance.QuerySHA256 == "super-secret-query-token" || strings.Contains(out.Provenance.QuerySHA256, "secret") {
		t.Fatalf("provenance echoed the raw query: %q", out.Provenance.QuerySHA256)
	}
	if len(out.Provenance.QuerySHA256) != 64 {
		t.Fatalf("query_sha256 len=%d, want 64 (hex sha256)", len(out.Provenance.QuerySHA256))
	}
}

func TestInspectRepositoryNeverReturnsUnregisteredSecretPath(t *testing.T) {
	ws, reg := setupWS(t)
	writeFile(t, ws.Abs, ".env", "needle\n")
	writeFile(t, ws.Abs, "config/id_rsa", "needle\n")

	out := execInspect(t, reg, `{"query":"needle","max_results":10}`)
	for _, r := range out.Results {
		if r.Path == ".env" || strings.HasSuffix(r.Path, "id_rsa") {
			t.Fatalf("secret path leaked: %+v", r)
		}
	}
	if !out.Provenance.SecretPathsExcluded {
		t.Fatal("provenance must claim secret_paths_excluded=true")
	}
}

// --- Regressions found by bug audit ---

// TestGlobStillListsSymlinksAfterSharedWalkExtraction pins glob's pre-existing
// behavior (list matching paths regardless of file type) against the
// walkFilteredFiles extraction shared with grep and inspect_repository: grep
// and inspect_repository read content and correctly skip non-regular files,
// but glob only lists paths and never opens them, so it must not gain that
// restriction as a side effect of the refactor.
func TestGlobStillListsSymlinksAfterSharedWalkExtraction(t *testing.T) {
	ws, reg := setupWS(t)
	writeFile(t, ws.Abs, "real.md", "content\n")
	if err := os.Symlink(filepath.Join(ws.Abs, "real.md"), filepath.Join(ws.Abs, "link.md")); err != nil {
		t.Fatal(err)
	}
	raw, err := reg.Execute(context.Background(), "glob", json.RawMessage(`{"pattern":"*.md"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(raw, "link.md") {
		t.Fatalf("glob dropped a symlink match it used to list: %q", raw)
	}
}

// TestMarshalInspectOutputOverwritesReasonWhenByteTrimActuallyCuts: the engine
// may report truncated=true/reason="result_limit" from its own (approximate)
// byte accounting, but if the final marshaled envelope still does not fit
// under maxBytes and marshalInspectOutput has to drop results itself, THAT
// byte-cap trim is what determined the final shape - the reason must say so,
// not keep echoing whatever the engine guessed first.
func TestMarshalInspectOutputOverwritesReasonWhenByteTrimActuallyCuts(t *testing.T) {
	out := inspectOutput{
		Version: inspectRepositoryVersion,
		Provenance: inspectProvenance{
			WorkspaceRoot:       ".",
			Paths:               []string{"a"},
			QuerySHA256:         strings.Repeat("a", 64),
			IgnorePolicy:        "workspace-configured",
			SecretPathsExcluded: true,
		},
		Results: []inspectResult{
			{Path: "a.go", Line: 1, Text: "needle"},
			{Path: "a.go", Line: 2, Text: "needle"},
		},
		ResultCount:      2,
		Truncated:        true,
		TruncationReason: inspectTruncResultLimit,
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	tooSmall := len(raw) - 5 // forces the trim loop to actually remove a result

	got, err := marshalInspectOutput(out, tooSmall)
	if err != nil {
		t.Fatalf("marshalInspectOutput: %v", err)
	}
	if len(got) > tooSmall {
		t.Fatalf("output %d bytes exceeds cap %d", len(got), tooSmall)
	}
	var final inspectOutput
	if err := json.Unmarshal([]byte(got), &final); err != nil {
		t.Fatal(err)
	}
	if !final.Truncated {
		t.Fatal("expected truncated=true")
	}
	if final.TruncationReason != inspectTruncByteLimit {
		t.Fatalf("truncation_reason=%q, want %q once the byte trim actually fired", final.TruncationReason, inspectTruncByteLimit)
	}
}

// TestInspectRepositoryLargeProvenancePathsStillRespectsByteCap: Provenance.Paths
// echoes back every requested scope and is unbounded, unlike the rest of the
// envelope. A fixed reserve constant sized only for the fixed fields would
// under-budget here; the engine must size its reserve from the actual
// marshaled envelope so the byte cap invariant holds regardless of how many
// (or how long) paths were requested.
func TestInspectRepositoryLargeProvenancePathsStillRespectsByteCap(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{MaxInspectRepositoryBytes: testMinInspectRepositoryBytes})
	paths := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		// Two-digit suffix keeps names distinct without running past 'z' into
		// characters that are illegal in Windows file names (e.g. '|').
		name := fmt.Sprintf("d%s%02d", strings.Repeat("x", 60), i)
		writeFile(t, ws.Abs, name+"/f.txt", "needle\n")
		paths = append(paths, name)
	}
	pathsJSON, err := json.Marshal(paths)
	if err != nil {
		t.Fatal(err)
	}
	args := `{"query":"needle","max_results":5,"paths":` + string(pathsJSON) + `}`

	raw, err := reg.Execute(context.Background(), "inspect_repository", json.RawMessage(args))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(raw) > testMinInspectRepositoryBytes {
		t.Fatalf("output %d bytes exceeds configured cap %d", len(raw), testMinInspectRepositoryBytes)
	}
	if !json.Valid([]byte(raw)) {
		t.Fatalf("output is not valid JSON: %s", raw)
	}
}

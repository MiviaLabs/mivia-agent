package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// The code-navigation integration suite. Everything here runs the SHIPPED
// tools out of a real default registry against a real module on disk: no
// fakes, no injected analyzer. What it is here to prove is plan tools/03's
// central invariant - no stale positions, ever - which is a property of the
// combination (writer tool -> filesystem -> analyzer cache -> nav tool) and
// therefore cannot be established by any one of them in isolation.

const navModule = "example.com/navint"

// newNavRegistry builds a workspace holding a small two-package module and
// returns the default registry over it, with gofmt allowlisted so the
// run_command write path is exercised by a real external writer.
func newNavRegistry(t *testing.T) (*Registry, *workspace.Root) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module " + navModule + "\n\ngo 1.22\n",
		"widget.go": `package navint

import "` + navModule + `/sub"

// Base carries the id.
type Base struct {
	ID int
}

// Widget is a thing.
type Widget struct {
	Base
	Name string
}

// Label returns the name.
func (w *Widget) Label() string {
	return w.Name
}

// BuildWidget makes one.
func BuildWidget(name string) *Widget {
	return &Widget{Name: name, Base: Base{ID: sub.NextID()}}
}
`,
		"sub/sub.go": `package sub

// NextID hands out ids.
func NextID() int { return 1 }
`,
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{
		Workspace:     ws,
		MaxReadBytes:  256 * 1024,
		MaxWriteKB:    1024,
		RunAllowlist:  []string{"gofmt"},
		RunTimeoutSec: 60,
	})
	return reg, ws
}

// callTool invokes a registered tool and fails the test on a call error.
func callTool(t *testing.T, reg *Registry, name, args string) string {
	t.Helper()
	tool, ok := reg.Get(name)
	if !ok {
		t.Fatalf("%s not registered", name)
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	return out
}

// definitionOf runs go_to_definition and returns the decoded result, failing
// when the analyzer reported an error instead of a location.
func definitionOf(t *testing.T, reg *Registry, symbol string) goToDefinitionResult {
	t.Helper()
	out := callTool(t, reg, "go_to_definition", fmt.Sprintf(`{"symbol":%q}`, symbol))
	got := decodeDefinition(t, out)
	if got.Error != "" {
		t.Fatalf("go_to_definition(%s) reported: %s", symbol, got.Error)
	}
	if got.Line <= 0 || got.Path == "" {
		t.Fatalf("go_to_definition(%s) returned no position: %+v", symbol, got)
	}
	return got
}

// TestIntegrationNavStalenessMatrix is the plan's invariant §5 in full: FIVE
// write paths, each followed IMMEDIATELY by a query that must see the new
// text at the new position. Four of them are tools; the fifth is a plain
// os.WriteFile standing in for an editor, a git checkout, or anything else
// that never tells the agent it happened. That last case is the reason
// invalidation stats the filesystem instead of listening for write events.
type navWriteCase struct {
	name string
	// mutate shifts Label's declaration down and changes its body.
	mutate func(t *testing.T, reg *Registry, ws *workspace.Root)
	// want is a fragment the fresh definition source must contain.
	want string
}

// navWriteCases is the write surface the invariant has to survive: every way
// a source file changes under a running agent.
func navWriteCases() []navWriteCase {
	return []navWriteCase{
		{
			name: "write_file",
			mutate: func(t *testing.T, reg *Registry, ws *workspace.Root) {
				body := readWorkspaceFile(t, ws, "widget.go")
				body = "// rewritten wholesale by write_file\n" + strings.Replace(body,
					"return w.Name", "return \"write_file:\" + w.Name", 1)
				callTool(t, reg, "write_file", navArgs(t, map[string]any{
					"path": "widget.go", "content": body,
				}))
			},
			want: `return "write_file:" + w.Name`,
		},
		{
			name: "search_replace",
			mutate: func(t *testing.T, reg *Registry, ws *workspace.Root) {
				callTool(t, reg, "search_replace", navArgs(t, map[string]any{
					"path":       "widget.go",
					"old_string": "// Label returns the name.",
					"new_string": "// Label returns the name.\n// Edited by search_replace.",
				}))
				callTool(t, reg, "search_replace", navArgs(t, map[string]any{
					"path":       "widget.go",
					"old_string": "return w.Name",
					"new_string": `return "search_replace:" + w.Name`,
				}))
			},
			want: `return "search_replace:" + w.Name`,
		},
		{
			name: "multi_edit",
			mutate: func(t *testing.T, reg *Registry, ws *workspace.Root) {
				callTool(t, reg, "multi_edit", navArgs(t, map[string]any{
					"path": "widget.go",
					"edits": []map[string]any{
						{"old_string": "// Base carries the id.", "new_string": "// Base carries the id.\n// Edited by multi_edit."},
						{"old_string": "return w.Name", "new_string": `return "multi_edit:" + w.Name`},
					},
				}))
			},
			want: `return "multi_edit:" + w.Name`,
		},
		{
			name: "run_command_gofmt",
			mutate: func(t *testing.T, reg *Registry, ws *workspace.Root) {
				requireGofmt(t)
				// Unformatted on purpose: gofmt collapses the blank-line run
				// (moving every later declaration UP) and rewrites the
				// assignment. No tool announces either change.
				body := strings.Replace(
					readWorkspaceFile(t, ws, "widget.go"),
					"func (w *Widget) Label() string {\n\treturn w.Name\n}",
					"\n\n\n\nfunc (w *Widget) Label() string {\nname:=\"run_command:\" + w.Name\n\treturn name\n}",
					1)
				writeWorkspaceFile(t, ws, "widget.go", body)
				out := callTool(t, reg, "run_command", navArgs(t, map[string]any{
					"argv": []string{"gofmt", "-w", "widget.go"},
				}))
				if strings.Contains(out, "exit status 1") {
					t.Fatalf("gofmt failed: %s", out)
				}
			},
			want: `name := "run_command:" + w.Name`,
		},
		{
			name: "out_of_band_write",
			mutate: func(t *testing.T, reg *Registry, ws *workspace.Root) {
				body := readWorkspaceFile(t, ws, "widget.go")
				body = "// touched by an editor, outside every tool\n" + strings.Replace(body,
					"return w.Name", `return "out_of_band:" + w.Name`, 1)
				writeWorkspaceFile(t, ws, "widget.go", body)
			},
			want: `return "out_of_band:" + w.Name`,
		},
	}
}

func TestIntegrationNavStalenessMatrix(t *testing.T) {
	for _, tc := range navWriteCases() {
		t.Run(tc.name, func(t *testing.T) {
			reg, ws := newNavRegistry(t)

			// Warm the cache: this is the query whose snapshot must NOT be
			// reused after the write below.
			before := definitionOf(t, reg, "Widget.Label")
			if !strings.Contains(before.Source, "return w.Name") {
				t.Fatalf("fixture drifted; source = %q", before.Source)
			}

			tc.mutate(t, reg, ws)

			after := definitionOf(t, reg, "Widget.Label")
			if !strings.Contains(after.Source, tc.want) {
				t.Fatalf("stale source after %s:\nwant fragment %q\ngot %q", tc.name, tc.want, after.Source)
			}
			if after.Line == before.Line {
				t.Errorf("declaration line unchanged (%d) after %s; expected the edit to move it", before.Line, tc.name)
			}
			// The reported position must agree with the file on disk.
			assertLineMatchesDisk(t, ws, after)
		})
	}
}

// TestIntegrationListSymbolsSeesNewFileImmediately covers the write path a
// file-set-only stat pass cannot see at all: a source file that did not exist
// when the snapshot was built is in no snapshot, so nothing about it can be
// stale - it is simply invisible until the directory itself is checked.
func TestIntegrationListSymbolsSeesNewFileImmediately(t *testing.T) {
	reg, ws := newNavRegistry(t)

	// Warm the snapshot.
	definitionOf(t, reg, "BuildWidget")

	callTool(t, reg, "write_file", navArgs(t, map[string]any{
		"path":    "later.go",
		"content": "package navint\n\n// LaterSymbol arrived after the first query.\nfunc LaterSymbol() int { return 3 }\n",
	}))
	// Creating a file bumps the containing directory's mtime, which is the
	// signal invalidation reads. Set it explicitly so the test does not depend
	// on the filesystem's timestamp resolution.
	bumpWorkspaceDirMtime(t, ws)

	out := callTool(t, reg, "list_symbols", `{"symbol_prefix":"LaterSymbol"}`)
	got := decodeSymbols(t, out)
	if got.Error != "" {
		t.Fatalf("list_symbols reported: %s", got.Error)
	}
	if len(got.Symbols) == 0 {
		t.Fatal("a symbol written after the snapshot was built is invisible to workspace search")
	}
	if got.Symbols[0].Path != "later.go" {
		t.Errorf("path = %q, want later.go", got.Symbols[0].Path)
	}
	def := definitionOf(t, reg, "LaterSymbol")
	if !strings.Contains(def.Source, "return 3") {
		t.Errorf("definition source = %q", def.Source)
	}
}

// TestIntegrationDeletedFileDoesNotServeStalePositions: after a file is
// removed, a query must not answer from the snapshot that still holds it.
func TestIntegrationDeletedFileDoesNotServeStalePositions(t *testing.T) {
	reg, ws := newNavRegistry(t)
	definitionOf(t, reg, "sub.NextID")

	if err := os.Remove(filepath.Join(ws.Abs, "sub", "sub.go")); err != nil {
		t.Fatal(err)
	}
	// The workspace no longer compiles (widget.go imports sub), so the answer
	// must be an explicit failure rather than a position in a deleted file.
	out := callTool(t, reg, "go_to_definition", `{"symbol":"sub.NextID"}`)
	got := decodeDefinition(t, out)
	if got.Error == "" {
		t.Fatalf("resolved a symbol in a deleted file: %+v", got)
	}
}

// TestIntegrationNavToolsShareTheCacheAcrossTools proves D3 end to end: the
// snapshot one tool paid for is the snapshot the others use. Evidence for
// D1's cost claim (a stat pass is orders cheaper than the load it avoids) is
// logged rather than asserted, since absolute timings are not a stable gate.
func TestIntegrationNavToolsShareTheCacheAcrossTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		// NTFS defers directory-metadata updates past the load, so the first
		// warm call can pay one reload before the snapshot settles; the cache
		// is shared but the reuse timing is not stable enough to assert here.
		t.Skip("cache-reuse timing is not stable on NTFS (see codeintel TestSnapshotIsReusedAcrossQueries)")
	}
	reg, _ := newNavRegistry(t)

	start := time.Now()
	callTool(t, reg, "find_references", `{"symbol":"BuildWidget"}`)
	cold := time.Since(start)

	// A DIFFERENT tool, on the same registry: if the analyzer were per-tool
	// this would pay a second full load.
	start = time.Now()
	callTool(t, reg, "list_symbols", `{"symbol_prefix":"Widget"}`)
	warmOther := time.Since(start)

	start = time.Now()
	callTool(t, reg, "go_to_definition", `{"symbol":"Widget.Label"}`)
	warmThird := time.Since(start)

	t.Logf("cold load via find_references: %s; warm hit via list_symbols: %s; warm hit via go_to_definition: %s",
		cold, warmOther, warmThird)
	if warmOther >= cold {
		t.Errorf("second tool took %s against a %s cold load - the analyzer is not shared", warmOther, cold)
	}
	if warmThird >= cold {
		t.Errorf("third tool took %s against a %s cold load - the analyzer is not shared", warmThird, cold)
	}
}

// TestIntegrationOrientationCostsLessThanReadingTheFile is the token-economics
// smoke from plan §7: orienting in a file through its outline must cost a
// fraction of reading the file, or the tool has no reason to exist.
func TestIntegrationOrientationCostsLessThanReadingTheFile(t *testing.T) {
	reg, ws := newNavRegistry(t)

	// Ordinary code: 200 declarations with real bodies. The comparison is
	// deliberately made against realistic source rather than one-line stubs -
	// an outline of a file of one-liners cannot be smaller than the file, and
	// that is not the case the tool exists for.
	var body strings.Builder
	body.WriteString("package navint\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&body, `
// Helper%d does work.
func Helper%d(a, b int) (int, error) {
	sum := a + b
	if sum < 0 {
		return 0, fmt.Errorf("negative sum %%d", sum)
	}
	for i := 0; i < b; i++ {
		sum += i * %d
	}
	return sum, nil
}
`, i, i, i)
	}
	writeWorkspaceFile(t, ws, "helpers.go", body.String())

	outline := callTool(t, reg, "list_symbols", `{"path":"helpers.go"}`)
	whole := callTool(t, reg, "read_file", `{"path":"helpers.go"}`)
	// The workflow the plan is about: orient with the outline, then pull the
	// ONE declaration that matters. Both calls together must still cost less
	// than reading the file, or the pair of tools buys nothing.
	one := callTool(t, reg, "go_to_definition", `{"symbol":"Helper42"}`)

	got := decodeSymbols(t, outline)
	if len(got.Symbols) < 200 {
		t.Fatalf("outline listed %d of 200 declarations", len(got.Symbols))
	}
	if got.Truncated {
		t.Fatal("outline was truncated; the comparison would be against a partial answer")
	}
	if len(outline) >= len(whole) {
		t.Errorf("outline is %d bytes against %d for the whole file", len(outline), len(whole))
	}
	if len(outline)+len(one) >= len(whole) {
		t.Errorf("outline (%d) + one definition (%d) is not cheaper than reading the file (%d)",
			len(outline), len(one), len(whole))
	}
	t.Logf("orientation: outline %d bytes + definition %d bytes = %d, vs whole-file read %d bytes (%d declarations)",
		len(outline), len(one), len(outline)+len(one), len(whole), len(got.Symbols))
}

// TestIntegrationFileOutlineWorksWithoutAnalysis: file mode must answer in a
// workspace the analyzer cannot touch at all (D2). Here the module file is
// gone, so every type-checked query fails - and the outline still works.
func TestIntegrationFileOutlineWorksWithoutAnalysis(t *testing.T) {
	reg, ws := newNavRegistry(t)
	if err := os.Remove(filepath.Join(ws.Abs, "go.mod")); err != nil {
		t.Fatal(err)
	}

	unavailable := decodeDefinition(t, callTool(t, reg, "go_to_definition", `{"symbol":"BuildWidget"}`))
	if !strings.Contains(unavailable.Error, "analysis unavailable") {
		t.Fatalf("expected the explicit unavailable shape, got %+v", unavailable)
	}
	search := decodeSymbols(t, callTool(t, reg, "list_symbols", `{"symbol_prefix":"Build"}`))
	if !strings.Contains(search.Error, "analysis unavailable") {
		t.Fatalf("expected the explicit unavailable shape, got %+v", search)
	}

	outline := decodeSymbols(t, callTool(t, reg, "list_symbols", `{"path":"widget.go"}`))
	if outline.Error != "" {
		t.Fatalf("file outline failed without a module: %s", outline.Error)
	}
	if _, ok := findToolSymbol(outline, "BuildWidget"); !ok {
		t.Fatalf("outline missing BuildWidget: %+v", outline.Symbols)
	}
}

// TestIntegrationConcurrentNavQueriesUnderWrites runs the three nav tools
// concurrently against a workspace being rewritten underneath them. Run under
// -race this is the concurrency gate for the shared analyzer; without the
// race detector it still asserts that no call returns a malformed result.
func TestIntegrationConcurrentNavQueriesUnderWrites(t *testing.T) {
	reg, ws := newNavRegistry(t)

	done := make(chan error, 8)
	query := func(name, args string) {
		tool, ok := reg.Get(name)
		if !ok {
			done <- fmt.Errorf("%s not registered", name)
			return
		}
		out, err := tool.Execute(context.Background(), json.RawMessage(args))
		if err != nil {
			done <- fmt.Errorf("%s: %w", name, err)
			return
		}
		var any map[string]any
		if uerr := json.Unmarshal([]byte(out), &any); uerr != nil {
			done <- fmt.Errorf("%s: invalid JSON %q", name, out)
			return
		}
		done <- nil
	}

	for i := 0; i < 2; i++ {
		go query("find_references", `{"symbol":"BuildWidget"}`)
		go query("list_symbols", `{"symbol_prefix":"Widget"}`)
		go query("go_to_definition", `{"symbol":"Widget.Label"}`)
		go func(n int) {
			writeWorkspaceFile(t, ws, fmt.Sprintf("churn%d.go", n),
				fmt.Sprintf("package navint\n\nfunc churn%d() int { return %d }\n", n, n))
			done <- nil
		}(i)
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

// --- helpers -------------------------------------------------------------

func readWorkspaceFile(t *testing.T, ws *workspace.Root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ws.Abs, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// navArgs marshals a call payload for callTool.
func navArgs(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// bumpWorkspaceDirMtime moves the workspace root's mtime forward by a second.
func bumpWorkspaceDirMtime(t *testing.T, ws *workspace.Root) {
	t.Helper()
	st, err := os.Stat(ws.Abs)
	if err != nil {
		t.Fatal(err)
	}
	next := st.ModTime().Add(time.Second)
	if err := os.Chtimes(ws.Abs, next, next); err != nil {
		t.Fatal(err)
	}
}

func requireGofmt(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("gofmt"); err != nil {
		t.Skip("gofmt not on PATH; the external-writer path needs a real formatter")
	}
}

// assertLineMatchesDisk checks the reported span against the file as it is on
// disk right now - the point of the whole exercise.
func assertLineMatchesDisk(t *testing.T, ws *workspace.Root, def goToDefinitionResult) {
	t.Helper()
	lines := strings.Split(readWorkspaceFile(t, ws, def.Path), "\n")
	if def.Line > len(lines) {
		t.Fatalf("reported line %d past end of %s (%d lines)", def.Line, def.Path, len(lines))
	}
	first, _, _ := strings.Cut(def.Source, "\n")
	if got := lines[def.Line-1]; got != first {
		t.Fatalf("line %d of %s is %q, but the result's first source line is %q",
			def.Line, def.Path, got, first)
	}
}

func findToolSymbol(res listSymbolsResult, name string) (int, bool) {
	for i, s := range res.Symbols {
		if s.Name == name {
			return i, true
		}
	}
	return 0, false
}

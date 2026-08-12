package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Coverage for the in-place edit tools (plan 48 step 5): the file-size guard
// that used to be absent entirely, the declared result budget, mode-preserving
// writes, and the batched multi_edit variant.

func writeEditFixture(t *testing.T, ws string, name, content string, mode os.FileMode) string {
	t.Helper()
	abs := filepath.Join(ws, name)
	if err := os.WriteFile(abs, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile applies perm through the process umask on create; force the
	// exact mode so the assertion is about the tool, not about the umask.
	if err := os.Chmod(abs, mode); err != nil {
		t.Fatal(err)
	}
	return abs
}

func fileMode(t *testing.T, abs string) os.FileMode {
	t.Helper()
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// TestSearchReplacePreservesFileMode: editing a script must not cost it its
// executable bit. An agent that edits a hook or a Makefile helper and silently
// makes it non-executable breaks the build in a way the diff does not show.
func TestSearchReplacePreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; every file mode reads back as 0666")
	}
	ws, reg := setupWS(t)
	abs := writeEditFixture(t, ws.Abs, "hook.sh", "#!/bin/sh\necho old\n", 0o755)

	mustExec(t, reg, "search_replace", map[string]any{
		"path": "hook.sh", "old_string": "echo old", "new_string": "echo new",
	})

	if got := fileMode(t, abs); got != 0o755 {
		t.Fatalf("mode after search_replace = %04o, want 0755", got)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "#!/bin/sh\necho new\n" {
		t.Fatalf("content = %q", string(data))
	}
}

// A restrictive mode must survive too: the old os.WriteFile(0644) call site
// would have widened a 0600 file had the file been recreated under it.
func TestSearchReplacePreservesRestrictiveFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; every file mode reads back as 0666")
	}
	ws, reg := setupWS(t)
	abs := writeEditFixture(t, ws.Abs, "private.txt", "secret old\n", 0o600)

	mustExec(t, reg, "search_replace", map[string]any{
		"path": "private.txt", "old_string": "old", "new_string": "new",
	})

	if got := fileMode(t, abs); got != 0o600 {
		t.Fatalf("mode after search_replace = %04o, want 0600", got)
	}
}

func TestMultiEditPreservesFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no chmod permission bits; every file mode reads back as 0666")
	}
	ws, reg := setupWS(t)
	abs := writeEditFixture(t, ws.Abs, "hook.sh", "#!/bin/sh\necho a\necho b\n", 0o755)

	mustExec(t, reg, "multi_edit", map[string]any{
		"path": "hook.sh",
		"edits": []map[string]any{
			{"old_string": "echo a", "new_string": "echo A"},
			{"old_string": "echo b", "new_string": "echo B"},
		},
	})

	if got := fileMode(t, abs); got != 0o755 {
		t.Fatalf("mode after multi_edit = %04o, want 0755", got)
	}
}

// TestEditToolsRefuseFileAboveReadBound: before plan 48 step 5, search_replace
// read a file of ANY size into memory with no guard - the one workspace tool
// with no bound at all. The refusal must state the real size and name a way
// forward, or the model just retries the identical call.
func TestEditToolsRefuseFileAboveReadBound(t *testing.T) {
	ws, reg := setupWSWithOpts(t, DefaultOptions{MaxReadBytes: 1024})
	big := strings.Repeat("padding line\n", 500) // ~6.5 KiB
	writeEditFixture(t, ws.Abs, "big.txt", big, 0o644)

	calls := map[string]any{
		"search_replace": map[string]any{
			"path": "big.txt", "old_string": "padding", "new_string": "PADDING", "replace_all": true,
		},
		"multi_edit": map[string]any{
			"path":  "big.txt",
			"edits": []map[string]any{{"old_string": "padding", "new_string": "PADDING", "replace_all": true}},
		},
	}
	for name, args := range calls {
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		_, err = reg.Execute(context.Background(), name, raw)
		if err == nil {
			t.Fatalf("%s: expected refusal for a file above max_read_bytes", name)
		}
		msg := err.Error()
		for _, want := range []string{"big.txt", "6500", "1024", "read_file", "write_file"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%s refusal %q does not mention %q", name, msg, want)
			}
		}
	}
	// The file must be untouched by a refused edit.
	data, err := os.ReadFile(filepath.Join(ws.Abs, "big.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != big {
		t.Fatal("refused edit modified the file")
	}
}

// An uncapped configuration must still edit ordinary files: the guard falls
// back to the 256 MiB memory backstop, not to zero.
func TestEditToolsUncappedStillEditOrdinaryFiles(t *testing.T) {
	_, reg := setupWSWithOpts(t, DefaultOptions{})
	mustExec(t, reg, "write_file", map[string]any{"path": "ok.txt", "content": "alpha\n"})
	mustExec(t, reg, "search_replace", map[string]any{
		"path": "ok.txt", "old_string": "alpha", "new_string": "beta",
	})
	out := mustExec(t, reg, "read_file", map[string]any{"path": "ok.txt"})
	if out != "beta\n" {
		t.Fatalf("got %q", out)
	}
}

// TestEditToolFileGuardComesFromMaxReadBytes pins the wiring: the guard tracks
// max_read_bytes (or the memory backstop when unset) and is NOT clamped by
// max_tool_result_bytes, which bounds the diff the tool returns rather than
// the file it may load.
func TestEditToolFileGuardComesFromMaxReadBytes(t *testing.T) {
	cases := []struct {
		name string
		opts DefaultOptions
		want int
	}{
		{"default (memory backstop)", DefaultOptions{}, 256 << 20},
		{"explicit max_read_bytes", DefaultOptions{MaxReadBytes: 1 << 20}, 1 << 20},
		{"result cap does not shrink it", DefaultOptions{MaxReadBytes: 1 << 20, MaxToolResultBytes: 4096}, 1 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Workspace = budgetWorkspace(t)
			reg := NewDefaultRegistry(opts)
			sr, ok := reg.Get("search_replace")
			if !ok {
				t.Fatal("search_replace not registered")
			}
			if got := sr.(*searchReplaceTool).maxFileBytes; got != tc.want {
				t.Errorf("search_replace.maxFileBytes = %d, want %d", got, tc.want)
			}
			me, ok := reg.Get("multi_edit")
			if !ok {
				t.Fatal("multi_edit not registered")
			}
			if got := me.(*multiEditTool).maxFileBytes; got != tc.want {
				t.Errorf("multi_edit.maxFileBytes = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEditToolsDeclareAndHonorTheirResultBudget: the declaration the
// dispatcher derives its ceiling from must bound the WHOLE result, elision
// marker included - not just the diff body.
func TestEditToolsDeclareAndHonorTheirResultBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts DefaultOptions
		want int
	}{
		{"compiled-in default", DefaultOptions{}, searchReplaceResultMaxBytes},
		{"clamped by max_tool_result_bytes", DefaultOptions{MaxToolResultBytes: 2048}, 2048},
		{"cap above default does not raise it", DefaultOptions{MaxToolResultBytes: 1 << 20}, searchReplaceResultMaxBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Workspace = budgetWorkspace(t)
			reg := NewDefaultRegistry(opts)
			for _, name := range []string{"search_replace", "multi_edit"} {
				tool, ok := reg.Get(name)
				if !ok {
					t.Fatalf("%s not registered", name)
				}
				budgeted, ok := tool.(ResultBudgetTool)
				if !ok {
					t.Fatalf("%s does not implement ResultBudgetTool", name)
				}
				if got := budgeted.ResultBudgetBytes(); got != tc.want {
					t.Errorf("%s.ResultBudgetBytes() = %d, want %d", name, got, tc.want)
				}
			}

			// A large edit must come back inside the declaration.
			var b strings.Builder
			for i := 0; i < 400; i++ {
				b.WriteString("target line of some length to diff\n")
			}
			mustExec(t, reg, "write_file", map[string]any{"path": "wide.txt", "content": b.String()})
			out := mustExec(t, reg, "search_replace", map[string]any{
				"path": "wide.txt", "old_string": "target", "new_string": "REPLACED", "replace_all": true,
			})
			if len(out) > tc.want {
				t.Errorf("search_replace result %d bytes exceeds declared budget %d", len(out), tc.want)
			}
			out = mustExec(t, reg, "multi_edit", map[string]any{
				"path": "wide.txt",
				"edits": []map[string]any{
					{"old_string": "REPLACED", "new_string": "AGAIN", "replace_all": true},
					{"old_string": "line", "new_string": "row", "replace_all": true},
				},
			})
			if len(out) > tc.want {
				t.Errorf("multi_edit result %d bytes exceeds declared budget %d", len(out), tc.want)
			}
		})
	}
}

// TestEditToolsDoNotDeclareLoopTruncationBound: the loop's wire bound would
// tail-cut the "…" marker these tools pay for out of their own budget.
func TestEditToolsDoNotDeclareLoopTruncationBound(t *testing.T) {
	reg := NewDefaultRegistry(DefaultOptions{Workspace: budgetWorkspace(t)})
	for _, name := range []string{"search_replace", "multi_edit"} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		capable, ok := tool.(CapableTool)
		if !ok {
			continue
		}
		if got := capable.Capability(nil).MaxResultBytes; got != 0 {
			t.Errorf("%s declares Capability.MaxResultBytes = %d; the loop would tail-cut its honest framing", name, got)
		}
	}
}

func TestMultiEditAppliesEditsInOrder(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path": "seq.txt", "content": "alpha\nbeta\ngamma\n",
	})
	out := mustExec(t, reg, "multi_edit", map[string]any{
		"path": "seq.txt",
		"edits": []map[string]any{
			{"old_string": "alpha", "new_string": "one"},
			// Applies to the RESULT of the previous edit, not to the file on
			// disk: "one" exists only because edit 1 created it.
			{"old_string": "one\nbeta", "new_string": "one\ntwo"},
			{"old_string": "gamma", "new_string": "three"},
		},
	})
	got := mustExec(t, reg, "read_file", map[string]any{"path": "seq.txt"})
	if got != "one\ntwo\nthree\n" {
		t.Fatalf("content = %q", got)
	}
	if !strings.Contains(out, "updated seq.txt (3 edits, 3 replacements") {
		t.Fatalf("missing batch stats: %q", out)
	}
	if !strings.Contains(out, "--- a/seq.txt") || !strings.Contains(out, "+++ b/seq.txt") {
		t.Fatalf("missing unified diff framing: %q", out)
	}
}

// TestMultiEditIsAllOrNothing is the property the tool exists for: a failing
// edit K must not leave edits 1..K-1 on disk. A half-applied batch is worse
// than no batch, because the model's next call reasons about a file state
// neither it nor the user has seen.
func TestMultiEditIsAllOrNothing(t *testing.T) {
	ws, reg := setupWS(t)
	const original = "alpha\nbeta\n"
	writeEditFixture(t, ws.Abs, "atomic.txt", original, 0o644)

	_, err := reg.Execute(context.Background(), "multi_edit", json.RawMessage(
		`{"path":"atomic.txt","edits":[{"old_string":"alpha","new_string":"ALPHA"},{"old_string":"nope","new_string":"x"}]}`,
	))
	if err == nil {
		t.Fatal("expected failure on the second edit")
	}
	if !strings.Contains(err.Error(), "edit 2/2") {
		t.Errorf("error %q does not identify the failing edit", err)
	}
	data, err := os.ReadFile(filepath.Join(ws.Abs, "atomic.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("file was modified by a failed batch: %q", string(data))
	}
}

// An edit that matched the original file but was consumed by an earlier edit
// is this tool's distinctive failure; the message must say so.
func TestMultiEditReportsTextConsumedByEarlierEdit(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{"path": "c.txt", "content": "alpha beta\n"})
	_, err := reg.Execute(context.Background(), "multi_edit", json.RawMessage(
		`{"path":"c.txt","edits":[{"old_string":"alpha","new_string":"x"},{"old_string":"alpha","new_string":"y"}]}`,
	))
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(err.Error(), "earlier edit") {
		t.Errorf("error %q does not explain the consumed match", err)
	}
}

func TestMultiEditUniquenessAndReplaceAll(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{"path": "dup.txt", "content": "aa bb aa\n"})

	_, err := reg.Execute(context.Background(), "multi_edit", json.RawMessage(
		`{"path":"dup.txt","edits":[{"old_string":"aa","new_string":"xx"}]}`,
	))
	if err == nil || !strings.Contains(err.Error(), "found 2 times") {
		t.Fatalf("expected non-unique error, got %v", err)
	}

	out := mustExec(t, reg, "multi_edit", map[string]any{
		"path":  "dup.txt",
		"edits": []map[string]any{{"old_string": "aa", "new_string": "xx", "replace_all": true}},
	})
	if !strings.Contains(out, "1 edit, 2 replacements") {
		t.Fatalf("replace_all count not reported: %q", out)
	}
	got := mustExec(t, reg, "read_file", map[string]any{"path": "dup.txt"})
	if got != "xx bb xx\n" {
		t.Fatalf("content = %q", got)
	}
}

func TestMultiEditRejectsDegenerateInput(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{"path": "d.txt", "content": "alpha\n"})
	cases := []struct {
		name, args, want string
	}{
		{"no edits", `{"path":"d.txt","edits":[]}`, "at least one"},
		{"empty old_string", `{"path":"d.txt","edits":[{"old_string":"","new_string":"x"}]}`, "must not be empty"},
		{"no-op edit", `{"path":"d.txt","edits":[{"old_string":"alpha","new_string":"alpha"}]}`, "identical"},
		{"missing edits", `{"path":"d.txt"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := reg.Execute(context.Background(), "multi_edit", json.RawMessage(c.args))
			if err == nil {
				t.Fatalf("expected error for %s", c.args)
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// The batch diff's first hunk must be numbered from the first line that
// actually changed, wherever in the file that is.
func TestMultiEditDiffHunkStartsAtFirstChangedLine(t *testing.T) {
	_, reg := setupWS(t)
	var b strings.Builder
	for i := 1; i <= 40; i++ {
		b.WriteString("keep\n")
	}
	b.WriteString("target\n")
	mustExec(t, reg, "write_file", map[string]any{"path": "h.txt", "content": b.String()})

	out := mustExec(t, reg, "multi_edit", map[string]any{
		"path":  "h.txt",
		"edits": []map[string]any{{"old_string": "target", "new_string": "changed"}},
	})
	// 3 lines of leading context before line 41.
	if !strings.Contains(out, "@@ -38,") {
		t.Fatalf("hunk header not anchored at the changed line: %q", out)
	}
}

func TestMultiEditBlocksSecretPaths(t *testing.T) {
	ws, reg := setupWS(t)
	writeEditFixture(t, ws.Abs, ".env", "TOKEN=old\n", 0o600)
	_, err := reg.Execute(context.Background(), "multi_edit", json.RawMessage(
		`{"path":".env","edits":[{"old_string":"old","new_string":"new"}]}`,
	))
	if err == nil || !strings.Contains(err.Error(), "secret-like path") {
		t.Fatalf("expected secret-path refusal, got %v", err)
	}
}

func TestFirstChangedLine(t *testing.T) {
	cases := []struct {
		name     string
		old, new string
		want     int
	}{
		{"first line", "a\nb\n", "x\nb\n", 1},
		{"third line", "a\nb\nc\n", "a\nb\nX\n", 3},
		{"append", "a\n", "a\nb\n", 2},
		// Unreachable in production - multi_edit returns a "no change" result
		// before formatting - but the helper must still terminate past the end
		// rather than index out of range.
		{"identical", "a\nb\n", "a\nb\n", 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := firstChangedLine(c.old, c.new); got != c.want {
				t.Fatalf("firstChangedLine = %d, want %d", got, c.want)
			}
		})
	}
}

func TestSearchReplaceBlocksSecretPaths(t *testing.T) {
	ws, reg := setupWS(t)
	writeEditFixture(t, ws.Abs, ".env", "TOKEN=old\n", 0o600)
	_, err := reg.Execute(context.Background(), "search_replace", json.RawMessage(
		`{"path":".env","old_string":"old","new_string":"new"}`,
	))
	if err == nil || !strings.Contains(err.Error(), "secret-like path") {
		t.Fatalf("expected secret-path refusal, got %v", err)
	}
}

func TestMultiEditRejectsBadPathsAndArguments(t *testing.T) {
	ws := budgetWorkspace(t)
	tool := &multiEditTool{ws: ws, maxFileBytes: 1 << 20, maxBytes: searchReplaceResultMaxBytes}
	if err := os.Mkdir(filepath.Join(ws.Abs, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, args, want string
	}{
		{"malformed json", `{"path":`, ""},
		{"wrong edits type", `{"path":"x.txt","edits":"nope"}`, ""},
		{"escapes workspace", `{"path":"../outside.txt","edits":[{"old_string":"a","new_string":"b"}]}`, ""},
		{"directory", `{"path":"adir","edits":[{"old_string":"a","new_string":"b"}]}`, "directory"},
		{"missing file", `{"path":"gone.txt","edits":[{"old_string":"a","new_string":"b"}]}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), json.RawMessage(c.args))
			if err == nil {
				t.Fatalf("expected error for %s", c.args)
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not mention %q", err, c.want)
			}
		})
	}
}

// A batch whose edits all match but change nothing must say so rather than
// report a phantom update - and must not rewrite the file.
func TestMultiEditReportsNoChange(t *testing.T) {
	ws, reg := setupWS(t)
	writeEditFixture(t, ws.Abs, "same.txt", "alpha beta\n", 0o644)
	out := mustExec(t, reg, "multi_edit", map[string]any{
		"path": "same.txt",
		"edits": []map[string]any{
			{"old_string": "alpha", "new_string": "beta"},
			{"old_string": "beta beta", "new_string": "alpha beta"},
		},
	})
	if !strings.Contains(out, "no change to same.txt") {
		t.Fatalf("expected a no-change result, got %q", out)
	}
	data, err := os.ReadFile(filepath.Join(ws.Abs, "same.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha beta\n" {
		t.Fatalf("content = %q", string(data))
	}
}

func TestApplyEditsHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := applyEdits(ctx, "alpha\n", "x.txt", []editSpec{{OldString: "alpha", NewString: "beta"}})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}

// Above the diff engine's input bound the result must still name the file and
// report the batch honestly, with the diff replaced by a stated reason.
func TestFormatEditDiffResultDegradesWhenDiffIsRefused(t *testing.T) {
	huge := strings.Repeat("a\n", 400<<10) // past diff.Options.MaxInputBytes
	out := formatEditDiffResult("updated big.txt (1 edit, 1 replacement", "big.txt", huge, huge+"b\n", searchReplaceResultMaxBytes)
	if !strings.Contains(out, "updated big.txt") {
		t.Fatalf("header lost: %q", out)
	}
	if !strings.Contains(out, "diff omitted") {
		t.Fatalf("expected an explicit omission reason: %q", out)
	}
	if len(out) > searchReplaceResultMaxBytes {
		t.Fatalf("degraded result %d bytes exceeds budget", len(out))
	}
}

// clampEditResult is what makes the declared budget true, so its edges are
// pinned directly: a budget too small for the header, and one too small even
// for the elision marker, must still return something inside the budget.
func TestClampEditResultEdges(t *testing.T) {
	header := "updated x.txt (1 replacement, +1 −1)"
	dump := strings.Repeat("+line\n", 200)

	if got := clampEditResult(header, dump, 0); !strings.HasPrefix(got, header) || len(got) > searchReplaceResultMaxBytes {
		t.Fatalf("zero budget did not fall back to the compiled-in bound: %d bytes", len(got))
	}
	// len(header)+4 is the exact budget where the header, newline and marker
	// fit and the diff does not: the body must be dropped, not passed to a
	// truncator that treats a zero bound as "no bound".
	exact := clampEditResult(header, dump, len(header)+4)
	if exact != header+"\n…" {
		t.Fatalf("exact-fit budget did not drop the diff: %q", exact)
	}
	for _, budget := range []int{2, 3, 4, 12, len(header), len(header) + 2, len(header) + 4, 512} {
		got := clampEditResult(header, dump, budget)
		if len(got) > budget {
			t.Errorf("budget %d: result is %d bytes", budget, len(got))
		}
	}
	// With room for the header the header survives whole; the diff is what
	// gets cut, and the cut is reported.
	got := clampEditResult(header, dump, len(header)+40)
	if !strings.HasPrefix(got, header) {
		t.Fatalf("header not preserved: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncation not reported: %q", got)
	}
}

// The dispatcher serializes edits through the capability class, so multi_edit
// must classify as a WRITE on the file it names. A tool that fell through to
// the default would run concurrently with another write to the same path.
func TestMultiEditCapabilityIsAPathScopedWrite(t *testing.T) {
	_, reg := setupWS(t)
	got := reg.Capability("multi_edit", json.RawMessage(`{"path":"pkg/a.go"}`))
	if got.Class != ExecutionWrite {
		t.Errorf("class = %v, want ExecutionWrite", got.Class)
	}
	if !strings.HasPrefix(got.ResourceKey, "path:") || !strings.HasSuffix(got.ResourceKey, "pkg/a.go") {
		t.Errorf("resource key = %q, want the file path", got.ResourceKey)
	}

	// Same answer from the name-only table, which classifies a registered tool
	// that does not implement CapableTool. Without the name there, an edit tool
	// would be scheduled as an unrelated external call and could run
	// concurrently with another write to the same file.
	plain := NewRegistry()
	plain.Register(&nameOnlyTool{name: "multi_edit"})
	byName := plain.Capability("multi_edit", json.RawMessage(`{"path":"pkg/a.go"}`))
	if byName.Class != ExecutionWrite {
		t.Errorf("name-only class = %v, want ExecutionWrite", byName.Class)
	}
	if byName.ResourceKey != "path:pkg/a.go" {
		t.Errorf("name-only resource key = %q, want path:pkg/a.go", byName.ResourceKey)
	}
}

// nameOnlyTool is a registered tool that does NOT implement CapableTool, so
// the registry falls back to classifying it by name.
type nameOnlyTool struct{ name string }

func (t *nameOnlyTool) Name() string               { return t.name }
func (t *nameOnlyTool) Description() string        { return "stub" }
func (t *nameOnlyTool) Parameters() map[string]any { return schemaObject(nil, nil) }
func (t *nameOnlyTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

func TestMultiEditHonorsContextCancellation(t *testing.T) {
	ws, reg := setupWS(t)
	writeEditFixture(t, ws.Abs, "c.txt", "alpha\n", 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reg.Execute(ctx, "multi_edit", json.RawMessage(
		`{"path":"c.txt","edits":[{"old_string":"alpha","new_string":"beta"}]}`,
	)); err == nil {
		t.Fatal("expected cancellation error")
	}
}

// A file the agent may read but not write must fail as a write error, with the
// content left alone - not a partially applied batch.
func TestMultiEditReportsWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file write permissions")
	}
	ws, reg := setupWS(t)
	abs := writeEditFixture(t, ws.Abs, "ro.txt", "alpha\n", 0o444)
	_, err := reg.Execute(context.Background(), "multi_edit", json.RawMessage(
		`{"path":"ro.txt","edits":[{"old_string":"alpha","new_string":"beta"}]}`,
	))
	if err == nil {
		t.Fatal("expected a write failure on a read-only file")
	}
	data, readErr := os.ReadFile(abs)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "alpha\n" {
		t.Fatalf("content = %q", string(data))
	}
}

// A file the agent may stat but not read must fail as a read error before any
// edit is applied.
func TestMultiEditReportsReadFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file read permissions")
	}
	ws, reg := setupWS(t)
	writeEditFixture(t, ws.Abs, "noread.txt", "alpha\n", 0o000)
	if _, err := reg.Execute(context.Background(), "multi_edit", json.RawMessage(
		`{"path":"noread.txt","edits":[{"old_string":"alpha","new_string":"beta"}]}`,
	)); err == nil {
		t.Fatal("expected a read failure on an unreadable file")
	}
}

// Neither edit tool checks whether new_string is already present before
// applying old_string -> new_string; it only checks old_string still exists.
// When old_string survives as a substring of new_string (the common case for
// an edit that extends an anchor line, e.g. a function signature followed by
// an appended call), a retried or independently re-issued identical edit
// re-applies and duplicates the inserted text. These two tests pin the
// desired invariant - the duplication must not happen - and are expected to
// fail (RED) until the tools gain an idempotency check.

func TestSearchReplaceReapplyingIdenticalEditDoesNotDuplicate(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.go",
		"content": "func foo() {\n\treturn\n}\n",
	})
	edit := map[string]any{
		"path":       "f.go",
		"old_string": "func foo() {",
		"new_string": "func foo() {\n\textra()",
	}
	mustExec(t, reg, "search_replace", edit)
	// Re-issue the identical call: simulates a retried/duplicate delivery,
	// or a second agent independently applying the same fix.
	_, err := reg.Execute(context.Background(), "search_replace", mustJSON(t, edit))
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.go"})
	if n := strings.Count(got, "extra()"); n != 1 {
		t.Fatalf("extra() appears %d times after reapplying an already-applied edit, want 1 (second call err=%v, content=%q)", n, err, got)
	}
}

func TestMultiEditReapplyingIdenticalEditDoesNotDuplicate(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.go",
		"content": "func foo() {\n\treturn\n}\n",
	})
	args := map[string]any{
		"path": "f.go",
		"edits": []map[string]any{
			{"old_string": "func foo() {", "new_string": "func foo() {\n\textra()"},
		},
	}
	mustExec(t, reg, "multi_edit", args)
	_, err := reg.Execute(context.Background(), "multi_edit", mustJSON(t, args))
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.go"})
	if n := strings.Count(got, "extra()"); n != 1 {
		t.Fatalf("extra() appears %d times after reapplying an already-applied multi_edit, want 1 (second call err=%v, content=%q)", n, err, got)
	}
}

// A file that already contains new_string elsewhere but still contains a live
// old_string must still be edited. Before the fix, the alreadyApplied skip
// keyed only on new_string's presence, so a file that merely contained the
// replacement text (an earlier independent application to another spot, or the
// model pre-filling it) silently dropped the edit with a false "no change
// (edit already applied)" success report. The fixed predicate strips the
// landed new_string occurrences and only skips when no old_string remains
// outside them.
func TestSearchReplaceNewStringPreexistsStillApplies(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.txt",
		"content": "beta_marker already applied elsewhere\nalpha_marker still needs replacing\n",
	})
	out := mustExec(t, reg, "search_replace", map[string]any{
		"path":       "f.txt",
		"old_string": "alpha_marker",
		"new_string": "beta_marker",
	})
	if strings.Contains(out, "no change") {
		t.Fatalf("live edit was skipped as already applied: %q", out)
	}
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	if strings.Contains(got, "alpha_marker") {
		t.Fatalf("old_string still present after edit: %q", got)
	}
	if n := strings.Count(got, "beta_marker"); n != 2 {
		t.Fatalf("new_string appears %d times after edit, want 2: %q", n, got)
	}
}

// Same false-positive scenario through multi_edit: the batch must apply a live
// edit whose new_string merely pre-exists, while a retried identical edit
// (old_string only inside the landed new_string) still no-ops.
func TestMultiEditNewStringPreexistsStillApplies(t *testing.T) {
	_, reg := setupWS(t)
	mustExec(t, reg, "write_file", map[string]any{
		"path":    "f.txt",
		"content": "beta_marker already applied elsewhere\nalpha_marker still needs replacing\n",
	})
	out := mustExec(t, reg, "multi_edit", map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "alpha_marker", "new_string": "beta_marker"},
		},
	})
	if strings.Contains(out, "no change") {
		t.Fatalf("live edit was skipped as already applied: %q", out)
	}
	got := mustExec(t, reg, "read_file", map[string]any{"path": "f.txt"})
	if strings.Contains(got, "alpha_marker") {
		t.Fatalf("old_string still present after edit: %q", got)
	}
	if n := strings.Count(got, "beta_marker"); n != 2 {
		t.Fatalf("new_string appears %d times after edit, want 2: %q", n, got)
	}
}

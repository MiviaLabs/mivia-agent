package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// Unit coverage for the byte budgets added to the count-capped read-class
// tools. Before them, list_dir/grep/glob capped ENTRIES and MATCHES but not
// BYTES, so a directory of long names or a deep tree produced results with no
// byte bound at all - and the dispatcher, which hard-fails rather than
// truncates, destroyed them whole.

func budgetWorkspace(t *testing.T) *workspace.Root {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

// writeNamedFiles creates count files of the given name length in dir.
func writeNamedFiles(t *testing.T, dir string, count, nameLen int, ext string) {
	t.Helper()
	for i := 0; i < count; i++ {
		suffix := fmt.Sprintf("%05d%s", i, ext)
		pad := nameLen - len(suffix)
		if pad < 1 {
			pad = 1
		}
		name := strings.Repeat("n", pad) + suffix
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// parseListDirNotice returns the delivered entry count and the omitted count
// a list_dir result claims, so a test can check the claim against reality.
func parseListDirNotice(t *testing.T, out string) (delivered, omitted int, byteBound bool) {
	t.Helper()
	lines := strings.Split(out, "\n")
	last := lines[len(lines)-1]
	switch {
	case strings.HasPrefix(last, "... truncated at "):
		var budget int
		if n, err := fmt.Sscanf(last, "... truncated at %d bytes (%d more)", &budget, &omitted); n != 2 || err != nil {
			t.Fatalf("byte notice %q unparseable (%v)", last, err)
		}
		return len(lines) - 1, omitted, true
	case strings.HasPrefix(last, "... truncated ("):
		if n, err := fmt.Sscanf(last, "... truncated (%d more)", &omitted); n != 1 || err != nil {
			t.Fatalf("count notice %q unparseable (%v)", last, err)
		}
		return len(lines) - 1, omitted, false
	default:
		return len(lines), 0, false
	}
}

// TestListDirByteBudgetBindsAndStaysHonest: with an entry cap high enough to
// be irrelevant, the byte budget must bind, the whole result must fit inside
// it, and the notice's "(N more)" must equal the entries actually withheld.
// This is defect A's shape: max_list_dir_entries is a first-class operator
// knob, and a raised one used to produce a 400KB result the dispatcher
// destroyed.
func TestListDirByteBudgetBindsAndStaysHonest(t *testing.T) {
	ws := budgetWorkspace(t)
	const total = 400
	writeNamedFiles(t, ws.Abs, total, 100, "")

	const budget = 8192
	tool := &listDirTool{ws: ws, maxEntries: 100000, maxBytes: budget}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > budget {
		t.Fatalf("list_dir returned %d bytes, over its %d-byte budget", len(out), budget)
	}
	delivered, omitted, byteBound := parseListDirNotice(t, out)
	if !byteBound {
		t.Fatalf("expected the byte notice to close the listing; got tail %q", out[max(0, len(out)-80):])
	}
	if delivered+omitted != total {
		t.Fatalf("notice claims %d omitted after %d delivered = %d, but the directory holds %d",
			omitted, delivered, delivered+omitted, total)
	}
	if delivered == 0 {
		t.Fatal("byte budget delivered no entries at all")
	}
}

// TestListDirCountCapStillBindsFirst: when the entry cap is the tighter of
// the two, the historical notice must be unchanged - the byte budget adds a
// second bound, it does not replace the first.
func TestListDirCountCapStillBindsFirst(t *testing.T) {
	ws := budgetWorkspace(t)
	const total = 40
	writeNamedFiles(t, ws.Abs, total, 20, "")

	tool := &listDirTool{ws: ws, maxEntries: 10, maxBytes: 256 * 1024}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "... truncated (30 more)") {
		t.Fatalf("count-cap notice changed; tail=%q", out[max(0, len(out)-60):])
	}
	delivered, omitted, byteBound := parseListDirNotice(t, out)
	if byteBound {
		t.Fatal("byte notice used where the entry cap bound first")
	}
	if delivered != 10 || omitted != 30 {
		t.Fatalf("delivered=%d omitted=%d, want 10/30", delivered, omitted)
	}
}

// TestListDirNoticeAccurateAcrossCapInteraction sweeps both caps across the
// point where each binds. Whichever one stops the listing, delivered+omitted
// must equal the directory's real size and the result must fit the budget.
func TestListDirNoticeAccurateAcrossCapInteraction(t *testing.T) {
	ws := budgetWorkspace(t)
	const total = 120
	writeNamedFiles(t, ws.Abs, total, 60, "")

	for _, maxEntries := range []int{5, 50, 120, 10000} {
		for _, budget := range []int{512, 1024, 4096, 256 * 1024} {
			name := fmt.Sprintf("entries%d_bytes%d", maxEntries, budget)
			t.Run(name, func(t *testing.T) {
				tool := &listDirTool{ws: ws, maxEntries: maxEntries, maxBytes: budget}
				out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"."}`))
				if err != nil {
					t.Fatal(err)
				}
				if len(out) > budget {
					t.Fatalf("result %d bytes over budget %d", len(out), budget)
				}
				delivered, omitted, _ := parseListDirNotice(t, out)
				if delivered+omitted != total {
					t.Fatalf("delivered %d + omitted %d != %d entries present", delivered, omitted, total)
				}
			})
		}
	}
}

// TestGlobByteBudgetBindsAndStaysHonest is defect B's shape at unit level:
// glob's cap is a hardcoded 200 MATCHES, and a workspace-relative path
// approaches PATH_MAX, so the match cap bounds no number of bytes.
func TestGlobByteBudgetBindsAndStaysHonest(t *testing.T) {
	ws := budgetWorkspace(t)
	deep := filepath.Join(ws.Abs, strings.Repeat("d", 200), strings.Repeat("e", 200))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeNamedFiles(t, deep, 60, 80, ".md")

	const budget = 8192
	tool := &globTool{ws: ws, maxMatches: 200, maxBytes: budget}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"**/*.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > budget {
		t.Fatalf("glob returned %d bytes, over its %d-byte budget", len(out), budget)
	}
	if !strings.HasSuffix(out, fmt.Sprintf("... truncated at %d bytes", budget)) {
		t.Fatalf("byte notice missing; tail=%q", out[max(0, len(out)-80):])
	}
	if strings.Contains(out, "truncated at 200 matches") {
		t.Fatal("glob claims a match cap it never reached")
	}
}

// TestGlobMatchCapStillBindsFirst pins the historical notice when the match
// cap is the tighter bound.
func TestGlobMatchCapStillBindsFirst(t *testing.T) {
	ws := budgetWorkspace(t)
	writeNamedFiles(t, ws.Abs, 30, 12, ".md")

	tool := &globTool{ws: ws, maxMatches: 5, maxBytes: 256 * 1024}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"**/*.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "... truncated at 5 matches") {
		t.Fatalf("match-cap notice changed; tail=%q", out[max(0, len(out)-60):])
	}
	if got := len(strings.Split(out, "\n")) - 1; got != 5 {
		t.Fatalf("delivered %d paths, want 5", got)
	}
}

// TestGrepByteBudgetBindsAndStaysHonest: grep is safe at today's arithmetic
// (50 matches x a bounded line), but it is the same unbounded-by-bytes class,
// so it carries the same budget and the same honest notice.
func TestGrepByteBudgetBindsAndStaysHonest(t *testing.T) {
	ws := budgetWorkspace(t)
	deep := filepath.Join(ws.Abs, strings.Repeat("d", 200))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		name := strings.Repeat("g", 120) + fmt.Sprintf("%03d.txt", i)
		if err := os.WriteFile(filepath.Join(deep, name), []byte("NEEDLE here\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	const budget = 4096
	tool := &grepTool{ws: ws, maxMatches: 50, maxBytes: budget}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"NEEDLE"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > budget {
		t.Fatalf("grep returned %d bytes, over its %d-byte budget", len(out), budget)
	}
	if !strings.HasSuffix(out, fmt.Sprintf("... truncated at %d bytes", budget)) {
		t.Fatalf("byte notice missing; tail=%q", out[max(0, len(out)-80):])
	}
	if strings.Contains(out, "truncated at 50 matches") {
		t.Fatal("grep claims a match cap it never reached")
	}
}

// TestGrepMatchCapStillBindsFirst pins the historical notice.
func TestGrepMatchCapStillBindsFirst(t *testing.T) {
	ws := budgetWorkspace(t)
	for i := 0; i < 20; i++ {
		p := filepath.Join(ws.Abs, fmt.Sprintf("f%02d.txt", i))
		if err := os.WriteFile(p, []byte("NEEDLE\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tool := &grepTool{ws: ws, maxMatches: 3, maxBytes: 256 * 1024}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"NEEDLE"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(out, "... truncated at 3 matches") {
		t.Fatalf("match-cap notice changed; tail=%q", out)
	}
}

// TestSearchToolsReportBudgetWhenNothingFits: a budget too small for even one
// result must not report "no matches" - that would be a false negative about
// the workspace rather than a report about the budget.
func TestSearchToolsReportBudgetWhenNothingFits(t *testing.T) {
	ws := budgetWorkspace(t)
	writeNamedFiles(t, ws.Abs, 3, 40, ".md")
	if err := os.WriteFile(filepath.Join(ws.Abs, "hay.txt"), []byte("NEEDLE\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	g := &globTool{ws: ws, maxMatches: 200, maxBytes: 40}
	out, err := g.Execute(context.Background(), json.RawMessage(`{"pattern":"**/*.md"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out == "no matches" {
		t.Fatal("glob reported \"no matches\" for a workspace that has them")
	}
	if !strings.HasPrefix(out, "... truncated at 40 bytes") {
		t.Fatalf("glob budget report = %q", out)
	}

	gr := &grepTool{ws: ws, maxMatches: 50, maxBytes: 30}
	out, err = gr.Execute(context.Background(), json.RawMessage(`{"pattern":"NEEDLE"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out == "no matches" {
		t.Fatal("grep reported \"no matches\" for a workspace that has them")
	}
	if !strings.HasPrefix(out, "... truncated at 30 bytes") {
		t.Fatalf("grep budget report = %q", out)
	}
}

// TestWriteFileOverwriteResultFitsBudget: an overwrite result carries a
// unified diff of the PREVIOUS file contents, so its size is set by the file
// on disk, not by the request. Without a budget a small overwrite of a large
// file produced a ~380KB result that the dispatcher destroyed - the file was
// written and the model was told the call failed.
func TestWriteFileOverwriteResultFitsBudget(t *testing.T) {
	ws := budgetWorkspace(t)
	var old strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&old, "old line %d %s\n", i, strings.Repeat("o", 80))
	}
	if err := os.WriteFile(filepath.Join(ws.Abs, "f.txt"), []byte(old.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	const budget = 16384
	tool := &writeFileTool{ws: ws, maxWriteKB: 500, maxBytes: budget}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"f.txt","content":"tiny\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > budget {
		t.Fatalf("write_file returned %d bytes, over its %d-byte budget", len(out), budget)
	}
	if !strings.HasPrefix(out, "wrote f.txt (") {
		t.Fatalf("write confirmation header lost: %q", out[:min(len(out), 80)])
	}
	if !strings.HasSuffix(out, fmt.Sprintf("... diff truncated at %d bytes", budget)) {
		t.Fatalf("diff-truncation notice missing; tail=%q", out[max(0, len(out)-80):])
	}
	// The write itself must still have happened in full.
	data, err := os.ReadFile(filepath.Join(ws.Abs, "f.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "tiny\n" {
		t.Fatalf("file content = %q, want the full new content", string(data))
	}
}

// TestWriteFileSmallResultUntouched: the budget must not disturb the ordinary
// case where the diff already fits.
func TestWriteFileSmallResultUntouched(t *testing.T) {
	ws := budgetWorkspace(t)
	if err := os.WriteFile(filepath.Join(ws.Abs, "f.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &writeFileTool{ws: ws, maxWriteKB: 500, maxBytes: 256 * 1024}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"f.txt","content":"b\n"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "diff truncated") {
		t.Fatalf("small diff was truncated: %q", out)
	}
}

// TestReadClassBudgetsComeFromMaxReadBytes pins the wiring: the count-capped
// tools take the SAME budget read_file already declares, so the dispatcher's
// derived output ceiling covers them without being raised by them.
func TestReadClassBudgetsComeFromMaxReadBytes(t *testing.T) {
	cases := []struct {
		name string
		opts DefaultOptions
		want int
	}{
		{"default (safety backstop)", DefaultOptions{}, 256 << 20},
		{"explicit max_read_bytes", DefaultOptions{MaxReadBytes: 1 << 20}, 1 << 20},
		{"zero stays uncapped", DefaultOptions{MaxReadBytes: 0}, 256 << 20},
		{"clamped by max_tool_result_bytes", DefaultOptions{MaxReadBytes: 256 * 1024, MaxToolResultBytes: 4096}, 4096},
		{"cap above uncapped raises it", DefaultOptions{MaxReadBytes: 256 * 1024, MaxToolResultBytes: 8 << 20}, 256 * 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.Workspace = budgetWorkspace(t)
			reg := NewDefaultRegistry(opts)
			for _, name := range []string{"list_dir", "grep", "glob", "write_file"} {
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
		})
	}
}

// TestReadClassToolsDoNotDeclareLoopTruncationBound extends commit 2dca36b's
// rule to the newly budgeted tools: Capability.MaxResultBytes is the agent
// loop's wire truncation bound, and these tools append honest truncation
// notices that a tail cut would remove.
func TestReadClassToolsDoNotDeclareLoopTruncationBound(t *testing.T) {
	reg := NewDefaultRegistry(DefaultOptions{Workspace: budgetWorkspace(t)})
	for _, name := range []string{"list_dir", "grep", "glob", "write_file", "fetch_url"} {
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

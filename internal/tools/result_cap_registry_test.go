package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/codeintel"
	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// newCapRegistry builds a default registry over a temp workspace containing
// one file of lineCount lines, each lineLen bytes of 'a' plus a newline.
func newCapRegistry(t *testing.T, capBytes, lineCount, lineLen int) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	line := strings.Repeat("a", lineLen)
	var b strings.Builder
	for i := 0; i < lineCount; i++ {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return NewDefaultRegistry(DefaultOptions{Workspace: ws, MaxToolResultBytes: capBytes, MaxReadBytes: 256 * 1024}), "big.txt"
}

// parseWindowHeader extracts X, Y, and Z from a "… lines X–Y of Z" first line.
func parseWindowHeader(t *testing.T, out string) (first, last, total int) {
	t.Helper()
	header, _, ok := strings.Cut(out, "\n")
	if !ok {
		t.Fatalf("output has no header line: %q", out)
	}
	if n, err := fmt.Sscanf(header, "… lines %d–%d of %d", &first, &last, &total); n != 3 || err != nil {
		t.Fatalf("header %q does not match \"… lines X–Y of Z\" (%v)", header, err)
	}
	return first, last, total
}

// TestReadFileWindowHeaderHonestUnderResultCap pins that a configured
// [tools] max_tool_result_bytes keeps read_file honest: the tool's own
// output stays under the cap (so the loop never tail-cuts below what the
// window header claims), and the header's line range is exactly the lines
// delivered. 1024 and 1025 are adversarial caps at the config floor.
func TestReadFileWindowHeaderHonestUnderResultCap(t *testing.T) {
	for _, capBytes := range []int{1024, 1025} {
		t.Run(fmt.Sprintf("cap%d", capBytes), func(t *testing.T) {
			// 200 lines x 80 bytes ≈ 16KB, far over any adversarial cap.
			reg, path := newCapRegistry(t, capBytes, 200, 80)
			tool, ok := reg.Get("read_file")
			if !ok {
				t.Fatal("read_file not registered")
			}

			// Window request larger than the cap allows.
			out, err := tool.Execute(context.Background(),
				json.RawMessage(`{"path":"`+path+`","offset":1,"limit":200}`))
			if err != nil {
				t.Fatal(err)
			}
			if len(out) >= capBytes {
				t.Fatalf("window output is %d bytes, not under cap %d - the loop would tail-cut below the header's claim", len(out), capBytes)
			}
			if !strings.Contains(out, "truncated at max read size") {
				t.Fatalf("tool truncation notice missing:\n%s", out)
			}
			first, last, total := parseWindowHeader(t, out)
			if total != 200 {
				t.Fatalf("header total %d != 200 file lines", total)
			}
			lines := strings.Split(out, "\n")
			// header + delivered lines + truncation notice line.
			delivered := len(lines) - 2
			if got := last - first + 1; got != delivered {
				t.Fatalf("header claims %d lines (%d–%d) but %d were delivered", got, first, last, delivered)
			}

			// Full-file read of an over-cap file must refuse honestly.
			_, err = tool.Execute(context.Background(), json.RawMessage(`{"path":"`+path+`"}`))
			if err == nil {
				t.Fatal("full read of an over-cap file succeeded; want file-too-large error")
			}
			if !strings.Contains(err.Error(), "file too large") || !strings.Contains(err.Error(), "offset and limit") {
				t.Fatalf("error %q does not direct the model to offset/limit windowing", err)
			}
		})
	}
}

// TestFindReferencesBudgetClampedToConfiguredCap pins that a configured cap
// tightens find_references' self-truncation budget so its output is valid
// JSON that the loop never has to cut.
func TestFindReferencesBudgetClampedToConfiguredCap(t *testing.T) {
	reg, _ := newCapRegistry(t, 2048, 1, 8)
	tool, ok := reg.Get("find_references")
	if !ok {
		t.Fatal("find_references not registered")
	}
	fr, ok := tool.(*findReferencesTool)
	if !ok {
		t.Fatalf("unexpected tool type %T", tool)
	}
	if fr.maxBytes != 2048 {
		t.Fatalf("find_references budget = %d, want clamped to configured cap 2048", fr.maxBytes)
	}
	if got := fr.Capability(nil).MaxResultBytes; got != 2048 {
		t.Fatalf("Capability.MaxResultBytes = %d, want 2048", got)
	}

	// With many locations the self-truncating marshal must stay valid JSON
	// within the clamped budget.
	fake := &fakeReferenceFinder{}
	for i := 0; i < 500; i++ {
		fake.result.Locations = append(fake.result.Locations, codeintel.Location{
			Path: fmt.Sprintf("pkg/file_%d.go", i), Line: i + 1,
			Symbol: "Thing", Role: codeintel.RoleCaller,
		})
	}
	fake.result.Complete = true
	fr.finder = fake
	out, err := fr.Execute(context.Background(), json.RawMessage(`{"symbol":"Thing"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 2048 {
		t.Fatalf("output %d bytes exceeds clamped budget 2048", len(out))
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
}

// TestFindReferencesBudgetUnclampedWithoutCap pins the uncapped default: no
// configured ceiling leaves the tool's own 100KB budget in place.
func TestFindReferencesBudgetUnclampedWithoutCap(t *testing.T) {
	reg, _ := newCapRegistry(t, 0, 1, 8)
	tool, ok := reg.Get("find_references")
	if !ok {
		t.Fatal("find_references not registered")
	}
	fr := tool.(*findReferencesTool)
	if fr.maxBytes != 100_000 {
		t.Fatalf("find_references budget = %d, want 100000 with no configured cap", fr.maxBytes)
	}
}

// TestFindReferencesRegisteredLimitDefault pins the registry's find_references
// result-limit default at 50, matching the schema the model sees (Description
// and Parameters both document "default 50"). The registry change from an
// uncapped 0 to 50 is what makes the tool's limit fallback in Execute resolve
// to 50 for every caller that omits the parameter.
func TestFindReferencesRegisteredLimitDefault(t *testing.T) {
	reg, _ := newCapRegistry(t, 0, 1, 8)
	tool, ok := reg.Get("find_references")
	if !ok {
		t.Fatal("find_references not registered")
	}
	fr := tool.(*findReferencesTool)
	if fr.limit != 50 {
		t.Fatalf("find_references registered limit = %d, want 50", fr.limit)
	}
}

// newCapRegistryWithDiagnostics builds a default registry like newCapRegistry
// over a temp workspace, additionally wiring a get_diagnostics default command:
// its argv[0] is on the run_command allowlist and resolvable on PATH, so
// registerDiagnosticsTool registers the tool (get_diagnostics is advertised
// only when its configured default command can run). The command never executes
// in these budget pins; it only has to register.
func newCapRegistryWithDiagnostics(t *testing.T, capBytes int, argv ...string) *Registry {
	t.Helper()
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewDefaultRegistry(DefaultOptions{
		Workspace:           ws,
		MaxToolResultBytes:  capBytes,
		MaxReadBytes:        256 * 1024,
		DiagnosticsCommands: map[string][]string{"default": argv},
		RunAllowlist:        []string{argv[0]},
	})
}

// TestGetDiagnosticsBudgetClampedToConfiguredCap pins that a configured cap
// clamps get_diagnostics' result-envelope budget (diagnosticsDefaultBudget)
// down to the cap, so the tool's declared budget stays under the loop's
// result ceiling and the loop never has to tail-cut its JSON envelope.
func TestGetDiagnosticsBudgetClampedToConfiguredCap(t *testing.T) {
	requirePOSIXDiagnostics(t)
	reg := newCapRegistryWithDiagnostics(t, 2048, "sh", "-c", "true")
	tool, ok := reg.Get(GetDiagnosticsToolName)
	if !ok {
		t.Fatal("get_diagnostics not registered")
	}
	gd := tool.(*getDiagnosticsTool)
	if gd.maxBytes != 2048 {
		t.Fatalf("get_diagnostics budget = %d, want clamped to configured cap 2048", gd.maxBytes)
	}
	if got := gd.ResultBudgetBytes(); got != 2048 {
		t.Fatalf("ResultBudgetBytes = %d, want 2048", got)
	}
}

// TestGetDiagnosticsBudgetUnclampedWithoutCap pins the uncapped default: no
// configured max_tool_result_bytes leaves get_diagnostics' result-envelope
// budget at the 256 KiB dispatcher ceiling floor (diagnosticsDefaultBudget),
// so the tool's declared budget can never raise the shared output ceiling.
func TestGetDiagnosticsBudgetUnclampedWithoutCap(t *testing.T) {
	requirePOSIXDiagnostics(t)
	reg := newCapRegistryWithDiagnostics(t, 0, "sh", "-c", "true")
	tool, ok := reg.Get(GetDiagnosticsToolName)
	if !ok {
		t.Fatal("get_diagnostics not registered")
	}
	gd := tool.(*getDiagnosticsTool)
	if gd.maxBytes != 256*1024 {
		t.Fatalf("get_diagnostics budget = %d, want 256 KiB with no configured cap", gd.maxBytes)
	}
	if got := gd.ResultBudgetBytes(); got != 256*1024 {
		t.Fatalf("ResultBudgetBytes = %d, want 256 KiB", got)
	}
}

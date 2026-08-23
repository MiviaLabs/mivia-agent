package agent

// Tests for the per-call ref-only tool shim applied on the SDK path.
// The shim mirrors the legacy CLI's refOnlyTier
// (internal/agent/shape_batch_refonly.go:25-45) on top of the SDK's
// tool wrapper.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/remainder"
	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// refOnlyTestTool returns a fixed string per name. The shim should
// spool the result when the body clears the floor.
type refOnlyTestTool struct {
	name  string
	body  string
	calls int
}

func (r *refOnlyTestTool) Name() string               { return r.name }
func (r *refOnlyTestTool) Description() string        { return "ref-only test tool" }
func (r *refOnlyTestTool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (r *refOnlyTestTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	r.calls++
	return r.body, nil
}

// TestRefOnlyShimSpoolsOversizedResult asserts that when an inner
// tool returns a body longer than the floor and the tool is named
// in the ref-only list, the shim mints a ref through the CLI's
// *remainder.Spool and returns the same ref-notice text the legacy
// shape_batch produces.
func TestRefOnlyShimSpoolsOversizedResult(t *testing.T) {
	body := strings.Repeat("a", 32*1024) // 32 KiB > 16 KiB floor
	inner := &refOnlyTestTool{name: "bigtool", body: body}
	cliReg := tools.NewRegistry()
	cliReg.Register(inner)
	sdkReg, err := sdkadapter.ConvertToolRegistry(cliReg)
	if err != nil {
		t.Fatalf("ConvertToolRegistry: %v", err)
	}
	spool := remainder.NewSpool(&stubContentStore{})
	applyRefOnlyShim(sdkReg, nil, []string{"bigtool"}, spool, BatchDegradeFloorBytes, "principal-1")
	wrapped, ok := sdkReg.Get("bigtool")
	if !ok {
		t.Fatal("SDK registry missing bigtool")
	}
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, ok := out.Value.(string)
	if !ok {
		t.Fatalf("Run returned %T, want string", out.Value)
	}
	if !strings.Contains(s, "elided to a remainder ref") {
		t.Fatalf("Run returned %q, want ref-notice text", s)
	}
	if !strings.Contains(s, "read_output") {
		t.Fatalf("Run returned %q, want the read_output hint", s)
	}
	if inner.calls != 1 {
		t.Fatalf("inner tool invoked %d times; want 1 (shim should still call it once)", inner.calls)
	}
}

// TestRefOnlyShimPassesThroughSmallResult asserts that bodies under
// the floor pass through unchanged. Mirrors refOnlyTier:27 which
// short-circuits on `p.totalN < BatchDegradeFloorBytes`.
func TestRefOnlyShimPassesThroughSmallResult(t *testing.T) {
	body := "small"
	inner := &refOnlyTestTool{name: "smalltool", body: body}
	cliReg := tools.NewRegistry()
	cliReg.Register(inner)
	sdkReg, err := sdkadapter.ConvertToolRegistry(cliReg)
	if err != nil {
		t.Fatalf("ConvertToolRegistry: %v", err)
	}
	spool := remainder.NewSpool(&stubContentStore{})
	applyRefOnlyShim(sdkReg, nil, []string{"smalltool"}, spool, BatchDegradeFloorBytes, "principal-1")
	wrapped, _ := sdkReg.Get("smalltool")
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, _ := out.Value.(string)
	if s != body {
		t.Fatalf("Run returned %q, want the inner body %q", s, body)
	}
}

// TestRefOnlyShimDoesNotWrapUnnamedTool asserts the shim skips tools
// that are not in the named list, even when the body is oversized.
func TestRefOnlyShimDoesNotWrapUnnamedTool(t *testing.T) {
	body := strings.Repeat("a", 32*1024)
	inner := &refOnlyTestTool{name: "passthrough", body: body}
	cliReg := tools.NewRegistry()
	cliReg.Register(inner)
	sdkReg, err := sdkadapter.ConvertToolRegistry(cliReg)
	if err != nil {
		t.Fatalf("ConvertToolRegistry: %v", err)
	}
	spool := remainder.NewSpool(&stubContentStore{})
	applyRefOnlyShim(sdkReg, nil, []string{"bigtool"}, spool, BatchDegradeFloorBytes, "principal-1")
	wrapped, _ := sdkReg.Get("passthrough")
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, _ := out.Value.(string)
	if s != body {
		t.Fatalf("Run returned %q, want the inner body %q (unnamed tool should pass through)", s, body)
	}
}

// TestRefOnlyShimPlainNoticeOnSpoolFailure asserts the fallback path:
// a nil spool store yields a plain notice (no ref), matching
// refOnlyTier:36-43.
func TestRefOnlyShimPlainNoticeOnSpoolFailure(t *testing.T) {
	body := strings.Repeat("a", 32*1024)
	inner := &refOnlyTestTool{name: "failspool", body: body}
	cliReg := tools.NewRegistry()
	cliReg.Register(inner)
	sdkReg, err := sdkadapter.ConvertToolRegistry(cliReg)
	if err != nil {
		t.Fatalf("ConvertToolRegistry: %v", err)
	}
	// A nil-store spool returns "" for every Spool call (INV-AG-10).
	spool := remainder.NewSpool(nil)
	applyRefOnlyShim(sdkReg, nil, []string{"failspool"}, spool, BatchDegradeFloorBytes, "principal-1")
	wrapped, _ := sdkReg.Get("failspool")
	out, err := wrapped.Run(context.Background(), sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, _ := out.Value.(string)
	if strings.Contains(s, "remainder ref") {
		t.Fatalf("Run returned %q, want plain notice (no ref) on spool failure", s)
	}
	if !strings.Contains(s, "elided") {
		t.Fatalf("Run returned %q, want plain elision notice", s)
	}
}

// TestRefOnlySizeLabelPowerOfTwoBucket asserts the size label
// follows contextmgr's bucket convention: 32 KiB -> 32 KiB,
// 17 KiB -> 32 KiB, 1 MiB -> 1 MiB.
func TestRefOnlySizeLabelPowerOfTwoBucket(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{16 << 10, "16 KiB"},
		{(16 << 10) + 1, "32 KiB"},
		{1 << 20, "1 MiB"},
		{(1 << 20) + 1, "2 MiB"},
	}
	for _, c := range cases {
		if got := refOnlySizeLabel(c.n); got != c.want {
			t.Errorf("refOnlySizeLabel(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// stubContentStore satisfies remainder.ContentStore with no-op
// storage: StoreContent discards the bytes, LoadContent always
// reports not-found. The shim only needs Spool to mint a ref; the
// underlying bytes are never read by these tests.
type stubContentStore struct{}

func (*stubContentStore) StoreContent(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (*stubContentStore) LoadContent(_ context.Context, _ string) ([]byte, error) {
	return nil, remainder.ErrNotFound
}

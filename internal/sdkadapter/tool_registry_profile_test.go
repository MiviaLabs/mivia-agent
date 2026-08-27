package sdkadapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdktools "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// blockingCLITool is a CLI tool with NO Capability method (undeclared
// profile). Execute blocks for the configured duration, deliberately
// ignoring ctx, so only the SDK registry's run backstop can cut the
// call short.
type blockingCLITool struct {
	name  string
	block time.Duration
}

func (b *blockingCLITool) Name() string               { return b.name }
func (b *blockingCLITool) Description() string        { return "blocking" }
func (b *blockingCLITool) Parameters() map[string]any { return map[string]any{"type": "object"} }
func (b *blockingCLITool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	time.Sleep(b.block)
	return "done", nil
}

// TestConvertedRegistryToolPublishesCapabilityProfile pins Part 3 of
// the timeout bridge: the SDK tool value REGISTERED for a CLI
// CapableTool must satisfy the SDK's ProfiledTool interface and report
// the CLI Capability.Timeout, because the SDK's effectiveRunTimeout
// consults only the outermost registered value. Without the bridge the
// SDK sees no profile and hard-caps the run at DefaultRunTimeout
// (10 minutes) - the dispatch_tasks 12h-budget kill.
func TestConvertedRegistryToolPublishesCapabilityProfile(t *testing.T) {
	cli := &fakeCapableTool{name: "long_tool", cap: tools.Capability{
		Class:   tools.ExecutionWrite,
		Timeout: 42 * time.Minute,
	}}
	reg := tools.NewRegistry()
	reg.Register(cli)
	sdkReg, err := ConvertToolRegistry(reg)
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := sdkReg.Get("long_tool")
	if !ok {
		t.Fatal("long_tool missing from converted registry")
	}
	pt, ok := registered.(sdktools.ProfiledTool)
	if !ok {
		t.Fatalf("registered tool %T does not implement sdktools.ProfiledTool; the SDK run backstop cannot see Capability.Timeout", registered)
	}
	if got := pt.ExecutionProfile().Timeout; got != 42*time.Minute {
		t.Fatalf("ExecutionProfile().Timeout = %v, want 42m", got)
	}
}

// TestAdmissionWrappedToolPublishesCapabilityProfile proves every
// admission/approval wrapper layer forwards the profile: the OUTERMOST
// registered value is what the SDK consults, and Go interface wrappers
// silently strip optional interfaces unless each layer forwards them
// explicitly.
func TestAdmissionWrappedToolPublishesCapabilityProfile(t *testing.T) {
	cli := &fakeCapableTool{name: "long_tool", cap: tools.Capability{
		Class:   tools.ExecutionWrite,
		Timeout: 42 * time.Minute,
	}}
	reg := tools.NewRegistry()
	reg.Register(cli)
	pred := AdmissionPredicates{
		StagedMessage: func(string) (string, bool) { return "", false },
		ApprovalGate: func(context.Context, string, json.RawMessage) ApprovalResult {
			return ApprovalResult{Approved: true}
		},
		ApprovalPolicy: "auto",
	}
	sdkReg, err := ConvertToolRegistryWithAdmission(reg, pred)
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := sdkReg.Get("long_tool")
	if !ok {
		t.Fatal("long_tool missing from converted registry")
	}
	pt, ok := registered.(sdktools.ProfiledTool)
	if !ok {
		t.Fatalf("admission-wrapped tool %T does not implement sdktools.ProfiledTool", registered)
	}
	if got := pt.ExecutionProfile().Timeout; got != 42*time.Minute {
		t.Fatalf("ExecutionProfile().Timeout = %v, want 42m", got)
	}
}

// TestConvertedRegistryToolWithoutCapabilityReportsZeroTimeout pins
// the fall-through semantics: a CLI tool with no Capability must
// publish a ZERO profile Timeout ("undeclared" in the SDK's table), so
// the registry-wide default from CLI config applies - never TimeoutNone,
// which would exempt the tool from any configured backstop.
func TestConvertedRegistryToolWithoutCapabilityReportsZeroTimeout(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&blockingCLITool{name: "plain_tool"})
	sdkReg, err := ConvertToolRegistry(reg)
	if err != nil {
		t.Fatal(err)
	}
	registered, ok := sdkReg.Get("plain_tool")
	if !ok {
		t.Fatal("plain_tool missing from converted registry")
	}
	pt, ok := registered.(sdktools.ProfiledTool)
	if !ok {
		t.Fatalf("registered tool %T does not implement sdktools.ProfiledTool", registered)
	}
	if got := pt.ExecutionProfile().Timeout; got != 0 {
		t.Fatalf("no-capability tool ExecutionProfile().Timeout = %v, want 0 (undeclared)", got)
	}
}

// TestConvertToolRegistryForwardsRunTimeoutOption pins Part 2 of the
// bridge: registry options given to the converter must reach the SDK's
// New, so a CLI-config run-timeout default actually bounds tools that
// declare no profile Timeout.
func TestConvertToolRegistryForwardsRunTimeoutOption(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&blockingCLITool{name: "slow_tool", block: 2 * time.Second})
	sdkReg, err := ConvertToolRegistry(reg, sdktools.WithDefaultRunTimeout(150*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = sdkReg.Run(context.Background(), "slow_tool", sdktools.InOut{Value: map[string]any{}})
	if !errors.Is(err, sdktools.ErrRunTimeout) {
		t.Fatalf("Run err = %v, want ErrRunTimeout (configured 150ms default must reach the SDK registry)", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Run took %v; the 150ms bound did not fire", elapsed)
	}
}

// TestConvertToolRegistryTimeoutNoneUncaps proves the uncapped mapping:
// with TimeoutNone as the registry default, a no-profile tool runs to
// completion instead of being killed by the SDK's hardcoded 10-minute
// DefaultRunTimeout path.
func TestConvertToolRegistryTimeoutNoneUncaps(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&blockingCLITool{name: "slow_tool", block: 300 * time.Millisecond})
	sdkReg, err := ConvertToolRegistry(reg, sdktools.WithDefaultRunTimeout(sdktools.TimeoutNone))
	if err != nil {
		t.Fatal(err)
	}
	out, err := sdkReg.Run(context.Background(), "slow_tool", sdktools.InOut{Value: map[string]any{}})
	if err != nil {
		t.Fatalf("Run err = %v, want success under TimeoutNone", err)
	}
	if s, _ := out.Value.(string); s != "done" {
		t.Fatalf("out = %q, want %q", s, "done")
	}
}

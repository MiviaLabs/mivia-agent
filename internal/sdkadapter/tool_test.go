package sdkadapter

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// TestCapabilityCLItoSDK pins the forward mapping: CLI Capability
// (uint8 enum) converts to SDK ExecutionProfile (string enum). Every
// CLI ExecutionClass maps to its SDK counterpart; MaxResultBytes is
// dropped because the SDK ExecutionProfile has no equivalent.
func TestCapabilityCLItoSDK(t *testing.T) {
	for _, cliClass := range []tools.ExecutionClass{tools.ExecutionRead, tools.ExecutionWrite, tools.ExecutionExternal} {
		t.Run(classNameCLI(cliClass), func(t *testing.T) {
			in := tools.Capability{
				Class:          cliClass,
				ResourceKey:    "key-1",
				Timeout:        5 * time.Second,
				MaxResultBytes: 4096,
			}
			out := CapabilityToExecutionProfile(in)
			if !reflect.DeepEqual(out.Class, sdkClassForCLI(cliClass)) {
				t.Fatalf("Class mismatch: %v vs %v", out.Class, sdkClassForCLI(cliClass))
			}
			if out.ResourceKey != "key-1" {
				t.Fatalf("ResourceKey mismatch: %q", out.ResourceKey)
			}
			if out.Timeout != 5*time.Second {
				t.Fatalf("Timeout mismatch: %v", out.Timeout)
			}
		})
	}
}

// TestCapabilitySDKtoCLI pins the reverse mapping: SDK ExecutionProfile
// (string enum) converts to CLI Capability (uint8 enum). The SDK's
// unclassified class becomes the zero CLI Capability; the three named
// SDK classes map onto the matching CLI ExecutionClass. ResourceKey and
// Timeout round-trip; the SDK side has no MaxResultBytes field.
func TestCapabilitySDKtoCLI(t *testing.T) {
	cases := []struct {
		in   sdkshape.ExecutionClass
		want tools.ExecutionClass
	}{
		{sdkshape.ExecutionClassRead, tools.ExecutionRead},
		{sdkshape.ExecutionClassWrite, tools.ExecutionWrite},
		{sdkshape.ExecutionClassExternal, tools.ExecutionExternal},
		{sdkshape.ExecutionClassUnclassified, tools.ExecutionClass(0)},
	}
	for _, c := range cases {
		t.Run(string(c.in), func(t *testing.T) {
			in := sdkshape.ExecutionProfile{Class: c.in, ResourceKey: "k", Timeout: 3 * time.Second}
			out := ExecutionProfileToCapability(in)
			if out.Class != c.want {
				t.Fatalf("Class mismatch: %v vs %v", out.Class, c.want)
			}
			if out.ResourceKey != "k" {
				t.Fatalf("ResourceKey mismatch: %q", out.ResourceKey)
			}
			if out.Timeout != 3*time.Second {
				t.Fatalf("Timeout mismatch: %v", out.Timeout)
			}
			if out.MaxResultBytes != 0 {
				t.Fatalf("MaxResultBytes must be zero on reverse mapping: %d", out.MaxResultBytes)
			}
		})
	}
}

// TestCapabilityRoundTripForRecognizedClasses confirms that the three
// named classes survive a CLI -> SDK -> CLI round-trip with the same
// resource key and timeout.
func TestCapabilityRoundTripForRecognizedClasses(t *testing.T) {
	for _, cliClass := range []tools.ExecutionClass{tools.ExecutionRead, tools.ExecutionWrite, tools.ExecutionExternal} {
		t.Run(classNameCLI(cliClass), func(t *testing.T) {
			in := tools.Capability{
				Class:          cliClass,
				ResourceKey:    "key",
				Timeout:        2 * time.Second,
				MaxResultBytes: 8192,
			}
			sdk := CapabilityToExecutionProfile(in)
			back := ExecutionProfileToCapability(sdk)
			if back.Class != in.Class {
				t.Fatalf("Class did not round-trip: %v vs %v", back.Class, in.Class)
			}
			if back.ResourceKey != in.ResourceKey {
				t.Fatalf("ResourceKey did not round-trip")
			}
			if back.Timeout != in.Timeout {
				t.Fatalf("Timeout did not round-trip")
			}
			if back.MaxResultBytes != 0 {
				t.Fatalf("MaxResultBytes must reset on round-trip: %d", back.MaxResultBytes)
			}
		})
	}
}

// TestToolTranslatorCapabilityPreserved exercises the higher-level Tool
// translator: a CLI CapableTool that exposes Capability() returns an
// SDK ProfiledTool whose execution profile carries the same class,
// resource key, and timeout as the source.
func TestToolTranslatorCapabilityPreserved(t *testing.T) {
	src := &fakeCapableTool{
		name: "demo",
		cap: tools.Capability{
			Class:          tools.ExecutionRead,
			ResourceKey:    "k-1",
			Timeout:        10 * time.Second,
			MaxResultBytes: 1024,
		},
	}
	bridge := BridgeCapableTool(src)
	sdk := bridge.ExecutionProfile()
	if !reflect.DeepEqual(sdk.Class, sdkshape.ExecutionClassRead) {
		t.Fatalf("SDK Class: %v, want %v", sdk.Class, sdkshape.ExecutionClassRead)
	}
	if sdk.ResourceKey != "k-1" {
		t.Fatalf("ResourceKey: %q", sdk.ResourceKey)
	}
	if sdk.Timeout != 10*time.Second {
		t.Fatalf("Timeout: %v", sdk.Timeout)
	}
	if bridge.Name() != "demo" {
		t.Fatalf("Name: %q", bridge.Name())
	}
}

// classNameCLI is a tiny test helper that returns the string form of a
// CLI ExecutionClass for table-driven sub-test labels.
func classNameCLI(c tools.ExecutionClass) string {
	switch c {
	case tools.ExecutionRead:
		return "read"
	case tools.ExecutionWrite:
		return "write"
	case tools.ExecutionExternal:
		return "external"
	default:
		return "unknown"
	}
}

// sdkClassForCLI returns the matching SDK ExecutionClass for a CLI
// ExecutionClass. Used by tests only.
func sdkClassForCLI(c tools.ExecutionClass) sdkshape.ExecutionClass {
	switch c {
	case tools.ExecutionRead:
		return sdkshape.ExecutionClassRead
	case tools.ExecutionWrite:
		return sdkshape.ExecutionClassWrite
	case tools.ExecutionExternal:
		return sdkshape.ExecutionClassExternal
	default:
		return sdkshape.ExecutionClassUnclassified
	}
}

// fakeCapableTool is a tiny CLI Tool stand-in that exposes a Capability.
// Its Execute is unused; the tests only exercise the bridge's profile
// mapping.
type fakeCapableTool struct {
	name string
	cap  tools.Capability
}

func (f *fakeCapableTool) Name() string               { return f.name }
func (f *fakeCapableTool) Description() string        { return "fake" }
func (f *fakeCapableTool) Parameters() map[string]any { return nil }
func (f *fakeCapableTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", nil
}
func (f *fakeCapableTool) Capability(_ json.RawMessage) tools.Capability { return f.cap }

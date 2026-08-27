package sdkadapter

import (
	"github.com/MiviaLabs/mivia-agent/internal/tools"
	sdkshape "github.com/MiviaLabs/mivia-ai-sdk/tools"
)

// CapabilityToExecutionProfile maps the CLI's tools.Capability onto the
// SDK's tools.ExecutionProfile. Class, ResourceKey, and Timeout are
// preserved verbatim; MaxResultBytes is dropped because the SDK
// ExecutionProfile has no equivalent. The class conversion uses the
// three named classes by string identity; an out-of-enum CLI class
// (zero value or future expansion) maps to the SDK's
// ExecutionClassUnclassified so the bridge never invents a Class.
func CapabilityToExecutionProfile(c tools.Capability) sdkshape.ExecutionProfile {
	return sdkshape.ExecutionProfile{
		Class:       cliClassToSDKClass(c.Class),
		ResourceKey: c.ResourceKey,
		Timeout:     c.Timeout,
	}
}

// ExecutionProfileToCapability maps the SDK's tools.ExecutionProfile
// onto the CLI's tools.Capability. Class, ResourceKey, and Timeout
// round-trip; MaxResultBytes is the zero value because the SDK
// ExecutionProfile has no equivalent.
//
// The SDK ExecutionClassUnclassified ("") maps to the CLI zero class
// (i.e. ExecutionRead=0). A reverse bridge that mapped all SDK classes
// onto the highest-risk CLI class would silently over-classify
// unclassified tools; the zero-value mapping is the conservative
// default the runtime already relies on.
func ExecutionProfileToCapability(p sdkshape.ExecutionProfile) tools.Capability {
	return tools.Capability{
		Class:       sdkClassToCLIClass(p.Class),
		ResourceKey: p.ResourceKey,
		Timeout:     p.Timeout,
	}
}

// BridgeCapableTool wraps a CLI tools.CapableTool so the SDK's
// ProfiledTool interface (and only that interface) can call its
// Capability method through the bridge. The wrapped value carries the
// class, resource key, and timeout through one Round-Trip call; tool
// execution is intentionally not bridged - the CLI runtime is what
// actually runs tools today, and exposing Run on the SDK side would
// hand a second execution path to a Tool that does not know about it.
func BridgeCapableTool(t tools.CapableTool) *capableToolBridge {
	return &capableToolBridge{src: t}
}

// capableToolBridge is the SDK-facing adapter for a CLI CapableTool. It
// carries a reference to the source tool; the SDK-side ProfiledTool
// methods read from the source through the bridge.
type capableToolBridge struct {
	src tools.CapableTool
}

// Name returns the source tool's registration key, verbatim.
func (b *capableToolBridge) Name() string { return b.src.Name() }

// ExecutionProfile reads the source tool's capability through the
// bridge. The arg is ignored: a CLI CapableTool.Capability is called
// with an args payload by the runtime, but the SDK ProfiledTool
// interface is static (no arguments), and the bridge chooses the
// zero-payload shape to keep the SDK-side view deterministic.
func (b *capableToolBridge) ExecutionProfile() sdkshape.ExecutionProfile {
	return CapabilityToExecutionProfile(b.src.Capability(nil))
}

// cliClassToSDKClass maps a CLI ExecutionClass onto its SDK counterpart.
// The mapping is exhaustive for the three named classes; an
// out-of-enum CLI class becomes Unclassified.
func cliClassToSDKClass(c tools.ExecutionClass) sdkshape.ExecutionClass {
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

// sdkClassToCLIClass maps an SDK ExecutionClass onto its CLI counterpart.
// The mapping is exhaustive for the four declared SDK classes; an
// out-of-enum SDK class becomes the CLI zero class (ExecutionRead=0).
func sdkClassToCLIClass(c sdkshape.ExecutionClass) tools.ExecutionClass {
	switch c {
	case sdkshape.ExecutionClassRead:
		return tools.ExecutionRead
	case sdkshape.ExecutionClassWrite:
		return tools.ExecutionWrite
	case sdkshape.ExecutionClassExternal:
		return tools.ExecutionExternal
	default:
		// Unclassified and any out-of-enum value land on the CLI zero
		// class. The zero class is ExecutionRead=0, which is the
		// lowest-risk classification; a higher-risk default would
		// over-classify an unknown tool.
		return tools.ExecutionClass(0)
	}
}

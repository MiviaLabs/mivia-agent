package sdkadapter_test

import (
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/sdkadapter"
	sdkmcp "github.com/MiviaLabs/mivia-ai-sdk/mcp"
)

// TestMCPClientTypeAlias asserts that the bridge's Client type
// is the SDK's *mcp.Client so future code that has either name
// shares the same underlying type. A type-assertion is the
// canonical Go way to verify a type alias.
func TestMCPClientTypeAlias(t *testing.T) {
	var c *sdkadapter.Client
	if _, ok := interface{}(c).(*sdkmcp.Client); !ok {
		t.Fatal("sdkadapter.Client is not a *mivia-ai-sdk/mcp.Client alias")
	}
}

// TestMCPClientInfoTypeAlias asserts that ClientInfo is the SDK's
// ClientInfo alias.
func TestMCPClientInfoTypeAlias(t *testing.T) {
	var info sdkadapter.ClientInfo
	if _, ok := interface{}(info).(sdkmcp.ClientInfo); !ok {
		t.Fatal("sdkadapter.ClientInfo is not a *mivia-ai-sdk/mcp.ClientInfo alias")
	}
}

// TestMCPClientOptionsTypeAlias asserts that ClientOptions is the
// SDK's ClientOptions alias.
func TestMCPClientOptionsTypeAlias(t *testing.T) {
	var opts sdkadapter.ClientOptions
	if _, ok := interface{}(opts).(sdkmcp.ClientOptions); !ok {
		t.Fatal("sdkadapter.ClientOptions is not a *mivia-ai-sdk/mcp.ClientOptions alias")
	}
}

// TestMCPConnectCannotBeExercisedWithoutTransport covers the bridge
// boundary that cannot be reached without a real MCP server:
// Connect requires a non-nil Transport, and the SDK panics on a
// nil one. The bridge cannot validate Connect's caller-supplied
// arguments without duplicating the SDK's own validation, which
// is brittle. The three type-alias tests above already pin the
// bridge's type-assertion contract; an end-to-end Connect test
// belongs at the SDK repo (mivia-ai-sdk/mcp), not here.

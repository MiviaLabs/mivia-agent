// Package sdkadapter — MCP bridge.
//
// The CLI's internal/mcp/ package is the full lifecycle host:
// per-server process management (executable_unix.go /
// executable_other.go), the in-memory manager (manager.go), the
// tool render and wrap pipeline (render.go, tool.go), the
// redaction and schema-byte-cap enforcement
// (sanitizeToolDescription / sanitizeToolSchema), the host-safe
// tool-name encoder (config.go: EncodeToolName), and the inbound
// reader that pipes stdio into the SDK's Transport. None of
// these move to the SDK; they are CLI product code.
//
// This file re-exports the SDK's mcp.Client so future code that
// wants the canonical SDK shape (e.g. a codeintel analyzer
// driving an MCP server, or a future slash command that surfaces
// an SDK-shaped tool list) reaches it through the bridge without
// importing the SDK directly. The CLI's internal/mcp/ continues
// to wrap the SDK with the redaction, schema-byte-cap, and
// tool-name-encoding wrappers per the binding plan's B.2 #11
// row; the bridge is the entry point, not a replacement.
package sdkadapter

import (
	"context"

	sdkmcp "github.com/MiviaLabs/mivia-ai-sdk/mcp"
)

// Client re-exports the SDK's mcp.Client.
//
// The CLI reaches the bridge, never the SDK directly, so the SDK
// dependency stays inside internal/sdkadapter. The bridge is a
// type alias on purpose: the local code that has *sdkmcp.Client
// via the SDK's own wiring (B.2 #8, when it lands) shares the
// same pointer the bridge returns, and methods dispatched
// through either name reach the same Connect/Close/ListTools/
// CallTool implementation without a wrapper allocation.
type Client = sdkmcp.Client

// ClientInfo re-exports the SDK's ClientInfo (caller's name and
// version during the MCP initialize handshake).
type ClientInfo = sdkmcp.ClientInfo

// ClientOptions re-exports the SDK's ClientOptions.
type ClientOptions = sdkmcp.ClientOptions

// Connect re-exports the SDK's Connect (opens a Client over a
// Transport configured with opts.Info).
func Connect(ctx context.Context, t sdkmcp.Transport, opts ClientOptions) (*Client, error) {
	return sdkmcp.Connect(ctx, t, opts)
}

// ErrClosed re-exports the SDK's ErrClosed sentinel so CLI
// callers can errors.Is against sdkadapter.ErrClosed without an
// extra SDK import.
var ErrClosed = sdkmcp.ErrClosed

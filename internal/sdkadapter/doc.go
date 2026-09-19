// Package sdkadapter bridges CLI types and SDK types. Shape adapters in this
// package map pairs of types that share an intent but differ in field shape.
// The CLI shapes come from internal/{provider,tools,reasoning}. The SDK shapes
// come from github.com/MiviaLabs/mivia-ai-sdk.
//
// This package owns CLI type-shape adapters, while internal/agent directly
// bridges the SDK loop. It translates types across the boundary to prevent
// shape drift. The .mivia/policy/import-layers.json file pins the allowed
// import edges for this package.
//
// It may import internal/tools and internal/provider/reasoning, and the SDK
// module. internal/agent, internal/cli/agents, and internal/tui/run may
// import it; it may not import internal/agent.
//
// Applicable shape adapters have companion tests that verify round-trip
// conversions between CLI and SDK representations. These tests convert
// CLI to SDK to CLI or assert key fields on bridge output. The round-trip
// tests prove that the bridge is shape-faithful.
package sdkadapter

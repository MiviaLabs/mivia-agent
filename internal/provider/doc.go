// Package provider implements the LLM chat adapters for mivia: one client
// per provider protocol, streaming, retry, usage capture, and the capability
// surface (reasoning policy, context accounting, token estimation) that the
// agent loop consumes.
//
// It may import internal/config, internal/provider/reasoning, and
// internal/provider/registry, plus the standard library and the SDK module.
// The agent, cli, and sdkadapter packages may import it.
package provider

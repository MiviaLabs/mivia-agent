package config

// HarnessConfig controls harness-level execution behavior. It is distinct
// from verifier/project configuration: a project's evidence gates decide
// WHAT runs, HarnessConfig decides HOW the harness runs it.
type HarnessConfig struct {
	// Sandbox controls whether the harness runs verifier/evidence-gate
	// commands inside a bubblewrap sandbox (Linux only). nil (the key
	// omitted) means enabled, so existing configs load unchanged. Disabling
	// it runs those commands directly on the host with no filesystem,
	// network, or environment isolation from the workflow - see
	// .agents/rules/10-security-privacy.md before turning this off.
	Sandbox *bool `toml:"sandbox"`
}

// SandboxEnabled reports whether the sandbox is enabled (nil means enabled).
func (h HarnessConfig) SandboxEnabled() bool {
	return h.Sandbox == nil || *h.Sandbox
}

package config

import "testing"

func TestHarnessConfigDefaultsToSandboxEnabled(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Harness.SandboxEnabled() {
		t.Fatal("harness sandbox should default to enabled when [harness] is omitted")
	}
}

func TestHarnessConfigSandboxFromTOML(t *testing.T) {
	res, err := Load(LoadOptions{ConfigPath: writeMinimalConfig(t, "\n[harness]\nsandbox = false\n")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Harness.SandboxEnabled() {
		t.Fatal("[harness] sandbox = false should disable the sandbox")
	}
}

func TestHarnessConfigSandboxEnabledNilMeansTrue(t *testing.T) {
	var hc HarnessConfig
	if !hc.SandboxEnabled() {
		t.Fatal("nil sandbox must mean enabled")
	}
	disabled := false
	hc.Sandbox = &disabled
	if hc.SandboxEnabled() {
		t.Fatal("explicit false must disable")
	}
	enabled := true
	hc.Sandbox = &enabled
	if !hc.SandboxEnabled() {
		t.Fatal("explicit true must enable")
	}
}

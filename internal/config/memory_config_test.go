package config

import "testing"

func TestResolveMemoryConfigDefaults(t *testing.T) {
	mc, err := resolveMemoryConfig(File{}, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !mc.IsEnabled() || mc.StoreBackend != "markdown" {
		t.Fatalf("defaults: %+v", mc)
	}
	if mc.MaxEntryBytes != 8192 || mc.MaxEntries != 500 || mc.MaxSearchResults != 8 {
		t.Fatalf("bounds: %+v", mc)
	}
}

func TestResolveMemoryConfigAcceptsSupportedBackends(t *testing.T) {
	for _, backend := range []string{"memory", "markdown", "MARKDOWN"} {
		mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{StoreBackend: backend}}, "", "", false)
		if err != nil {
			t.Fatalf("%q: %v", backend, err)
		}
		if mc.StoreBackend != backend && !(backend == "MARKDOWN" && mc.StoreBackend == "markdown") {
			t.Fatalf("backend = %q", mc.StoreBackend)
		}
	}
}

func TestResolveMemoryConfigRejectsSQLite(t *testing.T) {
	_, err := resolveMemoryConfig(File{Memory: MemoryConfig{StoreBackend: "sqlite"}}, "", "", false)
	if err == nil {
		t.Fatal("sqlite memory backend must be rejected")
	}
}

func TestResolveMemoryConfigDisabledSkipsValidation(t *testing.T) {
	disabled := false
	mc, err := resolveMemoryConfig(File{Memory: MemoryConfig{Enabled: &disabled, StoreBackend: "sqlite"}}, "", "", false)
	if err != nil || mc.IsEnabled() {
		t.Fatalf("disabled config: %+v, %v", mc, err)
	}
}

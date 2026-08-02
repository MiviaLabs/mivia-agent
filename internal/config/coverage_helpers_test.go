package config

import (
	"path/filepath"
	"testing"
)

func TestHooksPathAndProviderChoicesHelpers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	source := HooksSource{}
	if source.UserPath() != UserConfigPath() {
		t.Fatalf("UserPath = %q, want %q", source.UserPath(), UserConfigPath())
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if !containsPath([]string{path}, filepath.Join(filepath.Dir(path), ".", filepath.Base(path))) || containsPath([]string{path}, path+".other") {
		t.Fatal("containsPath did not compare normalized paths")
	}
	resolved := &Resolved{ProviderName: "fallback", Models: []string{"fallback-model"}, modelCatalog: []ProviderModelGroup{
		{Provider: "openai", Selectable: true, Models: []ModelSpec{{Name: "gpt-a"}, {Name: "gpt-b"}}},
		{Provider: "disabled", Selectable: false, Models: []ModelSpec{{Name: "hidden"}}},
	}}
	if got := resolved.ModelChoicesFor(" OPENAI "); got != "gpt-a, gpt-b" {
		t.Fatalf("selectable provider choices = %q", got)
	}
	if got := resolved.ModelChoicesFor("fallback"); got != "fallback-model" {
		t.Fatalf("fallback choices = %q", got)
	}
	if got := resolved.ModelChoicesFor("disabled"); got != "" {
		t.Fatalf("disabled provider choices = %q", got)
	}
}

package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// Project config lives in the workspace namespace with everything else mivia
// owns. The repo-root path it replaced is not searched: one namespace, one
// place to look, no precedence for a user to reason about.
func TestDefaultConfigCandidatesUsesNamespace(t *testing.T) {
	t.Setenv("MIVIA_CONFIG", "")
	got := DefaultConfigCandidates()

	wantSuffix := filepath.Join(".mivia", "mivia.toml")
	var found bool
	for _, c := range got {
		if strings.HasSuffix(c, wantSuffix) {
			found = true
		}
		if strings.HasSuffix(c, string(filepath.Separator)+"mivia.toml") &&
			!strings.HasSuffix(c, wantSuffix) {
			t.Errorf("repo-root mivia.toml must not be searched, got %q", c)
		}
	}
	if !found {
		t.Fatalf("no candidate ends in %s: %v", wantSuffix, got)
	}
}

func TestDefaultConfigCandidatesHonorsEnvOverrideFirst(t *testing.T) {
	t.Setenv("MIVIA_CONFIG", "/tmp/explicit.toml")
	got := DefaultConfigCandidates()
	if len(got) == 0 || got[0] != "/tmp/explicit.toml" {
		t.Fatalf("MIVIA_CONFIG must win: %v", got)
	}
}

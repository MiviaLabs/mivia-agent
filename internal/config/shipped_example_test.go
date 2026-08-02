package config

import (
	"path/filepath"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
)

// The shipped example is what a new workspace copies. Nothing loaded it, so a
// key documented there could be one an actual config rejects - and the model
// object is a closed shape, which makes that failure a hard error on first run
// rather than a warning.
func TestShippedExampleConfigLoads(t *testing.T) {
	path := filepath.Join("..", "..", ".mivia", "mivia.toml.example")
	res, err := Load(LoadOptions{ConfigPath: path})
	if err != nil {
		t.Fatalf("the shipped example config does not load: %v", err)
	}

	// The reasoning documentation is only honest if the example's own entries
	// behave the way its comments claim.
	var configured, unset int
	for _, group := range res.ModelCatalog() {
		for _, spec := range group.Models {
			if !spec.Reasoning.Active() {
				unset++
				continue
			}
			configured++
			dialect := spec.ReasoningDialect
			if dialect == "" {
				var ok bool
				if dialect, ok = reasoning.DefaultDialect(group.Provider); !ok {
					t.Fatalf("example model %q sets reasoning with no resolvable dialect", spec.Name)
				}
			}
			if dialect == reasoning.DialectNone {
				t.Fatalf("example model %q sets reasoning on the none dialect", spec.Name)
			}
		}
	}
	if configured == 0 {
		t.Fatal("the example must show a configured reasoning model")
	}
	if unset == 0 {
		t.Fatal("the example must show an unset model beside the configured one")
	}
}

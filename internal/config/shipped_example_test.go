package config

import (
	"path/filepath"
	"runtime"
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

// The live shipped catalog (.mivia/mivia.toml, what this repository runs on)
// must advertise glm-5.2's real context window (~200K, the figure the provider
// bindings model at 200000). A larger number inflates the prompt budget
// (EffectivePromptTokens reserves only max_output_tokens from it), so context
// grows far past the provider limit before the 80% compaction trigger fires and
// the provider answers HTTP 400 prompt-too-long.
func TestShippedCatalogGLM52ContextWindowTokens(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(file)))
	res, err := Load(LoadOptions{ConfigPath: filepath.Join(root, ".mivia", "mivia.toml")})
	if err != nil {
		t.Fatalf("the shipped catalog does not load: %v", err)
	}
	spec := profileNamed(t, res, "zai", "glm-5.2")
	if spec.ContextWindowTokens != 200000 {
		t.Fatalf("shipped glm-5.2 context_window_tokens = %d, want 200000 (provider's real limit)", spec.ContextWindowTokens)
	}
	if spec.MaxOutputTokens >= spec.ContextWindowTokens {
		t.Fatalf("shipped glm-5.2 max_output_tokens = %d must stay below the 200000 context window", spec.MaxOutputTokens)
	}
}

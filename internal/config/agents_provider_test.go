package config

import (
	"strings"
	"testing"
)

// Phase 1 of the agent model routing plan: an agent binding is a
// provider-qualified pair, not a bare model name. These tests pin the parse
// layer's half of that contract.

func TestAgentProviderKeyParsed(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
provider = "zai"
model = "glm-5.2"
`)
	spec, _, err := ParseAgentFileTOML(body, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Provider == nil || *spec.Provider != "zai" {
		t.Fatalf("provider = %#v", spec.Provider)
	}
	if spec.Model == nil || *spec.Model != "glm-5.2" {
		t.Fatalf("model = %#v", spec.Model)
	}
}

func TestAgentProviderOmittedIsNil(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
`)
	spec, _, err := ParseAgentFileTOML(body, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Provider != nil {
		t.Fatalf("omitted provider must be nil, got %#v", spec.Provider)
	}
}

// A provider name is normalized at the trust boundary so catalog lookup,
// provider construction, and the definition digest all agree on one spelling.
func TestAgentProviderNormalized(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
provider = "  ZAI  "
model = "glm-5.2"
`)
	spec, _, err := ParseAgentFileTOML(body, "a.toml")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Provider == nil || *spec.Provider != "zai" {
		t.Fatalf("provider must be lowercased and trimmed, got %#v", spec.Provider)
	}
}

func TestAgentProviderEmptyIsError(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
provider = ""
model = "glm-5.2"
`)
	if _, _, err := ParseAgentFileTOML(body, "a.toml"); err == nil {
		t.Fatal("provider = \"\" must be an error")
	}
}

// The parse-time name check is a spelling check against the built-in
// descriptors, not the fail-closed authorization gate. Credentials and
// configuration are enforced later by provider.NewForProvider.
func TestAgentUnknownProviderRejected(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
provider = "not-a-provider"
model = "m"
`)
	_, _, err := ParseAgentFileTOML(body, "a.toml")
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("unknown provider must be rejected, got %v", err)
	}
}

// A provider with no model is ambiguous: it would silently pair a foreign
// provider with the session's model name. Fail closed at authoring time.
func TestAgentProviderWithoutModelRejected(t *testing.T) {
	body := []byte(`
name = "a"
description = "d"
provider = "zai"
`)
	_, _, err := ParseAgentFileTOML(body, "a.toml")
	if err == nil || !strings.Contains(err.Error(), "model") {
		t.Fatalf("provider without model must be rejected, got %v", err)
	}
}

package verifier

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
)

func TestDefaultCatalogueRegistersGoDefault(t *testing.T) {
	c := DefaultCatalogue()
	p, err := c.Lookup(GoDefaultName)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != GoDefaultName {
		t.Fatalf("name = %q", p.Name())
	}
}

func TestLookupUnknownFailsClosed(t *testing.T) {
	c := DefaultCatalogue()
	_, err := c.Lookup("shell-from-toml")
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err = %v", err)
	}
	_, err = c.Lookup("")
	if err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestRegisterDuplicateFails(t *testing.T) {
	c := NewCatalogue()
	if err := c.Register(NewGoDefault(nil)); err != nil {
		t.Fatal(err)
	}
	if err := c.Register(NewGoDefault(nil)); err == nil {
		t.Fatal("duplicate accepted")
	}
}

func TestGoDefaultProducesSchemaValidEvidence(t *testing.T) {
	schemaPath := filepath.Join("..", "testdata", "schemas", "verification-v1.json")
	rawSchema, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		t.Fatal(err)
	}
	compiled, err := jschema.Compile(schema)
	if err != nil {
		t.Fatal(err)
	}

	// Fake checks keep the unit test independent of the host module layout.
	p := NewGoDefault(func(context.Context, string) ([]Check, error) {
		return []Check{
			{Name: "workspace-dir", Status: "passed"},
			{Name: "go-module", Status: "passed"},
		}, nil
	})
	result, err := p.Verify(context.Background(), Request{WorkDir: t.TempDir(), StepID: "verify", RunID: "wfr-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "passed" {
		t.Fatalf("status = %q", result.Status)
	}
	body, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compiled.ValidateJSONBytes(body); err != nil {
		t.Fatalf("schema validation failed: %v\nbody=%s", err, body)
	}
}

func TestGoDefaultFailedCheckSetsStatusFailed(t *testing.T) {
	p := NewGoDefault(func(context.Context, string) ([]Check, error) {
		return []Check{
			{Name: "workspace-dir", Status: "passed"},
			{Name: "go-module", Status: "failed"},
		}, nil
	})
	result, err := p.Verify(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q", result.Status)
	}
}

func TestFixedGoDefaultChecksAgainstTempDir(t *testing.T) {
	dir := t.TempDir()
	// No go.mod → go-module failed.
	checks, err := fixedGoDefaultChecks(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) < 2 || checks[0].Status != "passed" || checks[1].Status != "failed" {
		t.Fatalf("checks = %+v", checks)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checks, err = fixedGoDefaultChecks(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Status != "passed" {
			t.Fatalf("checks = %+v", checks)
		}
	}
}

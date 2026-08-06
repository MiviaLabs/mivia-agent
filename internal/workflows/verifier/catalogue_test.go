package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
)

func TestDefaultCatalogueRegistersFixedGoProfiles(t *testing.T) {
	c := DefaultCatalogue(secretPolicy(t))
	wantNames := []string{GoFinalName, GoTestName, GoVerifyName}
	gotNames := c.Names()
	sort.Strings(gotNames)
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("names = %#v, want %#v", gotNames, wantNames)
	}
	for _, name := range wantNames {
		p, err := c.Lookup(name)
		if err != nil {
			t.Fatalf("lookup %q: %v", name, err)
		}
		if p.Name() != name {
			t.Fatalf("name = %q, want %q", p.Name(), name)
		}
	}
}

func TestLookupUnknownFailsClosed(t *testing.T) {
	c := DefaultCatalogue(secretPolicy(t))
	_, err := c.Lookup("shell-from-toml")
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err = %v", err)
	}
	_, err = c.Lookup("")
	if err == nil {
		t.Fatal("empty name accepted")
	}
}

func TestDefaultGoProfilesUseFixedHostCommands(t *testing.T) {
	want := map[string][]commandSpec{
		GoTestName: {
			{check: "go-test", program: "go", args: []string{"test", "./..."}},
		},
		GoVerifyName: {
			{check: "go-vet", program: "go", args: []string{"vet", "./..."}},
			{check: "go-build", program: "go", args: []string{"build", "./cmd/mivia"}},
		},
		GoFinalName: {
			{check: "go-test-race", program: "go", args: []string{"test", "-race", "./..."}},
		},
	}
	for _, profile := range defaultGoProfiles(secretPolicy(t)) {
		goProfile, ok := profile.(*goProfile)
		if !ok {
			t.Fatalf("profile %q type = %T", profile.Name(), profile)
		}
		if !reflect.DeepEqual(goProfile.commands, want[goProfile.name]) {
			t.Fatalf("commands for %q = %#v, want %#v", goProfile.name, goProfile.commands, want[goProfile.name])
		}
	}
}

func TestGoProfileReportsEveryCommandResult(t *testing.T) {
	workDir := t.TempDir()
	var calls []string
	profile := newGoProfile(GoVerifyName, []commandSpec{
		{check: "go-vet", program: "go", args: []string{"vet", "./..."}},
		{check: "go-build", program: "go", args: []string{"build", "./cmd/mivia"}},
	}, func(_ context.Context, gotDir, program string, args ...string) error {
		if gotDir != workDir {
			t.Fatalf("work dir = %q, want %q", gotDir, workDir)
		}
		calls = append(calls, program+" "+strings.Join(args, " "))
		if program == "go" && len(args) > 0 && args[0] == "vet" {
			return errors.New("test failure")
		}
		return nil
	})

	result, err := profile.Verify(context.Background(), Request{WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "failed" {
		t.Fatalf("status = %q", result.Status)
	}
	if want := []string{"go vet ./...", "go build ./cmd/mivia"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if want := []Check{{Name: "go-vet", Status: "failed", Class: "source", Detail: "host verifier command failed"}, {Name: "go-build", Status: "passed"}}; !reflect.DeepEqual(result.Checks, want) {
		t.Fatalf("checks = %#v, want %#v", result.Checks, want)
	}
}

func TestGoProfileStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	profile := newGoProfile(GoTestName, []commandSpec{{check: "go-test", program: "go", args: []string{"test", "./..."}}}, func(context.Context, string, string, ...string) error {
		called = true
		return nil
	})
	_, err := profile.Verify(ctx, Request{WorkDir: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if called {
		t.Fatal("runner was called after context cancellation")
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

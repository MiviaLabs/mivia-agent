package verifier

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MiviaLabs/mivia-agent/internal/jschema"
)

func TestNewDeclaredProfileRejectsBadInput(t *testing.T) {
	if _, err := NewDeclaredProfile("", []DeclaredCommand{{Check: "c", Program: "go"}}); err == nil {
		t.Fatal("empty name accepted")
	}
	if _, err := NewDeclaredProfile("p", nil); err == nil {
		t.Fatal("empty command list accepted")
	}
	if _, err := NewDeclaredProfile("p", []DeclaredCommand{{Check: "", Program: "go"}}); err == nil {
		t.Fatal("empty check name accepted")
	}
	if _, err := NewDeclaredProfile("p", []DeclaredCommand{{Check: "c", Program: "/bin/sh"}}); err == nil {
		t.Fatal("non-bare program accepted")
	}
	p, err := NewDeclaredProfile("p", []DeclaredCommand{{Check: "c", Program: "go", Args: []string{"version"}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "p" {
		t.Fatalf("name = %q, want %q", p.Name(), "p")
	}
}

func TestDeclaredProfileReportsEveryCommandResult(t *testing.T) {
	workDir := t.TempDir()
	var calls []string
	profile := newDeclaredProfile("go-verify", []commandSpec{
		{check: "go-vet", program: "go", args: []string{"vet", "./..."}},
		{check: "go-build", program: "go", args: []string{"build", "./..."}},
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
	if want := []string{"go vet ./...", "go build ./..."}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if want := []Check{{Name: "go-vet", Status: "failed", Class: "source", Detail: "host verifier command failed"}, {Name: "go-build", Status: "passed"}}; !reflect.DeepEqual(result.Checks, want) {
		t.Fatalf("checks = %#v, want %#v", result.Checks, want)
	}
}

func TestDeclaredProfileStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	profile := newDeclaredProfile("go-test", []commandSpec{{check: "go-test", program: "go", args: []string{"test", "./..."}}}, func(context.Context, string, string, ...string) error {
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

func TestDeclaredProfileProducesSchemaValidEvidence(t *testing.T) {
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

	// The injected run hook keeps the test independent of the host layout.
	profile := newDeclaredProfile("go-test", []commandSpec{
		{check: "go-test", program: "go", args: []string{"test", "./..."}},
	}, func(context.Context, string, string, ...string) error {
		return nil
	})
	result, err := profile.Verify(context.Background(), Request{WorkDir: t.TempDir(), StepID: "verify", RunID: "wfr-1"})
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

// TestDeclaredProfileSurfacesContextErrorFromRun verifies that a context
// error surfaced by a sandboxed run is returned to the controller instead of
// being swallowed into a failed host-class check with a nil error. The
// controller detects the context error and settles the run as timed_out;
// swallowing it would fabricate a host failure. Regression for
// verifier-gate-deadline-swallowed-as-host-failure.
func TestDeclaredProfileSurfacesContextErrorFromRun(t *testing.T) {
	profile := newDeclaredProfile("context-timeout", []commandSpec{
		{check: "first", program: "go", args: []string{"test", "./..."}},
		{check: "second", program: "go", args: []string{"vet", "./..."}},
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	profile.run = func(ctx context.Context, workDir, program string, args ...string) error {
		if len(args) > 0 && args[0] == "test" {
			// The first check fails normally so the returned result is a
			// failed result, not a passed one.
			return &commandFailure{class: "source", detail: "tests failed", err: errors.New("source check failed")}
		}
		cancel()
		return hostFailure(ctx.Err())
	}
	result, err := profile.Verify(ctx, Request{WorkDir: t.TempDir()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Verify err = %v, want context.Canceled", err)
	}
	if result.Status != "failed" {
		t.Fatalf("result status = %q, want failed", result.Status)
	}
}

func TestDeclaredProfileNilReceiverName(t *testing.T) {
	var p *declaredProfile
	if p.Name() != "" {
		t.Fatalf("nil profile name must be empty, got %q", p.Name())
	}
	if _, err := p.Verify(context.Background(), Request{}); err == nil {
		t.Fatal("nil profile must refuse to verify")
	}
}

// An empty Request.WorkDir must resolve to the current directory, not fail.
func TestDeclaredProfileResolvesEmptyWorkDir(t *testing.T) {
	var got string
	p := newDeclaredProfile("p", []commandSpec{{check: "c", program: "go"}}, func(_ context.Context, workDir, _ string, _ ...string) error {
		got = workDir
		return nil
	})
	result, err := p.Verify(context.Background(), Request{})
	if err != nil || result.Status != "passed" {
		t.Fatalf("verify: %v, %+v", err, result)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got != wd {
		t.Fatalf("work dir = %q, want current directory %q", got, wd)
	}
}

// runFixedCommand must fail before any execution when the work directory has
// no Go module baseline to capture.
func TestRunFixedCommandRequiresModuleBaseline(t *testing.T) {
	if err := runFixedCommand(context.Background(), t.TempDir(), "go", "version"); err == nil {
		t.Fatal("missing go.mod must fail the baseline capture")
	}
}

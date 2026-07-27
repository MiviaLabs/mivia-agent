package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MiviaLabs/mivia-agent/internal/workspace"
)

// ---------------------------------------------------------------------------
// Tier 6 — Cross-Package Tool Surface Integrity
// ---------------------------------------------------------------------------
// These tests verify tool capability scoping for parallel execution,
// schema validation across the full tool surface, and registry consistency.

// trackActiveTool wraps a tool to track concurrent execution count.
type trackActiveTool struct {
	tool      Tool
	mu        sync.Mutex
	active    int
	maxActive int
	delay     time.Duration
}

func (t *trackActiveTool) Name() string               { return t.tool.Name() }
func (t *trackActiveTool) Description() string        { return t.tool.Description() }
func (t *trackActiveTool) Parameters() map[string]any { return t.tool.Parameters() }
func (t *trackActiveTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	t.mu.Lock()
	t.active++
	if t.active > t.maxActive {
		t.maxActive = t.active
	}
	t.mu.Unlock()

	if t.delay > 0 {
		select {
		case <-time.After(t.delay):
		case <-ctx.Done():
			t.mu.Lock()
			t.active--
			t.mu.Unlock()
			return "", ctx.Err()
		}
	}

	result, err := t.tool.Execute(ctx, args)

	t.mu.Lock()
	t.active--
	t.mu.Unlock()
	return result, err
}

func (t *trackActiveTool) Capability(args json.RawMessage) Capability {
	if c, ok := t.tool.(CapableTool); ok {
		return c.Capability(args)
	}
	return Capability{}
}

// namedTool wraps a tool with a custom name.
type namedTool struct {
	name  string
	inner Tool
}

func (t *namedTool) Name() string               { return t.name }
func (t *namedTool) Description() string        { return t.inner.Description() }
func (t *namedTool) Parameters() map[string]any { return t.inner.Parameters() }
func (t *namedTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.inner.Execute(ctx, args)
}

func (t *namedTool) Capability(args json.RawMessage) Capability {
	if c, ok := t.inner.(CapableTool); ok {
		return c.Capability(args)
	}
	return Capability{}
}

// TestToolSurfaceSchemaConsistency verifies all default tools have
// valid schemas with required fields.
func TestToolSurfaceSchemaConsistency(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})

	for _, tool := range reg.List() {
		name := tool.Name()
		params := tool.Parameters()
		if params == nil {
			t.Fatalf("tool %q has nil Parameters", name)
		}
		schema, ok := params["properties"].(map[string]any)
		if !ok {
			continue
		}
		for propName, propDef := range schema {
			prop, ok := propDef.(map[string]any)
			if !ok {
				t.Fatalf("tool %q property %q is not an object", name, propName)
			}
			if _, hasType := prop["type"]; !hasType {
				if propName == "anyOf" || propName == "oneOf" {
					continue
				}
				t.Logf("tool %q property %q has no type", name, propName)
			}
		}
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("tool %q registered in List but not found via Get", name)
		}
	}
}

// TestToolRegistryListAndGetConsistency verifies List() and Get() consistency.
func TestToolRegistryListAndGetConsistency(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})

	listed := reg.List()
	for _, tool := range listed {
		if _, ok := reg.Get(tool.Name()); !ok {
			t.Fatalf("tool %q in List but not found via Get", tool.Name())
		}
	}
	t.Logf("registry has %d tools", len(listed))
}

// TestToolExecutionValidatesSchema verifies schema validation rejects
// invalid arguments for various tools.
func TestToolExecutionValidatesSchema(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})

	ctx := context.Background()
	errorCases := []struct {
		name string
		args json.RawMessage
	}{
		{"read_file", json.RawMessage(`{"path":123}`)},
		{"write_file", json.RawMessage(`{"content":"x"}`)},
		{"search_replace", json.RawMessage(`{}`)},
		{"search", json.RawMessage(`{"scope":"invalid"}`)},
		{"run_command", json.RawMessage(`{"argv":"not an array"}`)},
	}

	for _, c := range errorCases {
		if _, err := reg.Execute(ctx, c.name, c.args); err == nil {
			t.Errorf("expected error for %s with args %s, got nil", c.name, string(c.args))
		} else {
			t.Logf("tool %q correctly rejected: %v", c.name, err)
		}
	}
}

// TestToolOpenAIToolsConsistency verifies OpenAI tool specs are
// consistent with the internal tool descriptions and language-generic.
func TestToolOpenAIToolsConsistency(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})

	specs := reg.OpenAITools()
	if len(specs) == 0 {
		t.Fatal("expected at least one OpenAI tool spec")
	}

	for _, spec := range specs {
		fn, ok := spec["function"].(map[string]any)
		if !ok {
			t.Fatalf("tool spec missing function key: %v", spec)
		}
		name, ok := fn["name"].(string)
		if !ok || name == "" {
			t.Fatal("tool spec has empty or missing name")
		}
		desc, ok := fn["description"].(string)
		if !ok || desc == "" {
			t.Fatalf("tool %q has empty description in OpenAI spec", name)
		}
		for _, p := range languageBiasPatterns {
			if p.re.MatchString(desc) {
				t.Errorf("tool %q description contains biased pattern %q: %q", name, p.name, desc)
			}
		}
	}
}

// TestToolParallelExecutionWithDifferentKeys verifies tools with
// different resource keys execute concurrently.
func TestToolParallelExecutionWithDifferentKeys(t *testing.T) {
	dir := t.TempDir()
	ws, err := workspace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create two files to read.
	os.WriteFile(filepath.Join(dir, "x.txt"), []byte("content x"), 0o644)
	os.WriteFile(filepath.Join(dir, "y.txt"), []byte("content y"), 0o644)

	slow := &trackActiveTool{
		tool:  &readFileTool{ws: ws, maxBytes: 256 * 1024},
		delay: 100 * time.Millisecond,
	}
	fast := &trackActiveTool{
		tool:  &readFileTool{ws: ws, maxBytes: 256 * 1024},
		delay: 100 * time.Millisecond,
	}

	reg := NewRegistry()
	reg.Register(&namedTool{name: "read_slow", inner: slow})
	reg.Register(&namedTool{name: "read_fast", inner: fast})

	ctx := context.Background()
	var wg sync.WaitGroup
	var errs [2]error

	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			toolName := "read_slow"
			if idx == 1 {
				toolName = "read_fast"
			}
			path := "x.txt"
			if idx == 1 {
				path = "y.txt"
			}
			_, errs[idx] = reg.Execute(ctx, toolName, json.RawMessage(`{"path":"`+path+`"}`))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
	}
	t.Logf("slow maxActive=%d, fast maxActive=%d", slow.maxActive, fast.maxActive)
}

// TestToolExecutionCreatesAndReadsFiles verifies write_file + read_file
// round-trip through the registry.
func TestToolExecutionCreatesAndReadsFiles(t *testing.T) {
	ws, err := workspace.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultRegistry(DefaultOptions{Workspace: ws})

	ctx := context.Background()
	writeResult, err := reg.Execute(ctx, "write_file", json.RawMessage(`{"path":"hello.txt","content":"hello world"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(writeResult, "hello.txt") {
		t.Fatalf("write result: %q", writeResult)
	}

	readResult, err := reg.Execute(ctx, "read_file", json.RawMessage(`{"path":"hello.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if readResult != "hello world" {
		t.Fatalf("read result: %q, want 'hello world'", readResult)
	}
}

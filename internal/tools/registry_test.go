package tools

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMatchGlob(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "main_test.go", true},
		{"*.go", "README.md", false},
		{"**/*.go", "pkg/main.go", true},
		{"internal/*.go", "internal/main.go", true},
		{"internal/*.go", "pkg/main.go", false},
		{"", "anything", false},
	}
	for _, tt := range tests {
		if got := matchGlob(tt.pattern, tt.name); got != tt.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", tt.pattern, tt.name, got, tt.want)
		}
	}
}

func TestFilterNames(t *testing.T) {
	tests := []struct {
		name      string
		names     []string
		mode      ScopeMode
		allowlist map[string]struct{}
		extraDeny []string
		wantLen   int
	}{
		{"no filter", []string{"a", "b"}, 0, nil, nil, 2},
		{"denylist in spawn mode", []string{"delegate", "dispatch_tasks"}, ScopeSpawned, nil, nil, 0},
		{"allowlist only", []string{"a", "b", "c"}, 0, map[string]struct{}{"a": {}, "b": {}}, nil, 2},
		{"both filters in spawn mode", []string{"delegate", "grep"}, ScopeSpawned, map[string]struct{}{"delegate": {}}, nil, 0},
		{"allowlist overrides denylist", []string{"grep", "read_file"}, ScopeSpawned, map[string]struct{}{"grep": {}, "read_file": {}}, nil, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterNames(tt.names, tt.mode, tt.allowlist, tt.extraDeny)
			if len(got) != tt.wantLen {
				t.Errorf("FilterNames() = %v (len %d), want len %d", got, len(got), tt.wantLen)
			}
		})
	}
}

func TestCloneForGeneration(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "read_file", desc: "read", params: `{}`})
	r.Register(&mockTool{name: "write_file", desc: "write", params: `{}`})

	clone := r.CloneForGeneration()
	if clone == nil {
		t.Fatal("CloneForGeneration() returned nil")
	}
	list := clone.List()
	if len(list) != 2 {
		t.Errorf("CloneForGeneration() got %d tools, want 2", len(list))
	}
}

func TestCloneForGenerationExcluding(t *testing.T) {
	r := NewRegistry()
	r.Register(&mockTool{name: "read_file", desc: "read", params: `{}`})
	r.Register(&mockTool{name: "write_file", desc: "write", params: `{}`})

	clone := r.CloneForGenerationExcluding("read_file")
	if clone == nil {
		t.Fatal("CloneForGenerationExcluding() returned nil")
	}
	list := clone.List()
	if len(list) != 1 {
		t.Errorf("CloneForGenerationExcluding() = %v (len %d), want len 1", list, len(list))
	}
}

func TestCloneForGenerationNil(t *testing.T) {
	var r *Registry
	if got := r.CloneForGeneration(); got != nil {
		t.Errorf("nil registry CloneForGeneration() = %v, want nil", got)
	}
}

// mockTool implements Tool for testing.
type mockTool struct {
	name, desc, params string
}

func (m *mockTool) Name() string               { return m.name }
func (m *mockTool) Description() string        { return m.desc }
func (m *mockTool) Parameters() map[string]any { return nil }
func (m *mockTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "", nil
}

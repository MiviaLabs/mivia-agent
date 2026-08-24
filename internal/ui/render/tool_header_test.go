package render

import (
	"testing"
)

func TestFormatToolDetail(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "run_command",
			args: map[string]any{"command": "go test ./..."},
			want: "$ go test ./...",
		},
		{
			name: "read_file",
			args: map[string]any{"file_path": "internal/ui/app.go"},
			want: "internal/ui/app.go",
		},
		{
			name: "view_file",
			args: map[string]any{"AbsolutePath": "/foo/bar.go", "StartLine": 10, "EndLine": 50},
			want: "/foo/bar.go [L10-L50]",
		},
		{
			name: "grep_search",
			args: map[string]any{"Query": "func Run", "SearchPath": "internal/ui"},
			want: `"func Run" in internal/ui`,
		},
		{
			name: "read_url_content",
			args: map[string]any{"Url": "https://pkg.go.dev/charm.land/bubbletea/v2"},
			want: "[pkg.go.dev] /charm.land/bubbletea/v2",
		},
		{
			name: "search_web",
			args: map[string]any{"query": "golang lipgloss split layout"},
			want: `"golang lipgloss split layout"`,
		},
		{
			name: "read_url_content_multibyte",
			args: map[string]any{"Url": "https://example.com/api/v1/中文路径_test_route_long_endpoint"},
			want: "[example.com] /api/v1/中文路径_test_route_l…",
		},
		{
			name: "custom_generic_tool",
			args: map[string]any{"foo": "bar", "count": 42},
			want: "count=42 foo=bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatToolDetail(tt.name, tt.args)
			if got != tt.want {
				t.Errorf("FormatToolDetail(%q, %v) = %q, want %q", tt.name, tt.args, got, tt.want)
			}
		})
	}
}

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
			name: "memory_search",
			args: map[string]any{"query": "sandbox run_command landlock security", "scope": "project"},
			want: `"sandbox run_command landlock security" [project]`,
		},
		{
			name: "ledger_read",
			args: map[string]any{"ref": "ref:output:94f588477ee962db522a4e0b01d0dedf3bd3b85b8dab125d51b07577eab7b21e", "limit": 8192},
			want: "ref:output:94f58847",
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

// TestApprovalDetailRow pins C11: the approval box's key/value target
// row is the RAW arg value - no "$ " prefix, no "[Lx-Ly]" suffix - so a
// reader aligning "Command"/"File"/"Path" columns sees just the target,
// and a tool that names none of these gets no second row at all.
func TestApprovalDetailRow(t *testing.T) {
	tests := []struct {
		name      string
		args      map[string]any
		wantLabel string
		wantValue string
		wantOK    bool
	}{
		{
			name:      "run_command",
			args:      map[string]any{"command": "go test ./..."},
			wantLabel: "Command",
			wantValue: "go test ./...",
			wantOK:    true,
		},
		{
			name:      "edit_file",
			args:      map[string]any{"path": "internal/ui/app.go"},
			wantLabel: "File",
			wantValue: "internal/ui/app.go",
			wantOK:    true,
		},
		{
			name:      "view_file",
			args:      map[string]any{"AbsolutePath": "/foo/bar.go", "StartLine": 10, "EndLine": 50},
			wantLabel: "File",
			wantValue: "/foo/bar.go",
			wantOK:    true,
		},
		{
			name:      "list_directory",
			args:      map[string]any{"DirectoryPath": "internal/ui"},
			wantLabel: "Path",
			wantValue: "internal/ui",
			wantOK:    true,
		},
		{
			name:   "grep_search",
			args:   map[string]any{"Query": "func Run", "SearchPath": "internal/ui"},
			wantOK: false,
		},
		{
			name:   "custom_generic_tool",
			args:   map[string]any{"foo": "bar"},
			wantOK: false,
		},
		{
			name:   "run_command_no_args",
			args:   map[string]any{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLabel, gotValue, gotOK := ApprovalDetailRow(tt.name, tt.args)
			if gotOK != tt.wantOK || gotLabel != tt.wantLabel || gotValue != tt.wantValue {
				t.Errorf("ApprovalDetailRow(%q, %v) = (%q, %q, %v), want (%q, %q, %v)",
					tt.name, tt.args, gotLabel, gotValue, gotOK, tt.wantLabel, tt.wantValue, tt.wantOK)
			}
		})
	}
}

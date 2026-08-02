package agents

import "testing"

// TestAllowlistSet verifies that the effective tools list is converted into a
// set that ScopedRegistry membership checks can consult, with duplicates
// collapsed.
func TestAllowlistSet(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  map[string]bool
	}{
		{
			name:  "nil input yields empty set",
			names: nil,
			want:  map[string]bool{},
		},
		{
			name:  "empty input yields empty set",
			names: []string{},
			want:  map[string]bool{},
		},
		{
			name:  "single name",
			names: []string{"read_file"},
			want:  map[string]bool{"read_file": true},
		},
		{
			name:  "multiple names",
			names: []string{"read_file", "grep", "glob"},
			want:  map[string]bool{"read_file": true, "grep": true, "glob": true},
		},
		{
			name:  "duplicates collapse",
			names: []string{"read_file", "read_file", "grep"},
			want:  map[string]bool{"read_file": true, "grep": true},
		},
		{
			name:  "blank names are preserved verbatim",
			names: []string{"read_file", "", " "},
			want:  map[string]bool{"read_file": true, "": true, " ": true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AllowlistSet(tc.names)
			if len(got) != len(tc.want) {
				t.Fatalf("AllowlistSet(%v) = %v, want %v", tc.names, got, tc.want)
			}
			for name := range tc.want {
				if _, ok := got[name]; !ok {
					t.Errorf("AllowlistSet(%v) missing %q in %v", tc.names, name, got)
				}
			}
		})
	}
}

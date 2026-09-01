package chat

import "testing"

// TestIsAutoSaveName pins the auto-save name recognizer's basic shape
// contract - relocated from persistence_test.go when the legacy file-backed
// session store was removed; this function has no dependency on it.
func TestIsAutoSaveName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{AutoSaveName, true},
		{AutoSaveName + "20250115T103000", true},
		{AutoSaveName + "_foo", false},
		{"my-session", false},
		{"project-work", false},
		{"__last__", true},
		{"__last_", false},
		{"_last__", false},
		{"", false},
	}
	for _, tt := range tests {
		got := IsAutoSaveName(tt.name)
		if got != tt.want {
			t.Errorf("IsAutoSaveName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestIsAutoSaveNameRejectsUserNames covers the turn-marker and
// collision-suffix shapes IsAutoSaveName's basic contract above does not -
// relocated from retention_test.go, which was otherwise entirely about the
// removed legacy per-turn snapshot retention mechanism.
func TestIsAutoSaveNameRejectsUserNames(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{AutoSaveName, true},                           // legacy bare directory
		{AutoSaveName + "20250115T103000", true},       // legacy second-precision stamp
		{AutoSaveName + "20250115T103000.123", true},   // current millisecond stamp
		{AutoSaveName + "20250115T103000.123-2", true}, // collision suffix
		{AutoSaveName + turnSaveMarker + "20250115T103000", true},
		{AutoSaveName + "mywork", false},
		{AutoSaveName + "_foo", false},
		{AutoSaveName + "20250115", false},
		{AutoSaveName + "notatimestamp.000", false},
		{"my-session", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsAutoSaveName(tt.name); got != tt.want {
			t.Errorf("IsAutoSaveName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestSanitizeSessionName verifies path traversal prevention - relocated
// from persistence_test.go; sanitizeSessionName itself is unrelated to the
// removed legacy file-backed session store.
func TestSanitizeSessionName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"my-session", "my-session"},
		{"../evil", "__evil"},
		{"foo/bar", "foo_bar"},
		{"foo\\bar", "foo_bar"},
		{"a:b", "a_b"},
		{"", "unnamed"},
		{".", "unnamed"},
		{"..", "unnamed"},
		{"  spaced  ", "spaced"},
		{"\x00null", "null"},
	}
	for _, tt := range tests {
		got := sanitizeSessionName(tt.input)
		if got != tt.want {
			t.Errorf("sanitize(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

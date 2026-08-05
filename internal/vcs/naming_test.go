package vcs

import (
	"strings"
	"testing"
)

func TestSanitizeName(t *testing.T) {
	tests := []struct {
		input string
		want  string
		errOK bool
	}{
		{"feature-auth", "feature-auth", true},
		{"Feature Auth", "feature-auth", true},
		{"  spaces  ", "spaces", true},
		{"hello..world", "hello-world", true},
		{"../../../etc/passwd", "etc-passwd", true},
		{"", "", false},
		{"  ", "", false},
		{"---", "", false},
		{"...", "", false},
		{"..", "", false},
		{".git", "", false},
		{".mivia", "", false},
	}
	for _, tt := range tests {
		got, err := SanitizeName(tt.input)
		if err != nil && tt.errOK {
			t.Errorf("SanitizeName(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if err == nil && !tt.errOK {
			t.Errorf("SanitizeName(%q): expected error, got %q", tt.input, got)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("SanitizeName(%q): got %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeNameTruncation(t *testing.T) {
	long := strings.Repeat("a", MaxWorktreeNameLen+10)
	if got, err := SanitizeName(long); err == nil {
		t.Fatalf("SanitizeName accepted truncated alias %q", got)
	}
}

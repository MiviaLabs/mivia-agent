package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoDeadReferences verifies that symbols removed in R0 are not referenced
// anywhere in source files under internal/cli/.
func TestNoDeadReferences(t *testing.T) {
	root := "."
	dead := []string{
		"calcToolPanelLines",
		"consumeToolNavKey",
		"toolsNavActive",
		"focusTools",
	}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Skip hidden dirs and vendor.
			if strings.HasPrefix(info.Name(), ".") || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only check .go files (skip test files for focusTools exception check)
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(data), "\n")
		for _, sym := range dead {
			if sym != "focusTools" {
				for i, line := range lines {
					if strings.Contains(line, sym) {
						t.Errorf("file %s line %d: still contains %q", path, i+1, sym)
					}
				}
			}
		}
		// focusTools: allowed only inside a String() method (i.e. a switch case).
		// We still flag any reference that is NOT part of a String() switch-case.
		for i, line := range lines {
			if strings.Contains(line, "focusTools") {
				// Check if it's inside a String method — heuristic: look for nearby "String()" func
				if !isInsideStringMethod(lines, i) {
					t.Errorf("file %s line %d: still contains %q outside String()", path, i+1, "focusTools")
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
}

// isInsideStringMethod checks if the line at idx is likely inside a
// func (…) String() string { … } method. It scans backwards for "String() string {"
// and forward for the closing "}". This is a simple heuristic.
func isInsideStringMethod(lines []string, idx int) bool {
	// Scan backwards to find the function declaration
	braceDepth := 0
	foundStringFunc := false
	for i := idx; i >= 0; i-- {
		line := lines[i]
		if strings.Contains(line, "func ") && strings.Contains(line, "String()") {
			foundStringFunc = true
		}
		if foundStringFunc {
			for _, ch := range line {
				switch ch {
				case '{':
					braceDepth++
				case '}':
					braceDepth--
				}
			}
			if foundStringFunc && braceDepth > 0 {
				return true
			}
			if foundStringFunc && braceDepth == 0 {
				// We passed the function boundary — not inside it
				return false
			}
		}
	}
	return false
}

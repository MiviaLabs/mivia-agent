//go:build linux

package skills

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenDeclaredResourceFileDoesNotLeakFDs is the regression for
// skills-fd-leak-openat-linux. The old `defer unix.Close(fd)` captured the
// dup'd root fd value at defer registration; the traversal loop then closed
// and reassigned fd, so the dup'd root fd was double-closed after the loop
// reused it (an fd-reuse integrity hazard) and the final parent directory fd
// leaked once per call. Every nested resource path leaked a descriptor on
// both the success and error branches, so repeated resource reads exhausted
// the process fd table. The fix defers a closure that reads the live fd at
// return, closing each directory fd exactly once.
func TestOpenDeclaredResourceFileDoesNotLeakFDs(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "review")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"SKILL.md":        "---\nname: review\n---\nbody",
		"resources.toml":  "format = 1\n[[resources]]\nid = \"template\"\npath = \"sub/template.md\"\nsummary = \"Template\"\n",
		"sub/template.md": "TEMPLATE BODY",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := loadMarkdown(root)
	if err != nil {
		t.Fatal(err)
	}
	def, ok := reg.Get("review")
	if !ok {
		t.Fatal("skill missing")
	}
	activation, err := def.Activate()
	if err != nil {
		t.Fatal(err)
	}
	defer activation.Close()

	before := procSelfFDCount(t)
	const iterations = 256
	for i := 0; i < iterations; i++ {
		if i%2 == 0 {
			// Success path: nested resource path sub/template.md must read.
			file, err := openDeclaredResourceFile(activation.root, activation.rootFS, "sub/template.md")
			if err != nil {
				t.Fatalf("iteration %d: open template: %v", i, err)
			}
			data, readErr := io.ReadAll(file)
			_ = file.Close()
			if readErr != nil || string(data) != "TEMPLATE BODY" {
				t.Fatalf("iteration %d: template body = %q, err = %v", i, data, readErr)
			}
		} else {
			// Error path: missing file at depth 2 must fail.
			if _, err := openDeclaredResourceFile(activation.root, activation.rootFS, "sub/missing.md"); err == nil {
				t.Fatalf("iteration %d: missing resource opened", i)
			}
		}
	}
	after := procSelfFDCount(t)
	if after-before > 4 {
		t.Fatalf("openDeclaredResourceFile leaked fds: before=%d after=%d", before, after)
	}
}

func procSelfFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

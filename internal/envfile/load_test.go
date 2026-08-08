package envfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\nDEEPSEEK_API_KEY=sk-test\nexport OPENROUTER_API_KEY=\"or-key\"\nEMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["DEEPSEEK_API_KEY"] != "sk-test" {
		t.Fatalf("deepseek key: %q", m["DEEPSEEK_API_KEY"])
	}
	if m["OPENROUTER_API_KEY"] != "or-key" {
		t.Fatalf("openrouter key: %q", m["OPENROUTER_API_KEY"])
	}
}

func TestLoadDuplicateKey(t *testing.T) {
	t.Run("duplicate key returns error with key name and line number", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		content := "SECRET_KEY=alpha\nSECRET_KEY=beta\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatal("expected error for duplicate key, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "SECRET_KEY") {
			t.Fatalf("error should mention key SECRET_KEY, got: %q", errStr)
		}
		if !strings.Contains(errStr, "line") {
			t.Fatalf("error should mention a line number, got: %q", errStr)
		}
	})

	t.Run("single key returns nil error and correct value", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		content := "SECRET_KEY=alpha\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		m, err := Load(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m["SECRET_KEY"] != "alpha" {
			t.Fatalf("SECRET_KEY = %q, want alpha", m["SECRET_KEY"])
		}
	})

	t.Run("duplicate after export prefix detected", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		content := "export SECRET_KEY=alpha\nexport SECRET_KEY=beta\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatal("expected error for duplicate key after export prefix, got nil")
		}
		if !strings.Contains(err.Error(), "SECRET_KEY") {
			t.Fatalf("error should mention key SECRET_KEY, got: %q", err.Error())
		}
	})

	t.Run("triple duplicate reports first duplicate on line 2 referencing line 1", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, ".env")
		content := "KEY=a\nKEY=b\nKEY=c\n"
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil {
			t.Fatal("expected error for triple duplicate, got nil")
		}
		errStr := err.Error()
		if !strings.Contains(errStr, "KEY") {
			t.Fatalf("error should mention key KEY, got: %q", errStr)
		}
		// Error should fire on line 2 (first duplicate of line 1)
		if !strings.Contains(errStr, ":2:") {
			t.Fatalf("error should be on line 2 (first duplicate), got: %q", errStr)
		}
		if !strings.Contains(errStr, "line 1") {
			t.Fatalf("error should reference line 1 (first occurrence), got: %q", errStr)
		}
	})
}

func TestLoadEmptyValueNotLost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "EMPTY=\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["EMPTY"] != "" {
		t.Fatalf("EMPTY = %q, want empty string", m["EMPTY"])
	}
}

func TestLookupProcessWins(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "from-process")
	file := map[string]string{"DEEPSEEK_API_KEY": "from-file"}
	v, ok := Lookup("DEEPSEEK_API_KEY", file)
	if !ok || v != "from-process" {
		t.Fatalf("got %q ok=%v", v, ok)
	}
}

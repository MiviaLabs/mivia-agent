package definition

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSandboxCopiesSecretNamedGoTestFilesOnly(t *testing.T) {
	source := t.TempDir()
	for name, body := range map[string]string{
		"credentials_test.go": "package example\n",
		"credentials.go":      "package example\n",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	destination := t.TempDir()
	if _, err := copySandboxWorktree(source, destination, secretPolicy(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "credentials_test.go")); err != nil {
		t.Fatalf("sandbox did not copy Go test file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "credentials.go")); !os.IsNotExist(err) {
		t.Fatalf("sandbox copied secret-like source file: %v", err)
	}
}
